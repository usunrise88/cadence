package repos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var alice = Signature{Name: "Alice", Email: "alice@example.org"}

func commit(t *testing.T, s *Store, slug, branch, msg string, files map[string]string, del ...string) Commit {
	t.Helper()
	c := Change{Branch: branch, Author: alice, Message: msg, Files: map[string][]byte{}, Delete: del}
	for p, v := range files {
		c.Files[p] = []byte(v)
	}
	res, err := s.Commit(context.Background(), slug, c)
	if err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
	return res
}

func TestCommitReadList(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Create(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, "demo"); !errors.Is(err, ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	if head, err := s.Head(ctx, "demo"); err != nil || head != "" {
		t.Fatalf("empty head = %q, %v", head, err)
	}
	first := commit(t, s, "demo", "", "bootstrap", map[string]string{"project.yaml": "name: demo\n", "pipelines/a.yaml": "a: 1\n"})
	if first.SHA == "" || first.Parent != "" || len(first.Changes) != 2 || first.Changes[0].Status != Added {
		t.Fatalf("first commit %+v", first)
	}
	noop := commit(t, s, "demo", "", "again", map[string]string{"project.yaml": "name: demo\n"})
	if noop.SHA != "" {
		t.Fatalf("a commit that changes nothing made %s", noop.SHA)
	}
	second := commit(t, s, "demo", "", "edit", map[string]string{"project.yaml": "name: renamed\n"}, "pipelines/a.yaml")
	want := []FileChange{{"pipelines/a.yaml", Deleted}, {"project.yaml", Modified}}
	if !slices.Equal(second.Changes, want) || second.Parent != first.SHA {
		t.Fatalf("second commit %+v", second)
	}
	b, at, err := s.ReadFile(ctx, "demo", "main", "project.yaml")
	if err != nil || string(b) != "name: renamed\n" || at != second.SHA {
		t.Fatalf("read = %q at %s, %v", b, at, err)
	}
	if b, _, err := s.ReadFile(ctx, "demo", first.SHA, "pipelines/a.yaml"); err != nil || string(b) != "a: 1\n" {
		t.Fatalf("read at the first commit = %q, %v", b, err)
	}
	if _, _, err := s.ReadFile(ctx, "demo", "main", "../secrets"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read outside the tree: %v", err)
	}
	_, files, err := s.ListFiles(ctx, "demo", "", "")
	if err != nil || len(files) != 1 || files[0].Path != "project.yaml" || files[0].Bytes != len("name: renamed\n") {
		t.Fatalf("list = %+v, %v", files, err)
	}
	hist, err := s.History(ctx, "demo", "main", "project.yaml", 10)
	if err != nil || len(hist) != 2 || hist[0].SHA != second.SHA || hist[0].Subject != "edit" || hist[0].Author != "Alice" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if _, err := s.Commit(ctx, "demo", Change{Files: map[string][]byte{".git/config": nil}}); err == nil {
		t.Fatal("wrote into .git")
	}
}

func TestBranchesDiffMerge(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Create(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	base := commit(t, s, "demo", "", "bootstrap", map[string]string{"a.txt": "one\ntwo\nthree\n", "b.txt": "b\n"})

	// A session branch that main has not moved past merges as a fast-forward.
	if _, err := s.CreateBranch(ctx, "demo", "session/s1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch(ctx, "demo", "session/s1", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate branch: %v", err)
	}
	if _, err := s.CreateBranch(ctx, "demo", "-bad", ""); err == nil {
		t.Fatal("accepted a branch name starting with a dash")
	}
	turn := commit(t, s, "demo", "session/s1", "turn 1", map[string]string{"c.txt": "c\n"})
	open, err := s.Branches(ctx, "demo", false)
	if err != nil || len(open) != 1 || open[0].Name != "session/s1" || open[0].Ahead != 1 || open[0].Behind != 0 || open[0].Kind() != "session" {
		t.Fatalf("branches = %+v, %v", open, err)
	}
	d, err := s.DiffBranch(ctx, "demo", "session/s1")
	if err != nil || !d.FastForward || len(d.Files) != 1 || d.Files[0].Path != "c.txt" || d.Files[0].Additions != 1 ||
		!strings.Contains(d.Patch, "+c") || len(d.Conflicts) != 0 || d.Base != base.SHA {
		t.Fatalf("diff = %+v, %v", d, err)
	}
	if _, err := s.MergeBranch(ctx, "demo", "session/s1", base.SHA, alice, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("merge with a stale head: %v", err)
	}
	m, err := s.MergeBranch(ctx, "demo", "session/s1", turn.SHA, alice, "")
	if err != nil || !m.FastForward || m.Main != turn.SHA || len(m.Changes) != 1 {
		t.Fatalf("fast-forward = %+v, %v", m, err)
	}
	if open, _ := s.Branches(ctx, "demo", false); len(open) != 0 {
		t.Fatalf("a merged branch is still open: %+v", open)
	}

	// Main moved on: a branch touching other lines merges with a merge commit.
	if _, err := s.CreateBranch(ctx, "demo", "session/s2", ""); err != nil {
		t.Fatal(err)
	}
	commit(t, s, "demo", "", "main edits line one", map[string]string{"a.txt": "ONE\ntwo\nthree\n"})
	commit(t, s, "demo", "session/s2", "session edits line three", map[string]string{"a.txt": "one\ntwo\nTHREE\n"})
	d, err = s.DiffBranch(ctx, "demo", "session/s2")
	if err != nil || d.FastForward || len(d.Conflicts) != 0 {
		t.Fatalf("diff s2 = %+v, %v", d, err)
	}
	m, err = s.MergeBranch(ctx, "demo", "session/s2", "", alice, "merge s2")
	if err != nil || m.FastForward {
		t.Fatalf("three-way = %+v, %v", m, err)
	}
	if b, _, _ := s.ReadFile(ctx, "demo", "main", "a.txt"); string(b) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("merged a.txt = %q", b)
	}

	// Both sides change the same line: a conflict is reported and nothing moves.
	if _, err := s.CreateBranch(ctx, "demo", "sync/2026-09-30", ""); err != nil {
		t.Fatal(err)
	}
	commit(t, s, "demo", "", "main edits two", map[string]string{"a.txt": "ONE\nmain\nTHREE\n"})
	commit(t, s, "demo", "sync/2026-09-30", "sync edits two", map[string]string{"a.txt": "ONE\nsync\nTHREE\n"})
	before, _ := s.Head(ctx, "demo")
	d, err = s.DiffBranch(ctx, "demo", "sync/2026-09-30")
	if err != nil || !slices.Equal(d.Conflicts, []string{"a.txt"}) {
		t.Fatalf("predicted conflicts = %+v, %v", d.Conflicts, err)
	}
	_, err = s.MergeBranch(ctx, "demo", "sync/2026-09-30", "", alice, "")
	var ce *ConflictError
	if !errors.As(err, &ce) || !slices.Equal(ce.Files, []string{"a.txt"}) {
		t.Fatalf("conflicting merge: %v", err)
	}
	if after, _ := s.Head(ctx, "demo"); after != before {
		t.Fatalf("a conflicting merge moved main %s → %s", before, after)
	}
	// The working clone is clean afterwards: the next commit works.
	commit(t, s, "demo", "", "after the conflict", map[string]string{"d.txt": "d\n"})

	// Merged branches are kept for the retention, then pruned.
	s.SetClock(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) })
	deleted, err := s.PruneMerged(ctx, "demo", time.Now().Add(-BranchRetention))
	if err != nil || len(deleted) != 0 {
		t.Fatalf("pruned too early: %v %v", deleted, err)
	}
	deleted, err = s.PruneMerged(ctx, "demo", time.Now().Add(60*24*time.Hour))
	if err != nil || !slices.Equal(deleted, []string{"session/s1", "session/s2"}) {
		t.Fatalf("pruned %v, %v", deleted, err)
	}
	if err := s.DeleteBranch(ctx, "demo", "sync/2026-09-30", ""); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.Branches(ctx, "demo", true); len(all) != 0 {
		t.Fatalf("branches left: %+v", all)
	}
}

