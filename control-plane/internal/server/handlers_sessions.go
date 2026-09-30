package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/sessions"
)

// Agent sessions (phase 1 · wave 2 · sessions stream): the session entity, its transcript and the agent host's
// protocol (docs/spec/05-agents.md "Session lifecycle").

func (c commandResponse) VisitAgentSessionsNewResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentSessionsCancelResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentSessionsPauseResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentSessionsResumeResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentSessionsAcceptResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentSessionsRevertResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentMessagesNewResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// ---------------------------------------------------------------- sessions

// AgentSessionsList implements agentSessions.list.
func (s *Server) AgentSessionsList(ctx context.Context, req api.AgentSessionsListRequestObject) (api.AgentSessionsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	state := ""
	if req.Params.State != nil {
		state = string(*req.Params.State)
	}
	list, err := sessions.List(ctx, s.Pool, p.ID, state, deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out := api.AgentSessionList{Items: make([]api.AgentSession, 0, len(list))}
	for _, x := range list {
		v, err := convert[api.AgentSession](x.JSON())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return api.AgentSessionsList200JSONResponse(out), nil
}

// AgentSessionsGet implements agentSessions.get.
func (s *Server) AgentSessionsGet(ctx context.Context, req api.AgentSessionsGetRequestObject) (api.AgentSessionsGetResponseObject, error) {
	sess, err := s.scopedSession(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := convert[api.AgentSession](sess.JSON())
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(sess.Rev)
	return api.AgentSessionsGet200JSONResponse{Body: v, Headers: api.AgentSessionsGet200ResponseHeaders{ETag: &etag}}, nil
}

// scopedSession reads a session the request's credential reaches.
func (s *Server) scopedSession(ctx context.Context, id string) (sessions.Session, error) {
	sess, err := sessions.Get(ctx, s.Pool, id)
	if err != nil {
		return sessions.Session{}, err
	}
	if err := auth.CheckProject(ctx, sess.ProjectID); err != nil {
		return sessions.Session{}, err
	}
	return sess, nil
}

// AgentSessionsNew implements agentSessions.new: driver and model default to the project's agent profile, the
// preset is the profile's (read-only for read-only sessions), budgets come from the policies and defaults.yaml.
func (s *Server) AgentSessionsNew(ctx context.Context, req api.AgentSessionsNewRequestObject) (api.AgentSessionsNewResponseObject, error) {
	svc, err := s.repoService()
	if err != nil {
		return nil, err
	}
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	in, err := s.sessionInput(ctx, p, req.Body)
	if err != nil {
		return nil, err
	}
	if p.Repository == nil || !svc.Repos().Exists(p.Slug) {
		return nil, problems.Conflict.New("project %q has no repository yet; an agent session works on a clone of it", p.Slug)
	}
	if in.Kind == sessions.KindInteractive && p.State != projects.StateActive {
		return nil, problems.Conflict.New("project %q is %s; agent sessions start in active projects", p.Slug, p.State)
	}
	ctx = commands.WithProject(ctx, p.ID)
	cmd := command(ctx, "agentSessions.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.StartedBy, in.DryRun = cmd.Actor, cmd.DryRun
		sess, drafts, err := s.sessions.Create(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: sess.JSON(), ETag: commands.ETag(sess.Rev)}, drafts, nil
	})
}

func (s *Server) sessionInput(ctx context.Context, p projects.Project, b *api.AgentSessionNew) (sessions.NewInput, error) {
	d := s.defaultsDoc()
	in := sessions.NewInput{Project: p, Kind: sessions.KindInteractive, AutoMerge: d.Wizard.AutoMerge.Value,
		Preset: d.Wizard.PermissionPreset.Value}
	profile, err := projects.GetAgentProfile(ctx, s.Pool, p.ID)
	var pe *problems.Error
	switch {
	case err == nil:
		in.Driver, in.Model, in.Preset, in.AutoMerge = profile.Driver, profile.Model, profile.PermissionPreset, profile.AutoMerge
	case errors.As(err, &pe) && pe.Type == problems.NotFound:
		in.Driver = d.Wizard.Driver.Value
		if in.Model, err = s.modelFor(ctx, in.Driver); err != nil {
			return in, err
		}
	default:
		return in, err
	}
	if b != nil {
		if b.Kind != nil {
			in.Kind = string(*b.Kind)
		}
		if b.Driver != nil && string(*b.Driver) != in.Driver {
			in.Driver = string(*b.Driver)
			if in.Model, err = s.modelFor(ctx, in.Driver); err != nil {
				return in, err
			}
		}
		if b.Model != nil {
			in.Model = *b.Model
		}
		in.Prompt = deref(b.Prompt)
		if b.References != nil {
			refs, err := convert[[]sessions.Reference](*b.References)
			if err != nil {
				return in, err
			}
			if in.Refs, err = sessions.ParseReferences(refs); err != nil {
				return in, err
			}
		}
	}
	if err := bootstrap.CheckModel(in.Driver, in.Model); err != nil {
		return in, err
	}
	if in.Kind == sessions.KindReadOnly {
		in.Preset = sessions.ReadOnlyPreset
		if in.Prompt == "" {
			return in, problems.Validation([]problems.FieldError{{Path: "prompt", Message: "a read-only session answers one prompt; send it"}})
		}
	}
	if _, ok := s.Pipeline.Policy().Preset(in.Preset); !ok {
		return in, problems.Conflict.New("the agent profile names the unknown permission preset %q", in.Preset)
	}
	pol, err := policies.Get(ctx, s.Pool, d)
	if err != nil {
		return in, err
	}
	in.Budget = sessions.Budget{Turns: pol.Budgets.AgentTurnsPerSession, Tokens: d.Budgets.AgentTokensPerSession.Value,
		TokensPerTurn: d.Budgets.AgentTokensPerTurn.Value}
	if in.Kind == sessions.KindReadOnly {
		in.Budget.Turns = 1
	}
	return in, nil
}

// sessionCommand runs a command on an existing session: If-Match, the session's project for the pipeline, and fn.
func (s *Server) sessionCommand(ctx context.Context, op, id, key, ifMatch string, dry *bool,
	fn func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error)) (commandResponse, error) {
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	sess, err := s.scopedSession(ctx, id)
	if err != nil {
		return commandResponse{}, err
	}
	if _, err := s.repoService(); err != nil {
		return commandResponse{}, err
	}
	ctx = commands.WithProject(ctx, sess.ProjectID)
	cmd := command(ctx, op, key, dry)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		next, drafts, err := fn(ctx, tx, rev, cmd)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: next.JSON(), ETag: commands.ETag(next.Rev)}, drafts, nil
	})
}

