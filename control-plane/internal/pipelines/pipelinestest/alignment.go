package pipelinestest

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The alignment fixtures (phase 4 stream L): an aligner under a fixture name (align_reference needs an auxiliary the
// fixtures do not register) that writes an alignment artifact with the meta align_reference sets, timing every
// utterance unless its reference contains AlignSkip; and latency_score version 3, which reads the golden set's
// normalizer and, optionally, its alignment — the fixture worker reports fixed emission delay percentiles when the
// alignment is wired and emission delay unavailable when it is not.
const (
	KindAlign = "fx_align"
	AlignSkip = "unalignable"

	EmissionPR50 = 80.0
	EmissionPR90 = 160.0
)

// AlignmentFixtures are the fixture kinds as a worker publishes them.
var AlignmentFixtures = []map[string]any{
	{
		"name": KindAlign, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
		"params":   map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes": map[string]string{"data": "dataset"}, "produces": map[string]string{"alignment": "alignment"},
		"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.fx-align", "estimateSeconds": 5,
	},
	func() map[string]any {
		k := neutral(KindLatency, map[string]string{"hypotheses": "hypotheses", "data": "dataset", "vad": "vad",
			"normalizer": "normalizer", "alignment": "alignment"}, map[string]string{"scores": "metric_scores"}, "alignment", "normalizer")
		k["version"] = "3"
		return k
	}(),
}

// AlignmentPipeline aligns a dataset with the fixture aligner.
const AlignmentPipeline = `name: align
inputs: { data: dataset }
steps:
  - id: align
    kind: fx_align@1
    in: { data: $inputs.data }
`

// RegisterAlignment publishes the alignment fixtures (after RegisterRobustness: latency_score@3 is then the newest).
func RegisterAlignment(ctx context.Context, pool *pgxpool.Pool) error {
	return Register(ctx, pool, AlignmentFixtures...)
}

// runAlignment runs the fixture aligner.
func (l *Leases) runAlignment(spec steps.Spec) (steps.Outcome, bool, error) {
	if spec.Kind != KindAlign {
		return steps.Outcome{}, false, nil
	}
	utts, err := readLines[GoldenUtterance](l.CAS, spec.Inputs["data"].Hash)
	if err != nil {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: err.Error()}}, true, nil
	}
	var b strings.Builder
	b.WriteString(`{"alignment":{"format":"cadence.alignment/1","aligner":{"auxiliary":"auxiliary/fx-aligner"}}}` + "\n")
	aligned, words := 0, 0
	var reasons []string
	for _, u := range utts {
		row := map[string]any{"audio": u.Audio, "text": u.Ref}
		if strings.Contains(u.Ref, AlignSkip) {
			row["aligned"], row["reason"] = false, "the fixture aligner skips it"
			reasons = []string{"the fixture aligner skips it"}
		} else {
			var ws []map[string]any
			for i, w := range strings.Fields(u.Ref) {
				ws = append(ws, map[string]any{"index": i, "word": w, "start": 0.3 * float64(i), "end": 0.3*float64(i) + 0.25, "score": 0.9})
			}
			row["aligned"], row["words"] = true, ws
			aligned++
			words += len(ws)
		}
		line, _ := json.Marshal(row)
		b.Write(line)
		b.WriteByte('\n')
	}
	meta := map[string]any{"format": "cadence.alignment/1", "aligner": map[string]any{"auxiliary": "auxiliary/fx-aligner", "versionId": "ver_fx_aligner"},
		"method": "ctc-viterbi", "utterances": len(utts), "aligned": aligned, "unaligned": len(utts) - aligned, "words": words}
	if len(reasons) > 0 {
		meta["reasons"] = reasons
	}
	ref, err := l.put([]byte(b.String()), "alignment", meta)
	if err != nil {
		return steps.Outcome{}, true, err
	}
	return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"alignment": ref}}, true, nil
}

// emission is what the latency fixture reports for emission delay: fixed percentiles with an alignment input, else
// unavailable.
func emission(spec steps.Spec) map[string]any {
	for _, in := range spec.Inputs {
		if in.Type == "alignment" {
			return map[string]any{"available": true, "matchedWords": 12, "pr50Ms": EmissionPR50, "pr90Ms": EmissionPR90}
		}
	}
	return map[string]any{"available": false, "reason": "the golden set has no aligned references (run align_reference on its dataset)"}
}
