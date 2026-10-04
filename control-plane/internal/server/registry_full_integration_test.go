//go:build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// Phase 4 · stream R: adoption checks (licence, locale, purpose), the soft delete of registry versions, data.lock read
// by pipelines and step-kind deprecation.

// registerDataset registers a frozen dataset version directly, as an import would.
func (e *env) registerDataset(name, licence string, tags []string, payload string) string {
	e.t.Helper()
	var id string
	if err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		v, _, _, err := registry.Register(context.Background(), tx, registry.RegisterInput{
			Kind: registry.KindDataset, Name: name, Licence: licence, Tags: tags, Payload: []byte(payload), Freeze: true,
			Actor: auth.Actor{Kind: auth.KindAutomation, ID: "test", Name: "test"},
		}, time.Now())
		id = v.ID
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) projectRev(slug string) string {
	e.t.Helper()
	var p project
	resp := e.ok(e.do("GET", "/api/projects/"+slug, ""), 200, &p)
	return resp.Header.Get("ETag")
}

// adopt sends projects.adopt at the project's current revision and answers the status and, unless 200, the problem.
func (e *env) adopt(slug, body string) (int, *problem) {
	e.t.Helper()
	resp := e.do("POST", "/api/projects/"+slug+":adopt", body, "Idempotency-Key", e.key(), "If-Match", e.projectRev(slug))
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, nil
	}
	var p problem
	_ = json.Unmarshal(b, &p)
	return resp.StatusCode, &p
}

