package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

// minPasswordLen matches the contract's Password schema.
const minPasswordLen = 12

const adminUsage = `usage: cadence admin reset-password [--user NAME] [--password PASSWORD] [--disable-totp]
       cadence admin host-token [--keep-others]
       cadence admin worker-token [--host NAME] [--keep-others]

Sets a user's password from the host shell and signs out all of that user's browser sessions. A lost admin
password is reset this way, never by email (docs/spec/06-platform.md "Authentication and access").
Without --password the new password is read from the first line of standard input, e.g.

  docker compose exec -T control-plane cadence admin reset-password < new-password.txt

--disable-totp also turns off the user's second factor (a lost phone). Needs DATABASE_URL.

host-token prints a new agent host token (cah_…) for an agent host that does not share the control plane's
CADENCE_HOST_TOKEN_FILE volume (another machine); every other host token is revoked unless --keep-others.

worker-token prints a new worker token (cwk_…) for the compute host --host (default staging), for a worker that does
not share the control plane's CADENCE_WORKER_TOKEN_FILE volume; the host's other worker tokens are revoked unless
--keep-others.`

// admin runs the hand-written admin subcommands (R34).
func admin(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "host-token" {
		return hostToken(ctx, args[1:], getenv, stdout)
	}
	if len(args) > 0 && args[0] == "worker-token" {
		return workerToken(ctx, args[1:], getenv, stdout)
	}
	if len(args) == 0 || args[0] != "reset-password" {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return errors.New("unknown admin command; expected reset-password, host-token or worker-token")
	}
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	user := fs.String("user", "admin", "the user whose password is set")
	password := fs.String("password", "", "the new password (default: read from standard input)")
	disableTOTP := fs.Bool("disable-totp", false, "also turn the user's TOTP off")
	if err := fs.Parse(args[1:]); err != nil {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return fmt.Errorf("reset-password: %w", err)
	}
	pw := *password
	if pw == "" {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read the password from standard input: %w", err)
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if len([]rune(pw)) < minPasswordLen {
		return fmt.Errorf("the password must have at least %d characters", minPasswordLen)
	}
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := storage.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	store := credentials.NewStore(pool, time.Now, auth.DefaultPasswordParams())
	var (
		u       credentials.User
		revoked int64
	)
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		u, revoked, err = store.ResetPassword(ctx, tx, *user, pw, *disableTOTP)
		return err
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "password of %s set; %d session(s) signed out; TOTP %s\n", u.Name, revoked, onOff(u.TOTPEnabled))
	return err
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// hostToken issues an agent host token and prints it (shown once).
func hostToken(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("host-token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keep := fs.Bool("keep-others", false, "keep the other host tokens")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return fmt.Errorf("host-token: %w", err)
	}
	return printToken(ctx, getenv, stdout, func(tx pgx.Tx) (string, error) {
		tok, _, err := credentials.NewHostToken(ctx, tx, credentials.HostName, !*keep)
		return tok, err
	})
}

// workerToken issues a worker token for one compute host and prints it (shown once).
func workerToken(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("worker-token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	host := fs.String("host", defaultWorkerHost, "the compute host the worker runs on")
	keep := fs.Bool("keep-others", false, "keep the host's other worker tokens")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stdout, adminUsage)
		return fmt.Errorf("worker-token: %w", err)
	}
	return printToken(ctx, getenv, stdout, func(tx pgx.Tx) (string, error) {
		if _, err := compute.Get(ctx, tx, *host); err != nil {
			return "", err
		}
		tok, _, err := credentials.NewWorkerToken(ctx, tx, *host, !*keep)
		return tok, err
	})
}

// printToken opens the database, issues a token in one transaction and prints it.
func printToken(ctx context.Context, getenv func(string) string, stdout io.Writer, issue func(pgx.Tx) (string, error)) error {
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := storage.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var tok string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		tok, err = issue(tx)
		return err
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, tok)
	return err
}
