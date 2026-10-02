package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Evals, gates and models (phase 3 · stream E): evals.new|list|get|gate, gates.get|edit, models.register|list|get.
// The domain lives in internal/evals; these handlers map the contract onto it. evals.new plans once outside the
// command so the policy weighs its GPU-hour estimate against the budgets (as runs.new does), then again inside.

func (c commandResponse) VisitEvalsNewResponse(w http.ResponseWriter) error       { return c.write(w) }
func (c commandResponse) VisitEvalsGateResponse(w http.ResponseWriter) error      { return c.write(w) }
func (c commandResponse) VisitGatesEditResponse(w http.ResponseWriter) error      { return c.write(w) }
func (c commandResponse) VisitModelsRegisterResponse(w http.ResponseWriter) error { return c.write(w) }

// newEvalsService wires the evals domain to the server's engine, store, runs and repositories.
func (s *Server) newEvalsService() *evals.Service {
	return &evals.Service{Pool: s.Pool, Engine: s.Pipelines, CAS: s.CAS, Runs: s.runs, Defaults: s.defaultsDoc, Repo: s.projectRepos}
}

// projectRepos is the project repositories as the evals read them (nil without a repository service).
func (s *Server) projectRepos() pipelines.Repo {
	if s.Projects == nil {
		return nil
	}
	return s.Projects.Repos()
}

// scopedEval reads the project of eval id and checks the request's scope reaches it.
func (s *Server) scopedEval(ctx context.Context, id string) (string, error) {
	projectID, err := evals.ProjectOf(ctx, s.Pool, id)
	if err != nil {
		return "", err
	}
	return projectID, auth.CheckProject(ctx, projectID)
}

// ---------------------------------------------------------------- evals

// EvalsNew implements evals.new: the dry run answers the plan; the real call starts the eval's pipeline run and
// answers 201 with the eval (202 with an approval when an agent's estimate exceeds a GPU budget).
func (s *Server) EvalsNew(ctx context.Context, req api.EvalsNewRequestObject) (api.EvalsNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	actor, _ := auth.FromContext(ctx)
	in := evals.NewInput{ProjectID: p.ID, Actor: actor, GoldenSets: deref(b.GoldenSets), Profiles: deref(b.Profiles),
		Baseline: deref(b.Baseline), Priority: deref(b.Priority),
		Subject: evals.SubjectRef{CheckpointID: deref(b.Subject.CheckpointId), ModelVersionID: deref(b.Subject.ModelVersionId),
			BaseModelVersionID: deref(b.Subject.BaseModelVersionId)}}
	for _, d := range deref(b.Decoding) {
		in.Decoding = append(in.Decoding, evals.DecodingIn{Boost: d.Boost, Weight: d.Weight})
	}
	if pl, err := s.evals.Prepare(ctx, s.Pool, in); err != nil {
		ctx = spending(ctx, 0) // the command fails on the same plan; nothing to weigh
	} else {
		ctx = spending(ctx, pl.Estimate.GPUHours)
	}
	cmd := command(ctx, "evals.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		pl, err := s.evals.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: pl}, nil, nil
		}
		e, drafts, err := s.evals.Create(ctx, tx, pl)
		if err != nil {
			return commands.Result{}, nil, err
		}
		v, err := s.evals.View(ctx, tx, e, evals.ViewQuery{})
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}

// EvalsList implements evals.list.
func (s *Server) EvalsList(ctx context.Context, req api.EvalsListRequestObject) (api.EvalsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f := evals.ListFilter{ProjectID: p.ID, Subject: deref(req.Params.Subject), Limit: deref(req.Params.Limit)}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	list, err := s.evals.List(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.EvalList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.EvalsList200JSONResponse(out), nil
}

// EvalsGet implements evals.get.
func (s *Server) EvalsGet(ctx context.Context, req api.EvalsGetRequestObject) (api.EvalsGetResponseObject, error) {
	if _, err := s.scopedEval(ctx, req.Id); err != nil {
		return nil, err
	}
	vq := evals.ViewQuery{Worst: deref(req.Params.Worst), Cell: deref(req.Params.Cell), GoldenSet: deref(req.Params.GoldenSet),
		Profile: deref(req.Params.Profile)}
	if req.Params.Role != nil {
		vq.Role = string(*req.Params.Role)
	}
	v, err := s.evals.Get(ctx, s.Pool, req.Id, vq)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.Eval](v)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(v.Rev)
	return api.EvalsGet200JSONResponse{Body: out, Headers: api.EvalsGet200ResponseHeaders{ETag: &etag}}, nil
}

