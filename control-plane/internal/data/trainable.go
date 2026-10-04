package data

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Trainable fails with dataset-not-frozen for a draft (phase 4), and with eval-only-dataset when the dataset version v
// may not be trained on: it was registered for
// evaluation only (golden and replay test sets), its licence or one of its sources' forbids commercial use or
// derivative works (registry.TrainingForbidden, R26 — read now, whatever was adopted or cleared before), or one of
// its sources is not cleared for training now. Clearance
// is read at the time of asking, so clearing a source (sources.edit) makes its versions trainable without a
// re-import. Versions without sources (the phase-1 fixtures) are trainable. A version that shares an utterance with
// a golden set (by identity or fingerprint) fails with golden-set-leakage (NotGolden): mixes, runs and the pipeline
// engine's training inputs all come through here, so runs cannot reference golden-set audio.
func Trainable(ctx context.Context, q storage.Querier, v registry.Version) error {
	if v.State == registry.StateDraft {
		return problems.DatasetNotFrozen.New("%s %s is a draft (segments indexed on a mount, no audio copied); freeze it with datasets.freeze before it is mixed or trained on",
			v.Name, v.Version)
	}
	var p struct {
		EvalOnly  bool     `json:"evalOnly"`
		SourceIDs []string `json:"sourceIds"`
	}
	if len(v.Payload) > 0 {
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return fmt.Errorf("decode dataset %s: %w", v.ID, err)
		}
	}
	if p.EvalOnly {
		return problems.EvalOnlyDataset.New("%s %s is registered for evaluation only (golden or replay test set); it can never be mixed or trained on",
			v.Name, v.Version)
	}
	// The version's own licence (its collection's, else its payload's): a fixture without one is not refused here.
	if l := strings.TrimSpace(v.Licence); l != "" && !strings.EqualFold(l, "unknown") {
		if why := registry.TrainingForbidden(l); why != "" {
			return problems.EvalOnlyDataset.New("%s %s is licensed %s, which %s; it may be evaluated on but never trained on (R26: a fine-tuned model is a derivative work a project ships)",
				v.Name, v.Version, l, why)
		}
	}
	if len(p.SourceIDs) == 0 {
		return NotGolden(ctx, q, v)
	}
	type src struct {
		name, licence string
		cleared       bool
	}
	rows, err := q.Query(ctx, "SELECT name, licence, training_cleared FROM sources WHERE id = ANY($1) ORDER BY name", p.SourceIDs)
	if err != nil {
		return fmt.Errorf("query dataset sources: %w", err)
	}
	srcs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (src, error) {
		var s src
		return s, row.Scan(&s.name, &s.licence, &s.cleared)
	})
	if err != nil {
		return fmt.Errorf("query dataset sources: %w", err)
	}
	var blocked []string
	for _, s := range srcs {
		// The licence first: clearing a source never makes a non-commercial or no-derivatives licence trainable.
		if why := registry.TrainingForbidden(s.licence); why != "" {
			return problems.EvalOnlyDataset.New("%s %s is eval-only: source %s is licensed %s, which %s; its audio may be evaluated on but never trained on (R26)",
				v.Name, v.Version, s.name, s.licence, why)
		}
		if !s.cleared {
			blocked = append(blocked, s.name)
		}
	}
	if len(blocked) > 0 {
		return problems.EvalOnlyDataset.New("%s %s is eval-only: source %s is not cleared for training; a person clears it with sources.edit (trainingCleared: true)",
			v.Name, v.Version, strings.Join(blocked, ", "))
	}
	return NotGolden(ctx, q, v)
}
