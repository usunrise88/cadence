package drafts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Event types on entity.{kind}.{id}.
const (
	EventCreated         = "draft.created"
	EventUpdated         = "draft.updated"
	EventAccepted        = "draft.accepted"
	EventReverted        = "draft.reverted"
	EventPresenceChanged = "presence.changed"
)

// Store keeps drafts of the registered kinds.
type Store struct {
	kinds  map[string]Kind
	now    func() time.Time
	window time.Duration // how long a direct agent edit counts as editing
}

// NewStore returns a store; window is the presence window of direct agent edits (drafts.presence_seconds).
func NewStore(now func() time.Time, window time.Duration) *Store {
	return &Store{kinds: map[string]Kind{}, now: now, window: window}
}

// Register makes kind draftable.
func (s *Store) Register(kind string, k Kind) { s.kinds[kind] = k }

func (s *Store) kind(name string) (Kind, error) {
	k, ok := s.kinds[name]
	if !ok {
		return nil, problems.BadRequest.New("%q is not a draftable kind", name)
	}
	return k, nil
}

const draftCols = `id, project_id, entity_kind, entity_id, base_rev, rev, state, content, author, tool_call_id,
	created_at, updated_at, decided_by, decided_at, applied_rev`

func scan(row pgx.CollectableRow) (Draft, error) {
	var d Draft
	err := row.Scan(&d.ID, &d.ProjectID, &d.EntityKind, &d.EntityID, &d.BaseRev, &d.Rev, &d.State, &d.Content, &d.Author,
		&d.ToolCallID, &d.CreatedAt, &d.UpdatedAt, &d.DecidedBy, &d.DecidedAt, &d.AppliedRev)
	return d, err
}

func one(rows pgx.Rows, err error) (Draft, bool, error) {
	if err != nil {
		return Draft{}, false, fmt.Errorf("query draft: %w", err)
	}
	d, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, false, nil
	}
	if err != nil {
		return Draft{}, false, fmt.Errorf("read draft: %w", err)
	}
	return d, true, nil
}

// authorKey is what makes a draft "this author's": the actor and, for agents, the session.
func authorKey(a auth.Actor) string { return a.Kind + ":" + a.ID + "/" + a.SessionID }

// complete fills in the entity's current revision, staleness and the changes against the draft's base.
func (s *Store) complete(ctx context.Context, q storage.Querier, d Draft, current int) (Draft, error) {
	k, err := s.kind(d.EntityKind)
	if err != nil {
		return Draft{}, err
	}
	d.CurrentRev = current
	d.Stale = d.State == StateOpen && current != d.BaseRev
	base, err := k.Content(ctx, q, d.EntityID, d.BaseRev)
	if err != nil {
		return Draft{}, err
	}
	if d.Changes, err = Diff(base, d.Content); err != nil {
		return Draft{}, err
	}
	return d, nil
}

// Entity reads an entity of a draftable kind (handlers check its project before listing its drafts).
func (s *Store) Entity(ctx context.Context, q storage.Querier, kind, id string) (Entity, error) {
	k, err := s.kind(kind)
	if err != nil {
		return Entity{}, err
	}
	return k.Get(ctx, q, id)
}

// Get returns draft id with its changes.
func (s *Store) Get(ctx context.Context, q storage.Querier, id string) (Draft, error) {
	d, found, err := one(q.Query(ctx, "SELECT "+draftCols+" FROM drafts WHERE id = $1", id))
	if err != nil {
		return Draft{}, err
	}
	if !found {
		return Draft{}, problems.NotFound.New("no draft %q", id)
	}
	k, err := s.kind(d.EntityKind)
	if err != nil {
		return Draft{}, err
	}
	e, err := k.Get(ctx, q, d.EntityID)
	if err != nil {
		return Draft{}, err
	}
	return s.complete(ctx, q, d, e.Rev)
}