func TestLinkAndPushRemote(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// The "remote" is another store's bare repository with one commit.
	upstream := newStore(t)
	if err := upstream.Create(ctx, "upstream"); err != nil {
		t.Fatal(err)
	}
	commit(t, upstream, "upstream", "", "readme", map[string]string{"README.md": "hello\n"})
	remote := Remote{URL: "file://" + upstream.BarePath("upstream")}
	if err := s.Link(ctx, "linked", remote); err != nil {
		t.Fatal(err)
	}
	if b, _, err := s.ReadFile(ctx, "linked", "main", "README.md"); err != nil || string(b) != "hello\n" {
		t.Fatalf("linked README = %q, %v", b, err)
	}
	c := commit(t, s, "linked", "", "bootstrap", map[string]string{"project.yaml": "x\n"})
	if err := s.PushRemote(ctx, "linked", remote); err != nil {
		t.Fatal(err)
	}
	if head, _ := upstream.Head(ctx, "upstream"); head != c.SHA {
		t.Fatalf("remote main = %s, want %s", head, c.SHA)
	}
	if err := checkRemoteURL("ext::sh -c touch% /tmp/pwned"); err == nil {
		t.Fatal("accepted an ext:: remote")
	}
	if err := checkRemoteURL("ssh://git@github.com/a/b.git"); err == nil {
		t.Fatal("accepted ssh")
	}
}

