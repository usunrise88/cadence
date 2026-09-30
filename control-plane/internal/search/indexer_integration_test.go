//go:build integration

package search

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
	"github.com/usunrise88/cadence/control-plane/templates"
)

func TestMain(m *testing.M) { testdb.Main(m) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// command runs fn in a transaction and appends its events, as the command pipeline does.
func command(t *testing.T, pool *pgxpool.Pool, fn func(tx pgx.Tx) ([]events.Draft, error)) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		drafts, err := fn(tx)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newProject(t *testing.T, pool *pgxpool.Pool, slug, name string) projects.Project {
	t.Helper()
	var p projects.Project
	command(t, pool, func(tx pgx.Tx) ([]events.Draft, error) {
		var d []events.Draft
		var err error
		p, d, err = projects.Create(context.Background(), tx, projects.NewInput{Slug: slug, Name: name})
		return d, err
	})
	return p
}

func find(t *testing.T, pool *pgxpool.Pool, q string, vis Visibility, cur string) Result {
	t.Helper()
	parsed, err := Parse(q, Kinds(Sources()))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), pool, Request{Query: parsed, Visible: vis, CurrentProjectID: cur, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func titles(r Result) []string {
	var out []string
	for _, g := range r.Groups {
		for _, h := range g.Hits {
			out = append(out, g.Kind+":"+h.Title)
		}
	}
	return out
}

func has(r Result, kind, title string) bool {
	for _, g := range r.Groups {
		for _, h := range g.Hits {
			if h.Kind == kind && h.Title == title {
				return true
			}
		}
	}
	return false
}

// TestIndexerResumesFromItsCursor: the index follows the outbox, a second indexer (a restart) continues after the
// last committed batch instead of starting over, and a vanished entity leaves the index.
func TestIndexerResumesFromItsCursor(t *testing.T) {
	ctx := context.Background()
	pool := openDB(t)
	alpha := newProject(t, pool, "alpha", "Hebrew telephony")

	first := NewIndexer(pool, nil, quiet, Sources())
	n, err := first.CatchUp(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first catch-up consumed %d events, err %v", n, err)
	}
	all := Visibility{AllProjects: true, Registry: true}
	if r := find(t, pool, "telephony", all, alpha.ID); !has(r, "project", "Hebrew telephony") {
		t.Fatalf("project not indexed: %v", titles(r))
	}
	seq, _, err := Cursor(ctx, pool)
	if err != nil || seq == 0 {
		t.Fatalf("cursor = %d, %v", seq, err)
	}

	// Remove the document by hand: a restart must not replay the event that wrote it (it resumes after it).
	if err := Remove(ctx, pool, "project", alpha.ID); err != nil {
		t.Fatal(err)
	}
	beta := newProject(t, pool, "beta", "Russian read speech")
	command(t, pool, func(tx pgx.Tx) ([]events.Draft, error) {
		name := "Hebrew call centre"
		_, d, err := projects.Edit(ctx, tx, "beta", 1, projects.EditInput{Name: &name})
		return d, err
	})
	if _, err := registry.Seed(ctx, pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}

	restarted := NewIndexer(pool, nil, quiet, Sources())
	restarted.Batch = 3 // several transactions, each advancing the cursor
	if _, err := restarted.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if r := find(t, pool, "telephony", all, beta.ID); has(r, "project", "Hebrew telephony") {
		t.Errorf("the restart replayed events before its cursor: %v", titles(r))
	}
	r := find(t, pool, "call centre", all, beta.ID)
	if !has(r, "project", "Hebrew call centre") || has(r, "project", "Russian read speech") {
		t.Errorf("edit not reflected: %v", titles(r))
	}
	// Registry versions and their collections, with typo tolerance and numeric fields from the payload.
	r = find(t, pool, "flerus kind:dataset_version,collection", all, "")
	if len(r.Groups) != 2 || r.Total < 4 {
		t.Errorf("typo search: %v", titles(r))
	}
	r = find(t, pool, "kind:dataset hours>=2 lang:he", all, "")
	if r.Total != 1 || r.Groups[0].Hits[0].Lang != "he-IL" || r.Groups[0].Hits[0].Numbers["hours"] != 2 {
		t.Errorf("numeric and lang filters: %v", titles(r))
	}
	head, _, _ := Cursor(ctx, pool)
	var head2 int64
	if err := pool.QueryRow(ctx, "SELECT max(seq) FROM events").Scan(&head2); err != nil || head != head2 {
		t.Errorf("cursor %d, outbox head %d (%v)", head, head2, err)
	}

	// Gone from the database → gone from the index at its next event.
	if _, err := pool.Exec(ctx, "DELETE FROM projects WHERE id = $1", beta.ID); err != nil {
		t.Fatal(err)
	}
	command(t, pool, func(pgx.Tx) ([]events.Draft, error) {
		return []events.Draft{{Topic: events.EntityTopic("project", beta.ID), Type: "project.test", ProjectID: beta.ID,
			Entity: &events.EntityRef{Kind: "project", ID: beta.ID, Rev: 3}}}, nil
	})
	if _, err := restarted.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if r := find(t, pool, "call centre", all, ""); r.Total != 0 {
		t.Errorf("deleted project still indexed: %v", titles(r))
	}
}

// TestSearchScopesAndHebrew: visibility is enforced in SQL, the current project ranks first, and Hebrew is found
// without niqqud and across ktiv male/haser.
func TestSearchScopesAndHebrew(t *testing.T) {
	ctx := context.Background()
	pool := openDB(t)
	a := newProject(t, pool, "a", "תוכנית שיחות")
	b := newProject(t, pool, "b", "שִׂיחָה program")
	if _, err := NewIndexer(pool, nil, quiet, Sources()).CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	onlyA := Visibility{ProjectIDs: []string{a.ID}}
	if r := find(t, pool, "program", onlyA, a.ID); r.Total != 0 {
		t.Errorf("project b leaked into a's search: %v", titles(r))
	}
	if r := find(t, pool, "תכנית", onlyA, a.ID); !has(r, "project", "תוכנית שיחות") {
		t.Errorf("ktiv haser did not find ktiv male: %v", titles(r))
	}
	both := Visibility{ProjectIDs: []string{a.ID, b.ID}}
	r := find(t, pool, "שיחה", both, b.ID)
	if !has(r, "project", "שִׂיחָה program") {
		t.Errorf("niqqud not stripped: %v", titles(r))
	}
	r = find(t, pool, "", both, a.ID)
	if r.Total != 2 || r.Groups[0].Hits[0].ID != a.ID {
		t.Errorf("current project should rank first: %v", titles(r))
	}
	if r := find(t, pool, "", Visibility{Registry: true}, ""); r.Total != 0 {
		t.Errorf("registry-only search sees project work: %v", titles(r))
	}
}
