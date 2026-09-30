package projects

import (
	"context"
	"encoding/json"
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

// WorkspaceKind is the entity kind of a saved workspace.
const WorkspaceKind = "workspace"

// Workspace is one user's saved Dockview layout in a project.
type Workspace struct {
	ID            string
	UserID        string
	ProjectID     string
	Name          string
	SchemaVersion int
	Layout        json.RawMessage
	Panels        json.RawMessage
	Rev           int
	UpdatedAt     time.Time
}

// WorkspaceInput is the body of workspaces.set.
type WorkspaceInput struct {
	SchemaVersion int
	Layout        json.RawMessage
	Panels        json.RawMessage
}

const workspaceCols = "id, user_id, project_id, name, schema_version, layout, panels, rev, updated_at"

func scanWorkspace(row pgx.CollectableRow) (Workspace, error) {
	var w Workspace
	err := row.Scan(&w.ID, &w.UserID, &w.ProjectID, &w.Name, &w.SchemaVersion, &w.Layout, &w.Panels, &w.Rev, &w.UpdatedAt)
	return w, err
}

// ListWorkspaces returns the user's workspaces in a project by name, without layouts.
func ListWorkspaces(ctx context.Context, q storage.Querier, userID, projectID string) ([]Workspace, error) {
	rows, err := q.Query(ctx, `SELECT id, user_id, project_id, name, schema_version, 'null'::jsonb, 'null'::jsonb, rev, updated_at
		FROM workspaces WHERE user_id = $1 AND project_id = $2 ORDER BY name`, userID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanWorkspace)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return out, nil
}

// DefaultWorkspaces are the five workspaces every project starts with (docs/spec/11-ui-panels.md "Default
// workspaces"); their layouts are code factories in the web shell.
var DefaultWorkspaces = []string{"Training", "Eval", "Data", "Triage", "Ops"}

// CreateDefaultWorkspaces creates the default workspaces of a user in a project as placeholders — schema version
// 1, an empty layout, no panels — which the web shell fills with the default layout on first open and saves over
// with If-Match. Existing workspaces are left alone; it returns the names it created.
func CreateDefaultWorkspaces(ctx context.Context, tx pgx.Tx, userID, projectID string) ([]string, error) {
	var created []string
	for _, name := range DefaultWorkspaces {
		id := "wsp_" + uuid.Must(uuid.NewV7()).String()
		tag, err := tx.Exec(ctx, `INSERT INTO workspaces (id, user_id, project_id, name, schema_version, layout, panels)
			VALUES ($1, $2, $3, $4, 1, '{}', '{}') ON CONFLICT (user_id, project_id, name) DO NOTHING`, id, userID, projectID, name)
		if err != nil {
			return created, fmt.Errorf("create workspace %s: %w", name, err)
		}
		if tag.RowsAffected() > 0 {
			created = append(created, name)
		}
	}
	return created, nil
}

// GetWorkspace returns one workspace, or not-found.
func GetWorkspace(ctx context.Context, q storage.Querier, userID, projectID, name string) (Workspace, error) {
	w, found, err := getWorkspace(ctx, q, userID, projectID, name, "")
	if err == nil && !found {
		err = problems.NotFound.New("no workspace named %q in this project", name)
	}
	return w, err
}

func getWorkspace(ctx context.Context, q storage.Querier, userID, projectID, name, lock string) (Workspace, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+workspaceCols+` FROM workspaces
		WHERE user_id = $1 AND project_id = $2 AND name = $3 `+lock, userID, projectID, name)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("query workspace: %w", err)
	}
	w, err := pgx.CollectExactlyOneRow(rows, scanWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, fmt.Errorf("read workspace: %w", err)
	}
	return w, true, nil
}

// SetWorkspace saves a layout. Without ifMatch it only creates (an existing workspace answers
// precondition-required); with ifMatch the workspace must exist at that revision.
func SetWorkspace(ctx context.Context, tx pgx.Tx, userID, projectID, name string, ifMatch *int, in WorkspaceInput) (Workspace, []events.Draft, error) {
	cur, found, err := getWorkspace(ctx, tx, userID, projectID, name, "FOR UPDATE")
	if err != nil {
		return Workspace{}, nil, err
	}
	var w Workspace
	switch {
	case !found && ifMatch != nil:
		return Workspace{}, nil, problems.Stale(0, "workspace %q does not exist yet; omit If-Match to create it", name)
	case !found:
		w, found, err = insertWorkspace(ctx, tx, userID, projectID, name, in)
		if err != nil {
			return Workspace{}, nil, err
		}
		if !found { // created concurrently between our read and insert
			cur, _, err = getWorkspace(ctx, tx, userID, projectID, name, "")
			if err != nil {
				return Workspace{}, nil, err
			}
			return Workspace{}, nil, problems.Stale(cur.Rev, "workspace %q was just created by another save; re-read it and retry", name)
		}
	case ifMatch == nil:
		return Workspace{}, nil, commands.Precondition("workspace")
	default:
		if err := commands.CheckRev(WorkspaceKind, *ifMatch, cur.Rev); err != nil {
			return Workspace{}, nil, err
		}
		rows, err := tx.Query(ctx, `UPDATE workspaces SET schema_version = $2, layout = $3, panels = $4, rev = rev + 1,
			updated_at = now() WHERE id = $1 RETURNING `+workspaceCols, cur.ID, in.SchemaVersion, in.Layout, in.Panels)
		if err != nil {
			return Workspace{}, nil, fmt.Errorf("update workspace: %w", err)
		}
		if w, err = pgx.CollectExactlyOneRow(rows, scanWorkspace); err != nil {
			return Workspace{}, nil, fmt.Errorf("update workspace: %w", err)
		}
	}
	return w, []events.Draft{{
		Topic:     events.EntityTopic(WorkspaceKind, w.ID),
		Type:      "workspace.set",
		ProjectID: projectID,
		Entity:    &events.EntityRef{Kind: WorkspaceKind, ID: w.ID, Rev: w.Rev},
		// The summary without the layout: layouts can be large, and only the owner's shell needs them.
		Payload: map[string]any{"workspace": map[string]any{"id": w.ID, "name": w.Name, "rev": w.Rev, "updatedAt": w.UpdatedAt}},
	}}, nil
}

func insertWorkspace(ctx context.Context, tx pgx.Tx, userID, projectID, name string, in WorkspaceInput) (Workspace, bool, error) {
	id := "wsp_" + uuid.Must(uuid.NewV7()).String()
	rows, err := tx.Query(ctx, `INSERT INTO workspaces (id, user_id, project_id, name, schema_version, layout, panels)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (user_id, project_id, name) DO NOTHING RETURNING `+workspaceCols,
		id, userID, projectID, name, in.SchemaVersion, in.Layout, in.Panels)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("insert workspace: %w", err)
	}
	w, err := pgx.CollectExactlyOneRow(rows, scanWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, false, nil
	}
	if err != nil {
		return Workspace{}, false, fmt.Errorf("insert workspace: %w", err)
	}
	return w, true, nil
}
