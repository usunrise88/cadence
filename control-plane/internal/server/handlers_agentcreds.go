package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/agentcreds"
	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/sessions"
)

// Agent credentials (Settings → Agents): the agents' own model accounts, instance-wide and the admin's
// (docs/spec/08-resolutions.md R3, R6). Values are write-only: the response, the events, the audit row, the log line
// and the idempotency record never carry one (the request fingerprint uses a MAC of the value, as secrets.new does).

func (c commandResponse) VisitAgentCredentialsSetResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentCredentialsVerifyResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitAgentCredentialsArchiveResponse(w http.ResponseWriter) error {
	return c.write(w)
}

func (s *Server) transit() agentcreds.Transit {
	if s.Secrets == nil {
		return nil
	}
	return s.Secrets
}

// AgentCredentialsList implements agentCredentials.list.
func (s *Server) AgentCredentialsList(ctx context.Context, _ api.AgentCredentialsListRequestObject) (api.AgentCredentialsListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	list, err := agentcreds.List(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	connected, seenAt, err := agentcreds.HostStatus(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	model, source, err := s.opencodeDefault(ctx)
	if err != nil {
		return nil, err
	}
	items, err := convert[[]api.AgentCredential](list)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []api.AgentCredential{}
	}
	out := api.AgentCredentialList{Items: items}
	out.Host.Connected, out.Host.SeenAt = connected, seenAt
	out.OpencodeDefault.Model, out.OpencodeDefault.Source = model, api.AgentCredentialListOpencodeDefaultSource(source)
	return api.AgentCredentialsList200JSONResponse(out), nil
}

// opencodeDefault is the model new opencode projects start from: the admin's choice, else defaults.yaml.
func (s *Server) opencodeDefault(ctx context.Context) (model, source string, err error) {
	m, ok, err := agentcreds.DefaultModel(ctx, s.Pool, agentcreds.AgentOpencode)
	if err != nil {
		return "", "", err
	}
	if ok {
		return m, "configured", nil
	}
	return s.defaultsDoc().Wizard.OpencodeModel.Value, "defaults", nil
}

// modelFor is the default model of an agent driver for new projects and sessions: opencode's comes from Settings →
// Agents when the admin chose one, everything else from defaults.yaml.
func (s *Server) modelFor(ctx context.Context, driver string) (string, error) {
	if driver == projects.DriverOpencode {
		m, _, err := s.opencodeDefault(ctx)
		return m, err
	}
	return s.defaultsDoc().Wizard.ModelFor(driver).Value, nil
}

// AgentCredentialsGet implements agentCredentials.get.
func (s *Server) AgentCredentialsGet(ctx context.Context, req api.AgentCredentialsGetRequestObject) (api.AgentCredentialsGetResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	c, err := agentcreds.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	v, err := convert[api.AgentCredential](c)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(c.Rev)
	return api.AgentCredentialsGet200JSONResponse{Body: v, Headers: api.AgentCredentialsGet200ResponseHeaders{ETag: &etag}}, nil
}

// AgentCredentialsSet implements agentCredentials.set.
func (s *Server) AgentCredentialsSet(ctx context.Context, req api.AgentCredentialsSetRequestObject) (api.AgentCredentialsSetResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	in := agentcreds.SetInput{ID: req.Id, Name: req.Body.Name, BaseURL: req.Body.BaseUrl, DefaultModel: req.Body.DefaultModel,
		CatalogueID: deref(req.Body.CatalogueId)}
	if req.Params.IfMatch != nil && *req.Params.IfMatch != "" {
		rev, err := commands.ParseIfMatch(*req.Params.IfMatch)
		if err != nil {
			return nil, err
		}
		in.Rev = rev
	}
	mac := ""
	if req.Body.Value != nil {
		if s.Secrets == nil {
			return nil, errors.New("agentCredentials.set: no secret store configured")
		}
		in.Value = []byte(*req.Body.Value)
		mac = s.Secrets.RequestMAC(in.Value)
	}
	cmd := command(ctx, "agentCredentials.set", req.Params.IdempotencyKey, req.Params.DryRun)
	cmd.RequestHash = agentCredentialRequestHash(req, mac)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor, in.DryRun = cmd.Actor, cmd.DryRun
		c, drafts, err := agentcreds.Set(ctx, tx, s.transit(), in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: c, ETag: commands.ETag(c.Rev)}, drafts, nil
	})
}

// agentCredentialRequestHash fingerprints a set without its value: a MAC of the value under the master key stands in
// for it, so the idempotency record cannot be used to test guesses.
func agentCredentialRequestHash(req api.AgentCredentialsSetRequestObject, valueMAC string) string {
	body, _ := json.Marshal(map[string]any{
		"name": req.Body.Name, "catalogueId": req.Body.CatalogueId, "baseUrl": req.Body.BaseUrl,
		"defaultModel": req.Body.DefaultModel, "valueMac": valueMAC, "ifMatch": req.Params.IfMatch,
	})
	return commands.HashRequest(http.MethodPut, "/agent-credentials/"+req.Id, nil, "", body)
}

