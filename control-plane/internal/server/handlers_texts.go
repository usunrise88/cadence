package server

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/data"
)

// ---------------------------------------------------------------- texts to read (phase 4 tail · stream U)
//
// guidelines.get serves the guidelines file an annotation batch pinned (a reviewer of the batch reads it: reviewerOnly
// lets /batches/<batch>/guidelines through and nothing else of the repository); texts.get serves a text of the
// content store a registry version names to be read (a dataset card).

// GuidelinesGet implements guidelines.get.
func (s *Server) GuidelinesGet(ctx context.Context, req api.GuidelinesGetRequestObject) (api.GuidelinesGetResponseObject, error) {
	if _, _, err := s.batchViewer(ctx, req.Id); err != nil {
		return nil, err
	}
	g, err := s.annotation.Guidelines(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.GuidelinesText](g)
	if err != nil {
		return nil, err
	}
	return api.GuidelinesGet200JSONResponse(out), nil
}

// TextsGet implements texts.get.
func (s *Server) TextsGet(ctx context.Context, req api.TextsGetRequestObject) (api.TextsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	t, err := data.ReadText(ctx, s.Pool, s.CAS, req.Hash)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.RegistryText](t)
	if err != nil {
		return nil, err
	}
	return api.TextsGet200JSONResponse(out), nil
}
