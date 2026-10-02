// Command cadence is the Cadence control plane — the REST API, the event stream and the embedded web UI — and the
// command line of its API (R34, docs/spec/08-resolutions.md).
//
//	cadence [serve]                     run the server (default)
//	cadence admin reset-password [...]  set a user's password from the host shell (see admin.go)
//	cadence version                     print the version
//	cadence <entity> <verb> [flags]     call one API operation on a running server (internal/cli, generated)
//	cadence help [<entity> [<verb>]]    list the generated commands and their flags
//
// The generated commands talk to CADENCE_URL (http://127.0.0.1:8080) with the API key in CADENCE_TOKEN.
// Configuration comes from the environment: DATABASE_URL (required), CADENCE_ADDR (127.0.0.1:8080),
// CADENCE_DATA_DIR (./data), CADENCE_LOG_DIR ($CADENCE_DATA_DIR/logs), CADENCE_LOG_LEVEL (info),
// CADENCE_MASTER_KEY_FILE ($CADENCE_DATA_DIR/master.key; generated on first start when missing),
// CADENCE_GITHUB_API (https://api.github.com; GitHub Enterprise: https://<host>/api/v3), CADENCE_HOST_TOKEN_FILE (where
// the agent host's cah_ token is kept: a file on a volume both containers mount; see credentials.EnsureHostTokenFile),
// CADENCE_EGRESS_TOKEN_FILE (the egress proxy's cep_ token, the same way), CADENCE_CAS_DIR ($CADENCE_DATA_DIR/cas: the
// content-addressed artifact store, shared by volume with the worker), CADENCE_WORKER_TOKEN_FILE (the worker's cwk_
// token for the compute host CADENCE_WORKER_HOST, default staging, the same way). Job logs from workers live under
// $CADENCE_DATA_DIR/job-logs (14 days). CADENCE_BACKUP_DIR ($CADENCE_DATA_DIR/backups:
// backup sets and the content-store mirror; a mount in phase 4), CADENCE_PG_DUMP / CADENCE_PG_RESTORE (the client
// commands, default pg_dump / pg_restore on PATH; their major version must be at least the server's),
// CADENCE_TELEGRAM_API (https://api.telegram.org: the Bot API base URL; tests point it at a fake server).
// Project repositories live under $CADENCE_DATA_DIR/repos (bare), work (working clones) and worktrees; the git binary
// must be on PATH. `cadence egress-proxy` runs the agent sandbox's allowlisting proxy instead (internal/egress).
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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/backups"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/cli"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/eviction"
	"github.com/usunrise88/cadence/control-plane/internal/help"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/search"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/server"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/workers"
	"github.com/usunrise88/cadence/control-plane/migrations"
	"github.com/usunrise88/cadence/control-plane/templates"
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
	case "admin":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := admin(ctx, os.Args[2:], os.Getenv, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "cadence admin:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	case "egress-proxy":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := egressProxy(ctx, os.Getenv); err != nil {
			fmt.Fprintln(os.Stderr, "cadence egress-proxy:", err)
			os.Exit(1)
		}
	default:
		if cmd != "help" && cmd != "--help" && cmd != "-h" && !cli.Known(cmd) {
			fmt.Fprintf(os.Stderr, "usage: cadence [serve|admin|egress-proxy|version|help|<entity> <verb>]\nunknown command %q; `cadence help` lists the commands\n", cmd)
			os.Exit(cli.ExitUsage)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := cli.Run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr, cli.Options{})
		stop()
		os.Exit(code)
	}
}

