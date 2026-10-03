//go:build integration

package search

import (
	"context"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// TestIndexFoldsWithTheLocaleNormalizer: with a folder, Hebrew text is indexed after the he-IL pack's scoring
// normalizer (normalizer/he-il): an acronym typed with an ASCII quote (צה"ל) is indexed as one word and found by
// its plain spelling; without one, the index folds as before.
func TestIndexFoldsWithTheLocaleNormalizer(t *testing.T) {
	ctx := context.Background()
	pool := openDB(t)
	if _, err := registry.Seed(ctx, pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, pool, "idf", `שִׂיחוֹת צה"ל`)

	plain := NewIndexer(pool, nil, quiet, Sources())
	if _, err := plain.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	var norm string
	if err := pool.QueryRow(ctx, `SELECT title_norm FROM search_documents WHERE id = $1`, p.ID).Scan(&norm); err != nil {
		t.Fatal(err)
	}
	if norm != `שיחות צה"ל` {
		t.Fatalf("without a folder: title_norm %q", norm)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM search_documents WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE event_cursors SET seq = 0 WHERE name = 'search'`); err != nil {
		t.Fatal(err)
	}
	scorers := langpacks.NewScorers(templates.FS, nil)
	folded := NewIndexer(pool, nil, quiet, Sources())
	folded.Folder = scorers
	if _, err := folded.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT title_norm FROM search_documents WHERE id = $1`, p.ID).Scan(&norm); err != nil {
		t.Fatal(err)
	}
	if norm != "שיחות צהל" {
		t.Fatalf("with the he-IL normalizer: title_norm %q", norm)
	}

	// A query folds the same way: צה״ל (gershayim) and צה"ל both find it.
	all := Visibility{AllProjects: true, Registry: true}
	for _, text := range []string{"צהל", "צה״ל", "שיחות"} {
		q, err := Parse(text, Kinds(Sources()))
		if err != nil {
			t.Fatal(err)
		}
		fold, err := FoldFor(ctx, pool, scorers, p.ID, "", q.Text)
		if err != nil {
			t.Fatal(err)
		}
		q.Refold(fold)
		res, err := Search(ctx, pool, Request{Query: q, Visible: all, CurrentProjectID: p.ID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if !has(res, "project", `שִׂיחוֹת צה"ל`) {
			t.Errorf("query %s (terms %+v) did not find the project: %v", text, q.Terms, titles(res))
		}
	}
}
