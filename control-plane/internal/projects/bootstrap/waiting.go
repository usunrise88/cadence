package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Waiting is a branch of a project repository that waits for a person to accept or discard it.
type Waiting struct {
	Project   string // the slug
	Branch    string
	Kind      string // sync | session | other
	SessionID string
}

// liveSession is the states of a session that is still working on its branch.
var liveSession = map[string]bool{"created": true, "running": true, "waiting_approval": true, "paused": true}

// WaitingBranches lists, over every project that is not archived, the branches ahead of main that nobody is working
// on: template syncs, other branches, and the branches of agent sessions that ended (or are gone). A live session's
// branch is not waiting: its agent still commits to it. A project whose repository cannot be read is skipped.
func (s *Service) WaitingBranches(ctx context.Context, q storage.Querier) ([]Waiting, error) {
	ps, err := projects.List(ctx, q, false)
	if err != nil {
		return nil, err
	}
	var out []Waiting
	for _, p := range ps {
		bs, err := s.o.Repos.Branches(ctx, p.Slug, false)
		if err != nil {
			continue
		}
		for _, b := range bs {
			if b.Ahead == 0 {
				continue
			}
			w := Waiting{Project: p.Slug, Branch: b.Name, Kind: b.Kind()}
			if w.Kind == "session" {
				w.SessionID = strings.TrimPrefix(b.Name, repos.SessionPrefix)
				var state string
				err := q.QueryRow(ctx, `SELECT state FROM agent_sessions WHERE id = $1`, w.SessionID).Scan(&state)
				switch {
				case errors.Is(err, pgx.ErrNoRows):
				case err != nil:
					return nil, fmt.Errorf("session of %s: %w", b.Name, err)
				case liveSession[state]:
					continue
				}
			}
			out = append(out, w)
		}
	}
	return out, nil
}
