// Package eviction frees content-store space (docs/spec/06-platform.md "Artifacts, metrics and logs", Retention):
// artifacts.evict deletes the blobs of superseded training-state artifacts after a person approved it.
//
// Selection (Plan): every live training state of a pipeline run that finished is evictable; of a run that failed
// or was cancelled, every state but the newest (runs.resume continues from it); nothing of a run still running, and
// nothing a waiting or leased step job, a running pipeline's inputs, a registry version, a checkpoint or another live
// artifact's file list names. With a backup mirror configured, an artifact whose blobs the mirror does not hold yet
// stays until the next backup copied them, so every eviction can be undone from the mirror. A blob is deleted only
// when no artifact outside the eviction set lists it (the file index, artifact_files).
//
// The command (internal/server) is always gated; the approved replay enqueues the eviction job (Register), which
// marks the rows evicted (they stay: lineage resolves and artifacts.get says where the bytes went), emits
// artifact.evicted, deletes the blobs and leaves an audit entry with the bytes freed.
//
// Concurrency: one advisory lock on the content store (internal/artifacts LockShared/LockExclusive). Everything
// that relies on bytes being present — artifacts.Record (step outputs, run inputs), a step reused by input hash, the
// start-time restore — holds it shared from its store check until it commits. The job holds it exclusive twice:
// to plan and mark (so every such transaction is either visible to the plan or has not looked at the store yet),
// and to delete — it re-reads which rows are still marked by it (a Record that found the bytes meanwhile cleared
// the mark and keeps them) and which blobs a live artifact lists (a new directory sharing a file keeps it), and
// deletes before any Record runs again; a Record after that finds the bytes gone and answers ErrEvicted, so a
// reuse falls back to running the step. One global lock rather than one per hash: a directory shares file blobs
// with artifacts it does not name, and evictions are rare, approved, and short.
//
// Reference checks (referencesQuery) are index lookups: GIN indexes on artifact_hashes_in(document) for step job
// specs, pipeline run and step inputs and registry payloads, a btree on checkpoints.artifact_hash (migration 0022).
package eviction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// TypeTrainingState is the only artifact type v1 evicts.
const TypeTrainingState = "training-state"

// Pipeline run states the selection reads (internal/pipelines).
const (
	runRunning   = "running"
	runDone      = "done"
	runFailed    = "failed"
	runCancelled = "cancelled"
)

// Service plans and runs evictions.
type Service struct {
	Pool *pgxpool.Pool
	CAS  *cas.Store
	Jobs *jobs.Service
	Log  *slog.Logger
	// MirrorDir is the backup mirror of the store (CADENCE_BACKUP_DIR/cas, internal/backups): blobs live at
	// <MirrorDir>/b3/<2 hex>/<64 hex>. Training states are never mirrored (internal/backups), so their eviction is permanent;
	// the mirror restores other artifacts (Backfill).
	MirrorDir string
	// Mirror, when set, resolves the mirror's directory at each use instead (phase 4 · stream I: the mirror may live
	// on a mount, backups.mirror_mount); it answers MirrorDir's form.
	Mirror func(ctx context.Context) string
	// testHook, when set, runs at the job's phases ("marked": the rows are marked and the lock is released;
	// "deleting": the lock is held and the blobs are about to go), so a test can interleave a Record.
	testHook func(phase string)
}

// mirrorDir is the backup mirror's directory now ("" when there is none).
func (s *Service) mirrorDir(ctx context.Context) string {
	if s.Mirror != nil {
		return s.Mirror(ctx)
	}
	return s.MirrorDir
}

func (s *Service) hook(phase string) {
	if s.testHook != nil {
		s.testHook(phase)
	}
}

// Filter selects what an eviction considers. RunID and ProjectID select whole runs (so the newest state of a failed
// run is still the run's newest); OlderThan and Hashes narrow the evictable states.
type Filter struct {
	Type      string // TypeTrainingState (the default)
	RunID     string
	ProjectID string
	OlderThan time.Duration // 0: any age
	Hashes    []string      // exactly these; one that may not be evicted fails (Strict) or is skipped
	// Strict makes a named hash that may not be evicted an artifact-not-evictable problem (the command); the job
	// re-plans leniently and skips what stopped being evictable since the approval.
	Strict bool
	Now    time.Time
}

