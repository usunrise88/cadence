package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/drafts"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mixes"
)

// Mixes (R13) and the drafts of draftable kinds (docs/spec/06-platform.md "Real-time model"): an agent's edit of a
// mix lands as a draft under the project's draft policy; a person accepts or reverts it.

func (c commandResponse) VisitMixesNewResponse(w http.ResponseWriter) error     { return c.write(w) }
func (c commandResponse) VisitMixesPreviewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitMixesEditResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitDraftsAcceptResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitDraftsRevertResponse(w http.ResponseWriter) error { return c.write(w) }

func mixGroups(gs []api.MixGroup) []mixes.GroupInput {
	out := make([]mixes.GroupInput, 0, len(gs))
	for _, g := range gs {
		out = append(out, mixes.GroupInput{Name: g.Name, Weight: g.Weight, Replay: g.Replay, Datasets: g.Datasets})
	}
	return out
}

func mixInput(b *api.MixNew) mixes.Input {
	return mixes.Input{Name: b.Name, Description: deref(b.Description), Groups: mixGroups(b.Groups), Temperature: b.Temperature, ReplayShare: b.ReplayShare}
}

// ---------------------------------------------------------------- mixes

// MixesList implements mixes.list.
func (s *Server) MixesList(ctx context.Context, req api.MixesListRequestObject) (api.MixesListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	list, err := mixes.List(ctx, s.Pool, p.ID)
	if err != nil {
		return nil, err
	}
	views := make([]mixes.View, 0, len(list))
	for _, m := range list {
		v, err := s.mixes.View(ctx, s.Pool, m)
		if err != nil {
			return nil, err
		}
		views = append(views, v)
	}
	out, err := convert[api.MixList](map[string]any{"items": views})
	if err != nil {
		return nil, err
	}
	return api.MixesList200JSONResponse(out), nil
}

// MixesNew implements mixes.new.
func (s *Server) MixesNew(ctx context.Context, req api.MixesNewRequestObject) (api.MixesNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, "mixes.new", req.Params.IdempotencyKey, req.Params.DryRun)
	in := mixInput(req.Body)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		v, evs, err := s.mixes.Create(ctx, tx, p.ID, in, cmd.Actor, commands.ToolCallFromContext(ctx))
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(v.Rev)}, evs, nil
	})
}

// MixesPreview implements mixes.preview: nothing is written, no event is emitted.
func (s *Server) MixesPreview(ctx context.Context, req api.MixesPreviewRequestObject) (api.MixesPreviewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, "mixes.preview", req.Params.IdempotencyKey, req.Params.DryRun)
	in := mixInput(req.Body)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		pv, err := s.mixes.Preview(ctx, tx, p.ID, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: pv}, nil, nil
	})
}

// scopedMix reads mix id and checks that the request's scope reaches its project.
func (s *Server) scopedMix(ctx context.Context, id string) (mixes.Mix, error) {
	m, err := mixes.Get(ctx, s.Pool, id)
	if err != nil {
		return mixes.Mix{}, err
	}
	return m, auth.CheckProject(ctx, m.ProjectID)
}

// MixesGet implements mixes.get.
func (s *Server) MixesGet(ctx context.Context, req api.MixesGetRequestObject) (api.MixesGetResponseObject, error) {
	m, err := s.scopedMix(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := s.mixes.View(ctx, s.Pool, m)
	if err != nil {
		return nil, err
	}
	body, err := convert[api.Mix](v)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(m.Rev)
	return api.MixesGet200JSONResponse{Body: body, Headers: api.MixesGet200ResponseHeaders{ETag: &etag}}, nil
}

// MixesEdit implements mixes.edit.
func (s *Server) MixesEdit(ctx context.Context, req api.MixesEditRequestObject) (api.MixesEditResponseObject, error) {
	m, err := s.scopedMix(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	e := mixes.EditInput{Name: req.Body.Name, Description: req.Body.Description, Temperature: req.Body.Temperature, ReplayShare: req.Body.ReplayShare}
	if req.Body.Groups != nil {
		gs := mixGroups(*req.Body.Groups)
		e.Groups = &gs
	}
	ctx = commands.WithProject(ctx, m.ProjectID)
	cmd := command(ctx, "mixes.edit", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		res, etag, evs, err := s.mixes.Edit(ctx, tx, req.Id, rev, e, cmd.Actor, commands.ToolCallFromContext(ctx))
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: res, ETag: commands.ETag(etag)}, evs, nil
	})
}

// ---------------------------------------------------------------- drafts

// scopedDraft reads draft id and checks that the request's scope reaches its project.
func (s *Server) scopedDraft(ctx context.Context, id string) (drafts.Draft, error) {
	d, err := s.drafts.Get(ctx, s.Pool, id)
	if err != nil {
		return drafts.Draft{}, err
	}
	return d, auth.CheckProject(ctx, d.ProjectID)
}

// DraftsList implements drafts.list.
func (s *Server) DraftsList(ctx context.Context, req api.DraftsListRequestObject) (api.DraftsListResponseObject, error) {
	kind := string(req.Params.EntityKind)
	e, err := s.drafts.Entity(ctx, s.Pool, kind, req.Params.EntityId)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, e.ProjectID); err != nil {
		return nil, err
	}
	state := drafts.StateOpen
	if req.Params.State != nil {
		state = string(*req.Params.State)
	}
	list, err := s.drafts.List(ctx, s.Pool, kind, req.Params.EntityId, state)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.DraftList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.DraftsList200JSONResponse(out), nil
}

// DraftsGet implements drafts.get.
func (s *Server) DraftsGet(ctx context.Context, req api.DraftsGetRequestObject) (api.DraftsGetResponseObject, error) {
	d, err := s.scopedDraft(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	body, err := convert[api.Draft](d)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(d.Rev)
	return api.DraftsGet200JSONResponse{Body: body, Headers: api.DraftsGet200ResponseHeaders{ETag: &etag}}, nil
}

// DraftsAccept implements drafts.accept: the draft becomes the entity's next revision, attributed to the caller,
// with causedBy.draftId on its events.
func (s *Server) DraftsAccept(ctx context.Context, req api.DraftsAcceptRequestObject) (api.DraftsAcceptResponseObject, error) {
	d, err := s.scopedDraft(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithDraft(commands.WithProject(ctx, d.ProjectID), d.ID)
	cmd := command(ctx, "drafts.accept", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		accepted, entity, evs, err := s.drafts.Accept(ctx, tx, req.Id, rev, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body := map[string]any{"draft": accepted, "entity": entity}
		return commands.Result{Status: http.StatusOK, Body: body, ETag: commands.ETag(accepted.Rev)}, evs, nil
	})
}

// DraftsRevert implements drafts.revert.
func (s *Server) DraftsRevert(ctx context.Context, req api.DraftsRevertRequestObject) (api.DraftsRevertResponseObject, error) {
	d, err := s.scopedDraft(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, d.ProjectID)
	cmd := command(ctx, "drafts.revert", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		reverted, evs, err := s.drafts.Revert(ctx, tx, req.Id, rev, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: reverted, ETag: commands.ETag(reverted.Rev)}, evs, nil
	})
}
