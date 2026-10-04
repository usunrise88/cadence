package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// ---------------------------------------------------------------- authentication

// authenticator is the API's authentication middleware for this server's configuration.
func (s *Server) authenticator() *auth.Authenticator {
	a := &auth.Authenticator{Resolver: s.Credentials, Public: publicRequest, OnError: s.writeProblem}
	if s.Actor.ID != "" {
		fixed := s.Actor
		a.Fixed = &fixed
	}
	return a
}

// publicRequest lists what may run without a principal: first start, sign-in and sign-out, help (the sign-in
// screen links error types to their articles), a signed audio link (audio.get checks its signature) and a worker's live
// dial with its lease's live token (workerLive.connect checks the token).
func publicRequest(r *http.Request) bool {
	if signedAudioRequest(r) || liveTokenRequest(r) || signedDeliveryRequest(r) {
		return true
	}
	path := strings.TrimPrefix(r.URL.Path, APIPrefix)
	switch r.Method {
	case http.MethodGet:
		return path == "/auth" || path == "/help" || strings.HasPrefix(path, "/help/")
	case http.MethodPost:
		return path == "/auth:setup" || path == "/auth:login" || path == "/auth:logout" || path == "/auth:accept"
	}
	return false
}

// ---------------------------------------------------------------- auth.*

// AuthGet implements auth.get.
func (s *Server) AuthGet(ctx context.Context, _ api.AuthGetRequestObject) (api.AuthGetResponseObject, error) {
	required, err := credentials.SetupRequired(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	st := api.AuthStatus{SetupRequired: required}
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		a := apiActor(p.Actor)
		st.Actor = &a
		st.Reviewer = s.reviewerStatus(ctx, p)
		if p.Actor.Kind == auth.KindUser && p.UserID != "" {
			u, err := credentials.GetUser(ctx, s.Pool, p.UserID)
			if err != nil {
				return nil, err
			}
			st.TotpEnabled = &u.TOTPEnabled
		}
	}
	return api.AuthGet200JSONResponse(st), nil
}

// AuthSetup implements auth.setup: the admin's first password, then a session.
func (s *Server) AuthSetup(ctx context.Context, req api.AuthSetupRequestObject) (api.AuthSetupResponseObject, error) {
	var username string
	if req.Body.Username != nil {
		username = *req.Body.Username
	}
	var cookie string
	var user credentials.User
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		u, drafts, err := s.Credentials.Setup(ctx, tx, username, req.Body.Password)
		if err != nil {
			return err
		}
		if err := events.Append(ctx, tx, u.Actor(), nil, drafts); err != nil {
			return err
		}
		cookie, err = s.startSession(ctx, tx, u)
		user = u
		return err
	})
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "admin account set up", "user", user.ID, "ip", auth.ClientFrom(ctx).IP)
	return api.AuthSetup200JSONResponse{Body: statusOf(user), Headers: api.AuthSetup200ResponseHeaders{SetCookie: &cookie}}, nil
}

// AuthLogin implements auth.login. Failed attempts count against the address and the username; over the limit
// the answer is 429 rate-limited with Retry-After before any password is checked.
func (s *Server) AuthLogin(ctx context.Context, req api.AuthLoginRequestObject) (api.AuthLoginResponseObject, error) {
	client := auth.ClientFrom(ctx)
	keys := []string{"ip:" + client.IP, "user:" + strings.ToLower(req.Body.Username)}
	for _, k := range keys {
		if retry, ok := s.LoginLimiter.Check(k); !ok {
			e := problems.RateLimited.New("too many failed sign-ins; try again in %d seconds", int(math.Ceil(retry.Seconds())))
			e.RetryAfter = int(math.Ceil(retry.Seconds()))
			return nil, e
		}
	}
	var (
		cookie string
		user   credentials.User
	)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		u, err := s.Credentials.Login(ctx, tx, req.Body.Username, req.Body.Password, deref(req.Body.TotpCode))
		if err != nil {
			return err
		}
		cookie, err = s.startSession(ctx, tx, u)
		user = u
		return err
	})
	if pe, ok := problems.As(err); err != nil && ok && pe.Type == problems.Unauthenticated {
		for _, k := range keys {
			s.LoginLimiter.Record(k)
		}
		s.Log.WarnContext(ctx, "sign-in failed", "username", req.Body.Username, "ip", client.IP)
	}
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "signed in", "user", user.ID, "ip", client.IP)
	return api.AuthLogin200JSONResponse{Body: statusOf(user), Headers: api.AuthLogin200ResponseHeaders{SetCookie: &cookie}}, nil
}