// EvalsGate implements evals.gate.
func (s *Server) EvalsGate(ctx context.Context, req api.EvalsGateRequestObject) (api.EvalsGateResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	projectID, err := s.scopedEval(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	cmd := command(ctx, "evals.gate", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		v, drafts, err := s.evals.Gate(ctx, tx, req.Id, rev, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: v, ETag: commands.ETag(v.Rev)}, drafts, nil
	})
}

// ---------------------------------------------------------------- gates

// gatesView is the contract's Gates.
type gatesView struct {
	Path       string            `json:"path"`
	Exists     bool              `json:"exists"`
	Ref        string            `json:"ref"`
	Head       string            `json:"head"`
	Commit     string            `json:"commit,omitempty"`
	Content    string            `json:"content"`
	Config     evals.GateConfig  `json:"config"`
	Departures []evals.Departure `json:"departures"`
}

func (s *Server) gatesBody(f evals.GateFile) (gatesView, error) {
	v := gatesView{Path: evals.GatesFile, Exists: f.Exists, Ref: repos.Main, Head: f.Head, Commit: f.Commit, Content: string(f.Content),
		Config: f.Gate.Config(), Departures: evals.Departures(f.Gate, s.defaultsDoc())}
	if !f.Exists {
		b, err := evals.RenderGate(f.Gate.Config())
		if err != nil {
			return gatesView{}, err
		}
		v.Content = string(b)
	}
	return v, nil
}

// GatesGet implements gates.get: gates.yaml at main's head, the defaults where it is silent.
func (s *Server) GatesGet(ctx context.Context, req api.GatesGetRequestObject) (api.GatesGetResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f, err := evals.ReadGate(ctx, s.projectRepos(), p.Slug, s.defaultsDoc())
	if err != nil {
		return nil, err
	}
	body, err := s.gatesBody(f)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.Gates](body)
	if err != nil {
		return nil, err
	}
	etag := `"` + f.ETag() + `"`
	return api.GatesGet200JSONResponse{Body: out, Headers: api.GatesGet200ResponseHeaders{ETag: &etag}}, nil
}

