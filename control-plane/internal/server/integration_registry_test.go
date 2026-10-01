//go:build integration

package server

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// startSeeded runs the control plane like start does, seeds the registry and compute as main does, and returns the
// server too (its secret store is inspected by the tests).
func startSeeded(t *testing.T) (*env, *Server) {
	t.Helper()
	e := start(t)
	if _, err := compute.Seed(t.Context(), e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	return e, e.admin
}

type version struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	State       string   `json:"state"`
	Tags        []string `json:"tags"`
	Licence     string   `json:"licence"`
	Fingerprint string   `json:"fingerprint"`
	BaseModel   struct {
		HfRepo, Revision, FamilyID string
	} `json:"baseModel"`
	Dataset struct {
		Hours   float64 `json:"hours"`
		Fixture bool    `json:"fixture"`
	} `json:"dataset"`
	Template struct {
		TemplateKind string `json:"templateKind"`
	} `json:"template"`
	UsedBy []struct {
		ProjectSlug string   `json:"projectSlug"`
		Aliases     []string `json:"aliases"`
	} `json:"usedBy"`
}

type versionList struct {
	Items []version `json:"items"`
	Kinds []struct {
		Kind  string `json:"kind"`
		Count int    `json:"count"`
	} `json:"kinds"`
}

func (e *env) versionID(kind, collection string) string {
	e.t.Helper()
	var l versionList
	e.ok(e.do("GET", "/api/registry/"+kind+"?collection="+collection, ""), 200, &l)
	if len(l.Items) != 1 {
		e.t.Fatalf("%s %s: %d versions", kind, collection, len(l.Items))
	}
	return l.Items[0].ID
}

func TestRegistrySeedAndReads(t *testing.T) {
	e, _ := startSeeded(t)

	var bm versionList
	e.ok(e.do("GET", "/api/registry/base-models", ""), 200, &bm)
	if len(bm.Items) != 1 {
		t.Fatalf("base models: %+v", bm.Items)
	}
	nemo := bm.Items[0]
	if nemo.Name != "base-model/nemotron-3.5-asr-streaming-0.6b" || nemo.State != "frozen" || nemo.Licence != "openmdw-1.1" ||
		nemo.BaseModel.Revision != "ea30d66debe3740a08b573244286791d423d6b3e" || nemo.BaseModel.FamilyID != "nemo.fastconformer-rnnt.cache-aware" ||
		!strings.HasPrefix(nemo.Version, time.Now().UTC().Format(time.DateOnly)+".") || nemo.Version[11:] != nemo.Fingerprint[:12] {
		t.Fatalf("base model version: %+v", nemo)
	}
	var got version
	e.ok(e.do("GET", "/api/registry/base-models/"+nemo.ID, ""), 200, &got)
	if got.ID != nemo.ID || got.UsedBy == nil {
		t.Fatalf("baseModels.get: %+v", got)
	}
	expectProblem(t, e.do("GET", "/api/registry/datasets/"+nemo.ID, ""), 404, "not-found")

	var search versionList
	e.ok(e.do("GET", "/api/registry?q=locale:he-IL", ""), 200, &search)
	names := map[string]bool{}
	for _, v := range search.Items {
		names[v.Name] = true
	}
	if !names["base-model/nemotron-3.5-asr-streaming-0.6b"] || !names["dataset/fleurs-he-smoke"] || names["dataset/fleurs-ru-smoke"] {
		t.Errorf("locale:he-IL found %v", names)
	}
	e.ok(e.do("GET", "/api/registry?q=kind:dataset_version+fleurs", ""), 200, &search)
	if len(search.Items) != 2 || len(search.Kinds) != 1 || search.Kinds[0].Count != 2 {
		t.Errorf("kind:dataset_version fleurs = %+v", search)
	}
	e.ok(e.do("GET", "/api/registry", ""), 200, &search)
	counts := map[string]int{}
	for _, k := range search.Kinds {
		counts[k.Kind] = k.Count
	}
	if counts["base_model"] != 1 || counts["dataset_version"] != 2 || counts["template"] < 10 {
		t.Errorf("kind counts %v", counts)
	}

	var skills versionList
	e.ok(e.do("GET", "/api/registry/templates?templateKind=skill", ""), 200, &skills)
	if len(skills.Items) != 5 || skills.Items[0].Template.TemplateKind != "skill" {
		t.Errorf("skills: %+v", skills.Items)
	}
	var ds version
	e.ok(e.do("GET", "/api/registry/datasets/"+e.versionID("datasets", "dataset/fleurs-he-smoke"), ""), 200, &ds)
	if !ds.Dataset.Fixture || ds.Dataset.Hours != 2 {
		t.Errorf("dataset payload: %+v", ds.Dataset)
	}

	var cols struct {
		Items []struct {
			Name         string `json:"name"`
			VersionCount int    `json:"versionCount"`
			Latest       *struct{ ID string }
		} `json:"items"`
	}
	e.ok(e.do("GET", "/api/registry/collections?kind=dataset_version", ""), 200, &cols)
	if len(cols.Items) != 2 || cols.Items[0].Name != "dataset/fleurs-he-smoke" || cols.Items[0].VersionCount != 1 || cols.Items[0].Latest == nil {
		t.Errorf("collections.list: %+v", cols.Items)
	}
	var col struct {
		Name     string
		Versions []struct{ ID string }
	}
	e.ok(e.do("GET", "/api/registry/collections/dataset%2Ffleurs-he-smoke", ""), 200, &col)
	if col.Name != "dataset/fleurs-he-smoke" || len(col.Versions) != 1 {
		t.Errorf("collections.get by name: %+v", col)
	}
	expectProblem(t, e.do("GET", "/api/registry/collections/dataset%2Fnope", ""), 404, "not-found")

	// Registry events carry no projectId; seeding again adds nothing.
	if n := e.count(`SELECT count(*) FROM events WHERE topic LIKE 'entity.base_model.ver_%' AND project_id IS NULL
		AND type = 'base_model.registered'`); n != 1 {
		t.Errorf("%d base model events", n)
	}
	before := e.count("SELECT count(*) FROM events")
	if added, err := registry.Seed(t.Context(), e.pool, templates.FS, time.Now().Add(48*time.Hour)); err != nil || added != 0 {
		t.Errorf("second seed added %d (%v)", added, err)
	}
	if after := e.count("SELECT count(*) FROM events"); after != before {
		t.Errorf("second seed emitted %d events", after-before)
	}
}

