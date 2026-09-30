package mixes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/drafts"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Service runs mix commands and builds the mix the API shows (with presence and preview). It is the draftable kind
// "mix" of the draft store.
type Service struct {
	Drafts   *drafts.Store
	Defaults func() *defaults.Defaults
}

// NewService returns the mix service and registers it with the draft store.
func NewService(store *drafts.Store, d func() *defaults.Defaults) *Service {
	s := &Service{Drafts: store, Defaults: d}
	store.Register(Kind, kind{s})
	return s
}

// View is a mix as the API returns it (the contract's Mix).
type View struct {
	Mix
	Presence []drafts.Presence `json:"presence"`
	Preview  Preview           `json:"preview"`
}

// View builds the API form of m.
func (s *Service) View(ctx context.Context, q storage.Querier, m Mix) (View, error) {
	ps, err := s.Drafts.Presence(ctx, q, Kind, m.ID, entity(m))
	if err != nil {
		return View{}, err
	}
	if ps == nil {
		ps = []drafts.Presence{}
	}
	meta, err := LoadDatasets(ctx, q, m.ProjectID, m.Content)
	if err != nil {
		return View{}, err
	}
	return View{Mix: m, Presence: ps, Preview: ComputePreview(m.Content, meta)}, nil
}

// Preview normalizes in for the project and previews it without saving.
func (s *Service) Preview(ctx context.Context, q storage.Querier, projectID string, in Input) (Preview, error) {
	c, err := Normalize(ctx, q, projectID, in, s.Defaults())
	if err != nil {
		return Preview{}, err
	}
	meta, err := LoadDatasets(ctx, q, projectID, c)
	if err != nil {
		return Preview{}, err
	}
	return ComputePreview(c, meta), nil
}

// Create saves a new mix at revision 1 and emits mix.created (and presence.changed when an agent made it).
func (s *Service) Create(ctx context.Context, tx pgx.Tx, projectID string, in Input, actor auth.Actor, toolCallID string) (View, []events.Draft, error) {
	c, err := Normalize(ctx, tx, projectID, in, s.Defaults())
	if err != nil {
		return View{}, nil, err
	}
	m, err := insert(ctx, tx, projectID, c, actor, drafts.Cause{ToolCallID: toolCallID})
	if err != nil {
		return View{}, nil, err
	}
	return s.revised(ctx, tx, m, EventCreated)
}

// EditResult is what mixes.edit answers: the mix, and the draft when the edit landed as one.
type EditResult struct {
	Mix   View          `json:"mix"`
	Draft *drafts.Draft `json:"draft,omitempty"`
}

// Edit applies e to mix id at revision rev (If-Match). A person's edit, or an agent's under the direct policy,
// becomes the next revision; an agent's edit under the draft policy updates (or opens) its draft instead and the
// mix stays as it is. A stale rev is precondition-failed with the current revision either way.
func (s *Service) Edit(ctx context.Context, tx pgx.Tx, id string, rev int, e EditInput, actor auth.Actor, toolCallID string) (EditResult, int, []events.Draft, error) {
	m, err := Lock(ctx, tx, id)
	if err != nil {
		return EditResult{}, 0, nil, err
	}
	if err := auth.CheckProject(ctx, m.ProjectID); err != nil {
		return EditResult{}, 0, nil, err
	}
	if err := commands.CheckRev(Kind, rev, m.Rev); err != nil {
		return EditResult{}, 0, nil, err
	}
	if actor.Kind != auth.KindAgent || drafts.Policy(s.Defaults(), m.ProjectID, Kind) == drafts.PolicyDirect {
		in := m.Content.Input().Apply(e)
		c, err := Normalize(ctx, tx, m.ProjectID, in, s.Defaults())
		if err != nil {
			return EditResult{}, 0, nil, err
		}
		next, err := revise(ctx, tx, id, c, actor, drafts.Cause{ToolCallID: toolCallID})
		if err != nil {
			return EditResult{}, 0, nil, err
		}
		v, evs, err := s.revised(ctx, tx, next, EventRevised)
		return EditResult{Mix: v}, next.Rev, evs, err
	}
	working, err := s.Drafts.Working(ctx, tx, Kind, id, actor, entity(m))
	if err != nil {
		return EditResult{}, 0, nil, err
	}
	var base Content
	if err := json.Unmarshal(working, &base); err != nil {
		return EditResult{}, 0, nil, fmt.Errorf("decode draft content: %w", err)
	}
	c, err := Normalize(ctx, tx, m.ProjectID, base.Input().Apply(e), s.Defaults())
	if err != nil {
		return EditResult{}, 0, nil, err
	}
	content, err := json.Marshal(c)
	if err != nil {
		return EditResult{}, 0, nil, fmt.Errorf("encode draft content: %w", err)
	}
	d, evs, err := s.Drafts.Save(ctx, tx, drafts.SaveInput{
		Kind: Kind, EntityID: id, Entity: entity(m), Content: content, Author: actor, ToolCallID: toolCallID,
	})
	if err != nil {
		return EditResult{}, 0, nil, err
	}
	v, err := s.View(ctx, tx, m)
	if err != nil {
		return EditResult{}, 0, nil, err
	}
	return EditResult{Mix: v, Draft: &d}, m.Rev, evs, nil
}

