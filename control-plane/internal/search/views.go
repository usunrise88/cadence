package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// ViewKind is the entity kind of a saved search (docs/spec/02-domain-projects-registry.md "Saved search").
const ViewKind = "saved_search"

// View is a saved search: a named query in the qualifier language, per user per project. Its JSON form is the
// contract's SavedView.
type View struct {
	ID          string    `json:"id"`
	UserID      string    `json:"-"`
	ProjectID   string    `json:"-"`
	Name        string    `json:"name"`
	Query       string    `json:"query"`
	Description string    `json:"description,omitempty"`
	Rev         int       `json:"rev"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ViewInput is the body of views.set.
type ViewInput struct {
	Query       string
	Description string
}

const viewCols = "id, user_id, project_id, name, query, description, rev, created_at, updated_at"

func scanView(row pgx.CollectableRow) (View, error) {
	var v View
	err := row.Scan(&v.ID, &v.UserID, &v.ProjectID, &v.Name, &v.Query, &v.Description, &v.Rev, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// ListViews returns the user's saved searches in a project by name.
func ListViews(ctx context.Context, q storage.Querier, userID, projectID string) ([]View, error) {
	rows, err := q.Query(ctx, "SELECT "+viewCols+` FROM saved_views WHERE user_id = $1 AND project_id = $2 ORDER BY name`,
		userID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list saved searches: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanView)
	if err != nil {
		return nil, fmt.Errorf("list saved searches: %w", err)
	}
	return out, nil
}

// GetView returns one saved search, or not-found.
func GetView(ctx context.Context, q storage.Querier, userID, projectID, name string) (View, error) {
	v, found, err := getView(ctx, q, userID, projectID, name, "")
	if err == nil && !found {
		err = problems.NotFound.New("no saved search named %q in this project", name)
	}
	return v, err
}

func getView(ctx context.Context, q storage.Querier, userID, projectID, name, lock string) (View, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+viewCols+` FROM saved_views WHERE user_id = $1 AND project_id = $2 AND name = $3 `+lock,
		userID, projectID, name)
	if err != nil {
		return View{}, false, fmt.Errorf("query saved search: %w", err)
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanView)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, false, fmt.Errorf("read saved search: %w", err)
	}
	return v, true, nil
}

// SetView saves a search. Without ifMatch it only creates (an existing one answers precondition-required); with
// ifMatch the saved search must exist at that revision. The caller has parsed the query already.
func SetView(ctx context.Context, tx pgx.Tx, userID, projectID, name string, ifMatch *int, in ViewInput) (View, []events.Draft, error) {
	cur, found, err := getView(ctx, tx, userID, projectID, name, "FOR UPDATE")
	if err != nil {
		return View{}, nil, err
	}
	var v View
	switch {
	case !found && ifMatch != nil:
		return View{}, nil, problems.Stale(0, "saved search %q does not exist yet; omit If-Match to create it", name)
	case !found:
		id := "vew_" + uuid.Must(uuid.NewV7()).String()
		rows, err := tx.Query(ctx, `INSERT INTO saved_views (id, user_id, project_id, name, query, description)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id, project_id, name) DO NOTHING RETURNING `+viewCols,
			id, userID, projectID, name, in.Query, in.Description)
		if err != nil {
			return View{}, nil, fmt.Errorf("insert saved search: %w", err)
		}
		v, err = pgx.CollectExactlyOneRow(rows, scanView)
		if errors.Is(err, pgx.ErrNoRows) { // created concurrently between our read and insert
			cur, _, err = getView(ctx, tx, userID, projectID, name, "")
			if err != nil {
				return View{}, nil, err
			}
			return View{}, nil, problems.Stale(cur.Rev, "saved search %q was just created by another save; re-read it and retry", name)
		}
		if err != nil {
			return View{}, nil, fmt.Errorf("insert saved search: %w", err)
		}
	case ifMatch == nil:
		return View{}, nil, commands.Precondition("saved search")
	default:
		if err := commands.CheckRev(ViewKind, *ifMatch, cur.Rev); err != nil {
			return View{}, nil, err
		}
		rows, err := tx.Query(ctx, `UPDATE saved_views SET query = $2, description = $3, rev = rev + 1, updated_at = now()
			WHERE id = $1 RETURNING `+viewCols, cur.ID, in.Query, in.Description)
		if err != nil {
			return View{}, nil, fmt.Errorf("update saved search: %w", err)
		}
		if v, err = pgx.CollectExactlyOneRow(rows, scanView); err != nil {
			return View{}, nil, fmt.Errorf("update saved search: %w", err)
		}
	}
	return v, []events.Draft{{
		Topic:     events.EntityTopic(ViewKind, v.ID),
		Type:      ViewKind + ".set",
		ProjectID: projectID,
		Entity:    &events.EntityRef{Kind: ViewKind, ID: v.ID, Rev: v.Rev},
		// The summary without the query: saved searches are the owner's; other members of the project see only
		// that one changed (as with workspaces).
		Payload: map[string]any{"savedSearch": map[string]any{"id": v.ID, "name": v.Name, "rev": v.Rev, "updatedAt": v.UpdatedAt}},
	}}, nil
}
