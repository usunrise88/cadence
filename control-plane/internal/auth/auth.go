// Package auth resolves the actor behind a request.
//
// Phase 0 has no login: every request acts as the fixed development actor (the single admin, seeded by migration
// 0001). Phase 1 replaces Middleware with session-cookie and token authentication; everything downstream already
// reads the actor from the context.
package auth

import (
	"context"
	"net/http"
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

// DevActor is the fixed development actor used until phase 1 lands real login.
func DevActor() Actor { return Actor{Kind: KindUser, ID: "usr_admin", Name: "admin"} }

type ctxKey struct{}

// WithActor returns ctx carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// FromContext returns the actor of the request; ok is false when no middleware set one.
func FromContext(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(ctxKey{}).(Actor)
	return a, ok
}

// Middleware attributes every request to a.
func Middleware(a Actor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), a)))
		})
	}
}