// revised builds the view of m and its event (and presence.changed when an agent wrote the revision).
func (s *Service) revised(ctx context.Context, q storage.Querier, m Mix, typ string) (View, []events.Draft, error) {
	v, err := s.View(ctx, q, m)
	if err != nil {
		return View{}, nil, err
	}
	evs := []events.Draft{{
		Topic: events.EntityTopic(Kind, m.ID), Type: typ, ProjectID: m.ProjectID,
		Entity:  &events.EntityRef{Kind: Kind, ID: m.ID, Rev: m.Rev},
		Payload: map[string]any{"mix": v},
	}}
	if m.UpdatedBy.Kind == auth.KindAgent {
		p, err := s.Drafts.PresenceEvent(ctx, q, Kind, m.ID, entity(m))
		if err != nil {
			return View{}, nil, err
		}
		evs = append(evs, p)
	}
	return v, evs, nil
}

func entity(m Mix) drafts.Entity {
	e := drafts.Entity{ProjectID: m.ProjectID, Rev: m.Rev, UpdatedBy: m.UpdatedBy, UpdatedAt: m.UpdatedAt}
	if m.Cause != nil {
		e.ToolCallID = m.Cause.ToolCallID
	}
	e.Content, _ = json.Marshal(m.Content)
	return e
}

// kind adapts mixes to the draft store.
type kind struct{ s *Service }

func (k kind) Lock(ctx context.Context, tx pgx.Tx, id string) (drafts.Entity, error) {
	m, err := Lock(ctx, tx, id)
	return entity(m), err
}

func (k kind) Get(ctx context.Context, q storage.Querier, id string) (drafts.Entity, error) {
	m, err := Get(ctx, q, id)
	return entity(m), err
}

func (k kind) Content(ctx context.Context, q storage.Querier, id string, rev int) (json.RawMessage, error) {
	return RevisionContent(ctx, q, id, rev)
}

func (k kind) Apply(ctx context.Context, tx pgx.Tx, id string, content json.RawMessage, actor auth.Actor, cause drafts.Cause) (int, any, []events.Draft, error) {
	m, err := Lock(ctx, tx, id)
	if err != nil {
		return 0, nil, nil, err
	}
	var c Content
	if err := json.Unmarshal(content, &c); err != nil {
		return 0, nil, nil, fmt.Errorf("decode mix draft: %w", err)
	}
	// Frozen versions never change, but one may have been deprecated since the draft was made: validate again.
	if c, err = Normalize(ctx, tx, m.ProjectID, c.Input(), k.s.Defaults()); err != nil {
		return 0, nil, nil, err
	}
	next, err := revise(ctx, tx, id, c, actor, cause)
	if err != nil {
		return 0, nil, nil, err
	}
	v, evs, err := k.s.revised(ctx, tx, next, EventRevised)
	if err != nil {
		return 0, nil, nil, err
	}
	return next.Rev, v, evs, nil
}
