// Package projects holds projects, their agent profiles and the per-user workspaces saved in them.
//
// Mutations take a transaction from the command pipeline and return the events they emit; reads take any
// storage.Querier. The project repository itself (git) is internal/repos; what is written into it is
// internal/projects/layout; the bootstrap job and the commands that commit to it are internal/projects/bootstrap.
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
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the snake-singular entity kind used in topics and entity references.
const Kind = "project"

// States of a project (archived projects keep theirs and carry archivedAt).
const (
	StateBootstrapping = "bootstrapping"
	StateActive        = "active"
	StateFailed        = "failed"
)

// Repository kinds.
const (
	RepoInternal = "internal"
	RepoGitHub   = "github"
	RepoURL      = "url"
)

// Project is a unit of work. Its JSON form is the contract's Project (events carry it as is).
type Project struct {
	ID             string      `json:"id"`
	Slug           string      `json:"slug"`
	Name           string      `json:"name"`
	Description    string      `json:"description,omitempty"`
	Rev            int         `json:"rev"`
	State          string      `json:"state"`
	Locales        []string    `json:"locales"`
	Domain         string      `json:"domain"`
	BaseModel      *BaseModel  `json:"baseModel,omitempty"`
	Repository     *Repository `json:"repository,omitempty"`
	Budgets        Budgets     `json:"budgets"`
	BootstrapJobID string      `json:"bootstrapJobId,omitempty"`
	BootstrapError string      `json:"bootstrapError,omitempty"`
	ArchivedAt     *time.Time  `json:"archivedAt,omitempty"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}

// Archived reports whether the project is archived.
func (p Project) Archived() bool { return p.ArchivedAt != nil }

// BaseModel is the project's default base model, joined from the registry.
type BaseModel struct {
	VersionID string `json:"versionId"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	HFRepo    string `json:"hfRepo"`
	Revision  string `json:"revision"`
}

// Repository is where the project repository lives. Remote and Secret are set for github and url kinds.
type Repository struct {
	Kind      string `json:"kind"`
	Branch    string `json:"branch"`
	CloneURL  string `json:"cloneUrl"`
	Remote    string `json:"remote,omitempty"`
	Secret    string `json:"secret,omitempty"`
	PushError string `json:"pushError,omitempty"`
}

