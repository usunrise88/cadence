// Package projects holds projects and the per-user workspaces saved in them.
//
// Mutations take a transaction from the command pipeline and return the events they emit; reads take any
// storage.Querier.
package projects

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

// Kind is the snake-singular entity kind used in topics and entity references.
const Kind = "project"

// Project is a unit of work. Its JSON form is the contract's Project.
type Project struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Rev         int        `json:"rev"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

const projectCols = "id, slug, name, description, rev, archived_at, created_at, updated_at"

func scanProject(row pgx.CollectableRow) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.Rev, &p.ArchivedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// one reads a single project row; found is false when the query returned none.
func one(rows pgx.Rows, err error) (p Project, found bool, _ error) {
	if err != nil {
		return Project{}, false, fmt.Errorf("query project: %w", err)
	}
	p, err = pgx.CollectExactlyOneRow(rows, scanProject)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("read project: %w", err)
	}
	return p, true, nil
}

// existing is one for queries on a slug that must exist.
func existing(rows pgx.Rows, err error, slug string) (Project, error) {
	p, found, err := one(rows, err)
	if err == nil && !found {
		err = problems.NotFound.New("no project with slug %q", slug)
	}
	return p, err
}

// List returns projects, newest first; archived ones only when asked.
func List(ctx context.Context, q storage.Querier, archived bool) ([]Project, error) {
	rows, err := q.Query(ctx, "SELECT "+projectCols+" FROM projects WHERE $1 OR archived_at IS NULL ORDER BY created_at DESC, id DESC", archived)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanProject)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// Get returns the project with slug, or not-found.
func Get(ctx context.Context, q storage.Querier, slug string) (Project, error) {
	rows, err := q.Query(ctx, "SELECT "+projectCols+" FROM projects WHERE slug = $1", slug)
	return existing(rows, err, slug)
}

// NewInput is the body of projects.new.
type NewInput struct {
	Slug, Name, Description string
}

// Create inserts a project at revision 1; a taken slug is a conflict.
func Create(ctx context.Context, tx pgx.Tx, in NewInput) (Project, []events.Draft, error) {
	id := "prj_" + uuid.Must(uuid.NewV7()).String()
	rows, err := tx.Query(ctx, `INSERT INTO projects (id, slug, name, description) VALUES ($1, $2, $3, $4)
		ON CONFLICT (slug) DO NOTHING RETURNING `+projectCols, id, in.Slug, in.Name, in.Description)
	p, found, err := one(rows, err)
	if err != nil {
		return Project{}, nil, err
	}
	if !found {
		return Project{}, nil, problems.Conflict.New("a project with slug %q already exists; pick another slug", in.Slug)
	}
	return p, projectEvent(p, "project.created"), nil
}

// EditInput is the body of projects.edit; nil fields stay as they are.
type EditInput struct {
	Name, Description *string
}

// Edit renames or re-describes the project at revision rev.
func Edit(ctx context.Context, tx pgx.Tx, slug string, rev int, in EditInput) (Project, []events.Draft, error) {
	if _, err := lockAt(ctx, tx, slug, rev); err != nil {
		return Project{}, nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE projects SET name = coalesce($2, name), description = coalesce($3, description),
		rev = rev + 1, updated_at = now() WHERE slug = $1 RETURNING `+projectCols, slug, in.Name, in.Description)
	p, err := existing(rows, err, slug)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.edited"), nil
}

// Archive soft-deletes the project at revision rev; archiving an archived project is a conflict.
func Archive(ctx context.Context, tx pgx.Tx, slug string, rev int) (Project, []events.Draft, error) {
	cur, err := lockAt(ctx, tx, slug, rev)
	if err != nil {
		return Project{}, nil, err
	}
	if cur.ArchivedAt != nil {
		return Project{}, nil, problems.Conflict.New("project %q is already archived", slug)
	}
	rows, err := tx.Query(ctx, `UPDATE projects SET archived_at = now(), rev = rev + 1, updated_at = now()
		WHERE slug = $1 RETURNING `+projectCols, slug)
	p, err := existing(rows, err, slug)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.archived"), nil
}

// lockAt locks the project row and checks that it is at revision rev.
func lockAt(ctx context.Context, tx pgx.Tx, slug string, rev int) (Project, error) {
	rows, err := tx.Query(ctx, "SELECT "+projectCols+" FROM projects WHERE slug = $1 FOR UPDATE", slug)
	cur, err := existing(rows, err, slug)
	if err != nil {
		return Project{}, err
	}
	return cur, commands.CheckRev(Kind, rev, cur.Rev)
}

// Project events carry their own id as projectId, so a client filtering by project sees them, and the whole
// project as the API returns it under "project", so a client patches its cache without a re-fetch.
func projectEvent(p Project, typ string) []events.Draft {
	return []events.Draft{{
		Topic:     events.EntityTopic(Kind, p.ID),
		Type:      typ,
		ProjectID: p.ID,
		Entity:    &events.EntityRef{Kind: Kind, ID: p.ID, Rev: p.Rev},
		Payload:   map[string]any{"project": p},
	}}
}
