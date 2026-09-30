package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/playbooks"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/sessions"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Playbooks (phase 2 · stream K, R16): playbooks.list|get read the bundled templates with their estimate;
// playbooks.run starts a playbook session. The plan ticks through the command pipeline's session hook and the read
// observer (observeReads), both internal/playbooks.

func (c commandResponse) VisitPlaybooksRunResponse(w http.ResponseWriter) error { return c.write(w) }

// playbookView is the contract's Playbook.
type playbookView struct {
	Name          string              `json:"name"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	TypicalCost   string              `json:"typicalCost,omitempty"`
	AvailableFrom int                 `json:"availableFrom"`
	Runnable      bool                `json:"runnable"`
	Unavailable   string              `json:"unavailable,omitempty"`
	VersionID     string              `json:"versionId,omitempty"`
	Version       string              `json:"version,omitempty"`
	Inputs        []map[string]any    `json:"inputs"`
	Chain         []map[string]any    `json:"chain"`
	Stop          []playbooks.Stop    `json:"stop"`
	Prompt        string              `json:"prompt"`
	Estimate      *playbooks.Estimate `json:"estimate,omitempty"`
	EstimateError string              `json:"estimateError,omitempty"`
}

// playbookVersion is the registry template version of a bundled playbook (none before the registry is seeded).
func playbookVersion(ctx context.Context, q storage.Querier, name string) (registry.Version, bool, error) {
	v, err := registry.Latest(ctx, q, registry.KindTemplate, "template/playbook-"+name)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		return registry.Version{}, false, nil
	}
	return v, err == nil, err
}

// view renders p with its version and, with the project's facts where given, its inputs' defaults and the estimate.
func (s *Server) playbookView(ctx context.Context, p playbooks.Playbook, prj *projects.Project) (playbookView, error) {
	v := playbookView{Name: p.Name, Title: p.Title, Description: p.Description, TypicalCost: p.TypicalCost,
		AvailableFrom: p.AvailableFrom, Runnable: p.Runnable(), Stop: p.Stop, Prompt: p.Prompt,
		Inputs: []map[string]any{}, Chain: []map[string]any{}}
	if v.Stop == nil {
		v.Stop = []playbooks.Stop{}
	}
	if !p.Runnable() {
		v.Unavailable = "runs from roadmap phase " + strconv.Itoa(p.AvailableFrom) + "; listed for reference"
	}
	ver, ok, err := playbookVersion(ctx, s.Pool, p.Name)
	if err != nil {
		return v, err
	}
	if ok {
		v.VersionID, v.Version = ver.ID, ver.Version
	}
	r, est, err := s.playbooks.Estimate(ctx, s.Pool, p, prj, nil)
	var pe *problems.Error
	switch {
	case err == nil:
		v.Estimate = &est
	case errors.As(err, &pe) && pe.Type != problems.Internal:
		v.EstimateError = pe.Detail
		if len(pe.Errors) > 0 {
			v.EstimateError = pe.Errors[0].Path + ": " + pe.Errors[0].Message
		}
		r = playbooks.Resolved{Values: map[string]any{}}
	default:
		return v, err
	}
	d := s.defaultsDoc()
	for _, in := range p.Inputs {
		m := map[string]any{"name": in.Name, "type": in.Type, "required": in.Required, "multiple": in.Multiple}
		for k, val := range map[string]string{"description": in.Description, "defaultRef": in.DefaultRef, "from": in.From, "collection": in.Collection} {
			if val != "" {
				m[k] = val
			}
		}
		if dv, ok := r.Values[in.Name]; ok && dv != nil {
			m["default"] = dv
		}
		if in.DefaultRef != "" {
			lo, hi := playbooks.Bounds(d, in.DefaultRef)
			if lo != nil {
				m["min"] = *lo
			}
			if hi != nil {
				m["max"] = *hi
			}
		}
		v.Inputs = append(v.Inputs, m)
	}
	for _, st := range p.Chain {
		m := map[string]any{"id": st.ID, "title": st.Title, "command": st.Command, "spending": playbooks.Spending[st.Command],
			"available": st.Available()}
		if len(st.Accepts) > 0 {
			m["accepts"] = st.Accepts
		}
		if st.Until != "" {
			m["until"] = st.Until
		}
		if st.Phase > 0 {
			m["phase"] = st.Phase
		}
		v.Chain = append(v.Chain, m)
	}
	return v, nil
}

// optionalProject resolves a project query parameter (nil when absent) and checks the caller's scope.
func (s *Server) optionalProject(ctx context.Context, slug *api.Slug) (*projects.Project, error) {
	if slug == nil || *slug == "" {
		return nil, nil
	}
	p, err := scopedProject(ctx, s.Pool, *slug)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PlaybooksList implements playbooks.list.
func (s *Server) PlaybooksList(ctx context.Context, req api.PlaybooksListRequestObject) (api.PlaybooksListResponseObject, error) {
	prj, err := s.optionalProject(ctx, req.Params.Project)
	if err != nil {
		return nil, err
	}
	items := []playbookView{}
	for _, p := range s.playbooks.Library.List() {
		v, err := s.playbookView(ctx, p, prj)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	out, err := convert[api.PlaybookList](map[string]any{"items": items})
	if err != nil {
		return nil, err
	}
	return api.PlaybooksList200JSONResponse(out), nil
}

// PlaybooksGet implements playbooks.get; the ETag is the playbook's template version (If-Match of playbooks.run).
func (s *Server) PlaybooksGet(ctx context.Context, req api.PlaybooksGetRequestObject) (api.PlaybooksGetResponseObject, error) {
	p, ok := s.playbooks.Library.Get(req.Name)
	if !ok {
		return nil, problems.NotFound.New("no playbook %q (playbooks.list names them)", req.Name)
	}
	prj, err := s.optionalProject(ctx, req.Params.Project)
	if err != nil {
		return nil, err
	}
	v, err := s.playbookView(ctx, p, prj)
	if err != nil {
		return nil, err
	}
	body, err := convert[api.Playbook](v)
	if err != nil {
		return nil, err
	}
	resp := api.PlaybooksGet200JSONResponse{Body: body}
	if v.Version != "" {
		etag := `"` + v.Version + `"`
		resp.Headers.ETag = &etag
	}
	return resp, nil
}

// PlaybooksRun implements playbooks.run: resolve the inputs, sum the estimate, render the prompt and start an agent
// session of kind playbook whose plan is the chain; a dry run answers all of it without the session.
func (s *Server) PlaybooksRun(ctx context.Context, req api.PlaybooksRunRequestObject) (api.PlaybooksRunResponseObject, error) {
	pb, ok := s.playbooks.Library.Get(req.Name)
	if !ok {
		return nil, problems.NotFound.New("no playbook %q (playbooks.list names them)", req.Name)
	}
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ver, hasVer, err := playbookVersion(ctx, s.Pool, pb.Name)
	if err != nil {
		return nil, err
	}
	if want := versionOf(req.Params.IfMatch); want != "*" && (!hasVer || (want != ver.Version && want != ver.ID)) {
		return nil, problems.PreconditionFailed.New("the playbook %s is at version %q, not %q; re-read it (playbooks.get) or send If-Match: *",
			pb.Name, ver.Version, want)
	}
	body := api.AgentSessionNew{}
	var given map[string]any
	if req.Body != nil {
		body.Driver, body.Model = req.Body.Driver, req.Body.Model
		if req.Body.Inputs != nil {
			given = *req.Body.Inputs
		}
	}
	in, err := s.sessionInput(ctx, p, &body)
	if err != nil {
		return nil, err
	}
	if p.Repository == nil || !svc.Repos().Exists(p.Slug) {
		return nil, problems.Conflict.New("project %q has no repository yet; a playbook session works on a clone of it", p.Slug)
	}
	if p.State != projects.StateActive {
		return nil, problems.Conflict.New("project %q is %s; playbook sessions start in active projects", p.Slug, p.State)
	}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, "playbooks.run", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		prep, err := s.playbooks.Prepare(ctx, tx, pb, ver.ID, p, given)
		if err != nil {
			return commands.Result{}, nil, err
		}
		view, err := s.playbookView(ctx, pb, &p)
		if err != nil {
			return commands.Result{}, nil, err
		}
		result := map[string]any{"playbook": view, "inputs": prep.Inputs.Values, "estimate": prep.Estimate,
			"plan": prep.State.Plan, "prompt": prep.Prompt}
		raw, err := json.Marshal(prep.State)
		if err != nil {
			return commands.Result{}, nil, fmt.Errorf("encode the playbook: %w", err)
		}
		in.Kind, in.Prompt, in.Playbook, in.Notice = sessions.KindPlaybook, prep.Prompt, raw, prep.Notice
		in.StartedBy, in.DryRun = cmd.Actor, cmd.DryRun
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: result}, nil, nil
		}
		sess, drafts, err := s.sessions.Create(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		result["session"] = sess.JSON()
		return commands.Result{Status: http.StatusCreated, Body: result}, drafts, nil
	})
}