// Candidate is one artifact an eviction removes.
type Candidate struct {
	Hash          string    `json:"hash"`
	Type          string    `json:"type"`
	Size          int64     `json:"size"`
	ProjectID     string    `json:"projectId,omitempty"`
	PipelineRunID string    `json:"pipelineRunId,omitempty"`
	RunID         string    `json:"runId,omitempty"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"createdAt"`
	directory     bool
}

// Kept is a matched artifact that stays, with the reason.
type Kept struct {
	Hash   string `json:"hash"`
	RunID  string `json:"runId,omitempty"`
	Reason string `json:"reason"`
}

// Plan is what an eviction does: the artifacts, the ones kept, the blobs it deletes and their bytes.
type Plan struct {
	Artifacts  []Candidate `json:"artifacts"`
	Kept       []Kept      `json:"kept"`
	BytesFreed int64       `json:"bytesFreed"`
	Blobs      int         `json:"blobs"`
	Permanent  bool        `json:"permanent"`
	Disk       *Disk       `json:"disk,omitempty"` // the store's filesystem now (dry runs)
	blobs      []string
}

// Hashes lists the plan's artifacts.
func (p Plan) Hashes() []string {
	out := make([]string, len(p.Artifacts))
	for i, c := range p.Artifacts {
		out[i] = c.Hash
	}
	return out
}

// ---------------------------------------------------------------- selection (pure)

// state is one live training state with what the selection needs to know about it.
type state struct {
	Candidate
	pipelineState string
	referenced    string // why something still needs it ("" when nothing does)
}

// classify applies the rules to the live states of whole pipeline runs: it answers the evictable ones with the
// reason and the ones kept with theirs.
func classify(states []state) ([]Candidate, []Kept) {
	newest := map[string]state{}
	for _, s := range states {
		if s.PipelineRunID == "" {
			continue
		}
		n, ok := newest[s.PipelineRunID]
		if !ok || s.CreatedAt.After(n.CreatedAt) || (s.CreatedAt.Equal(n.CreatedAt) && s.Hash > n.Hash) {
			newest[s.PipelineRunID] = s
		}
	}
	var (
		evict []Candidate
		kept  []Kept
	)
	keep := func(s state, reason string) { kept = append(kept, Kept{Hash: s.Hash, RunID: s.RunID, Reason: reason}) }
	for _, s := range states {
		switch {
		case s.referenced != "":
			keep(s, s.referenced)
		case s.PipelineRunID == "":
			keep(s, "no pipeline run produced it, so nothing says it is superseded")
		case s.pipelineState == runRunning:
			keep(s, "its run is still running")
		case s.pipelineState == runDone:
			c := s.Candidate
			c.Reason = "its run finished: nothing resumes from it"
			evict = append(evict, c)
		case (s.pipelineState == runFailed || s.pipelineState == runCancelled) && newest[s.PipelineRunID].Hash == s.Hash:
			keep(s, fmt.Sprintf("the newest state of a %s run: runs.resume continues from it", s.pipelineState))
		case s.pipelineState == runFailed || s.pipelineState == runCancelled:
			c := s.Candidate
			c.Reason = fmt.Sprintf("superseded by a newer state of the same %s run", s.pipelineState)
			evict = append(evict, c)
		default:
			keep(s, fmt.Sprintf("its pipeline run is %s", s.pipelineState))
		}
	}
	return evict, kept
}