// GatesEdit implements gates.edit: the new gates.yaml is validated (gate-config-invalid), then committed to main
// through the recipes path (If-Match = the commit that last changed the file, or "defaults" to create it).
func (s *Server) GatesEdit(ctx context.Context, req api.GatesEditRequestObject) (api.GatesEditResponseObject, error) {
	expect := strings.Trim(strings.TrimPrefix(strings.TrimSpace(req.Params.IfMatch), "W/"), `"`)
	if expect != evals.DefaultsETag {
		var err error
		if expect, err = headOf(req.Params.IfMatch); err != nil {
			return nil, problems.BadRequest.New("If-Match must be the ETag of gates.get: the commit that last changed gates.yaml, or %q when there is none", evals.DefaultsETag)
		}
	}
	b := req.Body
	if (b.Content == nil) == (b.Config == nil) {
		return nil, problems.Validation([]problems.FieldError{{Path: "/", Message: "send exactly one of content (the YAML) and config"}})
	}
	d := s.defaultsDoc()
	var content []byte
	if b.Content != nil {
		content = []byte(*b.Content)
	} else {
		c, err := convert[evals.GateConfig](*b.Config)
		if err != nil {
			return nil, err
		}
		if content, err = evals.RenderGate(c); err != nil {
			return nil, err
		}
	}
	_, gate, err := evals.ParseGate(content, d)
	if err != nil {
		return nil, err
	}
	p, svc, err := s.projectRepo(ctx, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, "gates.edit", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		if err := evals.CheckAdopted(ctx, tx, p.ID, gate); err != nil {
			return commands.Result{}, nil, err
		}
		cur, err := evals.ReadGate(ctx, s.projectRepos(), p.Slug, d)
		if err != nil {
			if pe, ok := problems.As(err); !ok || pe.Type != problems.GateConfigInvalid {
				return commands.Result{}, nil, err // a broken file on main may be replaced
			}
			cur, err = rawGate(ctx, s.projectRepos(), p.Slug)
			if err != nil {
				return commands.Result{}, nil, err
			}
		}
		switch {
		case expect == evals.DefaultsETag && cur.Exists:
			return commands.Result{}, nil, problems.PreconditionFailed.New("gates.yaml exists now (last changed in %.12s); re-read it (gates.get)", cur.Commit)
		case expect != evals.DefaultsETag && !cur.Exists:
			return commands.Result{}, nil, problems.PreconditionFailed.New("the project has no gates.yaml; create it with If-Match %q", evals.DefaultsETag)
		}
		fw := bootstrap.FileWrite{Path: evals.GatesFile, Content: content, Message: deref(b.Message)}
		if expect != evals.DefaultsETag {
			fw.Expect = expect
		}
		if fw.Message == "" {
			fw.Message = "edit the gate (gates.edit)"
		}
		pr, err := projects.Get(ctx, tx, p.Slug)
		if err != nil {
			return commands.Result{}, nil, err
		}
		c, drafts, err := svc.WriteFile(ctx, tx, pr, fw, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		f := evals.GateFile{Exists: true, Head: cur.Head, Commit: c.SHA, Content: content, Gate: gate}
		if c.SHA != "" {
			if f, err = evals.ReadGate(ctx, s.projectRepos(), p.Slug, d); err != nil {
				return commands.Result{}, nil, err
			}
		}
		body, err := s.gatesBody(f)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: body, ETag: `"` + f.ETag() + `"`}, drafts, nil
	})
}

// rawGate reads gates.yaml without parsing it (a file on main that no longer validates).
func rawGate(ctx context.Context, repo pipelines.Repo, slug string) (evals.GateFile, error) {
	f := evals.GateFile{}
	if repo == nil {
		return f, nil
	}
	b, head, err := repo.ReadFile(ctx, slug, repos.Main, evals.GatesFile)
	if err != nil {
		return f, err
	}
	hist, err := repo.History(ctx, slug, repos.Main, evals.GatesFile, 1)
	if err != nil {
		return f, err
	}
	f.Exists, f.Head, f.Content = true, head, b
	if len(hist) > 0 {
		f.Commit = hist[0].SHA
	}
	return f, nil
}

// ---------------------------------------------------------------- models

// ModelsRegister implements models.register: a checkpoint whose latest gated eval passed becomes a frozen model
// version with its card (agents wait for an approval: the preset's registry-changes rule).
func (s *Server) ModelsRegister(ctx context.Context, req api.ModelsRegisterRequestObject) (api.ModelsRegisterResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	in := evals.RegisterInput{ProjectID: p.ID, CheckpointID: b.CheckpointId, Name: deref(b.Name), EvalID: deref(b.EvalId),
		Description: deref(b.Description)}
	cmd := command(ctx, "models.register", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		reg, err := s.evals.PlanRegister(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: reg}, nil, nil
		}
		v, drafts, err := s.evals.Register(ctx, tx, reg, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		items, err := modelVersions(ctx, tx, registry.Filter{Kind: registry.KindModel, IDs: []string{v.ID}})
		if err != nil {
			return commands.Result{}, nil, err
		}
		mv, err := one(items, registry.KindModel, v.ID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: mv}, drafts, nil
	})
}

func modelVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.ModelVersion, error) {
	common, payloads, used, err := versions[api.ModelPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.ModelVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.ModelVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, Model: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// ModelsList implements models.list.
func (s *Server) ModelsList(ctx context.Context, req api.ModelsListRequestObject) (api.ModelsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := modelVersions(ctx, s.Pool, versionFilter(registry.KindModel, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.ModelsList200JSONResponse{Items: items}, nil
}

// ModelsGet implements models.get.
func (s *Server) ModelsGet(ctx context.Context, req api.ModelsGetRequestObject) (api.ModelsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := modelVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindModel, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindModel, req.Id)
	if err != nil {
		return nil, err
	}
	return api.ModelsGet200JSONResponse(v), nil
}
