package eviction

// Eval artifacts are kept by age (owner decision 2026-10-03, docs/spec/07 "Open questions"): the per-utterance
// artifacts of an eval record — its scores and hypotheses, and the metric scores beside it (eval_metrics) — leave the
// store eval.artifact_retention_days after the record was last used, unless something still reads them. The records'
// summaries, the cells' deltas and the gate verdicts are rows and stay for ever; an eval that needs the bytes again
// computes the cell again (internal/evals refreshes the record).
//
// A record's last use is the newest of its own creation and the creation of every eval whose cells link it (a cached
// cell uses a record without computing it). An artifact is evictable when every record that names it was last used
// before the retention, no registered model's eval (a model version's evalId) and no unfinished eval links any of
// them, nothing else references it (referencesQuery's other checks) and — with a backup mirror — the mirror holds its
// blobs, so the eviction can be undone by copying them back (Backfill). The daily sweep (SweepEvalArtifacts) queues
// the ordinary eviction job with the retention instead of hashes; the job re-plans under the store lock.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// EvalTypes are the artifact types the age retention evicts: an eval record's scores and hypotheses (eval_records)
// and the metric scores beside it (eval_metrics). Other artifacts an eval reads (datasets, normalizers, boost lists,
// weights) are not the eval's and are never evicted by age.
var EvalTypes = []string{"scores", "hypotheses", "metric_scores"}

// evalUse is one eval record (erc_…) or metric row (erm_…) naming an artifact, with what protects it.
type evalUse struct {
	Record  string
	LastUse time.Time // the newest of the row's creation and the creation of the evals whose cells link it
	Model   string    // a registered model version whose eval links it ("" none)
	Active  string    // an unfinished (queued or running) eval that links it ("" none)
}

// evalArtifact is one live eval artifact with every row naming it.
type evalArtifact struct {
	Candidate
	uses       []evalUse
	referenced string // why something outside the evals still needs it ("" when nothing does)
}

// classifyEval applies the age retention: an artifact goes when nothing outside the evals references it, no
// registered model's eval and no unfinished eval reads it, and every row naming it was last used more than retention
// before now.
func classifyEval(arts []evalArtifact, retention time.Duration, now time.Time) ([]Candidate, []Kept) {
	var (
		evict []Candidate
		kept  []Kept
	)
	days := int(retention.Hours() / 24)
	for _, a := range arts {
		keep := func(reason string) { kept = append(kept, Kept{Hash: a.Hash, Reason: reason}) }
		var newest evalUse
		var model, active string
		for _, u := range a.uses {
			if u.LastUse.After(newest.LastUse) {
				newest = u
			}
			if model == "" && u.Model != "" {
				model = u.Model
			}
			if active == "" && u.Active != "" {
				active = u.Active
			}
		}
		switch {
		case a.referenced != "":
			keep(a.referenced)
		case len(a.uses) == 0:
			keep("no eval record names it")
		case model != "":
			keep(fmt.Sprintf("the eval of registered model version %s reads it", model))
		case active != "":
			keep(fmt.Sprintf("eval %s has not finished", active))
		case now.Sub(newest.LastUse) < retention:
			keep(fmt.Sprintf("eval record %s was last used on %s, less than %d days ago",
				newest.Record, newest.LastUse.UTC().Format(time.DateOnly), days))
		default:
			c := a.Candidate
			c.Reason = fmt.Sprintf("its newest eval record (%s) was last used on %s, more than %d days ago",
				newest.Record, newest.LastUse.UTC().Format(time.DateOnly), days)
			evict = append(evict, c)
		}
	}
	return evict, kept
}

// evalUsesQuery lists every live eval artifact with each row naming it: the row, its last use, a registered model
// version whose eval links it and an unfinished eval that links it. A metric row is linked by the cells of its key
// (model, golden set, decoding); a record by its cells' record_id.
const evalUsesQuery = `WITH uses AS (
		SELECT scores_hash AS hash, id AS rec, created_at FROM eval_records
		UNION ALL SELECT hypotheses_hash, id, created_at FROM eval_records WHERE hypotheses_hash IS NOT NULL
		UNION ALL SELECT scores_hash, id, created_at FROM eval_metrics
	), links AS (
		SELECT c.record_id AS rec, c.eval_id FROM eval_cells c WHERE c.record_id IS NOT NULL
		UNION SELECT m.id, c.eval_id FROM eval_metrics m JOIN eval_cells c ON c.model_key = m.model_key
			AND c.golden_set_version_id = m.golden_set_version_id AND c.decoding_hash = m.decoding_hash
	), models AS (
		SELECT v.payload->>'evalId' AS eval_id, min(v.id) AS version_id
		FROM registry_versions v JOIN registry_collections rc ON rc.id = v.collection_id
		WHERE rc.kind = $2 AND v.payload ? 'evalId' GROUP BY 1
	), recs AS (
		SELECT l.rec, max(e.created_at) AS used_at, min(mo.version_id) AS model,
			min(e.id) FILTER (WHERE e.status IN ('queued', 'running')) AS active
		FROM links l JOIN evals e ON e.id = l.eval_id LEFT JOIN models mo ON mo.eval_id = e.id
		GROUP BY l.rec
	)
	SELECT a.hash, a.type, a.size, a.directory, coalesce(a.project_id, ''), a.created_at,
		u.rec, greatest(u.created_at, coalesce(r.used_at, u.created_at)), coalesce(r.model, ''), coalesce(r.active, '')
	FROM uses u JOIN artifacts a ON a.hash = u.hash
	LEFT JOIN recs r ON r.rec = u.rec
	WHERE a.evicted_at IS NULL AND a.type = ANY($1)
	ORDER BY a.created_at, a.hash, u.rec`