func TestVersionsAreImmutable(t *testing.T) {
	e, _ := startSeeded(t)
	ctx := t.Context()
	id := e.versionID("datasets", "dataset/fleurs-he-smoke")
	for _, sql := range []string{
		`UPDATE registry_versions SET payload = '{"hours": 99}' WHERE id = $1`,
		`UPDATE registry_versions SET version = '2020-01-01.000000000000' WHERE id = $1`,
		`UPDATE registry_versions SET state = 'draft' WHERE id = $1`,
		`DELETE FROM registry_versions WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id); err == nil || !strings.Contains(err.Error(), "registry version") {
			t.Errorf("%s: err = %v, want the immutability trigger", sql, err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE registry_versions SET state = 'deprecated', deprecated_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("frozen → deprecated: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE registry_versions SET state = 'frozen' WHERE id = $1`, id); err == nil {
		t.Error("deprecated → frozen allowed")
	}

	// New content is a new version; a draft may still change; the old version is untouched.
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		v, created, _, err := registry.Register(ctx, tx, registry.RegisterInput{
			Kind: registry.KindDataset, Name: "dataset/fleurs-he-smoke", Payload: []byte(`{"hours": 3}`), Actor: registry.Bundled(),
		}, time.Now())
		if err != nil || !created || v.State != "draft" || v.ID == id {
			t.Errorf("register new content: %+v created=%v err=%v", v, created, err)
		}
		_, err = tx.Exec(ctx, `UPDATE registry_versions SET payload = '{"hours": 4}' WHERE id = $1`, v.ID)
		return err
	})
	if err != nil {
		t.Fatalf("draft update: %v", err)
	}
	var col struct{ Versions []struct{ ID, State string } }
	e.ok(e.do("GET", "/api/registry/collections/dataset%2Ffleurs-he-smoke", ""), 200, &col)
	if len(col.Versions) != 2 || col.Versions[1].ID != id || col.Versions[1].State != "deprecated" {
		t.Errorf("versions after re-registering: %+v", col.Versions)
	}
}