func jsonBody(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAdoptionChecks(t *testing.T) {
	e, _ := startSeeded(t)
	e.newProject("hebrew") // he-IL by the wizard's defaults
	ru := e.versionID("datasets", "dataset/fleurs-ru-smoke")
	payload := `{"source":"x","locales":["he-IL"],"splits":[],"hours":1,"utterances":1,"bytes":1,"fixture":true}`
	nc := e.registerDataset("dataset/nc-he", "CC-BY-NC-4.0", []string{"locale:he-IL"}, payload)
	ncEval := e.registerDataset("dataset/nc-he-eval", "CC-BY-NC-4.0", []string{"locale:he-IL"},
		strings.Replace(payload, `"fixture":true`, `"fixture":true,"evalOnly":true`, 1))
	unknown := e.registerDataset("dataset/unknown-he", "NOASSERTION", []string{"locale:he-IL"}, payload)

	for _, tc := range []struct {
		name, body string
		status     int
		problem    string
	}{
		{"another language as a target", `{"version":"` + ru + `"}`, 422, "locale-mismatch"},
		{"bad purpose", `{"version":"` + ru + `","purpose":"other"}`, 422, "validation-failed"},
		{"non-commercial training data", `{"version":"` + nc + `"}`, 422, "licence-forbids-adoption"},
		{"no licence", `{"version":"` + unknown + `","purpose":"replay"}`, 422, "licence-forbids-adoption"},
	} {
		status, p := e.adopt("hebrew", tc.body)
		if status != tc.status || (tc.problem != "" && (p == nil || !strings.HasSuffix(p.Type, "/"+tc.problem))) {
			t.Errorf("%s: %d %+v, want %d %s", tc.name, status, p, tc.status, tc.problem)
		}
	}
	// Replay data of another language, and non-commercial data that is only evaluated on, are adopted.
	if status, p := e.adopt("hebrew", `{"version":"`+ru+`","purpose":"replay"}`); status != 200 {
		t.Fatalf("replay adoption: %d %+v", status, p)
	}
	if status, p := e.adopt("hebrew", `{"version":"`+ncEval+`"}`); status != 200 {
		t.Fatalf("eval-only adoption: %d %+v", status, p)
	}
	// A worker-published kind is never adopted.
	if err := pipelinestest.RegisterKinds(context.Background(), e.pool); err != nil {
		t.Fatal(err)
	}
	var kinds versionList
	e.ok(e.do("GET", "/api/registry?kind=step_kind", ""), 200, &kinds)
	if status, _ := e.adopt("hebrew", `{"version":"`+kinds.Items[0].ID+`"}`); status != 409 {
		t.Errorf("adopting a step kind answered %d", status)
	}
	if lock := e.recipe("hebrew", "data.lock", ""); !strings.Contains(lock.Content, ru) || !strings.Contains(lock.Content, ncEval) {
		t.Errorf("data.lock lacks the adoptions:\n%s", lock.Content)
	}
}

func TestArchiveRegistryVersions(t *testing.T) {
	e, _ := startSeeded(t)
	e.newProject("hebrew")
	he := e.versionID("datasets", "dataset/fleurs-he-smoke")
	if status, p := e.adopt("hebrew", `{"version":"`+he+`"}`); status != 200 {
		t.Fatalf("adopt: %d %+v", status, p)
	}
	spare := e.registerDataset("dataset/spare-he", "CC-BY-4.0", []string{"locale:he-IL"},
		`{"source":"x","locales":["he-IL"],"splits":[],"hours":1,"utterances":1,"bytes":1,"fixture":true}`)
	// In use: the project adopted it.
	p := expectProblem(t, e.do("POST", "/api/registry/versions:archive", `{"version":"`+he+`"}`, "Idempotency-Key", e.key()), 409, "version-in-use")
	if len(p.Errors) == 0 || !strings.Contains(p.Errors[0].Message, "adopted by project hebrew") {
		t.Errorf("version-in-use errors: %+v", p.Errors)
	}
	// A dry run checks without archiving.
	var v version
	e.ok(e.do("POST", "/api/registry/versions:archive?dryRun=true", `{"version":"`+spare+`"}`, "Idempotency-Key", e.key()), 200, &v)
	if n := e.count("SELECT count(*) FROM registry_versions WHERE id = '" + spare + "' AND state = 'archived'"); n != 0 {
		t.Fatal("the dry run archived")
	}
	e.ok(e.do("POST", "/api/registry/versions:archive", `{"version":"`+spare+`"}`, "Idempotency-Key", e.key()), 200, &v)
	if v.State != "archived" {
		t.Fatalf("archived version: %+v", v)
	}
	// Archiving again answers the version; nothing changes.
	e.ok(e.do("POST", "/api/registry/versions:archive", `{"version":"`+spare+`"}`, "Idempotency-Key", e.key()), 200, &v)
	// Hidden from registry.search unless state:archived is asked; never adoptable.
	var search versionList
	e.ok(e.do("GET", "/api/registry?q=spare-he", ""), 200, &search)
	if len(search.Items) != 0 {
		t.Errorf("registry.search shows the archived version: %+v", search.Items)
	}
	e.ok(e.do("GET", "/api/registry?q="+url.QueryEscape("spare-he state:archived"), ""), 200, &search)
	if len(search.Items) != 1 {
		t.Errorf("state:archived: %+v", search.Items)
	}
	if status, _ := e.adopt("hebrew", `{"version":"`+spare+`"}`); status != 409 {
		t.Errorf("adopting an archived version answered %d", status)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE type = 'dataset_version.archived'`); n != 1 {
		t.Errorf("%d archived events", n)
	}
	// The database refuses every other move out of archived.
	if _, err := e.pool.Exec(context.Background(), "UPDATE registry_versions SET state = 'frozen' WHERE id = $1", spare); err == nil {
		t.Error("an archived version went back to frozen")
	}
	// Agents never archive (no-deletes).
	if resp := e.agent("POST", "/api/registry/versions:archive", `{"version":"`+spare+`"}`, "Idempotency-Key", e.key()); resp.StatusCode != 403 {
		t.Errorf("an agent's versions.archive answered %d", resp.StatusCode)
	}
}

// fxRef is a step kind whose dataset parameter names a registry version (x-cadence.registry: dataset_version).
var fxRef = map[string]any{
	"name": "fx_ref", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
	"params": map[string]any{"type": "object", "properties": map[string]any{
		"dataset": map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{
			"default": "", "description": "the dataset", "source": "test", "range": "any", "registry": "dataset_version"}},
	}},
	"consumes": map[string]string{}, "produces": map[string]string{"text": "text"},
	"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.fx-ref",
}

// fxOld is a step kind its pack deprecated in the past, replaced by fx_ref@1.
var fxOld = map[string]any{
	"name": "fx_old", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
	"params":   map[string]any{"type": "object", "properties": map[string]any{}},
	"consumes": map[string]string{}, "produces": map[string]string{"text": "text"},
	"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.fx-old",
	"deprecation": map[string]any{"after": "2026-01-01", "replacedBy": "fx_ref@1", "note": "test"},
}

func TestPipelinesReadDataLock(t *testing.T) {
	e, _ := startSeeded(t)
	if err := pipelinestest.Register(context.Background(), e.pool, fxRef); err != nil {
		t.Fatal(err)
	}
	e.newProject("hebrew")
	he := e.versionID("datasets", "dataset/fleurs-he-smoke")
	e.commitPipeline("hebrew", "lockcheck", "name: lockcheck\nsteps:\n  - {id: a, kind: fx_ref@1, params: {dataset: fleurs-he-smoke}}\n")
	run := "/api/projects/hebrew/pipelines/lockcheck:run?dryRun=true"

	p := expectProblem(t, e.do("POST", run, `{}`, "Idempotency-Key", e.key(), "If-Match", "*"), 422, "not-adopted")
	if !strings.Contains(p.Detail, "dataset/fleurs-he-smoke") || !strings.Contains(p.Detail, "data.lock at") {
		t.Errorf("not-adopted detail: %s", p.Detail)
	}
	if status, pr := e.adopt("hebrew", `{"version":"`+he+`"}`); status != 200 {
		t.Fatalf("adopt: %d %+v", status, pr)
	}
	var plan struct {
		Steps []struct {
			Locked []struct{ Param, Ref, VersionID, Collection string }
		}
		Warnings []struct{ Code string }
	}
	e.ok(e.do("POST", run, `{}`, "Idempotency-Key", e.key(), "If-Match", "*"), 200, &plan)
	if len(plan.Steps) != 1 || len(plan.Steps[0].Locked) != 1 || plan.Steps[0].Locked[0].VersionID != he ||
		plan.Steps[0].Locked[0].Param != "dataset" || plan.Steps[0].Locked[0].Collection != "dataset/fleurs-he-smoke" || len(plan.Warnings) != 0 {
		t.Errorf("plan: %+v", plan)
	}
	// A ver_ id the lock does not list is refused too.
	ru := e.versionID("datasets", "dataset/fleurs-ru-smoke")
	e.commitPipeline("hebrew", "lockcheck", "name: lockcheck\nsteps:\n  - {id: a, kind: fx_ref@1, params: {dataset: "+ru+"}}\n")
	expectProblem(t, e.do("POST", run, `{}`, "Idempotency-Key", e.key(), "If-Match", "*"), 422, "not-adopted")
}

func TestStepKindDeprecation(t *testing.T) {
	e := start(t)
	if err := pipelinestest.Register(context.Background(), e.pool, fxRef, fxOld); err != nil {
		t.Fatal(err)
	}
	e.newProject("demo")
	doc := func(prefix string) string {
		return "name: old\n# " + prefix + "\nsteps:\n  - {id: a, kind: fx_old@1}\n"
	}
	// A new file may not pin a kind whose deprecation has closed.
	p := expectProblem(t, e.do("POST", "/api/projects/demo/recipes", jsonBody(map[string]any{"path": "pipelines/old.yaml", "content": doc("one")}),
		"Idempotency-Key", e.key()), 422, "step-kind-deprecated")
	if len(p.Errors) != 1 || p.Errors[0].Path != "steps[0].kind" || !strings.Contains(p.Errors[0].Message, "fx_ref@1") {
		t.Errorf("step-kind-deprecated: %+v", p.Errors)
	}
	// A pin already on main keeps working: editing the file keeps it, and a plan warns.
	e.commitPipeline("demo", "old", doc("one"))
	rec := e.recipe("demo", "pipelines/old.yaml", "")
	e.ok(e.do("PATCH", "/api/projects/demo/recipes/"+url.PathEscape("pipelines/old.yaml"), jsonBody(map[string]any{"content": doc("two")}),
		"Idempotency-Key", e.key(), "If-Match", `"`+rec.History[0].Sha+`"`), 200, nil)
	var plan struct {
		Steps []struct {
			Deprecation *struct{ After, ReplacedBy string }
		}
		Warnings []struct{ Code, Step, Kind string }
	}
	e.ok(e.do("POST", "/api/projects/demo/pipelines/old:run?dryRun=true", `{}`, "Idempotency-Key", e.key(), "If-Match", "*"), 200, &plan)
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "step-kind-deprecated" || plan.Warnings[0].Kind != "fx_old@1" ||
		len(plan.Steps) != 1 || plan.Steps[0].Deprecation == nil || plan.Steps[0].Deprecation.ReplacedBy != "fx_ref@1" {
		t.Errorf("plan: %+v", plan)
	}
}