// evalArtifacts reads the live eval artifacts with their uses.
func evalArtifacts(ctx context.Context, tx pgx.Tx) ([]evalArtifact, error) {
	rows, err := tx.Query(ctx, evalUsesQuery, EvalTypes, registry.KindModel)
	if err != nil {
		return nil, fmt.Errorf("query eval artifacts: %w", err)
	}
	var (
		out []evalArtifact
		c   Candidate
		u   evalUse
	)
	if _, err := pgx.ForEachRow(rows, []any{&c.Hash, &c.Type, &c.Size, &c.directory, &c.ProjectID, &c.CreatedAt,
		&u.Record, &u.LastUse, &u.Model, &u.Active}, func() error {
		if n := len(out); n > 0 && out[n-1].Hash == c.Hash {
			out[n-1].uses = append(out[n-1].uses, u)
			return nil
		}
		out = append(out, evalArtifact{Candidate: c, uses: []evalUse{u}})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read eval artifacts: %w", err)
	}
	return out, nil
}

// PlanEvalRetention answers what the age retention evicts now, inside tx: the eval artifacts whose records were last
// used more than days ago and that nothing still reads. With a backup mirror an artifact whose blobs the mirror does
// not hold yet stays until the next backup copied them, so the eviction is not permanent.
func (s *Service) PlanEvalRetention(ctx context.Context, tx pgx.Tx, days int, now time.Time) (Plan, error) {
	if s.CAS == nil {
		return Plan{Artifacts: []Candidate{}, Kept: []Kept{}}, nil
	}
	if days < 1 {
		return Plan{}, fmt.Errorf("eval artifact retention of %d days: at least 1", days)
	}
	if now.IsZero() {
		now = time.Now()
	}
	arts, err := evalArtifacts(ctx, tx)
	if err != nil {
		return Plan{}, err
	}
	for i := range arts {
		if err := tx.QueryRow(ctx, evalReferencesQuery, arts[i].Hash).Scan(&arts[i].referenced); err != nil {
			return Plan{}, fmt.Errorf("check references of %s: %w", arts[i].Hash, err)
		}
	}
	evict, kept := classifyEval(arts, time.Duration(days)*24*time.Hour, now)
	p := Plan{Kept: kept, Permanent: s.MirrorDir == ""}
	for _, c := range evict {
		if !p.Permanent {
			missing, err := s.unmirrored(ctx, tx, c)
			if err != nil {
				return Plan{}, err
			}
			if missing != "" {
				p.Kept = append(p.Kept, Kept{Hash: c.Hash, Reason: "the backup mirror does not hold " + missing +
					" yet; it goes after the next backup"})
				continue
			}
		}
		p.Artifacts = append(p.Artifacts, c)
	}
	if err := s.sizeBlobs(ctx, tx, &p); err != nil {
		return Plan{}, err
	}
	if p.Artifacts == nil {
		p.Artifacts = []Candidate{}
	}
	if p.Kept == nil {
		p.Kept = []Kept{}
	}
	return p, nil
}

// unmirrored names the first blob of c that the backup mirror lacks (absent, or of another size), or "".
func (s *Service) unmirrored(ctx context.Context, tx pgx.Tx, c Candidate) (string, error) {
	blobs, err := blobsOf(ctx, tx, c.Hash, c.directory)
	if err != nil {
		return "", err
	}
	for _, b := range blobs {
		ok, size, err := s.CAS.Has(b)
		if err != nil {
			return "", fmt.Errorf("look up %s: %w", b, err)
		}
		if !ok {
			continue // already gone from the store: nothing to lose
		}
		hx := strings.TrimPrefix(b, cas.Prefix)
		fi, err := os.Stat(filepath.Join(s.MirrorDir, "b3", hx[:2], hx))
		if err != nil || fi.Size() != size {
			return b, nil
		}
	}
	return "", nil
}

// SweepEvalArtifacts is the daily retention of eval artifacts (a periodic job): when the plan at
// eval.artifact_retention_days finds anything, it queues the eviction job, which re-plans under the store lock,
// marks, deletes and audits like an approved eviction. No approval: the owner set the policy (2026-10-03), and with a
// backup mirror every artifact it takes is in the mirror first.
func (s *Service) SweepEvalArtifacts(ctx context.Context, d *defaults.Defaults) error {
	if s.CAS == nil || s.Jobs == nil {
		return nil
	}
	days := d.Eval.ArtifactRetentionDays.Value
	ctx = auth.WithActor(ctx, jobs.System)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		p, err := s.PlanEvalRetention(ctx, tx, days, time.Now())
		if err != nil || len(p.Artifacts) == 0 {
			return err
		}
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND state IN ('queued', 'running')
				AND actor->>'id' = $2)`, JobKind, jobs.System.ID).Scan(&queued); err != nil {
			return fmt.Errorf("find a queued eval retention: %w", err)
		}
		if queued {
			return nil // the one already queued re-plans when it runs
		}
		_, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobKind, Args: jobArgs{RetentionDays: days}})
		if err != nil {
			return err
		}
		s.log().InfoContext(ctx, "eval artifacts past their retention", "artifacts", len(p.Artifacts),
			"bytes", p.BytesFreed, "days", days)
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}
