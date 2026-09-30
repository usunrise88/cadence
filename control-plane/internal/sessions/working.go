package sessions

import (
	"slices"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// Working changes (docs/spec/05-agents.md "Worktree, drafts and merge"): while a turn runs the agent host's watcher
// reports the uncommitted files of the session's worktree — paths, status and sizes, never content. The session
// keeps the last report (AgentSession.working) and every file that enters, changes in or leaves that set becomes a
// recipe.{path} event of type recipe.working on the session's branch, so an open Recipe document and the Chat's
// Session changes follow the agent's edits before the turn's commit.

// EventRecipeWorking is the type of a working-change event on recipe.{path}.
const EventRecipeWorking = "recipe.working"

// MaxWorkingFiles is how many files a report lists at most (the contract's maxItems).
const MaxWorkingFiles = 200

// WorkingChange is one uncommitted file.
type WorkingChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Bytes     *int   `json:"bytes,omitempty"`
	Additions *int   `json:"additions,omitempty"`
	Deletions *int   `json:"deletions,omitempty"`
}

// Working is the worktree's uncommitted changes as last reported.
type Working struct {
	Turn      int             `json:"turn,omitempty"`
	Files     []WorkingChange `json:"files"`
	Truncated bool            `json:"truncated"`
	At        *time.Time      `json:"at,omitempty"`
}

// normalizeWorking sorts a report by path, drops duplicates and returns nil for a clean worktree.
func normalizeWorking(w *Working, now time.Time) *Working {
	if w == nil || (len(w.Files) == 0 && !w.Truncated) {
		return nil
	}
	out := &Working{Turn: w.Turn, Truncated: w.Truncated, At: &now}
	seen := map[string]bool{}
	for _, f := range w.Files {
		if f.Path == "" || seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		out.Files = append(out.Files, f)
	}
	slices.SortFunc(out.Files, func(a, b WorkingChange) int { return strings.Compare(a.Path, b.Path) })
	if len(out.Files) > MaxWorkingFiles {
		out.Files, out.Truncated = out.Files[:MaxWorkingFiles], true
	}
	if out.Files == nil {
		out.Files = []WorkingChange{}
	}
	return out
}

// sameWorking compares two reports without their receive time.
func sameWorking(a, b *Working) bool {
	strip := func(w *Working) *Working {
		if w == nil {
			return nil
		}
		c := *w
		c.At = nil
		return &c
	}
	return jsonEqual(strip(a), strip(b))
}

// workingEvents are the recipe.{path} events between two reports: a file that entered or changed carries its status
// and sizes; one that left the set (committed, or put back as it was) carries status "clean".
func workingEvents(sess Session, prev, next *Working) []events.Draft {
	old := map[string]WorkingChange{}
	if prev != nil {
		for _, f := range prev.Files {
			old[f.Path] = f
		}
	}
	turn := sess.Turn
	if next != nil && next.Turn > 0 {
		turn = next.Turn
	}
	draft := func(f WorkingChange) events.Draft {
		p := map[string]any{"path": f.Path, "branch": sess.Branch, "sessionId": sess.ID, "status": f.Status,
			"working": true, "turn": turn}
		if f.Bytes != nil {
			p["bytes"] = *f.Bytes
		}
		if f.Additions != nil {
			p["additions"] = *f.Additions
		}
		if f.Deletions != nil {
			p["deletions"] = *f.Deletions
		}
		return events.Draft{Topic: "recipe." + f.Path, Type: EventRecipeWorking, ProjectID: sess.ProjectID, Payload: p}
	}
	var out []events.Draft
	if next != nil {
		for _, f := range next.Files {
			o, had := old[f.Path]
			delete(old, f.Path)
			if had && jsonEqual(o, f) {
				continue
			}
			out = append(out, draft(f))
		}
	}
	gone := make([]string, 0, len(old))
	for p := range old {
		gone = append(gone, p)
	}
	slices.Sort(gone)
	for _, p := range gone {
		out = append(out, draft(WorkingChange{Path: p, Status: "clean"}))
	}
	return out
}
