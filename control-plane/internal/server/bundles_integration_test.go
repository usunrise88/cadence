//go:build integration

package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/bundles"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/exports"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// Project bundles (phase 4 tail) against two control planes on two databases and content stores sharing one exports
// mount: projects.export on the first writes the repository, data.lock and every version the project references; the
// second adopts the bundle into a project (bundles.adopt) and makes a new project from it (projects.new with bundle),
// both behind the admin's approval, registering what it lacks under the first instance's version strings.

// startBundles starts a server whose job runner has the bundle job kinds (main registers them through RegisterJobs;
// tests build the server after the runner starts) and returns its content store.
func startBundles(t *testing.T) (*env, *cas.Store) {
	t.Helper()
	bsvc, w := &bundles.Service{}, &exports.ProjectWriter{}
	var store *cas.Store
	e := startWith(t, func(c *Config) {
		store, bsvc.CAS, w.CAS = c.CAS, c.CAS, c.CAS
		bsvc.Facts, w.Repos = c.Projects, c.Projects.Repos()
		if c.Secrets != nil {
			bsvc.Secrets = c.Secrets
		}
	}, func(pool *pgxpool.Pool, js *jobs.Service) {
		bsvc.Pool, w.Pool = pool, pool
		bsvc.Register(js)
		w.Register(js)
	})
	return e, store
}

type bundleVersionView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Collection string `json:"collection"`
	Version    string `json:"version"`
	Adopted    bool   `json:"adopted"`
	Published  bool   `json:"published"`
	Blobs      int    `json:"blobs"`
	Path       string `json:"path"`
	Action     string `json:"action"`
	LocalID    string `json:"localId"`
}

type projectExportPlanView struct {
	Commit   string              `json:"commit"`
	Target   string              `json:"target"`
	Versions []bundleVersionView `json:"versions"`
	Aliases  []struct{ Name, VersionID string }
	Blobs    int      `json:"blobs"`
	Sources  []string `json:"sources"`
}

type bundlePlanView struct {
	Bundle   string              `json:"bundle"`
	Commit   string              `json:"commit"`
	Register int                 `json:"register"`
	Reuse    int                 `json:"reuse"`
	Blobs    int                 `json:"blobs"`
	Versions []bundleVersionView `json:"versions"`
	Aliases  []struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	} `json:"aliases"`
	Project struct {
		Name      string   `json:"name"`
		Locales   []string `json:"locales"`
		BaseModel string   `json:"baseModel"`
	} `json:"project"`
}

func (p bundlePlanView) version(collection string) bundleVersionView {
	for _, v := range p.Versions {
		if v.Collection == collection {
			return v
		}
	}
	return bundleVersionView{}
}

func (e *env) ifMatch(slug string) string { return `"` + strconv.Itoa(e.projectRev(slug)) + `"` }

// approveReplay approves approval id as the admin and returns the replay's status and body.
func (e *env) approveReplay(id, rule string) (int, json.RawMessage) {
	e.t.Helper()
	ap := e.approval(id)
	if ap.Rule != rule || ap.Scope != "registry" {
		e.t.Fatalf("approval %+v, want rule %s in registry scope", ap, rule)
	}
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.State != "approved" || decided.Result == nil {
		e.t.Fatalf("decided %+v", decided)
	}
	return decided.Result.Status, decided.Result.Body
}

