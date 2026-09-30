// Package mixes holds mixes: project work with revisions (docs/spec/08-resolutions.md R13) — named groups of frozen
// dataset versions with weights, a sampling temperature and a replay share. A mix is draftable: an agent's edit
// lands as a draft (internal/drafts) under the project's draft policy.
//
// Mutations take a transaction from the command pipeline and return the events they emit; reads take any
// storage.Querier. Every revision's content is kept in mix_revisions, so a run can record the revision it used and
// a draft can diff against its base.
package mixes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/drafts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the snake-singular entity kind used in topics, entity references and drafts.
const Kind = "mix"

// Event types on entity.mix.{id}.
const (
	EventCreated = "mix.created"
	EventRevised = "mix.revised"
)

// Group is one group of a mix: dataset versions (ver_ ids) sampled together with one weight.
type Group struct {
	Name     string   `json:"name"`
	Weight   float64  `json:"weight"`
	Replay   bool     `json:"replay"`
	Datasets []string `json:"datasets"`
}

// Content is what a revision of a mix holds (the contract's MixNew with defaults filled in and dataset references
// resolved). It is also a mix draft's content.
type Content struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Groups      []Group `json:"groups"`
	Temperature float64 `json:"temperature"`
	ReplayShare float64 `json:"replayShare"`
}

// Mix is a mix at its current revision.
type Mix struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Content
	Rev       int           `json:"rev"`
	CreatedBy auth.Actor    `json:"createdBy"`
	UpdatedBy auth.Actor    `json:"updatedBy"`
	Cause     *drafts.Cause `json:"cause,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

const mixCols = "id, project_id, content, rev, created_by, updated_by, cause, created_at, updated_at"

func scan(row pgx.CollectableRow) (Mix, error) {
	var (
		m       Mix
		content []byte
	)
	if err := row.Scan(&m.ID, &m.ProjectID, &content, &m.Rev, &m.CreatedBy, &m.UpdatedBy, &m.Cause, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return Mix{}, err
	}
	if err := json.Unmarshal(content, &m.Content); err != nil {
		return Mix{}, fmt.Errorf("decode mix %s content: %w", m.ID, err)
	}
	return m, nil
}

func one(rows pgx.Rows, err error, id string) (Mix, error) {
	if err != nil {
		return Mix{}, fmt.Errorf("query mix: %w", err)
	}
	m, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mix{}, problems.NotFound.New("no mix %q", id)
	}
	if err != nil {
		return Mix{}, fmt.Errorf("read mix: %w", err)
	}
	return m, nil
}

// Get returns mix id, or not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Mix, error) {
	rows, err := q.Query(ctx, "SELECT "+mixCols+" FROM mixes WHERE id = $1", id)
	return one(rows, err, id)
}

// Lock reads mix id and locks it until tx ends.
func Lock(ctx context.Context, tx pgx.Tx, id string) (Mix, error) {
	rows, err := tx.Query(ctx, "SELECT "+mixCols+" FROM mixes WHERE id = $1 FOR UPDATE", id)
	return one(rows, err, id)
}

// List returns the project's mixes, most recently changed first.
func List(ctx context.Context, q storage.Querier, projectID string) ([]Mix, error) {
	rows, err := q.Query(ctx, "SELECT "+mixCols+" FROM mixes WHERE project_id = $1 ORDER BY updated_at DESC, id DESC", projectID)
	if err != nil {
		return nil, fmt.Errorf("list mixes: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list mixes: %w", err)
	}
	return out, nil
}

// RevisionContent returns the content of revision rev of mix id.
func RevisionContent(ctx context.Context, q storage.Querier, id string, rev int) (json.RawMessage, error) {
	var c json.RawMessage
	err := q.QueryRow(ctx, "SELECT content FROM mix_revisions WHERE mix_id = $1 AND rev = $2", id, rev).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problems.NotFound.New("mix %s has no revision %d", id, rev)
	}
	if err != nil {
		return nil, fmt.Errorf("read mix revision: %w", err)
	}
	return c, nil
}

func causeJSON(c drafts.Cause) any {
	if c.Empty() {
		return nil
	}
	return c
}

// insert writes a new mix at revision 1; a name the project already uses is a conflict.
func insert(ctx context.Context, tx pgx.Tx, projectID string, c Content, actor auth.Actor, cause drafts.Cause) (Mix, error) {
	id := "mix_" + uuid.Must(uuid.NewV7()).String()
	rows, err := tx.Query(ctx, `INSERT INTO mixes (id, project_id, content, created_by, updated_by, cause)
		VALUES ($1, $2, $3, $4, $4, $5) RETURNING `+mixCols, id, projectID, c, actor, causeJSON(cause))
	m, err := one(rows, err, id)
	if err != nil {
		return Mix{}, nameTaken(err, c.Name)
	}
	return m, writeRevision(ctx, tx, m)
}

// revise writes c as the next revision of mix id (locked by the caller).
func revise(ctx context.Context, tx pgx.Tx, id string, c Content, actor auth.Actor, cause drafts.Cause) (Mix, error) {
	rows, err := tx.Query(ctx, `UPDATE mixes SET content = $2, rev = rev + 1, updated_by = $3, cause = $4, updated_at = now()
		WHERE id = $1 RETURNING `+mixCols, id, c, actor, causeJSON(cause))
	m, err := one(rows, err, id)
	if err != nil {
		return Mix{}, nameTaken(err, c.Name)
	}
	return m, writeRevision(ctx, tx, m)
}

func writeRevision(ctx context.Context, tx pgx.Tx, m Mix) error {
	if _, err := tx.Exec(ctx, `INSERT INTO mix_revisions (mix_id, rev, content, actor, cause) VALUES ($1, $2, $3, $4, $5)`,
		m.ID, m.Rev, m.Content, m.UpdatedBy, m.Cause); err != nil {
		return fmt.Errorf("record mix revision: %w", err)
	}
	return nil
}

func nameTaken(err error, name string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return problems.Conflict.New("the project already has a mix named %q; pick another name", name)
	}
	return err
}
