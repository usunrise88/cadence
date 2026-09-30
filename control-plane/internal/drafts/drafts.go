// Package drafts is the generic draft mechanism of draftable kinds (mix now; gate, note and language pack later):
// an unaccepted change to one entity, usually by an agent, that a person accepts (it becomes the entity's next
// revision, attributed to the person with causedBy naming the draft) or reverts (discarded). It also answers who is
// editing an entity right now (presence).
//
// A kind plugs in through the Kind interface; the Store owns the drafts table, the draft state machine, the diff
// against the draft's base revision and the draft, presence and accept events on entity.{kind}.{id}.
package drafts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// EntityKind is the kind drafts are named by in messages and 412 errors.
const EntityKind = "draft"

// Draft states. open → open (the author edits again), open → accepted, open → reverted; nothing leaves accepted or
// reverted.
const (
	StateOpen     = "open"
	StateAccepted = "accepted"
	StateReverted = "reverted"
)

// Draft policies: what an agent's edit of a draftable kind does.
const (
	PolicyDraft  = "draft"  // lands as a draft a person accepts or reverts
	PolicyDirect = "direct" // applies at once, still attributed to the agent
)

// Transition checks a state change of the draft state machine.
func Transition(from, to string) error {
	if from != StateOpen {
		return problems.Conflict.New("the draft is already %s; nothing more can happen to it", from)
	}
	switch to {
	case StateOpen, StateAccepted, StateReverted:
		return nil
	}
	return problems.Conflict.New("a draft cannot become %q", to)
}

// Policy returns the draft policy for an agent's edit of kind in a project: the one the project's agent profile
// names for the kind (Agent settings; the wizard fills it from defaults.yaml agent.draft_policy), else — for a
// project created before the wizard, without a profile — defaults.yaml drafts.<kind>.
func Policy(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID, kind string) (string, error) {
	a, err := projects.GetAgentProfile(ctx, q, projectID)
	if err != nil {
		if pe, ok := problems.As(err); !ok || pe.Type != problems.NotFound {
			return "", err
		}
		return resolvePolicy(nil, d, kind), nil
	}
	return resolvePolicy(a.DraftPolicy, d, kind), nil
}

// resolvePolicy picks the profile's policy for kind when it names a known one, else the defaults.yaml one.
func resolvePolicy(profile map[string]string, d *defaults.Defaults, kind string) string {
	switch p := profile[kind]; p {
	case PolicyDirect, PolicyDraft:
		return p
	}
	if kind == "mix" && d.Drafts.Mix.Value == PolicyDirect {
		return PolicyDirect
	}
	return PolicyDraft
}

// Draft is one draft. Its JSON form is the contract's Draft; CurrentRev, Stale and Changes are computed on read.
type Draft struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"projectId"`
	EntityKind string          `json:"entityKind"`
	EntityID   string          `json:"entityId"`
	BaseRev    int             `json:"baseRev"`
	CurrentRev int             `json:"currentRev"`
	Rev        int             `json:"rev"`
	State      string          `json:"state"`
	Content    json.RawMessage `json:"content"`
	Changes    []Change        `json:"changes"`
	Author     auth.Actor      `json:"author"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Stale      bool            `json:"stale"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
	DecidedBy  *auth.Actor     `json:"decidedBy,omitempty"`
	DecidedAt  *time.Time      `json:"decidedAt,omitempty"`
	AppliedRev *int            `json:"appliedRev,omitempty"`
}

// Presence is an agent editing an entity: an open draft, or a direct edit within the presence window. Its JSON
// form is the contract's Presence.
type Presence struct {
	Actor      auth.Actor `json:"actor"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	DraftID    string     `json:"draftId,omitempty"`
	Since      time.Time  `json:"since"`
	Until      *time.Time `json:"until,omitempty"`
}

// Cause is why a revision exists beyond its actor (the contract's RevisionCause).
type Cause struct {
	DraftID     string      `json:"draftId,omitempty"`
	DraftAuthor *auth.Actor `json:"draftAuthor,omitempty"`
	ToolCallID  string      `json:"toolCallId,omitempty"`
}

// Empty reports whether c says nothing.
func (c Cause) Empty() bool { return c.DraftID == "" && c.DraftAuthor == nil && c.ToolCallID == "" }

// Entity is a draftable entity as its kind reports it.
type Entity struct {
	ProjectID  string
	Rev        int
	Content    json.RawMessage
	UpdatedBy  auth.Actor
	UpdatedAt  time.Time
	ToolCallID string // the tool call that wrote the current revision, if an agent did
}

// Kind is what a draftable kind provides to the store.
type Kind interface {
	// Lock reads the entity inside tx and locks it until tx ends; a missing entity is not-found.
	Lock(ctx context.Context, tx pgx.Tx, id string) (Entity, error)
	// Get reads the entity.
	Get(ctx context.Context, q storage.Querier, id string) (Entity, error)
	// Content returns the entity's content at revision rev.
	Content(ctx context.Context, q storage.Querier, id string, rev int) (json.RawMessage, error)
	// Apply validates content and writes it as the entity's next revision, attributed to actor; it returns the
	// new revision, the entity as the API shows it and the events it emits.
	Apply(ctx context.Context, tx pgx.Tx, id string, content json.RawMessage, actor auth.Actor, cause Cause) (int, any, []events.Draft, error)
}