// AgentSessionsCancel implements agentSessions.cancel.
func (s *Server) AgentSessionsCancel(ctx context.Context, req api.AgentSessionsCancelRequestObject) (api.AgentSessionsCancelResponseObject, error) {
	end := req.Body != nil && req.Body.End != nil && *req.Body.End
	return s.sessionCommand(ctx, "agentSessions.cancel", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error) {
			return s.sessions.Cancel(ctx, tx, req.Id, rev, end, cmd.Actor)
		})
}

// AgentSessionsPause implements agentSessions.pause.
func (s *Server) AgentSessionsPause(ctx context.Context, req api.AgentSessionsPauseRequestObject) (api.AgentSessionsPauseResponseObject, error) {
	return s.sessionCommand(ctx, "agentSessions.pause", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error) {
			return s.sessions.Pause(ctx, tx, req.Id, rev, cmd.Actor)
		})
}

// AgentSessionsResume implements agentSessions.resume.
func (s *Server) AgentSessionsResume(ctx context.Context, req api.AgentSessionsResumeRequestObject) (api.AgentSessionsResumeResponseObject, error) {
	var budget *sessions.Budget
	if req.Body != nil && req.Body.Budget != nil {
		budget = &sessions.Budget{}
		if req.Body.Budget.Turns != nil {
			budget.Turns = *req.Body.Budget.Turns
		}
		if req.Body.Budget.Tokens != nil {
			budget.Tokens = *req.Body.Budget.Tokens
		}
	}
	return s.sessionCommand(ctx, "agentSessions.resume", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error) {
			return s.sessions.Resume(ctx, tx, req.Id, rev, budget, cmd.Actor)
		})
}

// AgentSessionsAccept implements agentSessions.accept.
func (s *Server) AgentSessionsAccept(ctx context.Context, req api.AgentSessionsAcceptRequestObject) (api.AgentSessionsAcceptResponseObject, error) {
	return s.sessionCommand(ctx, "agentSessions.accept", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error) {
			return s.sessions.Accept(ctx, tx, req.Id, rev, cmd.Actor, cmd.DryRun)
		})
}

