package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// The registry in full (phase 4 · stream R): the soft delete of registry versions. Adoption checks live in
// internal/registry (checks.go) and run inside projects.adopt (handlers_registry.go).

func (c commandResponse) VisitVersionsArchiveResponse(w http.ResponseWriter) error { return c.write(w) }

// VersionsArchive implements versions.archive: the admin's soft delete (spec 02 "Registry", "Deletion"); a version
// anything uses answers version-in-use.
func (s *Server) VersionsArchive(ctx context.Context, req api.VersionsArchiveRequestObject) (api.VersionsArchiveResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, "versions.archive", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		v, drafts, err := registry.Archive(ctx, tx, req.Body.Version, cmd.Actor, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiVersion(v)}, drafts, nil
	})
}
