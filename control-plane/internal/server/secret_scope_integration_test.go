//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// projectIDs creates projects with these slugs directly (no bootstrap) and returns their ids by slug.
func (w *wenv) projectIDs(slugs ...string) map[string]string {
	w.t.Helper()
	ids := map[string]string{}
	ctx := auth.WithActor(context.Background(), auth.DevActor())
	for _, slug := range slugs {
		if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
			p, drafts, err := projects.Create(ctx, tx, projects.NewInput{Slug: slug, Name: slug, Locales: []string{"he-IL"},
				Domain: "general", Budgets: projects.DefaultBudgets()})
			if err != nil {
				return err
			}
			ids[slug] = p.ID
			return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	return ids
}

// TestLeaseSecretScope: a project-scoped secret reaches only the leases of that project's steps; a step of another
// project, or of no project, that names it fails with an input error saying why, and never sees the value.
func TestLeaseSecretScope(t *testing.T) {
	w := startWorkers(t)
	ids := w.projectIDs("alpha", "beta")
	for _, s := range []secrets.NewInput{
		{Name: "alpha-token", Kind: "huggingface", Scope: "project:alpha", Value: []byte("alpha_secret_value")},
		{Name: "shared-token", Kind: "huggingface", Value: []byte("shared_secret_value")},
	} {
		if err := pgx.BeginFunc(context.Background(), w.pool, func(tx pgx.Tx) error {
			_, _, err := w.admin.Secrets.Create(context.Background(), tx, s, auth.DevActor(), false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"echo": kind("1", "data", false, true)})
	spec := func(project string, names ...string) steps.Spec {
		return steps.Spec{Kind: "echo", KindVersion: "1", Params: json.RawMessage(`{}`), ProjectID: ids[project], SecretNames: names,
			Resources: steps.Resources{JobKind: steps.JobData}}
	}

	for _, tc := range []struct {
		name, project string
		secrets       []string
		wantEnv       map[string]string
		wantErr       string // "" = leased
	}{
		{name: "own project", project: "alpha", secrets: []string{"alpha-token", "shared-token"},
			wantEnv: map[string]string{"ALPHA_TOKEN": "alpha_secret_value", "SHARED_TOKEN": "shared_secret_value"}},
		{name: "instance secret anywhere", project: "beta", secrets: []string{"shared-token"},
			wantEnv: map[string]string{"SHARED_TOKEN": "shared_secret_value"}},
		{name: "another project", project: "beta", secrets: []string{"shared-token", "alpha-token"},
			wantErr: `secret "alpha-token" is scoped to project:alpha, not to project beta`},
		{name: "no project", project: "", secrets: []string{"alpha-token"},
			wantErr: `secret "alpha-token" is scoped to project:alpha and this work belongs to no project`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobID := w.enqueue(spec(tc.project, tc.secrets...))
			l := f.claim(0)
			if tc.wantErr != "" {
				if l != nil {
					t.Fatalf("leased %+v", l)
				}
				o := w.outcome(jobID)
				if o.State != steps.StateFailed || o.Error == nil || o.Error.Type != steps.ErrInput || !strings.Contains(o.Error.Message, tc.wantErr) {
					t.Fatalf("outcome %+v", o.Error)
				}
				return
			}
			if l == nil || l.JobID != jobID || len(l.Env) != len(tc.wantEnv) {
				t.Fatalf("lease %+v", l)
			}
			for k, v := range tc.wantEnv {
				if l.Env[k] != v {
					t.Errorf("env %s = %q, want %q", k, l.Env[k], v)
				}
			}
			w.ok(f.release(l.ID, steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{}}), http.StatusNoContent, nil)
			w.outcome(jobID)
		})
	}
	if n := w.count("SELECT count(*) FROM events WHERE payload::text LIKE '%alpha_secret%'"); n != 0 {
		t.Fatal("a secret value reached an event")
	}
}