// storedRepo is the repository column.
type storedRepo struct {
	Kind      string `json:"kind,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Secret    string `json:"secret,omitempty"`
	PushError string `json:"pushError,omitempty"`
}

// Budgets are the project's daily budgets.
type Budgets struct {
	GPUHoursPerDay    float64 `json:"gpuHoursPerDay"`
	AgentTokensPerDay int64   `json:"agentTokensPerDay"`
	// QueuePriority orders the project's waiting step jobs against other projects' (higher first; internal/workers).
	QueuePriority int `json:"queuePriority"`
}

// DefaultBudgets are the budgets of defaults.yaml.
func DefaultBudgets() Budgets {
	d := defaults.Get().Budgets
	return Budgets{GPUHoursPerDay: d.GPUHoursPerProjectPerDay.Value, AgentTokensPerDay: d.AgentTokensPerProjectPerDay.Value,
		QueuePriority: d.QueuePriorityPerProject.Value}
}

// CloneURL is the path a project repository is served at over smart HTTP (internal/repos.HTTPPath).
func CloneURL(slug string) string { return "/git/" + slug + ".git" }

const projectSelect = `SELECT p.id, p.slug, p.name, p.description, p.rev, p.state, p.locales, p.domain, p.repository,
	p.budgets, coalesce(p.bootstrap_job_id, ''), coalesce(p.bootstrap_error, ''), p.archived_at, p.created_at, p.updated_at,
	coalesce(v.id, ''), coalesce(c.name, ''), coalesce(v.version, ''), coalesce(v.payload->>'hfRepo', ''),
	coalesce(v.payload->>'revision', '')
	FROM projects p
	LEFT JOIN registry_versions v ON v.id = p.base_model_version_id
	LEFT JOIN registry_collections c ON c.id = v.collection_id`

func scanProject(row pgx.CollectableRow) (Project, error) {
	var (
		p       Project
		repo    storedRepo
		budgets json.RawMessage
		bm      BaseModel
	)
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.Rev, &p.State, &p.Locales, &p.Domain, &repo, &budgets,
		&p.BootstrapJobID, &p.BootstrapError, &p.ArchivedAt, &p.CreatedAt, &p.UpdatedAt,
		&bm.VersionID, &bm.Name, &bm.Version, &bm.HFRepo, &bm.Revision)
	if err != nil {
		return Project{}, err
	}
	if bm.VersionID != "" {
		p.BaseModel = &bm
	}
	if repo.Kind != "" {
		p.Repository = &Repository{Kind: repo.Kind, Branch: "main", CloneURL: CloneURL(p.Slug), Remote: repo.Remote,
			Secret: repo.Secret, PushError: repo.PushError}
	}
	p.Budgets = DefaultBudgets()
	if len(budgets) > 0 {
		if err := json.Unmarshal(budgets, &p.Budgets); err != nil {
			return Project{}, fmt.Errorf("decode budgets of %s: %w", p.Slug, err)
		}
	}
	if p.Locales == nil {
		p.Locales = []string{}
	}
	return p, nil
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
	rows, err := q.Query(ctx, projectSelect+" WHERE $1 OR p.archived_at IS NULL ORDER BY p.created_at DESC, p.id DESC", archived)
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
	rows, err := q.Query(ctx, projectSelect+" WHERE p.slug = $1", slug)
	return existing(rows, err, slug)
}

// GetByID returns the project with id, or not-found.
func GetByID(ctx context.Context, q storage.Querier, id string) (Project, error) {
	rows, err := q.Query(ctx, projectSelect+" WHERE p.id = $1", id)
	return existing(rows, err, id)
}

// NewInput is a project as the wizard chose it (defaults already applied by the caller).
type NewInput struct {
	Slug, Name, Description string
	Locales                 []string
	Domain                  string
	BaseModelVersionID      string
	RepoKind                string
	Remote, Secret          string
	Budgets                 Budgets
	// State is bootstrapping for projects the bootstrap job will set up (active when empty).
	State string
}

// Create inserts a project at revision 1; a taken slug is a conflict. It returns the project and its
// project.created event.
func Create(ctx context.Context, tx pgx.Tx, in NewInput) (Project, []events.Draft, error) {
	id := "prj_" + uuid.Must(uuid.NewV7()).String()
	state := in.State
	if state == "" {
		state = StateActive
	}
	repo := storedRepo{Kind: in.RepoKind, Remote: in.Remote, Secret: in.Secret}
	budgets, err := json.Marshal(in.Budgets)
	if err != nil {
		return Project{}, nil, fmt.Errorf("marshal budgets: %w", err)
	}
	locales := in.Locales
	if locales == nil {
		locales = []string{}
	}
	var got string
	err = tx.QueryRow(ctx, `INSERT INTO projects (id, slug, name, description, state, locales, domain, base_model_version_id,
			repository, budgets)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10) ON CONFLICT (slug) DO NOTHING RETURNING id`,
		id, in.Slug, in.Name, in.Description, state, locales, in.Domain, in.BaseModelVersionID, repo, budgets).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, nil, problems.Conflict.New("a project with slug %q already exists; pick another slug", in.Slug)
	}
	if err != nil {
		return Project{}, nil, fmt.Errorf("insert project: %w", err)
	}
	p, err := GetByID(ctx, tx, id)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.created"), nil
}

// AttachJob records the job that bootstraps the project, in the transaction that created both; it does not
// change the revision (the project is still at its first).
func AttachJob(ctx context.Context, tx pgx.Tx, id, jobID string) (Project, error) {
	if _, err := tx.Exec(ctx, "UPDATE projects SET bootstrap_job_id = $2 WHERE id = $1", id, jobID); err != nil {
		return Project{}, fmt.Errorf("attach bootstrap job: %w", err)
	}
	return GetByID(ctx, tx, id)
}

// EditInput is the body of projects.edit; nil fields stay as they are.
type EditInput struct {
	Name, Description  *string
	Locales            []string
	Domain             *string
	BaseModelVersionID *string
	Budgets            *BudgetsEdit
}

// BudgetsEdit changes some budgets.
type BudgetsEdit struct {
	GPUHoursPerDay    *float64
	AgentTokensPerDay *int64
	QueuePriority     *int
}

// Facts reports whether the edit changes something the repository renders (project.yaml, AGENTS.md).
func (in EditInput) Facts() bool {
	return in.Name != nil || in.Description != nil || in.Locales != nil || in.Domain != nil ||
		in.BaseModelVersionID != nil || in.Budgets != nil
}

// Edit changes the project at revision rev.
func Edit(ctx context.Context, tx pgx.Tx, slug string, rev int, in EditInput) (Project, []events.Draft, error) {
	cur, err := lockAt(ctx, tx, slug, rev)
	if err != nil {
		return Project{}, nil, err
	}
	if cur.Archived() {
		return Project{}, nil, problems.Conflict.New("project %q is archived and read-only", slug)
	}
	budgets := cur.Budgets
	if in.Budgets != nil {
		if in.Budgets.GPUHoursPerDay != nil {
			budgets.GPUHoursPerDay = *in.Budgets.GPUHoursPerDay
		}
		if in.Budgets.AgentTokensPerDay != nil {
			budgets.AgentTokensPerDay = *in.Budgets.AgentTokensPerDay
		}
		if in.Budgets.QueuePriority != nil {
			budgets.QueuePriority = *in.Budgets.QueuePriority
		}
	}
	b, err := json.Marshal(budgets)
	if err != nil {
		return Project{}, nil, fmt.Errorf("marshal budgets: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET name = coalesce($2, name), description = coalesce($3, description),
			locales = coalesce($4, locales), domain = coalesce($5, domain),
			base_model_version_id = coalesce($6, base_model_version_id), budgets = $7, rev = rev + 1, updated_at = now()
		WHERE id = $1`, cur.ID, in.Name, in.Description, in.Locales, in.Domain, in.BaseModelVersionID, b); err != nil {
		return Project{}, nil, fmt.Errorf("edit project: %w", err)
	}
	p, err := GetByID(ctx, tx, cur.ID)
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
	if cur.Archived() {
		return Project{}, nil, problems.Conflict.New("project %q is already archived", slug)
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET archived_at = now(), rev = rev + 1, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
		return Project{}, nil, fmt.Errorf("archive project: %w", err)
	}
	p, err := GetByID(ctx, tx, cur.ID)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.archived"), nil
}

