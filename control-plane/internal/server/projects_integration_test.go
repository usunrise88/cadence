//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

type recipeList struct {
	Commit string `json:"commit"`
	Items  []struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	} `json:"items"`
}

type recipe struct {
	Path     string `json:"path"`
	Commit   string `json:"commit"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	History  []struct {
		Sha     string `json:"sha"`
		Message string `json:"message"`
		Author  string `json:"author"`
	} `json:"history"`
}

type agentProfile struct {
	Driver               string            `json:"driver"`
	Model                string            `json:"model"`
	PermissionPreset     string            `json:"permissionPreset"`
	InstructionsTemplate string            `json:"instructionsTemplate"`
	AutoMerge            string            `json:"autoMerge"`
	DraftPolicy          map[string]string `json:"draftPolicy"`
	Rev                  int               `json:"rev"`
	Commit               string            `json:"commit"`
	Files                []struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"files"`
}

func (a agentProfile) file(path string) string {
	for _, f := range a.Files {
		if f.Path == path {
			return f.Content
		}
	}
	return ""
}

func (e *env) recipe(slug, path, ref string) recipe {
	e.t.Helper()
	var r recipe
	q := ""
	if ref != "" {
		q = "?ref=" + url.QueryEscape(ref)
	}
	e.ok(e.do("GET", "/api/projects/"+slug+"/recipes/"+url.PathEscape(path)+q, ""), 200, &r)
	return r
}