type config struct {
	databaseURL string
	addr        string
	dataDir     string
	logDir      string
	logLevel    slog.Level
	masterKey   string
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		databaseURL: getenv("DATABASE_URL"),
		addr:        getenv("CADENCE_ADDR"),
		dataDir:     getenv("CADENCE_DATA_DIR"),
		logDir:      getenv("CADENCE_LOG_DIR"),
		masterKey:   getenv("CADENCE_MASTER_KEY_FILE"),
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
	if c.masterKey == "" {
		c.masterKey = filepath.Join(c.dataDir, "master.key")
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
	seeded, err := seed(ctx, pool, log)
	if err != nil {
		return err
	}
	if err := search.IndexHelp(ctx, pool, library.All(), time.Now()); err != nil {
		return fmt.Errorf("index help: %w", err)
	}
	store, err := openSecrets(pool, cfg, log)
	if err != nil {
		return err
	}

	// GPU budgets: the project's daily budget and the agent session's, metered from lease time on GPU cards.
	engine, err := policy.Embedded(runs.Meter{Pool: pool, Defaults: defaults.Get})
	if err != nil {
		return err
	}
	jobSvc := jobs.New(pool, log)
	jobSvc.Tracer = tp
	registerChores(jobSvc, pool, log)
	repoStore, err := repos.NewStore(cfg.dataDir)
	if err != nil {
		return err
	}
	projectRepos, err := bootstrap.New(bootstrap.Options{
		Pool: pool, Repos: repoStore, Secrets: store, Log: log,
		GitHub: repos.GitHub{BaseURL: getenv("CADENCE_GITHUB_API")},
	})
	if err != nil {
		return err
	}
	projectRepos.Register(jobSvc)

	metrics := obs.NewMetrics()
	hub := events.NewHub(256)
	casDir := getenv("CADENCE_CAS_DIR")
	if casDir == "" {
		casDir = filepath.Join(cfg.dataDir, "cas")
	}
	blobs, err := cas.New(casDir)
	if err != nil {
		return err
	}
	workerSvc := workers.New(workers.Options{
		Pool: pool, Secrets: store, CAS: blobs, LogDir: filepath.Join(cfg.dataDir, "job-logs"), Log: log,
	})
	backupDir := getenv("CADENCE_BACKUP_DIR")
	if backupDir == "" {
		backupDir = filepath.Join(cfg.dataDir, "backups")
	}
	backupSvc := &backups.Service{Pool: pool, Log: log, Jobs: jobSvc, Defaults: defaults.Get, Config: backups.Config{
		Dir: backupDir, CASDir: casDir, SecretsDir: store.Dir(), DSN: cfg.databaseURL,
		PgDump: strings.Fields(getenv("CADENCE_PG_DUMP")), PgRestore: strings.Fields(getenv("CADENCE_PG_RESTORE")),
	}}
	backupSvc.Register(jobSvc)
	jobSvc.AddPeriodic("backups.schedule", time.Minute, backupSvc.Tick)
	evictSvc := &eviction.Service{Pool: pool, CAS: blobs, Log: log, MirrorDir: filepath.Join(backupDir, "cas")}
	evictSvc.Register(jobSvc)
	storeWatch := &eviction.Watcher{Service: evictSvc, Defaults: defaults.Get}
	jobSvc.AddPeriodic("storage.watch", 10*time.Minute, storeWatch.Tick)
	if indexed, restored, err := eviction.Backfill(ctx, pool, blobs); err != nil {
		return fmt.Errorf("content-store file index: %w", err)
	} else if indexed+restored > 0 {
		log.Info("content-store file index backfilled", "directories", indexed, "restored", restored)
	}
	bot := notify.Bot{BaseURL: getenv("CADENCE_TELEGRAM_API"), Secrets: store}
	signer := notify.NewSigner(store.DeriveKey("telegram-callback"))
	notifyWake := make(chan struct{}, 1)
	poller := &notify.Poller{Pool: pool, Log: log, Bot: bot, Signer: signer, Defaults: defaults.Get, Spend: runs.Spend,
		Queue: func(ctx context.Context) ([]notify.QueueItem, error) {
			entries, err := workers.Queue(ctx, pool, "")
			out := make([]notify.QueueItem, 0, len(entries))
			for _, e := range entries {
				it := notify.QueueItem{Kind: e.Kind + "@" + e.KindVersion, JobKind: e.JobKind, State: e.State, Project: e.ProjectID}
				if l := e.Lease; l != nil {
					it.Where, it.Progress, it.Message = l.Host, l.Progress, l.Message
					if l.Card >= 0 {
						it.Where = fmt.Sprintf("%s · card %d", l.Host, l.Card)
					}
				}
				out = append(out, it)
			}
			return out, err
		}}
	digester := &notify.Digester{Pool: pool, Log: log, Defaults: defaults.Get, Wake: notifyWake, Spend: runs.Spend,
		Branches: func(ctx context.Context) ([]string, error) {
			ws, err := projectRepos.WaitingBranches(ctx, pool)
			out := make([]string, 0, len(ws))
			for _, w := range ws {
				out = append(out, w.Project+": "+w.Branch)
			}
			return out, err
		}}
	jobSvc.AddPeriodic("notifications.digest", time.Minute, func(ctx context.Context) error {
		_, err := digester.Tick(ctx)
		return err
	})
	srv, err := server.New(server.Config{
		Pool:     pool,
		Pipeline: commands.NewPipeline(pool, log, metrics.Commands, engine),
		Streamer: events.NewStreamer(pool, hub, log, metrics.SSEClients),
		Help:     library,
		Log:      log,
		Metrics:  metrics,
		Tracer:   tp,
		Version:  version,
		Jobs:     jobSvc,
		Secrets:  store,
		Projects: projectRepos,
		CAS:      blobs,
		Workers:  workerSvc,
		Backups:  backupSvc,
		Eviction: evictSvc,
		Telegram: bot,
		Poller:   poller,
	})
	if err != nil {
		return err
	}
	srv.RegisterJobs(jobSvc) // the pipeline engine's step jobs (phase 2)
	jobSvc.AddPeriodic("workerLeases.reap", 10*time.Second, func(ctx context.Context) error {
		_, err := workerSvc.Reap(ctx)
		return err
	})
	jobSvc.AddPeriodic("jobLogs.prune", 24*time.Hour, func(ctx context.Context) error {
		n, err := workerSvc.PruneLogs(ctx)
		if n > 0 {
			log.InfoContext(ctx, "job logs pruned", "count", n)
		}
		return err
	})
	if path := getenv("CADENCE_WORKER_TOKEN_FILE"); path != "" {
		host := getenv("CADENCE_WORKER_HOST")
		if host == "" {
			host = defaultWorkerHost
		}
		issued, err := credentials.EnsureWorkerTokenFile(ctx, pool, path, host)
		if err != nil {
			return fmt.Errorf("worker token: %w", err)
		}
		if issued {
			log.Info("issued a new worker token; older ones of the host are revoked", "file", path, "host", host)
		}
	}
	poller.Decider = srv
	jobSvc.AddPeriodic("agentSessions.sweep", 30*time.Second, srv.SweepSessions)
	jobSvc.AddPeriodic("agentCredentials.sweep", time.Minute, srv.SweepAgentCredentials)
	if path := getenv("CADENCE_HOST_TOKEN_FILE"); path != "" {
		issued, err := credentials.EnsureHostTokenFile(ctx, pool, path)
		if err != nil {
			return fmt.Errorf("agent host token: %w", err)
		}
		if issued {
			log.Info("issued a new agent host token; older host tokens are revoked", "file", path)
		}
	}
	if path := getenv("CADENCE_EGRESS_TOKEN_FILE"); path != "" {
		issued, err := credentials.EnsureEgressTokenFile(ctx, pool, path)
		if err != nil {
			return fmt.Errorf("egress proxy token: %w", err)
		}
		if issued {
			log.Info("issued a new egress proxy token; older ones are revoked", "file", path)
		}
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
		"data_dir", cfg.dataDir, "log_dir", cfg.logDir, "migrations_applied", applied, "help_articles", len(library.All()), "registry_versions_added", seeded)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := jobSvc.Start(runCtx); err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(runCtx)
	g.Go(func() error { return events.NewDispatcher(pool, hub, log, metrics.EventsDispatched).Run(gctx) })
	g.Go(func() error { return search.NewIndexer(pool, hub, log, search.Sources()).Run(gctx) })
	g.Go(func() error {
		return (&notify.Router{Pool: pool, Hub: hub, Log: log, Defaults: defaults.Get, Wake: notifyWake}).Run(gctx)
	})
	g.Go(func() error {
		return (&notify.Sender{Pool: pool, Log: log, Bot: bot, Signer: signer, Defaults: defaults.Get, Wake: notifyWake}).Run(gctx)
	})
	g.Go(func() error { return poller.Run(gctx) })
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

// defaultWorkerHost is the compute host of the worker token in CADENCE_WORKER_TOKEN_FILE when CADENCE_WORKER_HOST is
// not set: the staging host defaults.yaml seeds.
const defaultWorkerHost = "staging"

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

// seed registers the bundled registry versions (base-model catalogue, fixture dataset versions, templates) and
// creates the compute hosts of defaults.yaml that do not exist yet.
func seed(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (int, error) {
	d := defaults.Get()
	added, err := registry.Seed(ctx, pool, templates.FS, time.Now())
	if err != nil {
		return 0, err
	}
	hosts, err := compute.Seed(ctx, pool, d.Compute.Hosts, registry.Bundled())
	if err != nil {
		return 0, fmt.Errorf("seed compute: %w", err)
	}
	if hosts > 0 {
		log.Info("compute hosts created from defaults.yaml", "hosts", hosts)
	}
	return added, nil
}

// openSecrets opens the encrypted secret store under the data directory, generating the master key on first start.
func openSecrets(pool *pgxpool.Pool, cfg config, log *slog.Logger) (*secrets.Store, error) {
	key, created, err := secrets.LoadOrCreateKey(cfg.masterKey)
	if err != nil {
		return nil, err
	}
	if created {
		log.Warn("generated a new master key for the secret store; back it up — without it stored secrets cannot be read",
			"path", cfg.masterKey)
	}
	return secrets.NewStore(pool, filepath.Join(cfg.dataDir, "secrets"), key)
}
