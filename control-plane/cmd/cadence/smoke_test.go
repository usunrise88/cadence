package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The smoke dry-runs the playbook, starts it, follows the session until its plan ends and reports done or stopped.
func TestSmokeRunsAndFollowsThePlaybook(t *testing.T) {
	var polls atomic.Int32
	var gotInputs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/demo/playbooks/try-cadence:run":
			if r.Header.Get("Idempotency-Key") == "" || r.Header.Get("If-Match") != "*" || r.Header.Get("Authorization") != "Bearer cdk_x" {
				t.Errorf("headers %v", r.Header)
			}
			var body struct{ Inputs map[string]any }
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			gotInputs = body.Inputs
			if r.URL.Query().Get("dryRun") == "true" {
				_, _ = io.WriteString(w, `{"estimate":{"gpuHours":{"value":0.9,"low":0.45,"high":1.35},"basis":"hint"},
					"plan":[{"id":"clear","title":"FLEURS is cleared","state":"pending","person":"An admin approves"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"session":{"id":"ses_1"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent-sessions/ses_1":
			if polls.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"playbook":{"state":"running","plan":[{"id":"clear","title":"FLEURS is cleared","state":"running","note":"waiting","person":"An admin approves"}]}}`)
				return
			}
			_, _ = io.WriteString(w, `{"playbook":{"state":"done","summary":"The playbook is complete","next":"Note the timings","plan":[{"id":"clear","title":"FLEURS is cleared","state":"done"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := map[string]string{"CADENCE_URL": srv.URL, "CADENCE_TOKEN": "cdk_x"}
	var out, errs bytes.Buffer
	done, err := smoke(context.Background(), []string{"--project", "demo", "--input", "fleurs=sr_rs", "--input", "steps=300", "--poll", "1ms"},
		func(k string) string { return env[k] }, &out, &errs, srv.Client())
	if err != nil || !done {
		t.Fatalf("smoke: %v %v\n%s%s", done, err, out.String(), errs.String())
	}
	if gotInputs["fleurs"] != "sr_rs" || gotInputs["steps"] != float64(300) {
		t.Errorf("inputs %v", gotInputs)
	}
	for _, want := range []string{"estimate 0.90 GPU-hours", "a person: An admin approves", "session ses_1 started",
		"waiting for a person", "The playbook is complete", "Next: Note the timings"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// --dry-run starts nothing; a missing project is a usage error.
	out.Reset()
	polls.Store(0)
	if done, err := smoke(context.Background(), []string{"--project", "demo", "--dry-run"}, func(k string) string { return env[k] }, &out, &errs, srv.Client()); err != nil || !done || strings.Contains(out.String(), "started") {
		t.Errorf("dry run: %v %v %s", done, err, out.String())
	}
	if _, err := smoke(context.Background(), nil, func(string) string { return "" }, &out, &errs, srv.Client()); err == nil {
		t.Error("no --project accepted")
	}
}
