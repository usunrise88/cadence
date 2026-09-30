//go:build integration

package server

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/credentials"
)

// audit.list: the admin's session reads every row; an API key of one project reads that project's rows only (the
// evals grade sessions on the staging stand through such a key); a key without a project is refused.
func TestAuditListScope(t *testing.T) {
	h := startHost(t)
	e := h.env
	ctx := context.Background()
	a := h.newProject("hebrew")
	h.newProject("other")
	h.newMix(heMix) // a command in hebrew

	var keyA, registryKey string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		if keyA, _, _, err = credentials.NewAPIKey(ctx, tx, credentials.NewAPIKeyInput{UserID: "usr_admin", Name: "evals", ProjectID: a.ID, Registry: true}); err != nil {
			return err
		}
		registryKey, _, _, err = credentials.NewAPIKey(ctx, tx, credentials.NewAPIKeyInput{UserID: "usr_admin", Name: "reader", Registry: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var all, mine auditPage
	e.ok(e.do("GET", "/api/audit?limit=500", ""), 200, &all)
	h.ok(h.send(h.url, "GET", "/api/audit?limit=500", "", bearer(keyA)...), 200, &mine)
	if len(mine.Items) == 0 || len(mine.Items) >= len(all.Items) {
		t.Fatalf("project key read %d of %d rows", len(mine.Items), len(all.Items))
	}
	for _, it := range mine.Items {
		if it.ProjectID != a.ID {
			t.Fatalf("project key read a row of %q", it.ProjectID)
		}
	}
	h.ok(h.send(h.url, "GET", "/api/audit?project=hebrew", "", bearer(keyA)...), 200, &mine)
	expectProblem(t, h.send(h.url, "GET", "/api/audit?project=other", "", bearer(keyA)...), 403, "forbidden")
	expectProblem(t, h.send(h.url, "GET", "/api/audit", "", bearer(registryKey)...), 403, "forbidden")
}

type auditPage struct {
	Items []struct {
		ProjectID string `json:"projectId"`
	} `json:"items"`
}

// An API key of one project may run agent sessions there only when the admin allowed it (scope.agentSessions);
// the flag needs the project, and it opens nothing else.
func TestAPIKeyAgentSessions(t *testing.T) {
	h := startHost(t)
	e := h.env
	h.newProject("hebrew")
	var plain, allowed struct {
		Token string `json:"token"`
	}
	e.ok(e.do("POST", "/api/credentials", `{"name":"ci","scope":{"project":"hebrew","registryRead":true}}`, "Idempotency-Key", e.key()), 201, &plain)
	e.ok(e.do("POST", "/api/credentials", `{"name":"evals","scope":{"project":"hebrew","registryRead":true,"agentSessions":true}}`, "Idempotency-Key", e.key()), 201, &allowed)
	expectProblem(t, e.do("POST", "/api/credentials", `{"name":"x","scope":{"registryRead":true,"agentSessions":true}}`, "Idempotency-Key", e.key()), 422, "validation-failed")

	body := `{"prompt":"Say hi"}`
	expectProblem(t, h.send(h.url, "POST", "/api/projects/hebrew/agent-sessions", body, append(bearer(plain.Token), "Idempotency-Key", e.key())...), 403, "policy-denied")
	var s sessionView
	h.ok(h.send(h.url, "POST", "/api/projects/hebrew/agent-sessions", body, append(bearer(allowed.Token), "Idempotency-Key", e.key())...), 201, &s)
	if s.ID == "" {
		t.Fatal("no session")
	}
	// Everything else still follows the default preset: archiving the project waits for a person.
	h.ok(h.send(h.url, "POST", "/api/projects/hebrew:archive", "", append(bearer(allowed.Token), "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 202, nil)
}