// startSession issues a browser session for u and returns its Set-Cookie value.
func (s *Server) startSession(ctx context.Context, tx pgx.Tx, u credentials.User) (string, error) {
	client := auth.ClientFrom(ctx)
	name := "Browser"
	if client.IP != "" {
		name += " at " + client.IP
	}
	token, _, err := credentials.NewSession(ctx, tx, u.ID, name)
	if err != nil {
		return "", err
	}
	return auth.SessionCookie(token, client.Secure).String(), nil
}

// AuthLogout implements auth.logout: it revokes the session that made the request and clears the cookie.
func (s *Server) AuthLogout(ctx context.Context, _ api.AuthLogoutRequestObject) (api.AuthLogoutResponseObject, error) {
	if p, ok := auth.PrincipalFromContext(ctx); ok && p.CredentialKind == credentials.KindSession {
		if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			return credentials.Revoke(ctx, tx, p.CredentialID)
		}); err != nil {
			return nil, err
		}
		s.Log.InfoContext(ctx, "signed out", "user", p.UserID, "credential", p.CredentialID)
	}
	cleared := auth.ClearSessionCookie(auth.ClientFrom(ctx).Secure).String()
	return api.AuthLogout204Response{Headers: api.AuthLogout204ResponseHeaders{SetCookie: &cleared}}, nil
}

// ---------------------------------------------------------------- totp.*

// sessionUser is the signed-in person behind the request: TOTP belongs to a user, not to a key or an agent.
func sessionUser(ctx context.Context) (string, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return "", problems.Unauthenticated.New("sign in first")
	}
	if p.Actor.Kind != auth.KindUser || p.UserID == "" {
		return "", problems.Forbidden.New("only a signed-in person manages their second factor; API keys and agents cannot")
	}
	return p.UserID, nil
}

// TotpEnroll implements totp.enroll.
func (s *Server) TotpEnroll(ctx context.Context, _ api.TotpEnrollRequestObject) (api.TotpEnrollResponseObject, error) {
	userID, err := sessionUser(ctx)
	if err != nil {
		return nil, err
	}
	var out api.TotpEnrollment
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		secret, uri, err := s.Credentials.EnrollTOTP(ctx, tx, userID)
		out = api.TotpEnrollment{Secret: secret, Uri: uri}
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.TotpEnroll200JSONResponse(out), nil
}

type totpChange func(ctx context.Context, tx pgx.Tx, userID, code string) (credentials.User, []events.Draft, error)

func (s *Server) changeTOTP(ctx context.Context, code string, change totpChange) (api.AuthStatus, error) {
	userID, err := sessionUser(ctx)
	if err != nil {
		return api.AuthStatus{}, err
	}
	var user credentials.User
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		u, drafts, err := change(ctx, tx, userID, code)
		if err != nil {
			return err
		}
		user = u
		return events.Append(ctx, tx, u.Actor(), nil, drafts)
	})
	if err != nil {
		return api.AuthStatus{}, err
	}
	s.Log.InfoContext(ctx, "second factor changed", "user", user.ID, "totp", user.TOTPEnabled)
	return statusOf(user), nil
}

// TotpConfirm implements totp.confirm.
func (s *Server) TotpConfirm(ctx context.Context, req api.TotpConfirmRequestObject) (api.TotpConfirmResponseObject, error) {
	st, err := s.changeTOTP(ctx, req.Body.Code, s.Credentials.ConfirmTOTP)
	if err != nil {
		return nil, err
	}
	return api.TotpConfirm200JSONResponse(st), nil
}

// TotpDisable implements totp.disable.
func (s *Server) TotpDisable(ctx context.Context, req api.TotpDisableRequestObject) (api.TotpDisableResponseObject, error) {
	st, err := s.changeTOTP(ctx, req.Body.Code, s.Credentials.DisableTOTP)
	if err != nil {
		return nil, err
	}
	return api.TotpDisable200JSONResponse(st), nil
}

func statusOf(u credentials.User) api.AuthStatus {
	a := apiActor(u.Actor())
	totp := u.TOTPEnabled
	return api.AuthStatus{SetupRequired: false, Actor: &a, TotpEnabled: &totp}
}

// ---------------------------------------------------------------- credentials.*

// CredentialsList implements credentials.list.
func (s *Server) CredentialsList(ctx context.Context, req api.CredentialsListRequestObject) (api.CredentialsListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	var kind string
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	list, err := credentials.List(ctx, s.Pool, kind, req.Params.Revoked != nil && *req.Params.Revoked)
	if err != nil {
		return nil, err
	}
	slugs, err := s.projectSlugs(ctx)
	if err != nil {
		return nil, err
	}
	p, _ := auth.PrincipalFromContext(ctx)
	items := make([]api.Credential, 0, len(list))
	for _, c := range list {
		items = append(items, apiCredential(c, slugs, p.CredentialID))
	}
	return api.CredentialsList200JSONResponse{Items: items}, nil
}

