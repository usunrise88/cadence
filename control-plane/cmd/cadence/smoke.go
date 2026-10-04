package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/cli"
)

// The smoke project (R34: the hand-written `smoke` subcommand; docs/spec/06-platform.md "Test layers", end to end):
// it runs a playbook — "Try Cadence" by default — in a project on a running control plane and follows the playbook
// session until its plan ends. `make e2e` calls it.

const smokeUsage = `usage: cadence smoke --project SLUG [--playbook try-cadence] [--input NAME=VALUE]... [--dry-run]
                     [--timeout 3h] [--poll 30s]

Runs a playbook in a project of a running control plane (CADENCE_URL, default http://127.0.0.1:8080) and follows its
session until the plan is done (exit 0) or stopped (exit 1). The dry run comes first: it prints the estimate and the
plan; --dry-run stops there. Inputs are JSON values or plain text (--input fleurs=sr_rs --input language=sr-RS
--input steps=300). CADENCE_TOKEN is an API key scoped to the project that may run agent sessions (Settings → API
keys), or the session of a person. Steps a person does (an approval) wait for them: decide them in Approvals.
`

type smokeOptions struct {
	project, playbook string
	inputs            map[string]any
	dryRun            bool
	timeout, poll     time.Duration
}

func parseSmoke(args []string, stderr io.Writer) (smokeOptions, error) {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, smokeUsage) }
	o := smokeOptions{inputs: map[string]any{}}
	fs.StringVar(&o.project, "project", "", "the project's slug")
	fs.StringVar(&o.playbook, "playbook", "try-cadence", "the playbook to run")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the estimate and the plan, start nothing")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Hour, "how long to follow the session")
	fs.DurationVar(&o.poll, "poll", 30*time.Second, "how often to read the session")
	fs.Func("input", "NAME=VALUE, repeatable", func(s string) error {
		name, value, ok := strings.Cut(s, "=")
		if !ok || name == "" {
			return fmt.Errorf("--input %q is not NAME=VALUE", s)
		}
		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			v = value // plain text
		}
		o.inputs[name] = v
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.project == "" || fs.NArg() > 0 {
		fs.Usage()
		return o, errors.New("--project is required and nothing else is")
	}
	if o.poll <= 0 || o.timeout <= 0 {
		return o, errors.New("--poll and --timeout must be positive")
	}
	return o, nil
}

// smokePlan is the part of a playbook session the smoke reads (the contract's AgentPlaybook).
type smokePlan struct {
	State string `json:"state"`
	Plan  []struct {
		ID, Title, State, Note, Person string
	} `json:"plan"`
	Summary string `json:"summary"`
	Next    string `json:"next"`
}

type smokeClient struct {
	base, token string
	http        *http.Client
}

func (c smokeClient) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cadence-smoke")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", "*")
		key := make([]byte, 16)
		_, _ = rand.Read(key)
		req.Header.Set("Idempotency-Key", hex.EncodeToString(key))
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.Unmarshal(b, out)
}

// smoke runs the playbook and follows its session; done reports whether the plan finished (not stopped).
func smoke(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, client *http.Client) (bool, error) {
	o, err := parseSmoke(args, stderr)
	if err != nil {
		return false, err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	c := smokeClient{base: cli.BaseURL(getenv(cli.EnvURL)), token: getenv(cli.EnvToken), http: client}
	run := "/projects/" + url.PathEscape(o.project) + "/playbooks/" + url.PathEscape(o.playbook) + ":run"
	body := map[string]any{"inputs": o.inputs}

	var dry struct {
		Estimate struct {
			GPUHours struct{ Value, Low, High float64 } `json:"gpuHours"`
			Basis    string                             `json:"basis"`
		} `json:"estimate"`
		Plan []struct{ ID, Title, State, Person string } `json:"plan"`
	}
	if err := c.do(ctx, http.MethodPost, run+"?dryRun=true", body, &dry); err != nil {
		return false, fmt.Errorf("dry run: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "%s in %s: estimate %.2f GPU-hours (%.2f–%.2f, %s)\n", o.playbook, o.project,
		dry.Estimate.GPUHours.Value, dry.Estimate.GPUHours.Low, dry.Estimate.GPUHours.High, dry.Estimate.Basis)
	for i, it := range dry.Plan {
		line := fmt.Sprintf("  %2d. %-12s %s", i+1, it.State, it.Title)
		if it.Person != "" {
			line += " — a person: " + it.Person
		}
		_, _ = fmt.Fprintln(stdout, line)
	}
	if o.dryRun {
		return true, nil
	}

	var started struct {
		Session struct{ ID string } `json:"session"`
	}
	if err := c.do(ctx, http.MethodPost, run, body, &started); err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	if started.Session.ID == "" {
		return false, errors.New("start: the answer names no session")
	}
	_, _ = fmt.Fprintf(stdout, "session %s started; following it every %s\n", started.Session.ID, o.poll)

	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	seen := map[string]string{}
	for {
		var s struct {
			Playbook *smokePlan `json:"playbook"`
		}
		if err := c.do(ctx, http.MethodGet, "/agent-sessions/"+url.PathEscape(started.Session.ID), nil, &s); err != nil {
			return false, err
		}
		if s.Playbook == nil {
			return false, fmt.Errorf("session %s has no playbook", started.Session.ID)
		}
		for _, it := range s.Playbook.Plan {
			now := it.State + " " + it.Note
			if seen[it.ID] == now {
				continue
			}
			seen[it.ID] = now
			line := fmt.Sprintf("%s %-12s %s", time.Now().UTC().Format(time.TimeOnly), it.State, it.Title)
			if it.Note != "" {
				line += " (" + it.Note + ")"
			}
			if it.Person != "" && it.State != "done" && it.State != "skipped" {
				line += " — waiting for a person: " + it.Person
			}
			_, _ = fmt.Fprintln(stdout, line)
		}
		if s.Playbook.State != "running" {
			_, _ = fmt.Fprintln(stdout, s.Playbook.Summary)
			if s.Playbook.Next != "" {
				_, _ = fmt.Fprintln(stdout, "Next: "+s.Playbook.Next)
			}
			return s.Playbook.State == "done", nil
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("session %s still running after %s: %w", started.Session.ID, o.timeout, ctx.Err())
		case <-time.After(o.poll):
		}
	}
}