func TestBootstrapEndToEnd(t *testing.T) {
	e := start(t)
	// The Recommended wizard: a name and a language; everything else defaults from defaults.yaml.
	var acc struct {
		JobID string `json:"jobId"`
	}
	e.ok(e.do("POST", "/api/projects", `{"name":"Hebrew telephony","locales":["he-IL"]}`, "Idempotency-Key", e.key()), 202, &acc)
	var p project
	e.ok(e.do("GET", "/api/projects/hebrew-telephony", ""), 200, &p)
	if p.State != "bootstrapping" && p.State != "active" {
		t.Fatalf("state right after projects.new = %q", p.State)
	}
	j := e.waitJob(acc.JobID, "done")
	var result struct {
		Commit     string   `json:"commit"`
		Files      []string `json:"files"`
		Workspaces []string `json:"workspaces"`
	}
	if err := json.Unmarshal(j.Result, &result); err != nil || result.Commit == "" || len(result.Workspaces) != 5 {
		t.Fatalf("job result %s (%v)", j.Result, err)
	}
	e.ok(e.do("GET", "/api/projects/hebrew-telephony", ""), 200, &p)
	if p.State != "active" || p.Rev != 2 || p.Repository.Kind != "internal" || p.Repository.CloneURL != "/git/hebrew-telephony.git" ||
		p.BaseModel.Name != "base-model/nemotron-3.5-asr-streaming-0.6b" || p.BaseModel.Revision == "" {
		t.Fatalf("bootstrapped project %+v", p)
	}
	if !e.repos.Repos().Exists("hebrew-telephony") {
		t.Fatal("no bare repository on disk")
	}

	// The repository holds every file of the layout, committed as "bootstrap".
	var files recipeList
	e.ok(e.do("GET", "/api/projects/hebrew-telephony/recipes", ""), 200, &files)
	have := map[string]bool{}
	for _, f := range files.Items {
		have[f.Path] = true
	}
	for _, want := range []string{"project.yaml", "AGENTS.md", "CLAUDE.md", "NOTES.md", "data.lock", ".claude/settings.json",
		"opencode.json", ".claude/skills/cadence-data/SKILL.md", ".claude/skills/cadence-train/SKILL.md", "pipelines/train-stage.yaml"} {
		if !have[want] {
			t.Errorf("repository lacks %s (%d files)", want, len(files.Items))
		}
	}
	if files.Commit != result.Commit {
		t.Errorf("main at %s, job says %s", files.Commit, result.Commit)
	}
	agents := e.recipe("hebrew-telephony", "AGENTS.md", "")
	if !strings.Contains(agents.Content, "# Hebrew telephony") || !strings.Contains(agents.Content, "he-IL") ||
		len(agents.History) != 1 || agents.History[0].Message != "bootstrap" || agents.History[0].Author != "admin" {
		t.Errorf("AGENTS.md %+v", agents)
	}
	if c := e.recipe("hebrew-telephony", "CLAUDE.md", "main").Content; c != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", c)
	}
	settings := e.recipe("hebrew-telephony", ".claude/settings.json", "")
	if strings.Contains(settings.Content, "mcpServers") || !strings.Contains(settings.Content, "mcp__cadence__projects_note") {
		t.Errorf(".claude/settings.json:\n%s", settings.Content)
	}
	if oc := e.recipe("hebrew-telephony", "opencode.json", ""); !strings.Contains(oc.Content, `"model": "minimax/MiniMax-M2"`) ||
		strings.Contains(oc.Content, `"mcp"`) {
		t.Errorf("opencode.json:\n%s", oc.Content)
	}
	lock := e.recipe("hebrew-telephony", "data.lock", "").Content
	if !strings.Contains(lock, p.BaseModel.VersionID) || !strings.Contains(lock, "template/skill-cadence-data") {
		t.Errorf("data.lock:\n%s", lock)
	}
	expectProblem(t, e.do("GET", "/api/projects/hebrew-telephony/recipes/"+url.PathEscape("nope/x.yaml"), ""), 404, "not-found")

	// The base model and the templates are adopted; the agent profile is the wizard's defaults.
	var adoptions struct {
		Items []struct {
			Version struct{ Kind, Name string }
		}
	}
	e.ok(e.do("GET", "/api/projects/hebrew-telephony/adoptions", ""), 200, &adoptions)
	kinds := map[string]int{}
	for _, a := range adoptions.Items {
		kinds[a.Version.Kind]++
	}
	if kinds["base_model"] != 1 || kinds["template"] < 5 {
		t.Errorf("adoptions %v", kinds)
	}
	var ap agentProfile
	resp := e.ok(e.do("GET", "/api/projects/hebrew-telephony/agent-profile", ""), 200, &ap)
	if ap.Driver != "claude-code" || ap.Model != "sonnet" || ap.PermissionPreset != "guardrails-default" || ap.AutoMerge != "when-clean" ||
		ap.DraftPolicy["mix"] != "draft" || ap.Commit != result.Commit || len(ap.Files) != 4 || resp.Header.Get("ETag") != `"1"` {
		t.Errorf("agent profile %+v", ap)
	}
	// The default workspaces exist as placeholders for the web shell to fill.
	var ws struct{ Items []struct{ Name string } }
	e.ok(e.do("GET", "/api/me/projects/hebrew-telephony/workspaces", ""), 200, &ws)
	if len(ws.Items) != 5 {
		t.Errorf("workspaces %+v", ws.Items)
	}
	// Progress went out on job.{id}; the bootstrap and the files on the project's topics.
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'job." + acc.JobID + "' AND type = 'job.progress'"); n < 3 {
		t.Errorf("%d progress events", n)
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'project.bootstrapped' AND project_id = '" + p.ID + "'"); n != 1 {
		t.Errorf("%d bootstrapped events", n)
	}
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'recipe.AGENTS.md' AND project_id = '" + p.ID + "'"); n != 1 {
		t.Errorf("%d recipe events for AGENTS.md", n)
	}

	// Three fields suffice, but everything is validated.
	v := expectProblem(t, e.do("POST", "/api/projects", `{"name":"X","agent":{"driver":"opencode","model":"no-slash"},"repository":{"kind":"github"}}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
	paths := map[string]bool{}
	for _, fe := range v.Errors {
		paths[fe.Path] = true
	}
	if !paths["model"] || !paths["repository.secret"] {
		t.Errorf("validation errors %+v", v.Errors)
	}
	expectProblem(t, e.do("POST", "/api/projects", `{"name":"Y","baseModel":"base-model/nope"}`, "Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/projects", `{"name":"Z","agent":{"permissionPreset":"nope"}}`, "Idempotency-Key", e.key()), 422, "validation-failed")
}

func TestAgentProfileNotesSyncAndBranches(t *testing.T) {
	e := start(t)
	p := e.newProject("demo")

	// agentProfile.edit commits the rendered files; switching driver starts from its default model.
	var ap agentProfile
	resp := e.ok(e.do("PATCH", "/api/projects/demo/agent-profile", `{"driver":"opencode"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &ap)
	if ap.Driver != "opencode" || ap.Model != "minimax/MiniMax-M2" || ap.Rev != 2 || resp.Header.Get("ETag") != `"2"` || ap.Commit == "" {
		t.Fatalf("edit profile %+v", ap)
	}
	if !strings.Contains(e.recipe("demo", "project.yaml", "").Content, "driver: opencode") {
		t.Error("project.yaml not re-rendered")
	}
	// A dry run shows the rendering and commits nothing.
	e.ok(e.do("PATCH", "/api/projects/demo/agent-profile?dryRun=true", `{"permissionPreset":"read-only"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &ap)
	if !strings.Contains(ap.file(".claude/settings.json"), "dontAsk") || strings.Contains(e.recipe("demo", ".claude/settings.json", "").Content, "dontAsk") {
		t.Error("dry run of a preset change")
	}
	// Editing AGENTS.md makes the instructions custom; they are no longer re-rendered.
	e.ok(e.do("PATCH", "/api/projects/demo/agent-profile", `{"agentsMd":"# Mine\n"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &ap)
	if ap.InstructionsTemplate != "custom" || e.recipe("demo", "AGENTS.md", "").Content != "# Mine\n" {
		t.Fatalf("custom AGENTS.md: %+v", ap)
	}
	e.ok(e.do("PATCH", "/api/projects/demo", `{"domain":"broadcast"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	if e.recipe("demo", "AGENTS.md", "").Content != "# Mine\n" || !strings.Contains(e.recipe("demo", "project.yaml", "").Content, "domain: broadcast") {
		t.Error("projects.edit re-rendered custom instructions or missed project.yaml")
	}
	expectProblem(t, e.do("PATCH", "/api/projects/demo/agent-profile", `{"instructionsTemplate":"nope"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 422, "validation-failed")
	expectProblem(t, e.do("PATCH", "/api/projects/demo/agent-profile", `{"model":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	// An agent changing its own profile waits for a person.
	var gated struct {
		ApprovalID string `json:"approvalId"`
	}
	e.ok(e.agent("PATCH", "/api/projects/demo/agent-profile", `{"autoMerge":"never"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 202, &gated)
	if !strings.HasPrefix(gated.ApprovalID, "apr_") {
		t.Fatalf("agent profile edit by an agent: %+v", gated)
	}

	// Notes: dated, appended, committed.
	var n struct {
		Date, Text, Path, Commit string
	}
	e.ok(e.do("POST", "/api/projects/demo:note", `{"text":"Batch 32 OOMs at the 24 GB cap."}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 200, &n)
	e.ok(e.agent("POST", "/api/projects/demo:note", `{"text":"Bucketing halves padding."}`, "Idempotency-Key", e.key(), "If-Match", `"4"`), 200, &n)
	notes := e.recipe("demo", "NOTES.md", "")
	today := time.Now().UTC().Format(time.DateOnly)
	if n.Date != today || strings.Count(notes.Content, "## "+today) != 1 || !strings.Contains(notes.Content, "- Batch 32 OOMs") ||
		!strings.Contains(notes.Content, "- Bucketing halves padding.") || notes.History[0].Sha != n.Commit ||
		!strings.Contains(notes.History[0].Author, "session ses_test") {
		t.Fatalf("NOTES.md %+v / %+v", notes, n)
	}

	// A sync with nothing to update reports it up to date.
	var sync struct {
		UpToDate bool   `json:"upToDate"`
		Branch   string `json:"branch"`
		Changes  []struct{ Path, Status string }
	}
	e.ok(e.do("POST", "/api/projects/demo:sync", "", "Idempotency-Key", e.key(), "If-Match", `"5"`), 200, &sync)
	if !sync.UpToDate || sync.Branch != "" || len(sync.Changes) != 0 {
		t.Fatalf("fresh sync %+v", sync)
	}
	// Someone edits a skill on main; a sync proposes restoring Cadence's version on a draft branch.
	if _, err := e.repos.Repos().Commit(t.Context(), "demo", repos.Change{Message: "local skill edit",
		Files: map[string][]byte{".claude/skills/cadence-data/SKILL.md": []byte("mine\n")}}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/projects/demo:sync?dryRun=true", "", "Idempotency-Key", e.key(), "If-Match", `"6"`), 200, &sync)
	if sync.UpToDate || sync.Branch != "" || len(sync.Changes) != 1 {
		t.Fatalf("dry sync %+v", sync)
	}
	e.ok(e.do("POST", "/api/projects/demo:sync", "", "Idempotency-Key", e.key(), "If-Match", `"6"`), 200, &sync)
	if sync.UpToDate || sync.Branch != "sync/"+today || len(sync.Changes) != 1 || sync.Changes[0].Status != "modified" {
		t.Fatalf("sync %+v", sync)
	}
	var branches struct {
		Main  string
		Items []struct {
			Name, Kind, Head string
			Ahead, Behind    int
		}
	}
	e.ok(e.do("GET", "/api/projects/demo/branches", ""), 200, &branches)
	if len(branches.Items) != 1 || branches.Items[0].Name != sync.Branch || branches.Items[0].Kind != "sync" || branches.Items[0].Ahead != 1 {
		t.Fatalf("branches %+v", branches)
	}
	var diff struct {
		Head        string
		FastForward bool
		Conflicts   []string
		Files       []struct{ Path string }
		Patch       string
	}
	branchPath := "/api/projects/demo/branches/" + url.PathEscape(sync.Branch)
	resp = e.ok(e.do("GET", branchPath, ""), 200, &diff)
	if !diff.FastForward || len(diff.Conflicts) != 0 || len(diff.Files) != 1 || !strings.Contains(diff.Patch, "-mine") ||
		resp.Header.Get("ETag") != `"`+diff.Head+`"` {
		t.Fatalf("branch diff %+v", diff)
	}
	expectProblem(t, e.do("POST", branchPath+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"`+branches.Main+`"`), 412, "precondition-failed")
	var merge struct {
		Main        string
		FastForward bool
		Changes     []struct{ Path string }
	}
	e.ok(e.do("POST", branchPath+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"`+diff.Head+`"`), 200, &merge)
	if !merge.FastForward || merge.Main != diff.Head || len(merge.Changes) != 1 ||
		strings.Contains(e.recipe("demo", ".claude/skills/cadence-data/SKILL.md", "").Content, "mine") {
		t.Fatalf("accept %+v", merge)
	}
	e.ok(e.do("GET", "/api/projects/demo/branches", ""), 200, &branches)
	if len(branches.Items) != 0 {
		t.Fatalf("an accepted branch is still open: %+v", branches.Items)
	}

	// A sync branch and main both change the same file: the merge reports the conflict and leaves main alone.
	store := e.repos.Repos()
	if _, err := store.Commit(t.Context(), "demo", repos.Change{Message: "edit 1",
		Files: map[string][]byte{"pipelines/train-stage.yaml": []byte("mine: 1\n")}}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/projects/demo:sync", "", "Idempotency-Key", e.key(), "If-Match", `"7"`), 200, &sync)
	if sync.Branch != "sync/"+today+"-2" {
		t.Fatalf("second sync branch %q", sync.Branch)
	}
	if _, err := store.Commit(t.Context(), "demo", repos.Change{Message: "edit 2",
		Files: map[string][]byte{"pipelines/train-stage.yaml": []byte("mine: 2\n")}}); err != nil {
		t.Fatal(err)
	}
	branchPath = "/api/projects/demo/branches/" + url.PathEscape(sync.Branch)
	e.ok(e.do("GET", branchPath, ""), 200, &diff)
	if diff.FastForward || !slices.Equal(diff.Conflicts, []string{"pipelines/train-stage.yaml"}) {
		t.Fatalf("conflicting diff %+v", diff)
	}
	// branches.compare: the conflicting file with its three versions and hunks.
	var cmp struct {
		Head, Base  string
		FastForward bool
		Files       []struct {
			Path, Conflict     string
			Clean              bool
			Base, Main, Branch struct {
				Exists bool
				Text   *string
			}
			Hunks []struct{ Kind string }
		}
	}
	resp = e.ok(e.do("GET", branchPath+":compare", ""), 200, &cmp)
	if cmp.FastForward || cmp.Head != diff.Head || resp.Header.Get("ETag") != `"`+diff.Head+`"` || len(cmp.Files) == 0 {
		t.Fatalf("compare %+v", cmp)
	}
	cf := cmp.Files[0]
	if cf.Path != "pipelines/train-stage.yaml" || cf.Clean || cf.Conflict != "content" || cf.Main.Text == nil ||
		*cf.Main.Text != "mine: 2\n" || cf.Branch.Text == nil || cf.Base.Text == nil || *cf.Base.Text != "mine: 1\n" {
		t.Fatalf("compared file %+v", cf)
	}
	conflictHunks := 0
	for _, h := range cf.Hunks {
		if h.Kind == "conflict" {
			conflictHunks++
		}
	}
	if conflictHunks == 0 {
		t.Fatalf("no conflict hunk in %+v", cf.Hunks)
	}
	expectProblem(t, e.do("GET", "/api/projects/demo/branches/nope:compare", ""), 404, "not-found")
	before, _ := store.Head(t.Context(), "demo")
	expectProblem(t, e.do("POST", branchPath+":accept", "", "Idempotency-Key", e.key(), "If-Match", `"`+diff.Head+`"`), 409, "merge-conflict")
	if after, _ := store.Head(t.Context(), "demo"); after != before {
		t.Fatal("a conflicting accept moved main")
	}
	e.ok(e.do("POST", branchPath+":revert", "", "Idempotency-Key", e.key(), "If-Match", `"`+diff.Head+`"`), 200, nil)
	expectProblem(t, e.do("GET", branchPath, ""), 404, "not-found")
	// Session branches are the agent-session stream's to accept.
	if _, err := store.CreateBranch(t.Context(), "demo", repos.SessionBranch("ses_1"), ""); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("POST", "/api/projects/demo/branches/session%2Fses_1:accept", "", "Idempotency-Key", e.key(),
		"If-Match", `"`+before+`"`), 409, "conflict")

	// Archive removes the working state and makes the repository read-only.
	var cur project
	e.ok(e.do("GET", "/api/projects/demo", ""), 200, &cur)
	e.ok(e.do("POST", "/api/projects/demo:archive", "", "Idempotency-Key", e.key(), "If-Match", `"`+strconv.Itoa(cur.Rev)+`"`), 200, &cur)
	if _, err := os.Stat(store.WorkPath("demo")); !os.IsNotExist(err) {
		t.Errorf("working clone left after archive: %v", err)
	}
	if !store.Exists("demo") {
		t.Error("archive removed the repository")
	}
	e.ok(e.do("GET", "/api/projects/demo/recipes/NOTES.md", ""), 200, nil)
	expectProblem(t, e.do("POST", "/api/projects/demo:note", `{"text":"late"}`, "Idempotency-Key", e.key(), "If-Match", `"`+strconv.Itoa(cur.Rev)+`"`), 409, "conflict")
	_ = p
}

// git runs git in dir with an isolated configuration.
func gitCmd(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=t@example.org")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSmartHTTPWithTokens(t *testing.T) {
	e := start(t)
	p := e.newProject("repo")
	other := e.newProject("other")

	// A server that authenticates (no fixed actor), over the same database, jobs and repositories.
	authed := newTestServer(t, e.pool, events.NewHub(8), obs.NewMetrics(), func(c *Config) {
		c.Actor, c.Jobs, c.Projects = auth.Actor{}, e.jobs, e.repos
	})
	srv := httptest.NewServer(authed.Handler())
	defer srv.Close()

	var key, otherKey, agentTok string
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		var err error
		if key, _, _, err = credentials.NewAPIKey(t.Context(), tx, credentials.NewAPIKeyInput{UserID: "usr_admin", Name: "ci", ProjectID: p.ID}); err != nil {
			return err
		}
		if otherKey, _, _, err = credentials.NewAPIKey(t.Context(), tx, credentials.NewAPIKeyInput{UserID: "usr_admin", Name: "other", ProjectID: other.ID}); err != nil {
			return err
		}
		agentTok, _, err = credentials.MintAgentToken(t.Context(), tx, "ses_42", p.ID, "guardrails-default")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	remote := func(token string) string {
		return strings.Replace(srv.URL, "http://", "http://x-token:"+token+"@", 1) + "/git/repo.git"
	}
	dir := t.TempDir()
	if out, err := gitCmd(t, dir, "clone", "-q", srv.URL+"/git/repo.git", "anon"); err == nil { // 401: git asks for a user name, and may not
		t.Fatalf("clone without a token: %v %s", err, out)
	}
	if out, err := gitCmd(t, dir, "clone", "-q", remote(otherKey), "wrong"); err == nil || !strings.Contains(out, "403") {
		t.Fatalf("clone with another project's key: %v %s", err, out)
	}
	if out, err := gitCmd(t, dir, "clone", "-q", remote(key), "work"); err != nil {
		t.Fatalf("clone with the project's key: %v %s", err, out)
	}
	work := filepath.Join(dir, "work")
	if _, err := os.Stat(filepath.Join(work, "AGENTS.md")); err != nil {
		t.Fatalf("the clone lacks AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "pipelines", "mine.yaml"), []byte("name: mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "a pipeline from outside"}, {"push", "-q", "origin", "main"}} {
		if out, err := gitCmd(t, work, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	// The push is on main and appears as a recipe event attributed to the key.
	r := e.recipe("repo", "pipelines/mine.yaml", "")
	if r.Content != "name: mine\n" || r.History[0].Message != "a pipeline from outside" {
		t.Fatalf("pushed file %+v", r)
	}
	var actor struct{ Kind, ID string }
	if err := e.pool.QueryRow(context.Background(), `SELECT actor->>'kind', actor->>'id' FROM events
		WHERE topic = 'recipe.pipelines/mine.yaml' AND project_id = $1`, p.ID).Scan(&actor.Kind, &actor.ID); err != nil {
		t.Fatalf("no recipe event for the push: %v", err)
	}
	if actor.Kind != "automation" || !strings.HasPrefix(actor.ID, "crd_") {
		t.Errorf("push attributed to %+v", actor)
	}
	// The server's next commit builds on the pushed main.
	e.ok(e.do("POST", "/api/projects/repo:note", `{"text":"after a push"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	e.recipe("repo", "pipelines/mine.yaml", "")

	// An agent session token clones, pushes its own session branch, and nothing else.
	if out, err := gitCmd(t, dir, "clone", "-q", remote(agentTok), "agent"); err != nil {
		t.Fatalf("agent clone: %v %s", err, out)
	}
	aw := filepath.Join(dir, "agent")
	for _, args := range [][]string{{"checkout", "-q", "-b", "session/ses_42"}, {"commit", "-q", "--allow-empty", "-m", "turn 1"}} {
		if out, err := gitCmd(t, aw, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if out, err := gitCmd(t, aw, "push", "-q", "origin", "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "may push only refs/heads/session/ses_42") {
		t.Fatalf("an agent pushed to main: %v %s", err, out)
	}
	if out, err := gitCmd(t, aw, "push", "-q", "origin", "session/ses_42"); err != nil {
		t.Fatalf("agent push of its session branch: %v %s", err, out)
	}
	var branches struct {
		Items []struct{ Name, Kind, SessionID string }
	}
	e.ok(e.do("GET", "/api/projects/repo/branches", ""), 200, &branches)
	if len(branches.Items) != 1 || branches.Items[0].Kind != "session" || branches.Items[0].SessionID != "ses_42" {
		t.Fatalf("branches after the agent push %+v", branches)
	}
	resp, err := http.Get(srv.URL + "/git/repo.git/HEAD") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("dumb protocol path answered %d", resp.StatusCode)
	}
}

func TestLinkedAndGitHubRepositories(t *testing.T) {
	e := start(t)
	// An existing repository to link: another store's bare repository with a README, reachable as file:// in tests.
	upstream, err := repos.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := upstream.Create(t.Context(), "upstream"); err != nil {
		t.Fatal(err)
	}
	if _, err := upstream.Commit(t.Context(), "upstream", repos.Change{Message: "readme", Files: map[string][]byte{"README.md": []byte("hi\n")}}); err != nil {
		t.Fatal(err)
	}
	u := "file://" + upstream.BarePath("upstream")
	p := e.createProject(`{"name":"Linked","repository":{"kind":"url","url":"`+u+`"}}`, "linked")
	if p.Repository.Kind != "url" || p.Repository.Remote != u || p.Repository.PushError != "" {
		t.Fatalf("linked project %+v", p)
	}
	head, _ := upstream.Head(t.Context(), "upstream")
	local, _ := e.repos.Repos().Head(t.Context(), "linked")
	if head != local {
		t.Fatalf("the remote has %s, Cadence %s: bootstrap not mirrored", head, local)
	}
	if e.recipe("linked", "README.md", "").Content != "hi\n" {
		t.Error("the linked repository's files are missing")
	}
	e.ok(e.do("POST", "/api/projects/linked:note", `{"text":"mirrored"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	if b, _, err := upstream.ReadFile(t.Context(), "upstream", "main", "NOTES.md"); err != nil || !strings.Contains(string(b), "mirrored") {
		t.Errorf("note not mirrored: %q %v", b, err)
	}

	// GitHub: a fake API creates the repository (its clone URL is another local bare repository).
	gh, err := repos.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := gh.Create(t.Context(), "created"); err != nil {
		t.Fatal(err)
	}
	var auth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"clone_url": "file://" + gh.BarePath("created"), "html_url": "x", "full_name": "acme/created"})
	}))
	defer api.Close()
	e.repos.SetGitHub(repos.GitHub{BaseURL: api.URL})
	e.ok(e.do("POST", "/api/secrets", `{"name":"gh","kind":"github","value":"ghp_test"}`, "Idempotency-Key", e.key()), 201, nil)
	p = e.createProject(`{"name":"On GitHub","repository":{"kind":"github","secret":"gh","owner":"acme"}}`, "on-github")
	if p.Repository.Kind != "github" || p.Repository.Remote != "file://"+gh.BarePath("created") || auth != "Bearer ghp_test" {
		t.Fatalf("github project %+v (auth %q)", p, auth)
	}
	if head, _ := gh.Head(t.Context(), "created"); head == "" {
		t.Error("bootstrap was not pushed to the created repository")
	}

	// A remote that refuses: the bootstrap fails and says why.
	var acc struct {
		JobID string `json:"jobId"`
	}
	e.ok(e.do("POST", "/api/projects", `{"name":"Broken","repository":{"kind":"url","url":"file:///nonexistent/repo.git"}}`,
		"Idempotency-Key", e.key()), 202, &acc)
	e.waitJob(acc.JobID, "failed")
	e.ok(e.do("GET", "/api/projects/broken", ""), 200, &p)
	var raw map[string]any
	e.ok(e.do("GET", "/api/projects/broken", ""), 200, &raw)
	if p.State != "failed" || raw["bootstrapError"] == nil {
		t.Fatalf("failed bootstrap %+v", raw)
	}
	expectProblem(t, e.do("POST", "/api/projects", `{"name":"Nope","repository":{"kind":"url","url":"ssh://git@example.org/x.git"}}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
}