// AgentSessionsRevert implements agentSessions.revert.
func (s *Server) AgentSessionsRevert(ctx context.Context, req api.AgentSessionsRevertRequestObject) (api.AgentSessionsRevertResponseObject, error) {
	return s.sessionCommand(ctx, "agentSessions.revert", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int, cmd commands.Command) (sessions.Session, []events.Draft, error) {
			return s.sessions.Revert(ctx, tx, req.Id, rev, cmd.Actor, cmd.DryRun)
		})
}

// ---------------------------------------------------------------- transcript

// AgentMessagesList implements agentMessages.list.
func (s *Server) AgentMessagesList(ctx context.Context, req api.AgentMessagesListRequestObject) (api.AgentMessagesListResponseObject, error) {
	if _, err := s.scopedSession(ctx, req.Id); err != nil {
		return nil, err
	}
	limit := 200
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	after := int64(deref(req.Params.After))
	list, err := sessions.ListMessages(ctx, s.Pool, req.Id, after, limit+1)
	if err != nil {
		return nil, err
	}
	var next *int64
	if len(list) > limit {
		list = list[:limit]
		n := list[len(list)-1].Seq
		next = &n
	}
	out := api.AgentMessageList{Items: make([]api.AgentMessage, 0, len(list)), Next: next}
	for _, m := range list {
		v, err := convert[api.AgentMessage](m.JSON())
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return api.AgentMessagesList200JSONResponse(out), nil
}

// AgentMessagesNew implements agentMessages.new.
func (s *Server) AgentMessagesNew(ctx context.Context, req api.AgentMessagesNewRequestObject) (api.AgentMessagesNewResponseObject, error) {
	sess, err := s.scopedSession(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	var refs []sessions.Reference
	if req.Body.References != nil {
		raw, err := convert[[]sessions.Reference](*req.Body.References)
		if err != nil {
			return nil, err
		}
		if refs, err = sessions.ParseReferences(raw); err != nil {
			return nil, err
		}
	}
	ctx = commands.WithProject(ctx, sess.ProjectID)
	cmd := command(ctx, "agentMessages.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		m, drafts, err := s.sessions.Post(ctx, tx, req.Id, req.Body.Text, refs, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: m.JSON()}, drafts, nil
	})
}

// ---------------------------------------------------------------- the agent host's protocol

// fixedActor reports whether the server attributes every request to a fixed actor (tests and development); the
// host protocol then accepts it too.
func (s *Server) fixedActor() bool { return s.Actor.ID != "" }

// HostSessionsClaim implements hostSessions.claim.
func (s *Server) HostSessionsClaim(ctx context.Context, req api.HostSessionsClaimRequestObject) (api.HostSessionsClaimResponseObject, error) {
	p, err := sessions.CheckHost(ctx, s.fixedActor())
	if err != nil {
		return nil, err
	}
	wait := 20
	if req.Body.Wait != nil {
		wait = *req.Body.Wait
	}
	capacity := 8
	if req.Body.Capacity != nil {
		capacity = *req.Body.Capacity
	}
	w, err := s.sessions.Claim(ctx, sessions.ClaimInput{HostID: req.Body.HostId, CredentialID: p.CredentialID,
		Version: deref(req.Body.Version), Wait: time.Duration(wait) * time.Second, Capacity: capacity})
	if err != nil {
		return nil, err
	}
	out, err := convert[api.HostWork](w)
	if err != nil {
		return nil, err
	}
	return api.HostSessionsClaim200JSONResponse(out), nil
}

// hostReport is the report body as the sessions package reads it (entry fields stay JSON).
type hostReport struct {
	HostID  string           `json:"hostId"`
	Entries []map[string]any `json:"entries"`
	State   *struct {
		State  string           `json:"state"`
		Busy   *bool            `json:"busy"`
		Turn   *int             `json:"turn"`
		Reason *sessions.Reason `json:"reason"`
		Error  string           `json:"error"`
	} `json:"state"`
	Use          *sessions.Use `json:"use"`
	ACPSessionID string        `json:"acpSessionId"`
	Note         string        `json:"note"`
	Withdraw     []string      `json:"withdraw"`
}

// HostSessionsReport implements hostSessions.report.
func (s *Server) HostSessionsReport(ctx context.Context, req api.HostSessionsReportRequestObject) (api.HostSessionsReportResponseObject, error) {
	if _, err := sessions.CheckHost(ctx, s.fixedActor()); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req.Body)
	if err != nil {
		return nil, fmt.Errorf("re-encode the report: %w", err)
	}
	var r hostReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, problems.BadRequest.New("read the report: %v", err)
	}
	in := sessions.ReportInput{HostID: r.HostID, Use: r.Use, ACPSessionID: r.ACPSessionID, Note: r.Note, Withdraw: r.Withdraw}
	for _, body := range r.Entries {
		key, _ := body["key"].(string)
		kind, _ := body["kind"].(string)
		turn, _ := body["turn"].(float64)
		delete(body, "key")
		delete(body, "kind")
		delete(body, "turn")
		in.Entries = append(in.Entries, sessions.HostEntry{Key: key, Kind: kind, Turn: int(turn), Body: body})
	}
	if st := r.State; st != nil {
		in.State = &sessions.StateReport{State: st.State, Busy: st.Busy, Turn: st.Turn, Reason: st.Reason, Error: st.Error}
	}
	sess, err := s.sessions.Report(ctx, req.Id, in)
	if err != nil {
		return nil, err
	}
	v, err := convert[api.AgentSession](sess.JSON())
	if err != nil {
		return nil, err
	}
	return api.HostSessionsReport200JSONResponse(v), nil
}

