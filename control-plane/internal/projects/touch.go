package projects

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// Touch locks the project, checks that it is at revision rev and moves it to the next revision. Commands that
// change what a project references (adoptions) without changing its own fields use it, so If-Match on the
// project covers them too.
func Touch(ctx context.Context, tx pgx.Tx, slug string, rev int) (Project, error) {
	if _, err := lockAt(ctx, tx, slug, rev); err != nil {
		return Project{}, err
	}
	rows, err := tx.Query(ctx, `UPDATE projects SET rev = rev + 1, updated_at = now() WHERE slug = $1 RETURNING `+projectCols, slug)
	return existing(rows, err, slug)
}

// Event is the project's revision event of type typ; extra fields join the payload next to "project".
func Event(p Project, typ string, extra map[string]any) []events.Draft {
	drafts := projectEvent(p, typ)
	payload := map[string]any{"project": p}
	for k, v := range extra {
		payload[k] = v
	}
	drafts[0].Payload = payload
	return drafts
}
