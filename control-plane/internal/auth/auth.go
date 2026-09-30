// Package auth resolves the actor behind a request and what it may reach.
//
// Every API request carries an Actor (who a command is attributed to) and a Scope (what it may touch). The
// Authenticator middleware resolves both from the session cookie or a Bearer token through a Resolver (the
// credentials store); with a fixed actor configured (tests, development) it attributes every request to that actor
// with full scope. The package also holds the primitives of sign-in that need no database: Argon2id password
// hashes, RFC 6238 TOTP, opaque tokens and their hashes, the login rate limiter and the CSRF rule.
package auth

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Actor kinds, as in the contract's Actor.kind.
const (
	KindUser       = "user"
	KindAgent      = "agent"
	KindAutomation = "automation"
)

// Actor is who a command is attributed to. Its JSON form is the contract's Actor.
type Actor struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// DevActor is the admin user (seeded by migration 0001), used as the fixed actor of tests and development.
func DevActor() Actor { return Actor{Kind: KindUser, ID: "usr_admin", Name: "admin"} }

// Scope is what a credential reaches. The admin's sessions reach everything (All); API keys reach one project,
// the registry (read), or both; agent session tokens reach one project and the registry (read) under a
// permission preset. Its JSON form is stored with the credential and is the contract's CredentialScope.
type Scope struct {
	// All is full access: every project, the registry and instance-wide configuration.
	All bool `json:"all,omitempty"`
	// ProjectID is the one project a scoped credential reaches; empty when it reaches none (or with All).
	ProjectID string `json:"projectId,omitempty"`
	// RegistryRead allows reading registry versions.
	RegistryRead bool `json:"registryRead,omitempty"`
	// Preset is the permission preset of an agent session token (docs/spec/08-resolutions.md R7).
	Preset string `json:"preset,omitempty"`
	// AgentSessions lets an API key of one project run agent sessions there (opt-in; automation such as the evals).
	AgentSessions bool `json:"agentSessions,omitempty"`
}

// FullScope is the scope of the admin's sessions and of the fixed actor.
func FullScope() Scope { return Scope{All: true, RegistryRead: true} }

// AllowsProject reports whether s reaches the project with id projectID.
func (s Scope) AllowsProject(projectID string) bool {
	return s.All || (s.ProjectID != "" && s.ProjectID == projectID)
}

// AllowsRegistryRead reports whether s may read the registry.
func (s Scope) AllowsRegistryRead() bool { return s.All || s.RegistryRead }

type (
	actorKey     struct{}
	scopeKey     struct{}
	principalKey struct{}
)

// WithActor returns ctx carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// FromContext returns the actor of the request; ok is false when the request is not authenticated.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// WithScope returns ctx carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFromContext returns the scope of the request; ok is false when the request is not authenticated.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok
}

// CheckProject fails with forbidden unless the request's scope reaches the project with id projectID.
func CheckProject(ctx context.Context, projectID string) error {
	s, ok := ScopeFromContext(ctx)
	if !ok {
		return problems.Unauthenticated.New("sign in or send a Bearer token")
	}
	if !s.AllowsProject(projectID) {
		return problems.Forbidden.New("this credential does not reach project %s", projectID)
	}
	return nil
}

// CheckRegistryRead fails with forbidden unless the request's scope may read the registry.
func CheckRegistryRead(ctx context.Context) error {
	s, ok := ScopeFromContext(ctx)
	if !ok {
		return problems.Unauthenticated.New("sign in or send a Bearer token")
	}
	if !s.AllowsRegistryRead() {
		return problems.Forbidden.New("this credential may not read the registry")
	}
	return nil
}

// CheckAll fails with forbidden unless the request has full scope: instance-wide operations (credentials, new
// projects, compute, secrets, policies) are the admin's.
func CheckAll(ctx context.Context) error {
	s, ok := ScopeFromContext(ctx)
	if !ok {
		return problems.Unauthenticated.New("sign in or send a Bearer token")
	}
	if !s.All {
		return problems.Forbidden.New("only the admin's own session may do this; this credential is scoped")
	}
	return nil
}

// Principal is what an authenticated request resolved to.
type Principal struct {
	Actor Actor
	Scope Scope
	// CredentialID is the credential that authenticated the request (crd_…); empty for the fixed actor.
	CredentialID string
	// CredentialKind is session, api_key or agent.
	CredentialKind string
	// UserID is the user behind a session or the owner of an API key.
	UserID string
}

// WithPrincipal returns ctx carrying p, its actor and its scope.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = context.WithValue(ctx, principalKey{}, p)
	return WithScope(WithActor(ctx, p.Actor), p.Scope)
}

// PrincipalFromContext returns the principal of the request; ok is false when the request is not authenticated.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