// Bootstrapped marks a bootstrapping project active (revision + 1) and returns its project.bootstrapped event.
func Bootstrapped(ctx context.Context, tx pgx.Tx, id string) (Project, []events.Draft, error) {
	if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'active', bootstrap_error = NULL, rev = rev + 1, updated_at = now()
		WHERE id = $1`, id); err != nil {
		return Project{}, nil, fmt.Errorf("mark project active: %w", err)
	}
	p, err := GetByID(ctx, tx, id)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.bootstrapped"), nil
}

// BootstrapFailed marks the project failed with the reason (revision + 1).
func BootstrapFailed(ctx context.Context, tx pgx.Tx, id, reason string) (Project, []events.Draft, error) {
	if _, err := tx.Exec(ctx, `UPDATE projects SET state = 'failed', bootstrap_error = $2, rev = rev + 1, updated_at = now()
		WHERE id = $1`, id, reason); err != nil {
		return Project{}, nil, fmt.Errorf("mark project failed: %w", err)
	}
	p, err := GetByID(ctx, tx, id)
	if err != nil {
		return Project{}, nil, err
	}
	return p, projectEvent(p, "project.bootstrap_failed"), nil
}

// SetRemote records the remote a github or url repository mirrors to (the bootstrap job learns it when it creates
// the GitHub repository); the revision does not move.
func SetRemote(ctx context.Context, tx pgx.Tx, id, remote string) error {
	if _, err := tx.Exec(ctx, `UPDATE projects SET repository = jsonb_set(repository, '{remote}', to_jsonb($2::text))
		WHERE id = $1`, id, remote); err != nil {
		return fmt.Errorf("set remote: %w", err)
	}
	return nil
}

// SetPushError records whether the last push to the remote failed (empty clears it). It is a status, not an edit:
// the revision does not move, and the event carries the project so open panels show it.
func SetPushError(ctx context.Context, tx pgx.Tx, id, msg string) (Project, []events.Draft, error) {
	tag, err := tx.Exec(ctx, `UPDATE projects SET repository = CASE WHEN $2 = '' THEN repository - 'pushError'
			ELSE jsonb_set(repository, '{pushError}', to_jsonb($2::text)) END
		WHERE id = $1 AND coalesce(repository->>'pushError', '') <> $2`, id, msg)
	if err != nil {
		return Project{}, nil, fmt.Errorf("set push error: %w", err)
	}
	p, err := GetByID(ctx, tx, id)
	if err != nil || tag.RowsAffected() == 0 {
		return p, nil, err
	}
	return p, projectEvent(p, "project.push_status"), nil
}

// lockAt locks the project row and checks that it is at revision rev.
func lockAt(ctx context.Context, tx pgx.Tx, slug string, rev int) (Project, error) {
	var id string
	var cur int
	err := tx.QueryRow(ctx, "SELECT id, rev FROM projects WHERE slug = $1 FOR UPDATE", slug).Scan(&id, &cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, problems.NotFound.New("no project with slug %q", slug)
	}
	if err != nil {
		return Project{}, fmt.Errorf("lock project: %w", err)
	}
	if err := commands.CheckRev(Kind, rev, cur); err != nil {
		return Project{}, err
	}
	return GetByID(ctx, tx, id)
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