type alias struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Rev      int     `json:"rev"`
	Reserved string  `json:"reserved"`
	Version  version `json:"version"`
}

func TestAdoptionAndAliases(t *testing.T) {
	e, _ := startSeeded(t)
	p := e.newProject("hebrew")
	he := e.versionID("datasets", "dataset/fleurs-he-smoke")
	ru := e.versionID("datasets", "dataset/fleurs-ru-smoke")
	nemo := e.versionID("base-models", "base-model/nemotron-3.5-asr-streaming-0.6b")

	expectProblem(t, e.do("POST", "/api/projects/hebrew:adopt", `{"version":"`+he+`"}`, "Idempotency-Key", e.key()), 428, "precondition-required")
	e.ok(e.do("POST", "/api/projects/hebrew:adopt?dryRun=true", `{"version":"`+he+`"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	if n := e.count("SELECT count(*) FROM adoptions WHERE version_id = '" + he + "'"); n != 0 {
		t.Fatalf("dry run adopted")
	}
	var a struct {
		ProjectID string  `json:"projectId"`
		Version   version `json:"version"`
	}
	resp := e.ok(e.do("POST", "/api/projects/hebrew:adopt", `{"version":"`+he+`"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &a)
	if resp.Header.Get("ETag") != `"3"` || a.ProjectID != p.ID || a.Version.ID != he {
		t.Fatalf("adopt: %+v etag %s", a, resp.Header.Get("ETag"))
	}
	if lock := e.recipe("hebrew", "data.lock", ""); !strings.Contains(lock.Content, he) || lock.History[0].Message != "adopt dataset/fleurs-he-smoke "+a.Version.Version {
		t.Errorf("data.lock after the adoption: %+v", lock)
	}
	expectProblem(t, e.do("POST", "/api/projects/hebrew:adopt", `{"version":"`+he+`"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 409, "conflict")
	expectProblem(t, e.do("POST", "/api/projects/hebrew:adopt", `{"version":"`+nemo+`"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	expectProblem(t, e.do("POST", "/api/projects/hebrew:adopt", `{"version":"ver_nope"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 404, "not-found")
	// The wizard adopted the project's base model already.
	expectProblem(t, e.do("POST", "/api/projects/hebrew:adopt", `{"version":"`+nemo+`"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 409, "conflict")

	var adoptions struct{ Items []struct{ Version version } }
	e.ok(e.do("GET", "/api/projects/hebrew/adoptions?kind=dataset_version", ""), 200, &adoptions)
	if len(adoptions.Items) != 1 || adoptions.Items[0].Version.ID != he {
		t.Errorf("adoptions.list: %+v", adoptions.Items)
	}

	// Aliases point only at adopted versions.
	put := func(name, ver string, hdr ...string) *http.Response {
		return e.do("PUT", "/api/projects/hebrew/aliases/"+name, `{"version":"`+ver+`"}`,
			append([]string{"Idempotency-Key", e.key()}, hdr...)...)
	}
	expectProblem(t, put("train-current", ru), 409, "conflict")
	var al alias
	resp = e.ok(put("train-current", he), 200, &al)
	if al.Rev != 1 || al.Reserved != "free" || al.Version.ID != he || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("aliases.set create: %+v", al)
	}
	expectProblem(t, put("train-current", he), 428, "precondition-required")
	expectProblem(t, put("brand-new", he, "If-Match", `"1"`), 412, "precondition-failed")
	e.ok(put("train-current", he, "If-Match", `"1"`), 200, &al)
	if al.Rev != 2 {
		t.Errorf("aliases.set update: rev %d", al.Rev)
	}
	pr := expectProblem(t, put("train-current", he, "If-Match", `"1"`), 412, "precondition-failed")
	if pr.CurrentRev == nil || *pr.CurrentRev != 2 {
		t.Errorf("412 currentRev %v", pr.CurrentRev)
	}

	// R8: production moves only by promotion; baseline is gated for everyone — 202, then approve replays it.
	pr = expectProblem(t, put("production", nemo), 409, "reserved-alias")
	if !strings.Contains(pr.Detail, "deployments.promote") {
		t.Errorf("reserved-alias detail %q should point at deployments.promote", pr.Detail)
	}
	var gated struct{ ApprovalID string }
	e.ok(put("baseline", nemo), 202, &gated)
	if gated.ApprovalID == "" {
		t.Fatal("aliases.set baseline answered 202 without an approval id")
	}
	// An alias is project work: the approval is project-scoped and carries the project.
	var apr struct{ Scope, ProjectID string }
	e.ok(e.do("GET", "/api/approvals/"+gated.ApprovalID, ""), 200, &apr)
	if apr.Scope != "project" || apr.ProjectID == "" {
		t.Errorf("baseline approval scope %q project %q, want a project-scoped approval", apr.Scope, apr.ProjectID)
	}
	e.ok(e.do("POST", "/api/approvals/"+gated.ApprovalID+":approve", "{}", "Idempotency-Key", "approve-baseline-0001",
		"If-Match", `"1"`), 200, nil)
	e.ok(e.do("GET", "/api/projects/hebrew/aliases/baseline", ""), 200, &al)
	if al.Reserved != "gated" {
		t.Errorf("baseline reserved = %q", al.Reserved)
	}
	expectProblem(t, e.do("GET", "/api/projects/hebrew/aliases/production", ""), 404, "not-found")
	resp = e.ok(e.do("GET", "/api/projects/hebrew/aliases/baseline", ""), 200, &al)
	if resp.Header.Get("ETag") != `"1"` || al.Version.ID != nemo {
		t.Errorf("aliases.get: %+v", al)
	}
	var aliases struct{ Items []alias }
	e.ok(e.do("GET", "/api/projects/hebrew/aliases", ""), 200, &aliases)
	if len(aliases.Items) != 2 || aliases.Items[0].Name != "baseline" || aliases.Items[1].Name != "train-current" {
		t.Errorf("aliases.list: %+v", aliases.Items)
	}

	// Used by: the base model knows the project and its alias.
	var bm version
	e.ok(e.do("GET", "/api/registry/base-models/"+nemo, ""), 200, &bm)
	if len(bm.UsedBy) != 1 || bm.UsedBy[0].ProjectSlug != "hebrew" || len(bm.UsedBy[0].Aliases) != 1 || bm.UsedBy[0].Aliases[0] != "baseline" {
		t.Errorf("usedBy: %+v", bm.UsedBy)
	}
	var search versionList
	e.ok(e.do("GET", "/api/registry?project=hebrew", ""), 200, &search)
	// The dataset and the base model, plus the templates the bootstrap rendered from.
	byKind := map[string]int{}
	for _, it := range search.Items {
		byKind[it.Kind]++
	}
	if byKind["dataset_version"] != 1 || byKind["base_model"] != 1 || byKind["template"] == 0 {
		t.Errorf("registry.search project=hebrew: %v", byKind)
	}

	// Adoption and alias events are project work; the registry's own are not.
	if n := e.count(`SELECT count(*) FROM events WHERE type = 'project.adopted' AND project_id IS NOT NULL`); n != 1 {
		t.Errorf("%d adoption events", n)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE type = 'alias.set' AND topic LIKE 'entity.alias.als_%' AND project_id IS NOT NULL`); n != 3 {
		t.Errorf("%d alias events", n)
	}
}

func TestSecretsNeverReturned(t *testing.T) {
	e, s := startSeeded(t)
	const value = "hf_Zq8-very-secret-value"
	body := `{"name":"hf-token","kind":"huggingface","value":"` + value + `"}`

	e.ok(e.do("POST", "/api/secrets?dryRun=true", body, "Idempotency-Key", e.key()), 200, nil)
	if n := e.count("SELECT count(*) FROM secrets"); n != 0 {
		t.Fatal("dry run stored a secret")
	}
	resp := e.do("POST", "/api/secrets", body, "Idempotency-Key", "secret-key-1")
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 201 || strings.Contains(string(raw), value) || strings.Contains(string(raw), `"value"`) {
		t.Fatalf("secrets.new = %d %s", resp.StatusCode, raw)
	}
	replay := e.do("POST", "/api/secrets", body, "Idempotency-Key", "secret-key-1")
	_ = replay.Body.Close()
	if replay.StatusCode != 201 || replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay = %d", replay.StatusCode)
	}
	expectProblem(t, e.do("POST", "/api/secrets", strings.Replace(body, value, "other", 1), "Idempotency-Key", "secret-key-1"),
		422, "idempotency-key-reused")
	expectProblem(t, e.do("POST", "/api/secrets", body, "Idempotency-Key", e.key()), 409, "conflict")
	expectProblem(t, e.do("POST", "/api/secrets", `{"name":"x-token","kind":"github","scope":"project:nope","value":"v"}`,
		"Idempotency-Key", e.key()), 404, "not-found")
	expectProblem(t, e.do("POST", "/api/secrets", `{"name":"y-token","kind":"github"}`, "Idempotency-Key", e.key()), 422, "validation-failed")

	list := e.do("GET", "/api/secrets", "")
	raw, _ = io.ReadAll(list.Body)
	_ = list.Body.Close()
	if !strings.Contains(string(raw), `"name":"hf-token"`) || strings.Contains(string(raw), value) {
		t.Fatalf("secrets.list = %s", raw)
	}

	// Nowhere in the database: not the row, the event, the idempotency record or its request hash.
	var dump string
	if err := e.pool.QueryRow(t.Context(), `SELECT
			(SELECT coalesce(string_agg(s::text, ''), '') FROM secrets s) ||
			(SELECT coalesce(string_agg(ev::text, ''), '') FROM events ev) ||
			(SELECT coalesce(string_agg(k.request_hash || convert_from(k.body, 'UTF8') || k.headers::text, ''), '') FROM idempotency_keys k)`).
		Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, value) || !strings.Contains(dump, "hf-token") {
		t.Fatal("the value reached the database, or the dump missed the secret")
	}

	// On disk only encrypted; server-side consumers read it back and the use is recorded.
	entries, err := os.ReadDir(s.Secrets.Dir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("store holds %d files (%v)", len(entries), err)
	}
	sealed, _ := os.ReadFile(s.Secrets.Dir() + "/" + entries[0].Name())
	if strings.Contains(string(sealed), value) {
		t.Fatal("the value is on disk in plain text")
	}
	got, err := s.Secrets.Read(t.Context(), "hf-token")
	if err != nil || string(got) != value {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if n := e.count("SELECT count(*) FROM secrets WHERE last_used_at IS NOT NULL"); n != 1 {
		t.Error("last use not recorded")
	}
	if _, err := s.Secrets.Read(t.Context(), "nope"); err == nil {
		t.Error("reading a missing secret succeeded")
	}
}

func TestComputeAndPolicies(t *testing.T) {
	e, _ := startSeeded(t)
	var hosts struct {
		Items []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Cards []struct {
				MemoryCapGb     float64  `json:"memoryCapGb"`
				AllowedJobKinds []string `json:"allowedJobKinds"`
			} `json:"cards"`
			Health struct{ State string } `json:"health"`
		} `json:"items"`
	}
	e.ok(e.do("GET", "/api/compute", ""), 200, &hosts)
	if len(hosts.Items) != 1 || hosts.Items[0].Name != "staging" || len(hosts.Items[0].Cards) != 1 ||
		hosts.Items[0].Cards[0].MemoryCapGb != 22 || hosts.Items[0].Health.State != "unknown" {
		t.Fatalf("compute.list: %+v", hosts.Items)
	}
	resp := e.ok(e.do("GET", "/api/compute/staging", ""), 200, nil)
	if resp.Header.Get("ETag") != `"1"` {
		t.Errorf("compute.get etag %s", resp.Header.Get("ETag"))
	}
	v := expectProblem(t, e.do("PATCH", "/api/compute/staging", `{"cards":[{"index":0,"memoryCapGb":200},{"index":3}]}`,
		"Idempotency-Key", e.key(), "If-Match", `"1"`), 422, "validation-failed")
	if len(v.Errors) != 2 {
		t.Errorf("compute.edit errors %+v", v.Errors)
	}
	var h struct {
		Rev   int
		Cards []struct {
			MemoryCapGb     float64  `json:"memoryCapGb"`
			AllowedJobKinds []string `json:"allowedJobKinds"`
		}
	}
	e.ok(e.do("PATCH", "/api/compute/"+hosts.Items[0].ID, `{"cards":[{"index":0,"memoryCapGb":20,"allowedJobKinds":["eval"]}]}`,
		"Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &h)
	if h.Rev != 2 || h.Cards[0].MemoryCapGb != 20 || len(h.Cards[0].AllowedJobKinds) != 1 {
		t.Errorf("compute.edit: %+v", h)
	}
	expectProblem(t, e.do("PATCH", "/api/compute/staging", `{"description":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")

	var pol struct {
		Rev     int
		Budgets struct {
			GpuHoursPerProjectPerDay float64 `json:"gpuHoursPerProjectPerDay"`
			AgentTurnsPerSession     int     `json:"agentTurnsPerSession"`
		}
		Departures []string
	}
	e.ok(e.do("GET", "/api/policies", ""), 200, &pol)
	if pol.Rev != 1 || pol.Budgets.GpuHoursPerProjectPerDay != 8 || pol.Budgets.AgentTurnsPerSession != 200 || len(pol.Departures) != 0 {
		t.Fatalf("policies.get: %+v", pol)
	}
	expectProblem(t, e.do("PATCH", "/api/policies", `{"budgets":{"gpuHoursPerProjectPerDay":500}}`, "Idempotency-Key", e.key(), "If-Match", `"1"`),
		422, "validation-failed")
	e.ok(e.do("PATCH", "/api/policies", `{"budgets":{"gpuHoursPerProjectPerDay":12}}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &pol)
	if pol.Rev != 2 || pol.Budgets.GpuHoursPerProjectPerDay != 12 || len(pol.Departures) != 1 || pol.Departures[0] != "budgets.gpuHoursPerProjectPerDay" {
		t.Errorf("policies.edit: %+v", pol)
	}
	e.ok(e.do("PATCH", "/api/policies", `{"budgets":{"gpuHoursPerProjectPerDay":8}}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &pol)
	if len(pol.Departures) != 0 {
		t.Errorf("back at the default, departures = %v", pol.Departures)
	}

	var d struct {
		Version int
		Budgets map[string]struct {
			Value  any
			Source string
		}
	}
	resp = e.ok(e.do("GET", "/api/defaults", ""), 200, &d)
	if d.Version < 1 || resp.Header.Get("ETag") == "" || d.Budgets["gpu_hours_per_project_per_day"].Source == "" {
		t.Errorf("defaults.get: %+v", d)
	}
}

type estimate struct {
	Basis     string  `json:"basis"`
	PlusMinus float64 `json:"plusMinus"`
	GpuHours  struct {
		Value, Low, High float64
	} `json:"gpuHours"`
	DurationSeconds struct {
		Value, Low, High float64
	} `json:"durationSeconds"`
	Steps int `json:"steps"`
	Gpus  int `json:"gpus"`
	Init  string
	Card  struct {
		Host        string  `json:"host"`
		Index       int     `json:"index"`
		CardClass   string  `json:"cardClass"`
		MemoryCapGb float64 `json:"memoryCapGb"`
	} `json:"card"`
	BaseModel version `json:"baseModel"`
	Data      struct {
		Datasets []string `json:"datasets"`
		Hours    float64  `json:"hours"`
		Bytes    int64    `json:"bytes"`
	} `json:"data"`
	Budget struct {
		GpuHoursPerProjectPerDay float64 `json:"gpuHoursPerProjectPerDay"`
		WithinDailyBudget        bool    `json:"withinDailyBudget"`
	} `json:"budget"`
}

func TestRunEstimate(t *testing.T) {
	e, _ := startSeeded(t)
	e.newProject("hebrew")
	dry := func(body string) *http.Response {
		return e.do("POST", "/api/projects/hebrew/runs?dryRun=true", body, "Idempotency-Key", e.key())
	}
	eventsBefore := e.count("SELECT count(*) FROM events")
	var est estimate
	e.ok(dry(`{"init":"base","gpus":1,"datasets":["dataset/fleurs-he-smoke","dataset/fleurs-ru-smoke","dataset/fleurs-he-smoke"]}`), 200, &est)
	// 120 s of lease overhead (estimates.lease_overhead_seconds) + 3 000 steps × 0.7 ± 0.2 s/step (the table row,
	// spike A3) on the staging card; the ± applies to the steps.
	if est.Basis != "table" || est.PlusMinus != 0.2 || est.Steps != 3000 || est.Gpus != 1 || est.Init != "base" ||
		est.GpuHours.Value != 0.617 || est.GpuHours.Low != 0.5 || est.GpuHours.High != 0.733 ||
		est.DurationSeconds.Value != 2220 || est.DurationSeconds.Low != 1800 || est.DurationSeconds.High != 2640 {
		t.Fatalf("estimate numbers: %+v", est)
	}
	if est.Card.Host != "staging" || est.Card.Index != 0 || est.Card.CardClass != "blackwell-48gb" || est.Card.MemoryCapGb != 22 ||
		est.BaseModel.Name != "base-model/nemotron-3.5-asr-streaming-0.6b" {
		t.Errorf("estimate card or base model: %+v", est)
	}
	if len(est.Data.Datasets) != 2 || est.Data.Hours != 3 || est.Data.Bytes != 345600000 ||
		est.Budget.GpuHoursPerProjectPerDay != 8 || !est.Budget.WithinDailyBudget {
		t.Errorf("estimate data or budget: %+v %+v", est.Data, est.Budget)
	}
	if n := e.count("SELECT count(*) FROM events"); n != eventsBefore {
		t.Error("a dry run emitted an event")
	}

	e.ok(dry(`{"steps":50000}`), 200, &est)
	if est.GpuHours.Value != 9.756 || est.Budget.WithinDailyBudget {
		t.Errorf("long run: %+v", est.GpuHours)
	}

	expectProblem(t, e.do("POST", "/api/projects/hebrew/runs", `{}`, "Idempotency-Key", e.key()), 422, "validation-failed") // a real run needs a mix
	expectProblem(t, dry(`{"gpus":2}`), 422, "validation-failed")
	expectProblem(t, dry(`{"init":"checkpoint"}`), 422, "validation-failed")
	expectProblem(t, dry(`{"init":"checkpoint","checkpoint":"ckp_1"}`), 404, "not-found")
	expectProblem(t, dry(`{"init":"scratch"}`), 422, "validation-failed")
	expectProblem(t, dry(`{"precision":"fp32"}`), 422, "estimate-unavailable")
	expectProblem(t, dry(`{"datasets":["@nope"]}`), 404, "not-found")
	expectProblem(t, dry(`{"baseModel":"dataset/fleurs-he-smoke"}`), 404, "not-found")
	expectProblem(t, e.do("POST", "/api/projects/nope/runs?dryRun=true", `{}`, "Idempotency-Key", e.key()), 404, "not-found")

	// An alias resolves; a card whose cap the table does not know has no estimate.
	nemo := e.versionID("base-models", "base-model/nemotron-3.5-asr-streaming-0.6b")
	e.ok(e.do("PUT", "/api/projects/hebrew/aliases/train-base", `{"version":"`+nemo+`"}`, "Idempotency-Key", e.key()), 200, nil)
	e.ok(dry(`{"baseModel":"@train-base"}`), 200, &est)
	if est.BaseModel.ID != nemo {
		t.Errorf("alias resolved to %s", est.BaseModel.ID)
	}
	e.ok(e.do("PATCH", "/api/compute/staging", `{"cards":[{"index":0,"memoryCapGb":20}]}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	pr := expectProblem(t, dry(`{}`), 422, "estimate-unavailable")
	if !strings.Contains(pr.Detail, "20 GB") {
		t.Errorf("detail %q", pr.Detail)
	}
	e.ok(e.do("PATCH", "/api/compute/staging", `{"cards":[{"index":0,"allowedJobKinds":["eval"]}]}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	expectProblem(t, dry(`{}`), 409, "conflict")
}
