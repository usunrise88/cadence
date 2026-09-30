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

	"github.com/usunrise88/cadence/control-plane/internal/agentcreds"
	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/backups"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/drafts"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
	"github.com/usunrise88/cadence/control-plane/internal/mixes"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/sessions"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/webui"
	"github.com/usunrise88/cadence/control-plane/internal/workers"
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
	// Actor, when set (non-empty id), is attributed to every request with full scope and no authentication at all:
	// tests and development. Production leaves it empty and authenticates through Credentials.
	Actor auth.Actor
	// Defaults overrides the embedded defaults.yaml (tests); nil means defaults.Get(). It also backs the MCP
	// resource defaults://.
	Defaults *defaults.Defaults
	// Selection backs the MCP resource selection://current (nil: empty).
	Selection mcp.SelectionStore
	// Jobs runs and mirrors jobs; nil in tests that never reach a job.
	Jobs *jobs.Service
	// Secrets is the encrypted secret store (R9); secrets.new fails without it.
	Secrets *secrets.Store
	// Credentials resolves sessions and tokens and checks sign-ins; New creates one on Pool when nil.
	Credentials *credentials.Store
	// LoginLimiter throttles failed sign-ins per address and per username; New creates the default when nil.
	LoginLimiter *auth.Limiter
	// Projects bootstraps and commits to project repositories and serves them at /git; nil in tests that never
	// reach a repository (projects.new then fails).
	Projects *bootstrap.Service
	// CAS is the content-addressed blob store under the artifact store (R15); nil in tests that never reach it.
	CAS *cas.Store
	// StepHooks react to step outputs by artifact type (phase 2: dataset, checkpoint, calibration); New creates an
	// empty registry when nil.
	StepHooks *steps.Hooks
	// Leases is the worker protocol as the pipeline engine sees it; Workers when nil and set, else steps.NoLeases.
	Leases steps.Leases
	// Pipelines is the pipeline engine; New builds one from the fields above when nil (register its step job
	// kind with RegisterJobs before the job service starts).
	Pipelines *pipelines.Engine
	// Workers is the worker protocol (registrations, leases, logs, metrics, the step queue); nil in tests that never
	// reach it (its operations then answer 501).
	Workers *workers.Service

	// Backups takes and restore-tests backup sets (phase 2 · stream O); backups.* fail without it.
	Backups *backups.Service
	// Telegram reaches the Bot API (base URL, HTTP client); the token comes from Secrets.
	Telegram notify.Bot
	// Poller is the Telegram long-poll loop, read for its status only; nil when it does not run.
	Poller *notify.Poller
}

// Server implements api.StrictServerInterface. Planned operations fall through to api.Planned (501).
type Server struct {
	api.Planned
	Config
	spec     *openapi3.T
	api      http.Handler // the contract, routed relative to APIPrefix
	mcp      *mcp.Server
	replay   http.Handler // the API without authentication, for replaying approved requests as their actor
	drafts   *drafts.Store
	mixes    *mixes.Service
	sessions *sessions.Service
	// agentCreds is the agent host's side of agent credentials (claims, reports, the transit sweeper).
	agentCreds *agentcreds.Service
	// runs are training runs over the pipeline engine (checkpoint and calibration hooks, run status).
	runs *runs.Service
}

var _ api.StrictServerInterface = (*Server)(nil)

