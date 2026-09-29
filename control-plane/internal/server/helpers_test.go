package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
)

// newTestServer wires a server like main does; pool may be nil for tests that never reach the database.
func newTestServer(t *testing.T, pool *pgxpool.Pool, hub *events.Hub, metrics *obs.Metrics) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	lib, err := help.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Pool:     pool,
		Pipeline: commands.NewPipeline(pool, log, metrics.Commands),
		Streamer: events.NewStreamer(pool, hub, log, metrics.SSEClients),
		Help:     lib,
		Log:      log,
		Metrics:  metrics,
		Tracer:   noop.NewTracerProvider(),
		Version:  "test",
		Actor:    auth.DevActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type problem struct {
	Type       string `json:"type"`
	Status     int    `json:"status"`
	Detail     string `json:"detail"`
	CurrentRev *int   `json:"currentRev"`
	Errors     []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	} `json:"errors"`
}

// expectProblem checks that resp is problem+json of the given slug and status.
func expectProblem(t *testing.T, resp *http.Response, status int, slug string) problem {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var p problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("%d %s: not JSON: %s", resp.StatusCode, resp.Request.URL, body)
	}
	if resp.StatusCode != status || !strings.HasSuffix(p.Type, "/errors/"+slug) || p.Status != status {
		t.Fatalf("got %d %s, want %d %s; body %s", resp.StatusCode, p.Type, status, slug, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	return p
}
