package projects

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// Touch locks the project, checks that it is at revision rev and moves it to the next revision. Commands that
// change what a project references (adoptions) or its repository (notes, syncs) without changing its own fields
// use it, so If-Match on the project covers them too.
func Touch(ctx context.Context, tx pgx.Tx, slug string, rev int) (Project, error) {
	cur, err := lockAt(ctx, tx, slug, rev)
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE projects SET rev = rev + 1, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
		return Project{}, fmt.Errorf("touch project: %w", err)
	}
	return GetByID(ctx, tx, cur.ID)
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

// Branches is the topic of branch.waiting, and EventBranchWaiting the event: a branch of the project repository now
// waits for a person to accept or discard it — a template sync's draft, or the changes of an agent session that
// ended without merging (a conflict, or no auto-merge). The status bar counts these branches; notifications route
// the event as an outcome.
const (
	TopicBranches      = "branches"
	EventBranchWaiting = "branch.waiting"
)

// BranchWaiting is the event's payload.
type BranchWaiting struct {
	ProjectID string `json:"projectId"`
	Project   string `json:"project"` // the slug
	Branch    string `json:"branch"`
	Kind      string `json:"kind"` // sync | session
	SessionID string `json:"sessionId,omitempty"`
	Files     int    `json:"files"`
	Conflicts int    `json:"conflicts"`
	Reason    string `json:"reason"`
}

// BranchWaitingEvent is the draft announcing w.
func BranchWaitingEvent(w BranchWaiting) events.Draft {
	return events.Draft{Topic: TopicBranches, Type: EventBranchWaiting, ProjectID: w.ProjectID, Payload: w}
}
