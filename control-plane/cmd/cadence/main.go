// Command cadence is the Cadence control plane: the REST API, the event stream and the embedded web UI.
//
//	cadence [serve]   run the server (default)
//	cadence version   print the version
//
// Configuration comes from the environment: DATABASE_URL (required), CADENCE_ADDR (127.0.0.1:8080),
// CADENCE_DATA_DIR (./data), CADENCE_LOG_DIR ($CADENCE_DATA_DIR/logs), CADENCE_LOG_LEVEL (info).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/server"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

// version is set at build time: -ldflags "-X main.version=<semver>".
var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serve(ctx, os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, "cadence:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "usage: cadence [serve|version]\nunknown command %q\n", cmd)
		os.Exit(2)
	}
}

type config struct {
	databaseURL string
	addr        string
	dataDir     string
	logDir      string
	logLevel    slog.Level
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		databaseURL: getenv("DATABASE_URL"),
		addr:        getenv("CADENCE_ADDR"),
		dataDir:     getenv("CADENCE_DATA_DIR"),
		logDir:      getenv("CADENCE_LOG_DIR"),
	}
	if c.databaseURL == "" {
		return c, errors.New("DATABASE_URL is not set")
	}
	if c.addr == "" {
		c.addr = "127.0.0.1:8080"
	}
	if c.dataDir == "" {
		c.dataDir = "./data"
	}
	if c.logDir == "" {
		c.logDir = filepath.Join(c.dataDir, "logs")
	}
	if lvl := getenv("CADENCE_LOG_LEVEL"); lvl != "" {
		if err := c.logLevel.UnmarshalText([]byte(lvl)); err != nil {
			return c, fmt.Errorf("CADENCE_LOG_LEVEL: %w", err)
		}
	}
	return c, nil
}

func serve(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	log, logFile, err := obs.NewLogger(cfg.logDir, cfg.logLevel)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	tp, err := obs.NewTracerProvider(cfg.logDir, version)
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(sctx); err != nil {
			log.Warn("flush traces", "err", err)
		}
	}()

	pool, err := storage.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	library, err := help.Bundled()
	if err != nil {
		return err
	}

	engine, err := policy.Embedded(policy.StubBudget{GPUHoursPerDay: stubGPUHoursPerDay})
	if err != nil {
		return err
	}
	jobSvc := jobs.New(pool, log)
	registerChores(jobSvc, pool, log)

	metrics := obs.NewMetrics()
	hub := events.NewHub(256)
	srv, err := server.New(server.Config{
		Pool:     pool,
		Pipeline: commands.NewPipeline(pool, log, metrics.Commands, engine),
		Streamer: events.NewStreamer(pool, hub, log, metrics.SSEClients),
		Help:     library,
		Log:      log,
		Metrics:  metrics,
		Tracer:   tp,
		Version:  version,
		Actor:    auth.DevActor(),
		Jobs:     jobSvc,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// No WriteTimeout: the event stream is long-lived; it sets per-write deadlines itself.
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	log.Info("cadence control plane started", "version", version, "addr", listener.Addr().String(),
		"data_dir", cfg.dataDir, "log_dir", cfg.logDir, "migrations_applied", applied, "help_articles", len(library.All()))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := jobSvc.Start(runCtx); err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(runCtx)
	g.Go(func() error { return events.NewDispatcher(pool, hub, log, metrics.EventsDispatched).Run(gctx) })
	g.Go(func() error {
		if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve http: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		log.Info("shutting down")
		hub.Close() // end event streams so Shutdown does not wait for them
		jctx, jcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer jcancel()
		if err := jobSvc.Stop(jctx); err != nil {
			log.Warn("stop jobs", "err", err)
		}
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer scancel()
		if err := httpServer.Shutdown(sctx); err != nil {
			return fmt.Errorf("shutdown http: %w", err)
		}
		return nil
	})
	return g.Wait()
}

// stubGPUHoursPerDay is the daily GPU-hours allowance the policy engine checks spend against until phase 2 meters
// use and reads the project's budget (policy.StubBudget).
const stubGPUHoursPerDay = 8

// registerChores schedules the periodic maintenance jobs: approval expiry (R5) and audit retention.
func registerChores(j *jobs.Service, pool *pgxpool.Pool, log *slog.Logger) {
	j.AddPeriodic("approvals.expire", time.Minute, func(ctx context.Context) error {
		n, err := approvals.Sweep(ctx, pool, time.Now())
		if n > 0 {
			log.InfoContext(ctx, "approvals expired", "count", n)
		}
		return err
	})
	j.AddPeriodic("audit.prune", 24*time.Hour, func(ctx context.Context) error {
		n, err := audit.Prune(ctx, pool, time.Now())
		if n > 0 {
			log.InfoContext(ctx, "audit entries pruned", "count", n)
		}
		return err
	})
}