// agentCredentialCommand runs verify or archive: If-Match, full scope, fn.
func (s *Server) agentCredentialCommand(ctx context.Context, op, id, key, ifMatch string, dry *bool,
	fn func(ctx context.Context, tx pgx.Tx, rev int) (agentcreds.Credential, []events.Draft, error)) (commandResponse, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return commandResponse{}, err
	}
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	cmd := command(ctx, op, key, dry)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		c, drafts, err := fn(ctx, tx, rev)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: c, ETag: commands.ETag(c.Rev)}, drafts, nil
	})
}

// AgentCredentialsVerify implements agentCredentials.verify.
func (s *Server) AgentCredentialsVerify(ctx context.Context, req api.AgentCredentialsVerifyRequestObject) (api.AgentCredentialsVerifyResponseObject, error) {
	return s.agentCredentialCommand(ctx, "agentCredentials.verify", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int) (agentcreds.Credential, []events.Draft, error) {
			return agentcreds.Verify(ctx, tx, req.Id, rev)
		})
}

// AgentCredentialsArchive implements agentCredentials.archive.
func (s *Server) AgentCredentialsArchive(ctx context.Context, req api.AgentCredentialsArchiveRequestObject) (api.AgentCredentialsArchiveResponseObject, error) {
	return s.agentCredentialCommand(ctx, "agentCredentials.archive", req.Id, req.Params.IdempotencyKey, req.Params.IfMatch, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, rev int) (agentcreds.Credential, []events.Draft, error) {
			return agentcreds.Archive(ctx, tx, req.Id, rev)
		})
}

// AgentProvidersList implements agentProviders.list.
func (s *Server) AgentProvidersList(ctx context.Context, _ api.AgentProvidersListRequestObject) (api.AgentProvidersListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	items, err := convert[[]api.AgentProvider](agentcreds.Catalogue)
	if err != nil {
		return nil, err
	}
	return api.AgentProvidersList200JSONResponse{Items: items}, nil
}

// ---------------------------------------------------------------- the agent host's side

// HostCredentialsClaim implements hostCredentials.claim.
func (s *Server) HostCredentialsClaim(ctx context.Context, req api.HostCredentialsClaimRequestObject) (api.HostCredentialsClaimResponseObject, error) {
	p, err := sessions.CheckHost(ctx, s.fixedActor())
	if err != nil {
		return nil, err
	}
	if s.Secrets == nil {
		return nil, fmt.Errorf("hostCredentials.claim: no secret store configured")
	}
	wait := 20
	if req.Body.Wait != nil {
		wait = *req.Body.Wait
	}
	tasks, err := s.agentCreds.Claim(ctx, req.Body.HostId, p.CredentialID, time.Duration(wait)*time.Second)
	if err != nil {
		return nil, err
	}
	out, err := convert[[]api.HostCredentialTask](tasks)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []api.HostCredentialTask{}
	}
	return api.HostCredentialsClaim200JSONResponse{Tasks: out}, nil
}

// HostCredentialsReport implements hostCredentials.report.
func (s *Server) HostCredentialsReport(ctx context.Context, req api.HostCredentialsReportRequestObject) (api.HostCredentialsReportResponseObject, error) {
	if _, err := sessions.CheckHost(ctx, s.fixedActor()); err != nil {
		return nil, err
	}
	r := agentcreds.Report{HostID: req.Body.HostId, OK: req.Body.Ok, Detail: deref(req.Body.Detail), Model: deref(req.Body.Model)}
	if req.Body.Models != nil {
		r.Models = *req.Body.Models
	}
	state, err := s.agentCreds.Report(ctx, req.Id, r)
	if err != nil {
		return nil, err
	}
	return api.HostCredentialsReport200JSONResponse{Id: req.Id, State: api.HostCredentialAckState(state)}, nil
}

// EgressHostsList implements egressHosts.list: the egress proxy's own credential (cep_) or the admin.
func (s *Server) EgressHostsList(ctx context.Context, _ api.EgressHostsListRequestObject) (api.EgressHostsListResponseObject, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, problems.Unauthenticated.New("send the egress proxy token (cep_…) as a Bearer token")
	}
	if p.CredentialKind != credentials.KindEgressProxy && !p.Scope.All {
		return nil, problems.Forbidden.New("only the egress proxy (a cep_… token) and the admin read the egress allowlist")
	}
	hosts, err := agentcreds.EgressHosts(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	return api.EgressHostsList200JSONResponse{Hosts: hosts}, nil
}

// SweepAgentCredentials drops transit values no open task needs; main schedules it.
func (s *Server) SweepAgentCredentials(ctx context.Context) error {
	if s.Secrets == nil {
		return nil
	}
	return s.agentCreds.Sweep(ctx, s.Secrets.TransitNames)
}
