package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

// adminActor is the actor of admin subcommands run from the host shell.
var adminActor = auth.Actor{Kind: auth.KindAutomation, ID: "cadence-admin", Name: "cadence admin (host shell)"}

// rotateSigningKey replaces the instance signing key (R33): every delivery target's chain gets a key-rotation record
// signed by the old key that names the new one, then the old key is retired. It prints the new public key, which a
// person installs as the pin on every production host before the next delivery.
func rotateSigningKey(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rotate-signing-key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reason := fs.String("reason", "", "why the key is rotated (written into every key-rotation record)")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return fmt.Errorf("rotate-signing-key: %w", err)
	}
	if *reason == "" {
		return errors.New("rotate-signing-key: say why with --reason (it goes into every chain)")
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.masterKey); err != nil {
		return fmt.Errorf("rotate-signing-key: the master key %s is not readable here (run this in the control plane's container): %w", cfg.masterKey, err)
	}
	pool, err := storage.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	key, _, err := secrets.LoadOrCreateKey(cfg.masterKey)
	if err != nil {
		return err
	}
	store, err := secrets.NewStore(pool, filepath.Join(cfg.dataDir, "secrets"), key)
	if err != nil {
		return err
	}
	svc := &promotions.Service{Pool: pool, Keys: &promotions.Keyring{Secrets: store}}
	now := time.Now().UTC()
	var (
		old, next promotions.Key
		recs      []promotions.Record
	)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		var err error
		if old, next, recs, drafts, err = svc.Rotate(ctx, tx, adminActor, *reason, now); err != nil {
			return err
		}
		return events.Append(ctx, tx, adminActor, nil, drafts)
	}); err != nil {
		return err
	}
	if err := audit.Write(ctx, pool, audit.Entry{Operation: "signingKeys.rotate", Actor: adminActor, Outcome: audit.OutcomeOK,
		Status: 200, Detail: map[string]any{"retired": old.ID, "current": next.ID, "records": len(recs), "reason": *reason}}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "retired %s; the instance now signs with %s (%d key-rotation record(s) appended)\n"+
		"Install this public key as /etc/cadence/instance.pub on every production host before the next delivery:\n%s",
		old.ID, next.ID, len(recs), promotions.PublicPEM(next.Public))
	return err
}
