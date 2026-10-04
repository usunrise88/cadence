package goldensets

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Reference alignments (phase 4 · stream L; docs/review/2026-10-03-phase-4-plan.md decision 8; R51, R54): word
// timings of a dataset's reference texts, written by an aligning step as an artifact of type alignment
// (cadence.alignment/1). The output hook keeps one row per (dataset artifact, alignment artifact); a golden set carries
// the newest alignment of its dataset artifact, so the version itself stays immutable. Go reads only the step's output
// meta (counts, the aligner, the reasons); what the rows hold is the worker's and the scorer's business.
const (
	TypeAlignment   = "alignment"
	TypeDataset     = "dataset"
	AlignmentFormat = "cadence.alignment/1"
	maxReasons      = 5
)

// Alignment is one recorded reference alignment (the contract's GoldenSetAlignment).
type Alignment struct {
	ID               string    `json:"id"`
	DatasetHash      string    `json:"-"`
	Artifact         string    `json:"artifact"`
	Aligner          string    `json:"aligner"`
	AlignerVersionID string    `json:"alignerVersionId,omitempty"`
	Method           string    `json:"method,omitempty"`
	Utterances       int       `json:"utterances"`
	Aligned          int       `json:"aligned"`
	Words            int       `json:"words"`
	Reasons          []string  `json:"reasons,omitempty"`
	PipelineRunID    string    `json:"pipelineRunId,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

// alignmentMeta is the output meta an aligning step sets (align_reference: ctx.set_meta("alignment", …)).
type alignmentMeta struct {
	Format  string `json:"format"`
	Aligner struct {
		Auxiliary string `json:"auxiliary"`
		VersionID string `json:"versionId"`
	} `json:"aligner"`
	Method     *string  `json:"method"`
	Utterances int      `json:"utterances"`
	Aligned    int      `json:"aligned"`
	Words      int      `json:"words"`
	Reasons    []string `json:"reasons"`
}

// InstallAlignments registers the alignment output hook.
func InstallAlignments(hooks *steps.Hooks) { hooks.On(TypeAlignment, AlignmentHook) }

// AlignmentHook records an alignment output against the dataset artifact its step aligned (the step's one dataset
// input) and announces it on every golden set built on that dataset artifact (golden_set.aligned). Recording the same
// artifact again (a reused step) is a no-op.
func AlignmentHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	var dataset string
	for _, in := range out.Spec.Inputs {
		if in.Type == TypeDataset {
			if dataset != "" {
				return nil, fmt.Errorf("alignment output of step %s: the step has more than one dataset input", out.StepID)
			}
			dataset = in.Hash
		}
	}
	if !steps.ValidHash(dataset) {
		return nil, fmt.Errorf("alignment output of step %s: the step has no dataset input", out.StepID)
	}
	var m alignmentMeta
	if len(out.Artifact.Meta) > 0 {
		if err := json.Unmarshal(out.Artifact.Meta, &m); err != nil {
			return nil, fmt.Errorf("alignment output of step %s: meta: %w", out.StepID, err)
		}
	}
	if m.Format != AlignmentFormat {
		return nil, fmt.Errorf("alignment output of step %s: meta format %q, not %s", out.StepID, m.Format, AlignmentFormat)
	}
	if m.Aligned < 0 || m.Aligned > m.Utterances || m.Words < 0 {
		return nil, fmt.Errorf("alignment output of step %s: inconsistent counts %d of %d", out.StepID, m.Aligned, m.Utterances)
	}
	if m.Aligner.VersionID == "" {
		if ref, ok := out.Spec.Auxiliaries["aligner"]; ok {
			m.Aligner.VersionID, m.Aligner.Auxiliary = ref.VersionID, ref.Name
		}
	}
	method := ""
	if m.Method != nil {
		method = *m.Method
	}
	reasons := m.Reasons
	if len(reasons) > maxReasons {
		reasons = reasons[:maxReasons]
	}
	if reasons == nil {
		reasons = []string{}
	}
	rb, err := json.Marshal(reasons)
	if err != nil {
		return nil, err
	}
	var project *string
	if out.ProjectID != "" {
		project = &out.ProjectID
	}
	tag, err := tx.Exec(ctx, `INSERT INTO reference_alignments (id, dataset_hash, artifact_hash, aligner_version_id, aligner, method,
			utterances, aligned, words, reasons, project_id, pipeline_run_id, step_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (dataset_hash, artifact_hash) DO NOTHING`,
		"aln_"+uuid.Must(uuid.NewV7()).String(), dataset, out.Artifact.Hash, m.Aligner.VersionID, m.Aligner.Auxiliary, method,
		m.Utterances, m.Aligned, m.Words, rb, project, out.PipelineRunID, out.StepID)
	if err != nil {
		return nil, fmt.Errorf("record reference alignment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	sets, err := goldenSetsOn(ctx, tx, dataset)
	if err != nil {
		return nil, err
	}
	drafts := make([]events.Draft, 0, len(sets))
	for _, v := range sets {
		d := registry.VersionEvent(v, "golden_set.aligned")
		d.Payload = map[string]any{"version": v.Summary(), "alignment": map[string]any{"artifact": out.Artifact.Hash,
			"aligner": m.Aligner.Auxiliary, "utterances": m.Utterances, "aligned": m.Aligned, "words": m.Words}}
		drafts = append(drafts, d)
	}
	return drafts, nil
}

// goldenSetsOn lists the golden set versions whose dataset artifact is hash.
func goldenSetsOn(ctx context.Context, q storage.Querier, hash string) ([]registry.Version, error) {
	rows, err := q.Query(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $1 AND v.payload->>'datasetHash' = $2`, registry.KindGoldenSet, hash)
	if err != nil {
		return nil, fmt.Errorf("golden sets on %s: %w", hash, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("golden sets on %s: %w", hash, err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindGoldenSet, IDs: ids})
}

// Alignments returns the newest alignment of each dataset artifact in hashes that has one.
func Alignments(ctx context.Context, q storage.Querier, hashes []string) (map[string]Alignment, error) {
	out := map[string]Alignment{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (dataset_hash) id, dataset_hash, artifact_hash, aligner_version_id, aligner, method,
			utterances, aligned, words, reasons, pipeline_run_id, created_at
		FROM reference_alignments WHERE dataset_hash = ANY($1)
		ORDER BY dataset_hash, created_at DESC, id DESC`, hashes)
	if err != nil {
		return nil, fmt.Errorf("read reference alignments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a Alignment
		var reasons []byte
		if err := rows.Scan(&a.ID, &a.DatasetHash, &a.Artifact, &a.AlignerVersionID, &a.Aligner, &a.Method, &a.Utterances,
			&a.Aligned, &a.Words, &reasons, &a.PipelineRunID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("read reference alignment: %w", err)
		}
		if err := json.Unmarshal(reasons, &a.Reasons); err != nil {
			return nil, fmt.Errorf("reference alignment %s reasons: %w", a.ID, err)
		}
		out[a.DatasetHash] = a
	}
	return out, rows.Err()
}

// LatestAlignment is the newest alignment of one dataset artifact, if any.
func LatestAlignment(ctx context.Context, q storage.Querier, datasetHash string) (*Alignment, error) {
	m, err := Alignments(ctx, q, []string{datasetHash})
	if err != nil {
		return nil, err
	}
	a, ok := m[datasetHash]
	if !ok {
		return nil, nil
	}
	return &a, nil
}