// CredentialsNew implements credentials.new. The token is created inside the command but kept out of the stored
// response, so an idempotent replay (and the idempotency table) never carries it; only the first 201 does.
func (s *Server) CredentialsNew(ctx context.Context, req api.CredentialsNewRequestObject) (api.CredentialsNewResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	p, _ := auth.PrincipalFromContext(ctx)
	var token string
	cmd := command(ctx, "credentials.new", req.Params.IdempotencyKey, req.Params.DryRun)
	resp, err := s.Pipeline.Run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in := credentials.NewAPIKeyInput{
			UserID: p.UserID, Name: req.Body.Name, Registry: deref(req.Body.Scope.RegistryRead), ExpiresAt: req.Body.ExpiresAt,
			AgentSessions: deref(req.Body.Scope.AgentSessions),
		}
		slugs := map[string]string{}
		if req.Body.Scope.Project != nil {
			pr, err := projects.Get(ctx, tx, *req.Body.Scope.Project)
			if err != nil {
				return commands.Result{}, nil, err
			}
			in.ProjectID, slugs[pr.ID] = pr.ID, pr.Slug
		}
		t, c, drafts, err := credentials.NewAPIKey(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		token = t
		body := api.CredentialCreated{Credential: apiCredential(c, slugs, "")}
		return commands.Result{Status: http.StatusCreated, Body: body, ETag: commands.ETag(c.Rev)}, drafts, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusCreated && resp.Header.Get(commands.HeaderReplayed) == "" && token != "" {
		if resp, err = withToken(resp, token); err != nil {
			return nil, err
		}
	}
	return commandResponse(resp), nil
}

// withToken adds the one-time token to a credentials.new response.
func withToken(resp commands.Response, token string) (commands.Response, error) {
	var body api.CredentialCreated
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return resp, fmt.Errorf("decode credentials.new response: %w", err)
	}
	body.Token = &token
	b, err := json.Marshal(body)
	if err != nil {
		return resp, fmt.Errorf("encode credentials.new response: %w", err)
	}
	resp.Body = append(b, '\n')
	return resp, nil
}

// CredentialsRevoke implements credentials.revoke.
func (s *Server) CredentialsRevoke(ctx context.Context, req api.CredentialsRevokeRequestObject) (api.CredentialsRevokeResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	resp, err := s.Pipeline.Run(ctx, command(ctx, "credentials.revoke", req.Params.IdempotencyKey, req.Params.DryRun),
		func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
			c, drafts, err := credentials.RevokeAt(ctx, tx, req.Id, rev)
			if err != nil {
				return commands.Result{}, nil, err
			}
			slugs := map[string]string{}
			if c.Scope.ProjectID != "" {
				if slugs, err = s.projectSlugs(ctx); err != nil {
					return commands.Result{}, nil, err
				}
			}
			return commands.Result{Status: http.StatusOK, Body: apiCredential(c, slugs, ""), ETag: commands.ETag(c.Rev)}, drafts, nil
		})
	if err != nil {
		return nil, err
	}
	return commandResponse(resp), nil
}

func (c commandResponse) VisitCredentialsNewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitCredentialsRevokeResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// projectSlugs maps project ids to slugs, archived projects included.
func (s *Server) projectSlugs(ctx context.Context) (map[string]string, error) {
	list, err := projects.List(ctx, s.Pool, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, p := range list {
		out[p.ID] = p.Slug
	}
	return out, nil
}

func apiCredential(c credentials.Credential, slugs map[string]string, current string) api.Credential {
	out := api.Credential{
		Id: c.ID, Kind: api.CredentialKind(c.Kind), Name: c.Name, Rev: c.Rev, CreatedAt: c.CreatedAt,
		ExpiresAt: c.ExpiresAt, LastUsedAt: c.LastUsedAt, RevokedAt: c.RevokedAt,
		UserId: optional(c.UserID), Subject: optional(c.Subject),
	}
	if c.Scope.All {
		out.Scope.All = &c.Scope.All
	}
	if c.Scope.ProjectID != "" {
		out.Scope.ProjectId = &c.Scope.ProjectID
		if slug, ok := slugs[c.Scope.ProjectID]; ok {
			out.Scope.Project = &slug
		}
	}
	if c.Scope.RegistryRead {
		out.Scope.RegistryRead = &c.Scope.RegistryRead
	}
	out.Scope.Preset = optional(c.Scope.Preset)
	if c.Scope.AgentSessions {
		out.Scope.AgentSessions = &c.Scope.AgentSessions
	}
	if current != "" && current == c.ID {
		t := true
		out.Current = &t
	}
	return out
}
