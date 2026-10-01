package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/eviction"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// Content-store retention (phase 2 · stream E): artifacts.evict. The selection and the job live in
// internal/eviction; the policy gates every real call (the default preset's store-eviction rule, for people too),
// so the work below runs for a dry run or for the replay of an approved request.

// errCheckOnly rolls back the pre-check of named artifacts.
var errCheckOnly = errors.New("check only")

func (c commandResponse) VisitArtifactsEvictResponse(w http.ResponseWriter) error { return c.write(w) }

// ArtifactsEvict implements artifacts.evict: a dry run answers the plan; the approved replay queues the eviction job
// and answers 202 {jobId}.
func (s *Server) ArtifactsEvict(ctx context.Context, req api.ArtifactsEvictRequestObject) (api.ArtifactsEvictResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, eviction.Operation, req.Params.IdempotencyKey, req.Params.DryRun)
	if !cmd.DryRun && req.Body != nil && req.Body.Hashes != nil && len(*req.Body.Hashes) > 0 && s.Eviction != nil {
		// Named artifacts are checked before the policy gates the call, so a person is never asked to approve an
		// eviction that would be refused (the check rolls back; the replay checks again).
		if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			f, err := s.evictFilter(ctx, tx, req.Body)
			if err == nil {
				_, err = s.Eviction.Plan(ctx, tx, f)
			}
			if err == nil {
				err = errCheckOnly
			}
			return err
		}); !errors.Is(err, errCheckOnly) {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		if s.Eviction == nil {
			return commands.Result{}, nil, errors.New("content-store eviction is not configured")
		}
		f, err := s.evictFilter(ctx, tx, req.Body)
		if err != nil {
			return commands.Result{}, nil, err
		}
		p, err := s.Eviction.Plan(ctx, tx, f)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: p}, nil, nil
		}
		j, drafts, err := s.Eviction.Enqueue(ctx, tx, p, commands.ReplayedApproval(ctx))
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: j.ID}}, drafts, nil
	})
}

// evictFilter turns the request body into the selection's filter (a project by slug or id).
func (s *Server) evictFilter(ctx context.Context, tx pgx.Tx, b *api.ArtifactEvict) (eviction.Filter, error) {
	f := eviction.Filter{Strict: true, Now: time.Now()}
	if b == nil {
		return f, nil
	}
	if b.Type != nil {
		f.Type = string(*b.Type)
	}
	f.RunID = deref(b.RunId)
	if b.OlderThanDays != nil {
		f.OlderThan = time.Duration(*b.OlderThanDays) * 24 * time.Hour
	}
	if b.Hashes != nil {
		f.Hashes = *b.Hashes
	}
	if slug := deref(b.Project); slug != "" {
		p, err := projects.Get(ctx, tx, slug)
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			p, err = projects.GetByID(ctx, tx, slug)
		}
		if err != nil {
			return f, err
		}
		f.ProjectID = p.ID
	}
	return f, nil
}