func TestProjectBundleExportAndImport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	src, store := startBundles(t)
	src.exportMounts(root)

	// The first instance: a project with a dataset version (replay data) and a golden set, and an alias.
	if err := src.runHook(sized(t, store, artifact(t, store, fleursHeader("fleurs-he"), heUtts)), "plr_import", `{}`); err != nil {
		t.Fatal(err)
	}
	golden := fleursHeader("calls-golden-he")
	golden.EvalOnly, golden.Tags, golden.SplitRule = true, []string{"golden"}, "all-test"
	if err := src.runHook(sized(t, store, artifact(t, store, golden, []fixtureUtt{{"g1", "אחת.", "test", "he-IL", ""},
		{"g2", "שתיים.", "test", "he-IL", ""}})), "plr_golden", `{}`); err != nil {
		t.Fatal(err)
	}
	_, gs := src.freezeGolden(`{"datasetVersionId":"dataset/calls-golden-he","normalizerVersionId":"normalizer/he-il"}`)
	src.newProject("origin")
	ds := src.datasetIn("dataset/fleurs-he")
	for _, id := range []string{ds.ID, gs.ID} {
		src.ok(src.do("POST", "/api/projects/origin:adopt", `{"version":"`+id+`","purpose":"replay"}`, "Idempotency-Key", src.key(),
			"If-Match", src.ifMatch("origin")), 200, nil)
	}
	src.ok(src.do("PUT", "/api/projects/origin/aliases/replay-train", `{"version":"`+ds.ID+`"}`, "Idempotency-Key", src.key()), 200, nil)

	// The export's plan: every adopted version and what they name, the dataset bundles and the default target.
	var plan projectExportPlanView
	src.ok(src.do("POST", "/api/projects/origin:export?dryRun=true", `{}`, "Idempotency-Key", src.key(), "If-Match", src.ifMatch("origin")), 200, &plan)
	if len(plan.Commit) != 40 || plan.Target != "mount://exports/projects/origin/"+plan.Commit[:12] || plan.Blobs == 0 ||
		len(plan.Sources) != 1 || plan.Sources[0] != "fleurs" {
		t.Fatalf("plan %+v", plan)
	}
	kinds := map[string]bundleVersionView{}
	for _, v := range plan.Versions {
		kinds[v.Collection] = v
	}
	if v := kinds["dataset/fleurs-he"]; !v.Adopted || v.Path != "datasets/dataset/fleurs-he/"+registryVersionOf(ds.ID, src) || v.Blobs != 6 {
		t.Errorf("dataset in the plan %+v", v)
	}
	if v := kinds["dataset/calls-golden-he"]; v.Adopted || v.Path == "" {
		t.Errorf("the golden set's dataset is carried as a reference: %+v", v)
	}
	if v := kinds["normalizer/he-il"]; v.Adopted || v.Kind != "normalizer" {
		t.Errorf("the golden set's normalizer %+v", v)
	}
	if v := kinds["golden-set/calls-golden-he"]; !v.Adopted {
		t.Errorf("golden set %+v", v)
	}
	expectProblem(t, src.do("POST", "/api/projects/origin:export?dryRun=true", `{"target":"cas"}`, "Idempotency-Key", src.key(),
		"If-Match", src.ifMatch("origin")), 422, "validation-failed")
	expectProblem(t, src.do("POST", "/api/projects/origin:export?dryRun=true", `{"target":"mount://corpora/out"}`, "Idempotency-Key", src.key(),
		"If-Match", src.ifMatch("origin")), 422, "validation-failed")
	expectProblem(t, src.do("POST", "/api/projects/origin:export", `{}`, "Idempotency-Key", src.key(), "If-Match", `"1"`), 412, "precondition-failed")

	// The real call: an export row (format cadence-project-bundle) and the job that writes it.
	var x struct {
		exportView
		JobID    string `json:"jobId"`
		Commit   string `json:"commit"`
		Versions int    `json:"versions"`
		Error    string `json:"error"`
	}
	src.ok(src.do("POST", "/api/projects/origin:export", `{}`, "Idempotency-Key", src.key(), "If-Match", src.ifMatch("origin")), 201, &x)
	if x.Format != "cadence-project-bundle" || x.State != "running" || x.JobID == "" || x.Commit != plan.Commit || x.Versions != len(plan.Versions) {
		t.Fatalf("started %+v", x)
	}
	src.waitJob(x.JobID, "done")
	src.ok(src.do("GET", "/api/exports/"+x.ID, ""), 200, &x)
	if x.State != "done" || x.Files == 0 || x.Copies == 0 || x.Error != "" {
		t.Fatalf("done %+v", x)
	}
	dir := filepath.Join(root, "projects", "origin", plan.Commit[:12])
	for _, f := range []string{"bundle.json", "repository.bundle", "data.lock", "datasets/dataset/fleurs-he/" + registryVersionOf(ds.ID, src) + "/bundle.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("bundle file %s: %v", f, err)
		}
	}
	var doc exports.BundleDoc
	b, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil || json.Unmarshal(b, &doc) != nil {
		t.Fatalf("bundle.json: %v", err)
	}
	if doc.Format != exports.ProjectBundleFormat || doc.Ref != "main" || doc.Commit != plan.Commit || doc.Project.Slug != "origin" ||
		len(doc.Aliases) != 1 || doc.Aliases[0].Name != "replay-train" {
		t.Errorf("bundle.json %+v", doc)
	}
	if n := src.count("SELECT count(*) FROM blob_copies WHERE mount_id = 'mnt_exports'"); n != x.Copies {
		t.Errorf("%d blob copies recorded, export says %d", n, x.Copies)
	}
	// A second export to the same directory is refused: it holds a bundle.
	expectProblem(t, src.do("POST", "/api/projects/origin:export?dryRun=true", `{}`, "Idempotency-Key", src.key(),
		"If-Match", src.ifMatch("origin")), 409, "conflict")

	// The second instance: another database and content store, the same exports mount.
	dst, dstStore := startBundles(t)
	dst.exportMounts(root)
	dst.newProject("copy")
	uri := `"mount://exports/projects/origin/` + plan.Commit[:12] + `"`
	expectProblem(t, dst.do("POST", "/api/projects/copy/bundles:adopt?dryRun=true", `{"bundle":"mount://exports/projects"}`,
		"Idempotency-Key", dst.key(), "If-Match", dst.ifMatch("copy")), 422, "bundle-invalid")
	var bp bundlePlanView
	dst.ok(dst.do("POST", "/api/projects/copy/bundles:adopt?dryRun=true", `{"bundle":`+uri+`}`, "Idempotency-Key", dst.key(),
		"If-Match", dst.ifMatch("copy")), 200, &bp)
	if bp.Commit != plan.Commit || bp.Register != 3 || bp.Blobs == 0 || bp.Project.Name != "origin" {
		t.Fatalf("import plan %+v", bp)
	}
	for c, want := range map[string]string{"dataset/fleurs-he": "register", "dataset/calls-golden-he": "register",
		"golden-set/calls-golden-he": "register", "normalizer/he-il": "reuse"} {
		if got := bp.version(c).Action; got != want {
			t.Errorf("%s: %s, want %s", c, got, want)
		}
	}
	if len(bp.Aliases) != 1 || bp.Aliases[0].Action != "set" {
		t.Errorf("aliases %+v", bp.Aliases)
	}
	if n := dst.count("SELECT count(*) FROM approvals"); n != 0 {
		t.Fatalf("a dry run asked for %d approvals", n)
	}

	// The real call is the admin's approval, for people too; the replay queues the import job.
	var acc accepted
	dst.ok(dst.do("POST", "/api/projects/copy/bundles:adopt", `{"bundle":`+uri+`}`, "Idempotency-Key", dst.key(),
		"If-Match", dst.ifMatch("copy")), 202, &acc)
	status, body := dst.approveReplay(acc.ApprovalID, "bundle-import")
	var imp struct {
		JobID string         `json:"jobId"`
		Plan  bundlePlanView `json:"plan"`
	}
	if err := json.Unmarshal(body, &imp); err != nil || status != 201 || imp.JobID == "" {
		t.Fatalf("replay %d %s", status, body)
	}
	j := dst.waitJob(imp.JobID, "done")
	var res bundles.Result
	if err := json.Unmarshal(j.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Registered) != 3 || res.Adopted < 2 || len(res.Aliases) != 1 || len(res.Changed) != 0 {
		t.Fatalf("import result %s", j.Result)
	}
	// Same content, same version strings; the golden set is a golden set here (its row, its dataset eval-only).
	got := dst.datasetIn("dataset/fleurs-he")
	if registryVersionOf(got.ID, dst) != registryVersionOf(ds.ID, src) || got.Fingerprint != ds.Fingerprint || got.ID == ds.ID || got.Dataset.Utterances != 3 {
		t.Errorf("dataset here %+v, there %+v", got, ds)
	}
	var gl struct{ Items []goldenSetView }
	dst.ok(dst.do("GET", "/api/registry/golden-sets", ""), 200, &gl)
	if len(gl.Items) != 1 || gl.Items[0].Name != "golden-set/calls-golden-he" || registryVersionOf(gl.Items[0].ID, dst) != registryVersionOf(gs.ID, src) {
		t.Fatalf("golden sets here %+v", gl.Items)
	}
	if n := dst.count("SELECT count(*) FROM golden_sets"); n != 1 {
		t.Errorf("%d golden_sets rows", n)
	}
	var adoptions struct {
		Items []struct{ Version struct{ Name string } }
	}
	dst.ok(dst.do("GET", "/api/projects/copy/adoptions", ""), 200, &adoptions)
	names := map[string]bool{}
	for _, a := range adoptions.Items {
		names[a.Version.Name] = true
	}
	if !names["dataset/fleurs-he"] || !names["golden-set/calls-golden-he"] {
		t.Errorf("adoptions %v", names)
	}
	if lock, _, err := dst.repos.Repos().ReadFile(ctx, "copy", repos.Main, "data.lock"); err != nil || !strings.Contains(string(lock), "golden-set/calls-golden-he") {
		t.Errorf("data.lock on copy: %v\n%s", err, lock)
	}
	for _, h := range []string{ds.Dataset.Artifact.Hash} {
		if ok, _, _ := dstStore.Has(h); !ok {
			t.Errorf("blob %s not copied", h)
		}
	}

	// A new project from the same bundle: its repository is the bundle's history, everything is reused now.
	var acc2 accepted
	dst.ok(dst.do("POST", "/api/projects", `{"name":"Moved","slug":"moved","bundle":`+uri+`}`, "Idempotency-Key", dst.key()), 202, &acc2)
	status, body = dst.approveReplay(acc2.ApprovalID, "bundle-import")
	var job struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(body, &job); err != nil || status != 202 || job.JobID == "" {
		t.Fatalf("projects.new replay %d %s", status, body)
	}
	dst.waitJob(job.JobID, "done")
	var moved project
	dst.ok(dst.do("GET", "/api/projects/moved", ""), 200, &moved)
	if moved.State != "active" {
		t.Fatalf("moved %+v", moved)
	}
	hist, err := dst.repos.Repos().History(ctx, "moved", repos.Main, "", 50)
	if err != nil || len(hist) < 2 || !strings.HasPrefix(hist[0].Subject, "import bundle") {
		t.Fatalf("history %+v %v", hist, err)
	}
	found := false
	for _, c := range hist {
		found = found || c.SHA == plan.Commit
	}
	if !found {
		t.Errorf("the bundled commit %s is not in moved's history", plan.Commit)
	}
	dst.ok(dst.do("GET", "/api/projects/moved/adoptions", ""), 200, &adoptions)
	names = map[string]bool{}
	for _, a := range adoptions.Items {
		names[a.Version.Name] = true
	}
	if !names["dataset/fleurs-he"] || !names["golden-set/calls-golden-he"] {
		t.Errorf("moved adoptions %v", names)
	}
	var al struct{ Items []struct{ Name string } }
	dst.ok(dst.do("GET", "/api/projects/moved/aliases", ""), 200, &al)
	if len(al.Items) != 1 || al.Items[0].Name != "replay-train" {
		t.Errorf("moved aliases %+v", al.Items)
	}

	// Agents ask too: the import waits for the admin.
	dst.ok(dst.agent("POST", "/api/projects/copy/bundles:adopt", `{"bundle":`+uri+`}`, "Idempotency-Key", dst.key(),
		"If-Match", dst.ifMatch("copy")), 202, &acc)
}

// registryVersionOf reads a version's string.
func registryVersionOf(id string, e *env) string {
	e.t.Helper()
	var v string
	if err := e.pool.QueryRow(context.Background(), "SELECT version FROM registry_versions WHERE id = $1", id).Scan(&v); err != nil {
		e.t.Fatalf("version %s: %v", id, err)
	}
	return v
}
