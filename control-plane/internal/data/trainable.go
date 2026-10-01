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

// Trainable fails with eval-only-dataset when the dataset version v may not be trained on: it was registered for
// evaluation only (golden and replay test sets), or one of its sources is not cleared for training now. Clearance
// is read at the time of asking, so clearing a source (sources.edit) makes its versions trainable without a
// re-import. Versions without sources (the phase-1 fixtures) are trainable.
func Trainable(ctx context.Context, q storage.Querier, v registry.Version) error {
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
	if len(p.SourceIDs) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, "SELECT name FROM sources WHERE id = ANY($1) AND NOT training_cleared ORDER BY name", p.SourceIDs)
	if err != nil {
		return fmt.Errorf("query dataset sources: %w", err)
	}
	blocked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("query dataset sources: %w", err)
	}
	if len(blocked) > 0 {
		return problems.EvalOnlyDataset.New("%s %s is eval-only: source %s is not cleared for training; a person clears it with sources.edit (trainingCleared: true)",
			v.Name, v.Version, strings.Join(blocked, ", "))
	}
	return nil
}