// HostSessionsAsk implements hostSessions.ask.
func (s *Server) HostSessionsAsk(ctx context.Context, req api.HostSessionsAskRequestObject) (api.HostSessionsAskResponseObject, error) {
	if _, err := sessions.CheckHost(ctx, s.fixedActor()); err != nil {
		return nil, err
	}
	var in struct {
		HostID   string           `json:"hostId"`
		Turn     int              `json:"turn"`
		ToolCall map[string]any   `json:"toolCall"`
		Options  []map[string]any `json:"options"`
	}
	raw, err := json.Marshal(req.Body)
	if err == nil {
		err = json.Unmarshal(raw, &in)
	}
	if err != nil {
		return nil, problems.BadRequest.New("read the permission request: %v", err)
	}
	d, err := s.sessions.Ask(ctx, req.Id, sessions.AskInput{HostID: in.HostID, Turn: in.Turn, ToolCall: in.ToolCall, Options: in.Options})
	if err != nil {
		return nil, err
	}
	out, err := convert[api.HostDecision](d)
	if err != nil {
		return nil, err
	}
	return api.HostSessionsAsk200JSONResponse(out), nil
}

// HostSessionsDecision implements hostSessions.decision.
func (s *Server) HostSessionsDecision(ctx context.Context, req api.HostSessionsDecisionRequestObject) (api.HostSessionsDecisionResponseObject, error) {
	if _, err := sessions.CheckHost(ctx, s.fixedActor()); err != nil {
		return nil, err
	}
	wait := time.Duration(deref(req.Params.Wait)) * time.Second
	d, err := s.sessions.Decision(ctx, req.Id, req.Params.HostId, req.Params.ApprovalId, wait)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.HostDecision](d)
	if err != nil {
		return nil, err
	}
	return api.HostSessionsDecision200JSONResponse(out), nil
}

// HostSessionsRelease implements hostSessions.release.
func (s *Server) HostSessionsRelease(ctx context.Context, req api.HostSessionsReleaseRequestObject) (api.HostSessionsReleaseResponseObject, error) {
	if _, err := sessions.CheckHost(ctx, s.fixedActor()); err != nil {
		return nil, err
	}
	in := sessions.ReleaseInput{HostID: req.Body.HostId}
	if req.Body.Messages != nil {
		in.Messages = *req.Body.Messages
	}
	ids, err := s.sessions.Release(ctx, in)
	if err != nil {
		return nil, err
	}
	return api.HostSessionsRelease200JSONResponse(api.HostReleased{Released: ids}), nil
}

// SweepSessions runs the server-side session clocks (the idle pause of R5, sessions whose approvals expired); main
// schedules it.
func (s *Server) SweepSessions(ctx context.Context) error { return s.sessions.Sweep(ctx) }