// New returns a server; it loads the embedded contract for request validation and the MCP tool manifest.
func New(c Config) (*Server, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded contract: %w", err)
	}
	if c.Credentials == nil {
		c.Credentials = credentials.NewStore(c.Pool, time.Now, auth.DefaultPasswordParams())
	}
	if c.LoginLimiter == nil {
		c.LoginLimiter = auth.NewLimiter(time.Now, auth.DefaultLoginLimits()...)
	}
	if c.StepHooks == nil {
		c.StepHooks = &steps.Hooks{}
	}
	(&data.Importer{CAS: c.CAS}).Register(c.StepHooks) // dataset artifacts register dataset versions (R18)
	if c.Leases == nil && c.Workers != nil {
		c.Leases = c.Workers
	}
	if c.Leases == nil {
		c.Leases = steps.NoLeases{}
	}
	if c.Pipelines == nil {
		var repo pipelines.Repo
		if c.Projects != nil {
			repo = c.Projects.Repos()
		}
		c.Pipelines = pipelines.New(pipelines.Options{
			Pool: c.Pool, Jobs: c.Jobs, CAS: c.CAS, Hooks: c.StepHooks, Leases: c.Leases, Repos: repo, Log: c.Log,
			Defaults: func() *defaults.Defaults {
				if c.Defaults != nil {
					return c.Defaults
				}
				return defaults.Get()
			},
		})
	}
	if c.Workers != nil {
		c.Workers.OnLeased(c.Pipelines.Leased) // a granted lease marks its pipeline step running
	}
	s := &Server{Config: c, spec: spec}
	s.runs = s.newRunsService()
	s.runs.Install(c.StepHooks) // checkpoint and calibration outputs; the engine reports run status changes
	window := time.Duration(s.defaultsDoc().Drafts.PresenceSeconds.Value) * time.Second
	s.drafts = drafts.NewStore(time.Now, window)
	s.mixes = mixes.NewService(s.drafts, s.defaultsDoc)
	s.sessions = &sessions.Service{Pool: c.Pool, Projects: c.Projects, Defaults: s.defaultsDoc, Log: c.Log, Attribution: s.drafts}
	s.agentCreds = &agentcreds.Service{Pool: c.Pool, Transit: s.transit(), Log: c.Log}
	if c.Pipeline != nil {
		s.sessions.Policy = c.Pipeline.Policy()
		c.Pipeline.SetGateHook(s.sessions.GatedCommand)
	}
	if c.Selection == nil && c.Pool != nil {
		c.Selection = sessions.Selection{Q: c.Pool}
		s.Selection = c.Selection
	}
	s.api = s.APIHandler()
	apiRouter := chi.NewRouter()
	apiRouter.Mount(APIPrefix, s.api)
	// In-process requests from the MCP server inherit the context of the /mcp request, routing state included;
	// chi would take that for a parent router's and skip routing, so each one starts with none.
	apiRoot := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiRouter.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, (*chi.Context)(nil))))
	})
	s.mcp, err = mcp.New(mcp.Options{
		API: apiRoot, APIPrefix: APIPrefix, Log: c.Log, Version: c.Version, Defaults: defaultsSource{c.Defaults}, Selection: c.Selection,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp server: %w", err)
	}
	replay := chi.NewRouter()
	replay.Mount(APIPrefix, s.apiRouter(false))
	s.replay = replay
	return s, nil
}

// RegisterJobs registers the pipeline engine's step job kind and sweep; call it before the job service starts.
func (s *Server) RegisterJobs(j *jobs.Service) { s.Pipelines.Register(j) }

// Handler is the whole HTTP surface: /api (the contract), /mcp (the same operations as MCP tools), /git (the
// project repositories over smart HTTP), /healthz, /metrics, and the SPA for every other path.
func (s *Server) Handler() http.Handler {
	root := chi.NewRouter()
	root.Use(obs.RequestLog(s.Log, s.Metrics))
	root.Get("/healthz", s.healthz)
	root.Handle("/metrics", s.Metrics.Handler())
	root.Mount(APIPrefix, s.api)
	root.Handle(mcp.Path, s.mcp.Handler())
	if s.Projects != nil {
		root.Handle(repos.HTTPPath+"/*", s.gitHandler())
	}
	root.Handle("/*", webui.Handler())
	return obs.Trace(root, s.Tracer)
}

// APIHandler routes the contract's operations (paths relative to /api).
func (s *Server) APIHandler() http.Handler { return s.apiRouter(true) }

// apiRouter routes the contract's operations; without authentication it serves approved requests replayed with
// their actor already in the context.
func (s *Server) apiRouter(authenticate bool) http.Handler {
	r := chi.NewRouter()
	if authenticate {
		r.Use(s.authenticator().Middleware)
		r.Use(s.workerOnly)
		r.Use(policyScope)
	}
	r.Use(skipForUploads(commands.HashMiddleware(s.writeProblem)))
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

// defaultsSource serves defaults.yaml to the MCP resource defaults://: the configured document or the embedded one.
type defaultsSource struct{ d *defaults.Defaults }

func (s defaultsSource) Defaults(context.Context) (any, error) {
	d := s.d
	if d == nil {
		d = defaults.Get()
	}
	return d.Document(), nil
}

// policyScope hands the authenticated credential's scope to the policy engine: a credential bound to a project
// keeps its project and preset; a person or an unscoped key reaches every project.
func policyScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := auth.ScopeFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		ps := policy.Scope{RegistryRead: sc.RegistryRead || sc.All, Preset: sc.Preset, AgentSessions: sc.AgentSessions}
		if !sc.All {
			ps.ProjectID = sc.ProjectID
		}
		next.ServeHTTP(w, r.WithContext(policy.WithScope(r.Context(), ps)))
	})
}