// List returns the drafts of one entity in state (empty or "all": every state), newest first.
func (s *Store) List(ctx context.Context, q storage.Querier, kind, entityID, state string) ([]Draft, error) {
	k, err := s.kind(kind)
	if err != nil {
		return nil, err
	}
	e, err := k.Get(ctx, q, entityID)
	if err != nil {
		return nil, err
	}
	if state == "all" {
		state = ""
	}
	rows, err := q.Query(ctx, "SELECT "+draftCols+` FROM drafts WHERE entity_kind = $1 AND entity_id = $2
		AND ($3 = '' OR state = $3) ORDER BY created_at DESC, id DESC`, kind, entityID, state)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	list, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	for i := range list {
		if list[i], err = s.complete(ctx, q, list[i], e.Rev); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Open returns author's open draft of the entity, locked until tx ends; found is false when there is none.
func (s *Store) Open(ctx context.Context, tx pgx.Tx, kind, entityID string, author auth.Actor) (Draft, bool, error) {
	return one(tx.Query(ctx, "SELECT "+draftCols+` FROM drafts
		WHERE entity_kind = $1 AND entity_id = $2 AND author_key = $3 AND state = 'open' FOR UPDATE`,
		kind, entityID, authorKey(author)))
}

// Working returns the content an author's next edit applies to: the author's open draft carried over to the
// entity's current revision (Rebase), or the entity's own content when there is no open draft.
func (s *Store) Working(ctx context.Context, tx pgx.Tx, kind, entityID string, author auth.Actor, e Entity) (json.RawMessage, error) {
	k, err := s.kind(kind)
	if err != nil {
		return nil, err
	}
	d, found, err := s.Open(ctx, tx, kind, entityID, author)
	if err != nil || !found {
		return e.Content, err
	}
	if d.BaseRev == e.Rev {
		return d.Content, nil
	}
	base, err := k.Content(ctx, tx, entityID, d.BaseRev)
	if err != nil {
		return nil, err
	}
	return Rebase(base, d.Content, e.Content)
}

// SaveInput is an author's edit that lands as a draft.
type SaveInput struct {
	Kind       string
	EntityID   string
	Entity     Entity          // the entity as locked by the caller
	Content    json.RawMessage // the full content the draft proposes, already validated by the kind
	Author     auth.Actor
	ToolCallID string
}

// Save creates the author's draft of the entity, or updates the open one (rev + 1, rebased onto the entity's
// current revision). The caller holds the entity's lock (Kind.Lock) and has checked the edit's If-Match against
// it. It emits draft.created or draft.updated and presence.changed.
func (s *Store) Save(ctx context.Context, tx pgx.Tx, in SaveInput) (Draft, []events.Draft, error) {
	cur, found, err := s.Open(ctx, tx, in.Kind, in.EntityID, in.Author)
	if err != nil {
		return Draft{}, nil, err
	}
	var (
		d   Draft
		typ string
	)
	if found {
		typ = EventUpdated
		d, found, err = one(tx.Query(ctx, `UPDATE drafts SET content = $2, base_rev = $3, rev = rev + 1, tool_call_id = $4,
			updated_at = now() WHERE id = $1 RETURNING `+draftCols, cur.ID, in.Content, in.Entity.Rev, in.ToolCallID))
	} else {
		typ = EventCreated
		id := "drf_" + uuid.Must(uuid.NewV7()).String()
		d, found, err = one(tx.Query(ctx, `INSERT INTO drafts (id, project_id, entity_kind, entity_id, base_rev, content,
			author, author_key, tool_call_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+draftCols,
			id, in.Entity.ProjectID, in.Kind, in.EntityID, in.Entity.Rev, in.Content, in.Author, authorKey(in.Author), in.ToolCallID))
	}
	if err == nil && !found {
		err = errors.New("save draft: no row returned")
	}
	if err != nil {
		return Draft{}, nil, err
	}
	if d, err = s.complete(ctx, tx, d, in.Entity.Rev); err != nil {
		return Draft{}, nil, err
	}
	evs, err := s.draftEvents(ctx, tx, d, in.Entity, typ)
	if err != nil {
		return Draft{}, nil, err
	}
	return d, evs, nil
}

// lockDraft locks draft id and checks the If-Match revision and that it is still open.
func (s *Store) lockDraft(ctx context.Context, tx pgx.Tx, id string, rev int, to string) (Draft, Kind, error) {
	d, found, err := one(tx.Query(ctx, "SELECT "+draftCols+" FROM drafts WHERE id = $1 FOR UPDATE", id))
	if err != nil {
		return Draft{}, nil, err
	}
	if !found {
		return Draft{}, nil, problems.NotFound.New("no draft %q", id)
	}
	if err := auth.CheckProject(ctx, d.ProjectID); err != nil {
		return Draft{}, nil, err
	}
	if err := commands.CheckRev(EntityKind, rev, d.Rev); err != nil {
		return Draft{}, nil, err
	}
	if err := Transition(d.State, to); err != nil {
		return Draft{}, nil, err
	}
	k, err := s.kind(d.EntityKind)
	return d, k, err
}

// decide closes the draft in state to, by actor.
func decide(ctx context.Context, tx pgx.Tx, id, to string, actor auth.Actor) (Draft, error) {
	d, found, err := one(tx.Query(ctx, `UPDATE drafts SET state = $2, rev = rev + 1, decided_by = $3, decided_at = now(),
		updated_at = now() WHERE id = $1 RETURNING `+draftCols, id, to, actor))
	if err == nil && !found {
		err = fmt.Errorf("decide draft %s: gone", id)
	}
	return d, err
}

// Accept applies draft id (at draft revision rev) as the next revision of its entity, attributed to actor with the
// draft as cause. A draft whose base is no longer the entity's revision is draft-stale with the entity's current
// revision. It returns the accepted draft, the entity as the API shows it, and the kind's events plus
// draft.accepted and presence.changed.
func (s *Store) Accept(ctx context.Context, tx pgx.Tx, id string, rev int, actor auth.Actor) (Draft, any, []events.Draft, error) {
	d, k, err := s.lockDraft(ctx, tx, id, rev, StateAccepted)
	if err != nil {
		return Draft{}, nil, nil, err
	}
	e, err := k.Lock(ctx, tx, d.EntityID)
	if err != nil {
		return Draft{}, nil, nil, err
	}
	if e.Rev != d.BaseRev {
		pe := problems.DraftStale.New("the %s moved to revision %d after this draft was made on revision %d; revert the draft "+
			"or have its author redo the edit on revision %d", d.EntityKind, e.Rev, d.BaseRev, e.Rev)
		pe.CurrentRev = &e.Rev
		return Draft{}, nil, nil, pe
	}
	author := d.Author
	closed, err := decide(ctx, tx, id, StateAccepted, actor)
	if err != nil {
		return Draft{}, nil, nil, err
	}
	newRev, body, evs, err := k.Apply(ctx, tx, d.EntityID, d.Content, actor, Cause{DraftID: d.ID, DraftAuthor: &author, ToolCallID: d.ToolCallID})
	if err != nil {
		return Draft{}, nil, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE drafts SET applied_rev = $2 WHERE id = $1", id, newRev); err != nil {
		return Draft{}, nil, nil, fmt.Errorf("record applied revision: %w", err)
	}
	closed.AppliedRev = &newRev
	if closed, err = s.complete(ctx, tx, closed, newRev); err != nil {
		return Draft{}, nil, nil, err
	}
	e.Rev, e.UpdatedBy, e.UpdatedAt, e.ToolCallID = newRev, actor, s.now(), ""
	own, err := s.draftEvents(ctx, tx, closed, e, EventAccepted)
	if err != nil {
		return Draft{}, nil, nil, err
	}
	return closed, body, append(evs, own...), nil
}

// Revert discards draft id (at draft revision rev); the entity does not change. It emits draft.reverted and
// presence.changed.
func (s *Store) Revert(ctx context.Context, tx pgx.Tx, id string, rev int, actor auth.Actor) (Draft, []events.Draft, error) {
	d, k, err := s.lockDraft(ctx, tx, id, rev, StateReverted)
	if err != nil {
		return Draft{}, nil, err
	}
	e, err := k.Get(ctx, tx, d.EntityID)
	if err != nil {
		return Draft{}, nil, err
	}
	closed, err := decide(ctx, tx, id, StateReverted, actor)
	if err != nil {
		return Draft{}, nil, err
	}
	if closed, err = s.complete(ctx, tx, closed, e.Rev); err != nil {
		return Draft{}, nil, err
	}
	evs, err := s.draftEvents(ctx, tx, closed, e, EventReverted)
	if err != nil {
		return Draft{}, nil, err
	}
	return closed, evs, nil
}

// ToolCallRetagger is a draftable kind that keeps tool-call ids in its revisions (the revision's cause).
type ToolCallRetagger interface {
	RetagToolCall(ctx context.Context, tx pgx.Tx, from, to string) ([]events.Draft, error)
}

// RetagToolCall gives the drafts, and the revisions of the kinds that keep one, whose tool-call id is from (an MCP
// call without a tool-use id) the agent's own id to. Each changed draft is sent again as draft.updated (its rev
// stays: the content did not change, and a person's accept keeps its If-Match) with presence.changed.
func (s *Store) RetagToolCall(ctx context.Context, tx pgx.Tx, from, to string) ([]events.Draft, error) {
	rows, err := tx.Query(ctx, `UPDATE drafts SET tool_call_id = $2 WHERE tool_call_id = $1 RETURNING `+draftCols, from, to)
	if err != nil {
		return nil, fmt.Errorf("retag tool call %s in drafts: %w", from, err)
	}
	list, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read retagged drafts: %w", err)
	}
	var out []events.Draft
	for _, d := range list {
		k, err := s.kind(d.EntityKind)
		if err != nil {
			return nil, err
		}
		e, err := k.Get(ctx, tx, d.EntityID)
		if err != nil {
			return nil, err
		}
		if d, err = s.complete(ctx, tx, d, e.Rev); err != nil {
			return nil, err
		}
		evs, err := s.draftEvents(ctx, tx, d, e, EventUpdated)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	names := make([]string, 0, len(s.kinds))
	for name := range s.kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if r, ok := s.kinds[name].(ToolCallRetagger); ok {
			more, err := r.RetagToolCall(ctx, tx, from, to)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
	}
	return out, nil
}

// Presence lists the agents editing the entity: authors of open drafts, then an agent whose direct edit wrote the
// current revision within the presence window.
func (s *Store) Presence(ctx context.Context, q storage.Querier, kind, entityID string, e Entity) ([]Presence, error) {
	rows, err := q.Query(ctx, `SELECT id, author, tool_call_id, created_at FROM drafts
		WHERE entity_kind = $1 AND entity_id = $2 AND state = 'open' ORDER BY created_at, id`, kind, entityID)
	if err != nil {
		return nil, fmt.Errorf("read presence: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Presence, error) {
		var p Presence
		err := row.Scan(&p.DraftID, &p.Actor, &p.ToolCallID, &p.Since)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read presence: %w", err)
	}
	if e.UpdatedBy.Kind == auth.KindAgent {
		until := e.UpdatedAt.Add(s.window)
		if s.now().Before(until) && !present(out, e.UpdatedBy) {
			out = append(out, Presence{Actor: e.UpdatedBy, ToolCallID: e.ToolCallID, Since: e.UpdatedAt, Until: &until})
		}
	}
	return out, nil
}

func present(ps []Presence, a auth.Actor) bool {
	for _, p := range ps {
		if authorKey(p.Actor) == authorKey(a) {
			return true
		}
	}
	return false
}

// PresenceEvent is presence.changed for the entity as it is inside tx: the whole presence list, so a client
// replaces what it shows.
func (s *Store) PresenceEvent(ctx context.Context, q storage.Querier, kind, entityID string, e Entity) (events.Draft, error) {
	ps, err := s.Presence(ctx, q, kind, entityID, e)
	if err != nil {
		return events.Draft{}, err
	}
	return events.Draft{
		Topic: events.EntityTopic(kind, entityID), Type: EventPresenceChanged, ProjectID: e.ProjectID,
		Entity:  &events.EntityRef{Kind: kind, ID: entityID, Rev: e.Rev},
		Payload: map[string]any{"presence": ps},
	}, nil
}

func (s *Store) draftEvents(ctx context.Context, q storage.Querier, d Draft, e Entity, typ string) ([]events.Draft, error) {
	pres, err := s.PresenceEvent(ctx, q, d.EntityKind, d.EntityID, e)
	if err != nil {
		return nil, err
	}
	return []events.Draft{{
		Topic: events.EntityTopic(d.EntityKind, d.EntityID), Type: typ, ProjectID: d.ProjectID,
		Entity:  &events.EntityRef{Kind: d.EntityKind, ID: d.EntityID, Rev: e.Rev},
		Payload: map[string]any{"draft": d},
	}, pres}, nil
}
