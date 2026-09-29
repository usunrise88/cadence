// Package server implements the API contract (api.StrictServerInterface) on top of the command pipeline, the
// stores and the event stream, and assembles the HTTP handler: /api, /mcp, /healthz, /metrics and the SPA.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/webui"
)

// APIPrefix is where the contract's server URL (/api) is mounted.
const APIPrefix = "/api"

// Config wires the server's dependencies.
type Config struct {
	Pool     *pgxpool.Pool
	Pipeline *commands.Pipeline
	Streamer *events.Streamer
	Help     *help.Library
	Log      *slog.Logger
	Metrics  *obs.Metrics
	Tracer   trace.TracerProvider
	Version  string
	// Actor every request is attributed to until phase 1 brings login.
	Actor auth.Actor
	// Defaults backs the MCP resource defaults:// (nil: "not available yet").
	Defaults mcp.DefaultsSource
	// Selection backs the MCP resource selection://current (nil: empty).
	Selection mcp.SelectionStore
}

// Server implements api.StrictServerInterface. Planned operations fall through to api.Planned (501).
type Server struct {
	api.Planned
	Config
	spec *openapi3.T
	api  http.Handler // the contract, routed relative to APIPrefix
	mcp  *mcp.Server
}

var _ api.StrictServerInterface = (*Server)(nil)

// New returns a server; it loads the embedded contract for request validation and the MCP tool manifest.
func New(c Config) (*Server, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded contract: %w", err)
	}
	s := &Server{Config: c, spec: spec}
	s.api = s.APIHandler()
	apiRouter := chi.NewRouter()
	apiRouter.Mount(APIPrefix, s.api)
	// In-process requests from the MCP server inherit the context of the /mcp request, routing state included;
	// chi would take that for a parent router's and skip routing, so each one starts with none.
	apiRoot := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiRouter.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, (*chi.Context)(nil))))
	})
	s.mcp, err = mcp.New(mcp.Options{
		API: apiRoot, APIPrefix: APIPrefix, Log: c.Log, Version: c.Version, Defaults: c.Defaults, Selection: c.Selection,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp server: %w", err)
	}
	return s, nil
}

// Handler is the whole HTTP surface: /api (the contract), /mcp (the same operations as MCP tools), /healthz,
// /metrics, and the SPA for every other path.
func (s *Server) Handler() http.Handler {
	root := chi.NewRouter()
	root.Use(obs.RequestLog(s.Log, s.Metrics))
	root.Get("/healthz", s.healthz)
	root.Handle("/metrics", s.Metrics.Handler())
	root.Mount(APIPrefix, s.api)
	root.Handle(mcp.Path, s.mcp.Handler())
	root.Handle("/*", webui.Handler())
	return obs.Trace(root, s.Tracer)
}

// APIHandler routes the contract's operations (paths relative to /api).
func (s *Server) APIHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.Middleware(s.Actor))
	r.Use(commands.HashMiddleware(s.writeProblem))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.writeProblem(w, r, problems.NotFound.New("no API operation at %s %s", r.Method, r.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.writeProblem(w, r, problems.MethodNotAllowed.New("%s is not an operation on %s", r.Method, r.URL.Path))
	})
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.writeProblem(w, r, problems.BadRequest.New("%v", err))
		},
		ResponseErrorHandlerFunc: s.writeProblem,
	})
	api.HandlerWithOptions(eventStream{ServerInterface: strict, s: s}, api.ChiServerOptions{
		BaseRouter:       r,
		Middlewares:      []api.MiddlewareFunc{newValidator(s.spec, APIPrefix, s.writeProblem).middleware},
		ErrorHandlerFunc: s.paramError,
	})
	return r
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	problems.Write(w, r, s.Log, err)
}

// paramError maps parameter binding failures: a missing If-Match is 428, anything else 400.
func (s *Server) paramError(w http.ResponseWriter, r *http.Request, err error) {
	var rh *api.RequiredHeaderError
	if errors.As(err, &rh) && rh.ParamName == "If-Match" {
		s.writeProblem(w, r, problems.PreconditionRequired.New("this operation changes an existing entity; send If-Match with the ETag of your last read"))
		return
	}
	s.writeProblem(w, r, problems.BadRequest.New("%v", err))
}

type health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	DB      string `json:"db"`
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	h, status := health{Status: "ok", Version: s.Version, DB: "ok"}, http.StatusOK
	if err := s.Pool.Ping(ctx); err != nil {
		s.Log.WarnContext(ctx, "health: database ping failed", "err", err)
		h, status = health{Status: "degraded", Version: s.Version, DB: "unreachable"}, http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, s.Log, status, h)
}
