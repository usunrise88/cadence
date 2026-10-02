//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

type langPack struct {
	Locale, Path, Sha, Commit, Branch string
	Files                             []struct {
		Path, Content string
		Bytes         int
	}
	Boost []struct {
		Domain, Path, Sha256 string
		Weight               float64
		Terms                []string
	}
	Scoring struct{ Normalizer, VersionId, Version string }
	Issues  []struct{ Path, Message string }
}

func (p langPack) file(path string) (string, bool) {
	for _, f := range p.Files {
		if f.Path == path {
			return f.Content, true
		}
	}
	return "", false
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestLanguagePacks(t *testing.T) {
	e := start(t)
	e.newProject("packs") // the wizard's default locale, he-IL
	base := "/api/projects/packs/langpacks"

	// Bootstrap copied the he-IL starter pack; the list names it and the shipped packs.
	var list struct {
		Items []struct {
			Locale, Path, Sha string
			Files             int
			Boost             []string
			Scoring           struct{ Normalizer, VersionId string }
		}
		Shipped []string
	}
	e.ok(e.do("GET", base, ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Locale != "he-IL" || list.Items[0].Files < 7 || len(list.Items[0].Boost) != 3 ||
		list.Items[0].Scoring.Normalizer != "normalizer/he-il" || !strings.HasPrefix(list.Items[0].Scoring.VersionId, "ver_") ||
		strings.Join(list.Shipped, ",") != "he-IL,sr" {
		t.Fatalf("langpacks.list %+v", list)
	}

	var pk langPack
	resp := e.ok(e.do("GET", base+"/he-IL", ""), 200, &pk)
	if pk.Sha == "" || resp.Header.Get("ETag") != `"`+pk.Sha+`"` || len(pk.Issues) != 0 || len(pk.Boost) != 3 || pk.Boost[0].Weight != 1 {
		t.Fatalf("langpacks.get %+v", pk)
	}
	if _, ok := pk.file("normalizer.yaml"); !ok {
		t.Fatal("no normalizer.yaml")
	}
	expectProblem(t, e.do("GET", base+"/ru-RU", ""), 404, "not-found")

	// langpacks.edit: a stale If-Match, a broken file and a dry run change nothing; a real edit commits.
	lid := "version: 1\nlocale: he-IL\naccept: [he-IL, en]\ncodeSwitch: flag\nminConfidence: 0.6\n"
	editBody := `{"files":[{"path":"lid.yaml","content":` + jsonString(lid) + `}],"message":"accept English"}`
	expectProblem(t, e.do("PATCH", base+"/he-IL", editBody, "Idempotency-Key", e.key(), "If-Match", `"0000000"`), 412, "precondition-failed")
	bad := `{"files":[{"path":"lid.yaml","content":"version: 1\nlocale: he-IL\naccept: []\ncodeSwitch: maybe\nminConfidence: 2\n"}]}`
	pr := expectProblem(t, e.do("PATCH", base+"/he-IL", bad, "Idempotency-Key", e.key(), "If-Match", `"`+pk.Sha+`"`), 422, "validation-failed")
	if !strings.Contains(fmt.Sprint(pr), "codeSwitch") {
		t.Fatalf("validation problem %+v", pr)
	}
	expectProblem(t, e.do("PATCH", base+"/he-IL", `{"files":[{"path":"../x.yaml","content":"x"}]}`, "Idempotency-Key", e.key(), "If-Match", `"`+pk.Sha+`"`), 422, "validation-failed")
	expectProblem(t, e.do("PATCH", base+"/he-IL", `{"files":[{"path":"normalizer.yaml","delete":true}]}`, "Idempotency-Key", e.key(), "If-Match", `"`+pk.Sha+`"`), 422, "validation-failed")
	var dry langPack
	e.ok(e.do("PATCH", base+"/he-IL?dryRun=true", editBody, "Idempotency-Key", e.key(), "If-Match", `"`+pk.Sha+`"`), 200, &dry)
	if c, _ := dry.file("lid.yaml"); c != lid || dry.Sha != pk.Sha || !strings.Contains(e.recipe("packs", "lang/he-IL/lid.yaml", "").Content, "codeSwitch: keep") {
		t.Fatalf("dry run %+v", dry)
	}
	var edited langPack
	resp = e.ok(e.do("PATCH", base+"/he-IL", editBody, "Idempotency-Key", e.key(), "If-Match", `"`+pk.Sha+`"`), 200, &edited)
	r := e.recipe("packs", "lang/he-IL/lid.yaml", "")
	if edited.Sha == pk.Sha || resp.Header.Get("ETag") != `"`+edited.Sha+`"` || r.Content != lid || r.History[0].Message != "accept English" ||
		r.History[0].Sha != edited.Sha || edited.Branch != "" {
		t.Fatalf("edit %+v / %+v", edited, r)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE topic = 'recipe.lang/he-IL/lid.yaml' AND payload->>'commit' = '` + edited.Sha + `'`); n != 1 {
		t.Fatalf("%d recipe events for the edited file", n)
	}

	// boost.edit renders one list; its weight stays unless sent, and the header comments are kept.
	var boosted langPack
	e.ok(e.do("PATCH", base+"/he-IL/boost/names", `{"terms":["  Moshe Cohen ","Dana Levi"]}`, "Idempotency-Key", e.key(), "If-Match", `"`+edited.Sha+`"`), 200, &boosted)
	names := e.recipe("packs", "lang/he-IL/boost/names.txt", "").Content
	if !strings.HasPrefix(names, "# weight: 1\n# Boost list") || !strings.HasSuffix(names, "Moshe Cohen\nDana Levi\n") {
		t.Fatalf("names.txt:\n%s", names)
	}
	e.ok(e.do("PATCH", base+"/he-IL/boost/brands", `{"terms":["WhatsApp"],"weight":2.5}`, "Idempotency-Key", e.key(), "If-Match", `"`+boosted.Sha+`"`), 200, &boosted)
	var brands *struct {
		Domain, Path, Sha256 string
		Weight               float64
		Terms                []string
	}
	for i := range boosted.Boost {
		if boosted.Boost[i].Domain == "brands" {
			brands = &boosted.Boost[i]
		}
	}
	if brands == nil || brands.Weight != 2.5 || len(brands.Terms) != 1 || len(brands.Sha256) != 64 {
		t.Fatalf("new list %+v", boosted.Boost)
	}
	expectProblem(t, e.do("PATCH", base+"/he-IL/boost/brands", `{"terms":["a","a"]}`, "Idempotency-Key", e.key(), "If-Match", `"`+boosted.Sha+`"`), 422, "validation-failed")
	expectProblem(t, e.do("PATCH", base+"/he-IL/boost/brands", `{"terms":["a"],"weight":50}`, "Idempotency-Key", e.key(), "If-Match", `"`+boosted.Sha+`"`), 422, "validation-failed")

	// An agent's edit lands on a draft branch (language_pack: draft); main keeps the list.
	var drafted langPack
	e.ok(e.agent("PATCH", base+"/he-IL/boost/brands", `{"terms":["Telegram"]}`, "Idempotency-Key", e.key(), "If-Match", `"`+boosted.Sha+`"`), 200, &drafted)
	today := time.Now().UTC().Format(time.DateOnly)
	if drafted.Branch != "langpack/he-il-"+today || !strings.Contains(e.recipe("packs", "lang/he-IL/boost/brands.txt", drafted.Branch).Content, "Telegram") ||
		strings.Contains(e.recipe("packs", "lang/he-IL/boost/brands.txt", "").Content, "Telegram") {
		t.Fatalf("agent edit %+v", drafted)
	}

	// projects.sync offers starter pack files three-way: an edited file stays, an unedited one that differs from
	// what Cadence ships is updated, a deleted one is not brought back.
	store := e.repos.Repos()
	if _, err := store.Commit(t.Context(), "packs", repos.Change{Message: "local pack changes",
		Delete: []string{"lang/he-IL/boost/streets.txt"}}); err != nil {
		t.Fatal(err)
	}
	var sync struct {
		UpToDate bool
		Changes  []struct{ Path, Status string }
	}
	var p project
	e.ok(e.do("GET", "/api/projects/packs", ""), 200, &p)
	e.ok(e.do("POST", "/api/projects/packs:sync?dryRun=true", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, p.Rev)), 200, &sync)
	if !sync.UpToDate {
		t.Fatalf("a project's own pack edits and deletions are offered back: %+v", sync.Changes)
	}
	// A project that predates the pack (or lost it) gets the whole pack offered.
	var del []string
	_, tree, err := store.ListFiles(t.Context(), "packs", repos.Main, "lang")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tree {
		del = append(del, f.Path)
	}
	if _, err := store.Commit(t.Context(), "packs", repos.Change{Message: "drop the pack", Delete: del}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/projects/packs:sync?dryRun=true", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, p.Rev)), 200, &sync)
	added := 0
	for _, c := range sync.Changes {
		if strings.HasPrefix(c.Path, "lang/he-IL/") && c.Status == "added" {
			added++
		}
	}
	if added < 8 {
		t.Fatalf("missing pack offered with %d files: %+v", added, sync.Changes)
	}
}

func TestLineage(t *testing.T) {
	e, p := startMixes(t)
	ctx := t.Context()
	m := e.newMix(heMix)

	// A model version whose payload names its base model and project; a golden set over the fixture dataset.
	var baseID, dsID string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM registry_versions WHERE id = $1`, p.BaseModel.VersionID).Scan(&baseID); err != nil {
		t.Fatal(err)
	}
	ds, err := registry.Latest(ctx, e.pool, registry.KindDataset, "dataset/fleurs-he-smoke")
	if err != nil {
		t.Fatal(err)
	}
	dsID = ds.ID
	norm, err := registry.Latest(ctx, e.pool, registry.KindNormalizer, "normalizer/he-il")
	if err != nil {
		t.Fatal(err)
	}
	var gs, model registry.Version
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{dsID}, registry.Bundled()); err != nil {
			return err
		}
		var err error
		gs, _, _, err = registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindGoldenSet, Name: "golden-set/he-smoke",
			Licence: "CC-BY-4.0", Actor: registry.Bundled(), Freeze: true,
			Payload: []byte(fmt.Sprintf(`{"datasetVersionId":%q,"normalizerVersionId":%q,"locale":"he-IL","utterances":1,"hours":0.1,
				"datasetHash":"b3:%064d","fingerprint":"x","groups":"speaker"}`, dsID, norm.ID, 0))}, time.Now())
		if err != nil {
			return err
		}
		model, _, _, err = registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindModel, Name: "model/he-smoke",
			Licence: "internal", Actor: registry.Bundled(), Freeze: true,
			Payload: []byte(fmt.Sprintf(`{"baseModelVersionId":%q,"projectId":%q,"lineage":{"datasetVersionIds":[%q]}}`, baseID, p.ID, dsID))}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	type graph struct {
		Root      string
		Truncated bool
		Hidden    int
		Nodes     []struct {
			ID                     string `json:"id"`
			Kind, Label, Direction string
			Distance               int
			ProjectID              string `json:"projectId"`
		}
		Edges []struct{ From, To, Relation string }
	}
	walk := func(id, query string) graph {
		t.Helper()
		var g graph
		e.ok(e.do("GET", "/api/registry/"+id+":lineage"+query, ""), 200, &g)
		return g
	}
	edge := func(g graph, from, to, rel string) bool {
		for _, x := range g.Edges {
			if x.From == from && x.To == to && strings.HasPrefix(x.Relation, rel) {
				return true
			}
		}
		return false
	}

	// Downstream of the dataset: the golden set and the model (payload references), the mix that reads it, the
	// project that adopted it.
	g := walk(dsID, "?direction=downstream&depth=1")
	if !edge(g, dsID, gs.ID, "datasetVersionId") || !edge(g, dsID, model.ID, "lineage.datasetVersionIds") ||
		!edge(g, dsID, m.ID, "dataset") || !edge(g, dsID, p.ID, "adopted") {
		t.Fatalf("downstream of the dataset: %+v", g.Edges)
	}
	// Upstream of the model: its base model, the dataset and (terminal) the project.
	g = walk(model.ID, "?direction=upstream")
	if !edge(g, baseID, model.ID, "baseModelVersionId") || !edge(g, dsID, model.ID, "lineage.datasetVersionIds") ||
		!edge(g, p.ID, model.ID, "projectId") {
		t.Fatalf("upstream of the model: %+v", g.Edges)
	}
	// The golden set both ways: dataset and normalizer upstream; the base model is "used by" the project as an alias.
	g = walk(gs.ID, "")
	if !edge(g, norm.ID, gs.ID, "normalizerVersionId") || !edge(g, dsID, gs.ID, "datasetVersionId") || g.Nodes[0].Direction != "root" {
		t.Fatalf("golden set: %+v", g.Edges)
	}
	g = walk(baseID, "?direction=downstream&depth=1")
	if !edge(g, baseID, p.ID, "adopted") || !edge(g, baseID, model.ID, "baseModelVersionId") {
		t.Fatalf("downstream of the base model: %+v", g.Edges)
	}
	// The mix is project work: upstream it names its dataset group.
	g = walk(m.ID, "?direction=upstream&depth=1")
	if !edge(g, dsID, m.ID, "group target") {
		t.Fatalf("upstream of the mix: %+v", g.Edges)
	}
	// Limits and unknown roots.
	if g = walk(dsID, "?limit=2"); !g.Truncated || len(g.Nodes) != 2 {
		t.Fatalf("limit 2: %d nodes, truncated %v", len(g.Nodes), g.Truncated)
	}
	expectProblem(t, e.do("GET", "/api/registry/ver_00000000-0000-7000-8000-000000000000:lineage", ""), 404, "not-found")
}