// narrow applies the age and hash filters to the evictable states; the rest are kept with the reason.
func narrow(evict []Candidate, kept []Kept, f Filter) ([]Candidate, []Kept, error) {
	want := map[string]bool{}
	for _, h := range f.Hashes {
		want[h] = true
	}
	var out []Candidate
	for _, c := range evict {
		switch {
		case len(want) > 0 && !want[c.Hash]:
			continue // not asked about: neither evicted nor reported
		case f.OlderThan > 0 && f.Now.Sub(c.CreatedAt) < f.OlderThan:
			kept = append(kept, Kept{Hash: c.Hash, RunID: c.RunID,
				Reason: fmt.Sprintf("recorded less than %d days ago", int(f.OlderThan.Hours()/24))})
		default:
			out = append(out, c)
		}
	}
	if len(want) == 0 {
		return out, kept, nil
	}
	var filtered []Kept
	reasons := map[string]string{}
	for _, k := range kept {
		if want[k.Hash] {
			filtered = append(filtered, k)
			reasons[k.Hash] = k.Reason
		}
	}
	found := map[string]bool{}
	for _, c := range out {
		found[c.Hash] = true
	}
	var refused []string
	for _, h := range f.Hashes {
		if found[h] {
			continue
		}
		r, ok := reasons[h]
		if !ok {
			r = "not a live " + typeOf(f) + " artifact (unknown, another type, already evicted, or outside the filter)"
			filtered = append(filtered, Kept{Hash: h, Reason: r})
		}
		refused = append(refused, h+": "+r)
	}
	if f.Strict && len(refused) > 0 {
		return nil, nil, problems.ArtifactNotEvictable.New("%s", strings.Join(refused, "; "))
	}
	return out, filtered, nil
}

func typeOf(f Filter) string {
	if f.Type == "" {
		return TypeTrainingState
	}
	return f.Type
}

// ---------------------------------------------------------------- planning (database and store)