func TestSmartHTTP(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Create(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	commit(t, s, "demo", "", "bootstrap", map[string]string{"project.yaml": "name: demo\n"})
	if _, err := s.CreateBranch(ctx, "demo", "session/s1", ""); err != nil {
		t.Fatal(err)
	}
	var pushes []map[string][2]string
	h := s.HTTPHandler(func(_ context.Context, slug, token string, write bool) (Access, error) {
		switch token {
		case "":
			return Access{}, ErrUnauthorized
		case "person":
			return Access{User: "admin", Write: true}, nil
		case "agent":
			return Access{User: "agent", Write: true, PushRefs: "refs/heads/session/s1"}, nil
		case "archived":
			return Access{User: "admin", Write: true, ReadOnly: true}, nil
		}
		return Access{}, ErrForbidden
	}, func(_ context.Context, slug string, _ Access, moved map[string][2]string) {
		pushes = append(pushes, moved)
	})
	mux := http.NewServeMux()
	mux.Handle(HTTPPath+"/", h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	url := func(token string) string {
		return strings.Replace(srv.URL, "http://", "http://x-token:"+token+"@", 1) + "/git/demo.git"
	}
	dir := t.TempDir()
	git := func(wd string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := git(dir, "clone", "--quiet", srv.URL+"/git/demo.git", "anon"); err == nil {
		t.Fatalf("cloned without a token: %s", out)
	}
	if out, err := git(dir, "clone", "--quiet", url("stranger"), "stranger"); err == nil || !strings.Contains(out, "403") {
		t.Fatalf("cloned with a foreign token: %v %s", err, out)
	}
	if out, err := git(dir, "clone", "--quiet", url("person"), "person"); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	work := filepath.Join(dir, "person")
	if err := os.WriteFile(filepath.Join(work, "notes.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "notes.md"}, {"commit", "-q", "-m", "from outside"}, {"push", "-q", "origin", "main"}} {
		if out, err := git(work, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if b, _, err := s.ReadFile(ctx, "demo", "main", "notes.md"); err != nil || string(b) != "hi\n" {
		t.Fatalf("pushed file = %q, %v", b, err)
	}
	if len(pushes) != 1 || pushes[0]["main"][1] == "" {
		t.Fatalf("push notifications %+v", pushes)
	}
	// The control plane's next commit builds on the pushed main.
	commit(t, s, "demo", "", "after push", map[string]string{"project.yaml": "name: again\n"})
	if b, _, _ := s.ReadFile(ctx, "demo", "main", "notes.md"); string(b) != "hi\n" {
		t.Fatalf("the working clone lost the pushed file")
	}

	// An agent token pushes only its session branch.
	if out, err := git(dir, "clone", "--quiet", url("agent"), "agent"); err != nil {
		t.Fatalf("agent clone: %v %s", err, out)
	}
	aw := filepath.Join(dir, "agent")
	for _, args := range [][]string{{"checkout", "-q", "session/s1"}, {"commit", "-q", "--allow-empty", "-m", "turn"}} {
		if out, err := git(aw, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if out, err := git(aw, "push", "-q", "origin", "session/s1:refs/heads/elsewhere"); err == nil || !strings.Contains(out, "may push only") {
		t.Fatalf("agent pushed outside its branch: %v %s", err, out)
	}
	if out, err := git(aw, "push", "-q", "origin", "session/s1"); err != nil {
		t.Fatalf("agent push to its branch: %v %s", err, out)
	}
	// An archived project's repository refuses pushes.
	if out, err := git(aw, "push", "-q", url("archived"), "session/s1:refs/heads/other"); err == nil || !strings.Contains(out, "read-only") {
		t.Fatalf("pushed to an archived repository: %v %s", err, out)
	}
	b, _ := json.Marshal(pushes)
	if len(pushes) != 2 {
		t.Fatalf("pushes %s", b)
	}
}

func TestGitHubCreateRepo(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		if got.body["name"] == "taken" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Repository creation failed.","errors":[{"message":"name already exists on this account"}]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"clone_url":"https://github.com/acme/demo.git","html_url":"https://github.com/acme/demo","full_name":"acme/demo"}`))
	}))
	defer srv.Close()
	g := GitHub{BaseURL: srv.URL}
	r, err := g.CreateRepo(context.Background(), "tok", "acme", "demo", "Demo", true)
	if err != nil || r.CloneURL != "https://github.com/acme/demo.git" || got.path != "/orgs/acme/repos" ||
		got.auth != "Bearer tok" || got.body["private"] != true {
		t.Fatalf("create = %+v %v; request %+v", r, err, got)
	}
	if _, err := g.CreateRepo(context.Background(), "tok", "", "taken", "", true); err == nil ||
		!strings.Contains(err.Error(), "already exists") || got.path != "/user/repos" {
		t.Fatalf("taken: %v (%s)", err, got.path)
	}
}
