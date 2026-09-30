// Command mintagent mints an agent session token (cst_) for the Playwright specs, which drive MCP the way an agent
// session does before the agent-session stream exists (spike A4). It is test tooling: the e2e stack script builds
// it next to the control plane and points it at the throwaway database; it never ships in the image.
//
//	DATABASE_URL=… mintagent --project <slug> --session ses_9 [--preset guardrails-default]
//
// It prints the token on stdout. Tokens are stored hashed by internal/credentials like any agent token; the agent
// session stream mints them itself when it creates a session (credentials.MintAgentToken).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mintagent:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mintagent", flag.ContinueOnError)
	project := fs.String("project", "", "project slug the token reaches")
	session := fs.String("session", "ses_e2e", "agent session id (the actor's sessionId)")
	preset := fs.String("preset", "guardrails-default", "permission preset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" || *project == "" {
		return errors.New("DATABASE_URL and --project are required")
	}
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	p, err := projects.Get(ctx, pool, *project)
	if err != nil {
		return err
	}
	var token string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		token, _, err = credentials.MintAgentToken(ctx, tx, *session, p.ID, *preset)
		return err
	}); err != nil {
		return err
	}
	_, err = fmt.Println(token)
	return err
}