// Plan answers what an eviction with f would do, inside tx. It first indexes training states that stopped steps
// saved into their lease outcomes only (a pause or a window close), so they can be selected like the rest.
func (s *Service) Plan(ctx context.Context, tx pgx.Tx, f Filter) (Plan, error) {
	if f.Type == "" {
		f.Type = TypeTrainingState
	}
	if f.Type != TypeTrainingState {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/type", Message: "v1 evicts training-state artifacts only"}})
	}
	if f.Now.IsZero() {
		f.Now = time.Now()
	}
	if s.CAS == nil {
		return Plan{}, errors.New("no content store is configured")
	}
	for _, h := range f.Hashes {
		if !steps.ValidHash(h) {
			return Plan{}, problems.Validation([]problems.FieldError{{Path: "/hashes", Message: fmt.Sprintf("%q is not an artifact hash (b3:<64 hex>)", h)}})
		}
	}
	if err := s.indexLeaseStates(ctx, tx); err != nil {
		return Plan{}, err
	}
	states, err := liveStates(ctx, tx, f)
	if err != nil {
		return Plan{}, err
	}
	for i := range states {
		if states[i].referenced, err = referenced(ctx, tx, states[i].Hash); err != nil {
			return Plan{}, err
		}
	}
	evict, kept := classify(states)
	evict, kept, err = narrow(evict, kept, f)
	if err != nil {
		return Plan{}, err
	}
	// Training states are not mirrored (internal/backups copyCAS): they are read only to resume, so evicting one is
	// permanent and needs no backup first (docs/spec/07 open question D, decided 2026-10-01).
	p := Plan{Kept: kept, Permanent: true, Artifacts: evict}
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

// liveStates reads the live (not evicted) artifacts of the filter's type with their producing pipeline run's state
// and its training run, for whole pipeline runs.
func liveStates(ctx context.Context, tx pgx.Tx, f Filter) ([]state, error) {
	rows, err := tx.Query(ctx, `SELECT a.hash, a.type, a.size, a.directory, coalesce(a.project_id, ''),
			coalesce(a.pipeline_run_id, ''), coalesce(r.id, ''), a.created_at, coalesce(pr.state, '')
		FROM artifacts a
		LEFT JOIN pipeline_runs pr ON pr.id = a.pipeline_run_id
		LEFT JOIN runs r ON r.pipeline_run_id = a.pipeline_run_id
		WHERE a.type = $1 AND a.evicted_at IS NULL
		  AND ($2 = '' OR r.id = $2)
		  AND ($3 = '' OR a.project_id = $3 OR pr.project_id = $3)
		ORDER BY a.created_at, a.hash`, f.Type, f.RunID, f.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("query training states: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (state, error) {
		var s state
		err := row.Scan(&s.Hash, &s.Type, &s.Size, &s.directory, &s.ProjectID, &s.PipelineRunID, &s.RunID, &s.CreatedAt,
			&s.pipelineState)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("read training states: %w", err)
	}
	return out, nil
}

// referencesQuery answers why something still needs artifact hash $1, or an empty string. Each document check is a lookup in a
// GIN index on artifact_hashes_in(doc) (migration 0022): the hashes that occur anywhere in the document's text. The
// expressions and state predicates must stay exactly those of the partial indexes, or the planner scans.
const referencesQuery = `SELECT CASE` + jobReferences + `
		WHEN EXISTS (SELECT 1 FROM eval_records WHERE scores_hash = $1)
			OR EXISTS (SELECT 1 FROM eval_records WHERE hypotheses_hash = $1)
			OR EXISTS (SELECT 1 FROM eval_metrics WHERE scores_hash = $1)
			THEN 'an eval record''s scores, hypotheses or metric scores (evals and gates read them)'` + fileReferences + `
		ELSE '' END`

// evalReferencesQuery is referencesQuery without the eval records: the age retention of eval artifacts weighs those
// itself (evals.go).
const evalReferencesQuery = `SELECT CASE` + jobReferences + fileReferences + `
		ELSE '' END`

// jobReferences are the checks of work and the registry that may still read artifact hash $1.
const jobReferences = `
		WHEN EXISTS (SELECT 1 FROM step_jobs WHERE state IN ('waiting', 'leased') AND artifact_hashes_in(spec) @> ARRAY[$1::text])
			THEN 'a waiting or running step job names it (an input or overrides.resumeFrom)'
		WHEN EXISTS (SELECT 1 FROM pipeline_runs WHERE state = 'running' AND artifact_hashes_in(inputs) @> ARRAY[$1::text])
			THEN 'an input of a running pipeline run'
		WHEN EXISTS (SELECT 1 FROM pipeline_steps WHERE state IN ('waiting', 'queued', 'running') AND artifact_hashes_in(inputs) @> ARRAY[$1::text])
			THEN 'an input of a pipeline step that has not finished'
		WHEN EXISTS (SELECT 1 FROM registry_versions WHERE artifact_hashes_in(payload) @> ARRAY[$1::text])
			THEN 'a registry version references it'
		WHEN EXISTS (SELECT 1 FROM checkpoints WHERE artifact_hash = $1)
			THEN 'a registered checkpoint'`

// fileReferences is the check that another live artifact lists artifact hash $1 as a file.
const fileReferences = `
		WHEN EXISTS (SELECT 1 FROM artifact_files f JOIN artifacts o ON o.hash = f.hash
				WHERE f.file_hash = $1 AND o.evicted_at IS NULL AND o.hash <> $1)
			THEN 'a file of another live artifact'`

// referenced answers why something still needs the artifact hash, or "".
func referenced(ctx context.Context, q pgx.Tx, hash string) (string, error) {
	var reason string
	err := q.QueryRow(ctx, referencesQuery, hash).Scan(&reason)
	if err != nil {
		return "", fmt.Errorf("check references of %s: %w", hash, err)
	}
	return reason, nil
}

// blobsOf lists the blobs of an artifact: its own (a file, or a directory's manifest) and a directory's files.
func blobsOf(ctx context.Context, q pgx.Tx, hash string, directory bool) ([]string, error) {
	out := []string{hash}
	if !directory {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT DISTINCT file_hash FROM artifact_files WHERE hash = $1 ORDER BY file_hash", hash)
	if err != nil {
		return nil, fmt.Errorf("query files of %s: %w", hash, err)
	}
	files, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read files of %s: %w", hash, err)
	}
	return append(out, files...), nil
}

// sizeBlobs fills the blobs the plan deletes and the bytes they hold: every blob of its artifacts that no live
// artifact outside the plan lists as a file or is.
func (s *Service) sizeBlobs(ctx context.Context, tx pgx.Tx, p *Plan) error {
	set := p.Hashes()
	blobs, err := s.deletable(ctx, tx, p.Artifacts, set)
	if err != nil {
		return err
	}
	p.blobs, p.BytesFreed, p.Blobs = blobs, 0, 0
	for _, b := range blobs {
		ok, size, err := s.CAS.Has(b)
		if err != nil {
			return fmt.Errorf("look up %s: %w", b, err)
		}
		if ok {
			p.BytesFreed += size
			p.Blobs++
		}
	}
	return nil
}

// deletable lists the blobs of arts that no live artifact outside set needs.
func (s *Service) deletable(ctx context.Context, tx pgx.Tx, arts []Candidate, set []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, c := range arts {
		blobs, err := blobsOf(ctx, tx, c.Hash, c.directory)
		if err != nil {
			return nil, err
		}
		for _, b := range blobs {
			if seen[b] {
				continue
			}
			seen[b] = true
			var needed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM artifact_files f JOIN artifacts o ON o.hash = f.hash
					WHERE f.file_hash = $1 AND o.evicted_at IS NULL AND NOT (o.hash = ANY($2)))
				OR EXISTS (SELECT 1 FROM artifacts WHERE hash = $1 AND evicted_at IS NULL AND NOT (hash = ANY($2)))`,
				b, set).Scan(&needed); err != nil {
				return nil, fmt.Errorf("check who needs %s: %w", b, err)
			}
			if !needed {
				out = append(out, b)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// indexLeaseStates records the training states stopped steps saved only into their lease outcomes (a pause or a
// window close requeues the step with overrides.resumeFrom and records nothing), with the step as producer and the
// lease's end as the time, so the selection orders them as runs.resume does. A state whose blob is gone is skipped.
func (s *Service) indexLeaseStates(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (o.value->>'hash') o.value->>'hash', o.key,
			coalesce((o.value->>'size')::bigint, 0), coalesce(o.value->'meta', '{}'),
			coalesce(sj.project_id, ''), coalesce(sj.spec->>'pipelineRunId', ''), coalesce(sj.spec->>'stepId', ''),
			coalesce(ps.step, ''), coalesce(l.ended_at, l.created_at)
		FROM leases l JOIN step_jobs sj ON sj.job_id = l.job_id
		LEFT JOIN pipeline_steps ps ON ps.id = sj.spec->>'stepId',
		     jsonb_each(CASE WHEN jsonb_typeof(l.outcome->'outputs') = 'object' THEN l.outcome->'outputs' ELSE '{}'::jsonb END) o
		WHERE o.value->>'type' = $1
		  AND NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.hash = o.value->>'hash')
		ORDER BY o.value->>'hash', l.ended_at`, TypeTrainingState)
	if err != nil {
		return fmt.Errorf("find unindexed training states: %w", err)
	}
	type found struct {
		ref                                 steps.ArtifactRef
		output, project, prun, stepID, step string
		at                                  time.Time
	}
	var list []found
	var f found
	if _, err := pgx.ForEachRow(rows, []any{&f.ref.Hash, &f.output, &f.ref.Size, &f.ref.Meta, &f.project, &f.prun,
		&f.stepID, &f.step, &f.at}, func() error {
		f.ref.Type = TypeTrainingState
		list = append(list, f)
		return nil
	}); err != nil {
		return fmt.Errorf("read unindexed training states: %w", err)
	}
	for _, f := range list {
		if !steps.ValidHash(f.ref.Hash) {
			continue
		}
		if ok, _, err := s.CAS.Has(f.ref.Hash); err != nil || !ok {
			continue
		}
		var prod *artifacts.Producer
		if f.prun != "" {
			prod = &artifacts.Producer{PipelineRunID: f.prun, StepID: f.stepID, Step: f.step, Output: f.output}
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("savepoint: %w", err)
		}
		if _, err := artifacts.Record(ctx, sp, s.CAS, f.ref, f.project, prod); err != nil {
			_ = sp.Rollback(ctx)
			s.log().WarnContext(ctx, "a lease's training state does not verify; it stays unindexed", "hash", f.ref.Hash, "err", err)
			continue
		}
		if _, err := sp.Exec(ctx, "UPDATE artifacts SET created_at = $2 WHERE hash = $1", f.ref.Hash, f.at); err != nil {
			_ = sp.Rollback(ctx)
			return fmt.Errorf("date training state %s: %w", f.ref.Hash, err)
		}
		if err := sp.Commit(ctx); err != nil {
			return fmt.Errorf("release savepoint: %w", err)
		}
	}
	return nil
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}
