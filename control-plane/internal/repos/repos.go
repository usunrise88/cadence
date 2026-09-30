// Package repos keeps the project repositories (docs/spec/08-resolutions.md R10) with the git binary: go-git
// cannot do the three-way merges and hooks this needs, and the image ships git anyway.
//
// Layout under the control plane's data directory:
//
//	repos/<slug>.git     the bare repository: the canonical copy, served over smart HTTP (/git/<slug>.git)
//	work/<slug>          the working clone; commits people and the API make to main are written here and pushed
//	worktrees/<slug>     server-side working state of the project's sessions (removed when the project is archived)
//
// Every write to one repository holds that repository's lock, so the control plane's own commits never race each
// other; pushes from outside (smart HTTP) race only with them, and a rejected push is retried after a fetch.
// Projects whose repository lives on GitHub or another host (a remote) keep the same bare repository and mirror
// main to the remote after each change (Store.PushRemote).
package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Main is the project branch.
const Main = "main"

// Branch name prefixes Cadence gives meaning to.
const (
	SessionPrefix = "session/"
	SyncPrefix    = "sync/"
)

// mergedRefs holds one marker ref per merged branch: refs/cadence/merged/<unix time>/<branch>. The time is when
// the branch was merged; PruneMerged deletes branches whose marker is older than the retention.
const mergedRefs = "refs/cadence/merged/"

// zeroOID is git's "no object" (a ref that does not exist yet).
const zeroOID = "0000000000000000000000000000000000000000"

var (
	// ErrNotFound is returned for a repository, ref or file that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrExists is returned when creating a repository or branch that already exists.
	ErrExists = errors.New("already exists")
	// ErrStale is returned when a branch moved since the caller read it (compare-and-swap failed).
	ErrStale = errors.New("the branch moved")
)

// ConflictError reports the files a merge would conflict on; nothing was changed.
type ConflictError struct {
	Branch string
	Files  []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("merging %s into %s conflicts on %s", e.Branch, Main, strings.Join(e.Files, ", "))
}

// Signature is a git author or committer.
type Signature struct {
	Name  string
	Email string
}

// Committer is who Cadence's own commits are committed by; the author is the actor behind them.
var Committer = Signature{Name: "Cadence", Email: "cadence@cadence.local"}

// Remote is a repository main is mirrored to (GitHub or a linked URL). Token, when set, authenticates over HTTPS
// (sent as an HTTP header through the environment, never written to a config file or a URL).
type Remote struct {
	URL   string
	Token string
}

// Store manages the repositories under a data directory.
type Store struct {
	data string
	git  string
	mu   sync.Mutex
	lock map[string]*sync.Mutex
	now  func() time.Time
}

// NewStore returns a store rooted at dataDir (created when missing); it needs the git binary on PATH.
func NewStore(dataDir string) (*Store, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("repos: the git binary is required: %w", err)
	}
	s := &Store{data: dataDir, git: gitPath, lock: map[string]*sync.Mutex{}, now: time.Now}
	for _, d := range []string{s.reposDir(), filepath.Join(dataDir, "work"), filepath.Join(dataDir, "worktrees")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("repos: create %s: %w", d, err)
		}
	}
	return s, nil
}

// SetClock replaces the clock (tests of branch retention).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) reposDir() string { return filepath.Join(s.data, "repos") }

// Root is the directory holding the bare repositories (GIT_PROJECT_ROOT of the smart HTTP backend).
func (s *Store) Root() string { return s.reposDir() }

// BarePath is the bare repository of a project.
func (s *Store) BarePath(slug string) string { return filepath.Join(s.reposDir(), slug+".git") }

// WorkPath is the working clone of a project.
func (s *Store) WorkPath(slug string) string { return filepath.Join(s.data, "work", slug) }

// WorktreesPath is where a project's server-side session worktrees live.
func (s *Store) WorktreesPath(slug string) string { return filepath.Join(s.data, "worktrees", slug) }

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

func checkSlug(slug string) error {
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("repos: %q is not a project slug", slug)
	}
	return nil
}

func (s *Store) locked(slug string) func() {
	s.mu.Lock()
	m, ok := s.lock[slug]
	if !ok {
		m = &sync.Mutex{}
		s.lock[slug] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Exists reports whether the project has a bare repository.
func (s *Store) Exists(slug string) bool {
	st, err := os.Stat(filepath.Join(s.BarePath(slug), "HEAD"))
	return err == nil && !st.IsDir()
}

// ---------------------------------------------------------------- running git

// gitError is a failed git command with its stderr.
type gitError struct {
	args   []string
	code   int
	stderr string
	err    error
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = e.err.Error()
	}
	return fmt.Sprintf("git %s: %s", firstArgs(e.args), msg)
}

func (e *gitError) Unwrap() error { return e.err }

func firstArgs(args []string) string {
	if len(args) > 3 {
		args = args[:3]
	}
	return strings.Join(args, " ")
}

func exitCode(err error) int {
	var ge *gitError
	if errors.As(err, &ge) {
		return ge.code
	}
	return -1
}

type run struct {
	dir   string
	env   []string
	stdin io.Reader
}

// baseEnv isolates git from the host's configuration and prompts.
func (s *Store) baseEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + s.data,
		"LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"GIT_COMMITTER_NAME=" + Committer.Name,
		"GIT_COMMITTER_EMAIL=" + Committer.Email,
	}
}

func (s *Store) run(ctx context.Context, r run, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, s.git, args...) //nolint:gosec // git with arguments built by this package
	cmd.Dir = r.dir
	cmd.Env = append(s.baseEnv(), r.env...)
	cmd.Stdin = r.stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.Bytes(), &gitError{args: args, code: code, stderr: stderr.String(), err: err}
	}
	return stdout.Bytes(), nil
}

// bare runs git in the project's bare repository.
func (s *Store) bare(ctx context.Context, slug string, args ...string) ([]byte, error) {
	return s.run(ctx, run{dir: s.BarePath(slug)}, args...)
}

func (s *Store) bareString(ctx context.Context, slug string, args ...string) (string, error) {
	out, err := s.bare(ctx, slug, args...)
	return strings.TrimSpace(string(out)), err
}

func authorEnv(a Signature) []string {
	if a.Name == "" {
		a = Committer
	}
	if a.Email == "" {
		a.Email = Committer.Email
	}
	return []string{"GIT_AUTHOR_NAME=" + a.Name, "GIT_AUTHOR_EMAIL=" + a.Email}
}

// remoteEnv authenticates HTTPS requests to a remote with the token as basic-auth password (GitHub accepts any
// user name with a token), passed as an extra header through git's environment configuration.
func remoteEnv(r Remote) []string {
	if r.Token == "" {
		return nil
	}
	cred := basicAuth("x-access-token", r.Token)
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + cred}
}

// ---------------------------------------------------------------- repositories

// hook refuses pushes the control plane did not allow: CADENCE_PUSH_REFS (a shell pattern, set by the smart
// HTTP handler for agent tokens) limits the refs a credential may update; CADENCE_READ_ONLY refuses everything.
const hook = `#!/bin/sh
# written by Cadence — refuses pushes the credential may not make (internal/repos).
if [ "${CADENCE_READ_ONLY:-}" = "1" ]; then
  echo "cadence: this repository is read-only (the project is archived)" >&2
  exit 1
fi
while read -r old new ref; do
  if [ "$ref" = "refs/heads/main" ] && [ "$new" = "` + zeroOID + `" ]; then
    echo "cadence: main cannot be deleted" >&2
    exit 1
  fi
  if [ -n "${CADENCE_PUSH_REFS:-}" ]; then
    case "$ref" in
      $CADENCE_PUSH_REFS) ;;
      *) echo "cadence: this credential may push only $CADENCE_PUSH_REFS, not $ref" >&2; exit 1 ;;
    esac
  fi
done
exit 0
`

// Create makes an empty bare repository with main as its default branch and the working clone.
func (s *Store) Create(ctx context.Context, slug string) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	defer s.locked(slug)()
	if s.Exists(slug) {
		return fmt.Errorf("repository %s: %w", slug, ErrExists)
	}
	if _, err := s.run(ctx, run{dir: s.reposDir()}, "init", "--bare", "--initial-branch="+Main, slug+".git"); err != nil {
		return err
	}
	return s.setup(ctx, slug)
}

// Link makes the bare repository a copy of remote (every branch) and the working clone. An empty remote is fine:
// the first commit creates main.
func (s *Store) Link(ctx context.Context, slug string, remote Remote) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	if err := checkRemoteURL(remote.URL); err != nil {
		return err
	}
	defer s.locked(slug)()
	if s.Exists(slug) {
		return fmt.Errorf("repository %s: %w", slug, ErrExists)
	}
	if _, err := s.run(ctx, run{dir: s.reposDir()}, "init", "--bare", "--initial-branch="+Main, slug+".git"); err != nil {
		return err
	}
	if _, err := s.run(ctx, run{dir: s.BarePath(slug), env: remoteEnv(remote)},
		"fetch", "--no-tags", "--", remote.URL, "+refs/heads/*:refs/heads/*"); err != nil {
		_ = os.RemoveAll(s.BarePath(slug))
		return fmt.Errorf("fetch %s: %w", redact(remote.URL), err)
	}
	return s.setup(ctx, slug)
}

func (s *Store) setup(ctx context.Context, slug string) error {
	for _, kv := range [][2]string{
		{"http.receivepack", "true"},
		{"http.uploadpack", "true"},
		{"receive.denyNonFastForwards", "true"},
		{"uploadpack.hideRefs", "refs/cadence"},
		{"receive.hideRefs", "refs/cadence"},
		{"core.hooksPath", "hooks"},
		{"gc.auto", "0"},
	} {
		if _, err := s.bare(ctx, slug, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	hooks := filepath.Join(s.BarePath(slug), "hooks")
	if err := os.MkdirAll(hooks, 0o750); err != nil {
		return fmt.Errorf("create hooks: %w", err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(hook), 0o750); err != nil { //nolint:gosec // a hook must be executable
		return fmt.Errorf("write pre-receive hook: %w", err)
	}
	if err := os.MkdirAll(s.WorktreesPath(slug), 0o750); err != nil {
		return fmt.Errorf("create worktrees directory: %w", err)
	}
	return s.ensureWork(ctx, slug)
}

// ensureWork clones the bare repository into the working clone when it is missing.
func (s *Store) ensureWork(ctx context.Context, slug string) error {
	if st, err := os.Stat(filepath.Join(s.WorkPath(slug), ".git")); err == nil && st.IsDir() {
		return nil
	}
	_ = os.RemoveAll(s.WorkPath(slug))
	if _, err := s.run(ctx, run{dir: filepath.Dir(s.WorkPath(slug))}, "clone", "--quiet", "--", s.BarePath(slug), slug); err != nil {
		return err
	}
	return nil
}

// RemoveWorkingState deletes the working clone and the session worktrees of an archived project; the bare
// repository stays (read-only through SetReadOnly).
func (s *Store) RemoveWorkingState(slug string) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	defer s.locked(slug)()
	for _, d := range []string{s.WorkPath(slug), s.WorktreesPath(slug)} {
		if err := os.RemoveAll(d); err != nil {
			return fmt.Errorf("remove %s: %w", d, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- refs and reads

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// CheckBranchName validates a branch name the way git does (and a little stricter: no leading dash).
func (s *Store) CheckBranchName(ctx context.Context, name string) error {
	if !refRe.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("%q is not a branch name", name)
	}
	if _, err := s.run(ctx, run{dir: s.reposDir()}, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("%q is not a branch name", name)
	}
	return nil
}

// Resolve returns the commit a ref (branch, tag or sha) points at; ErrNotFound when it names nothing, and an empty
// sha with no error for main of a repository without commits.
func (s *Store) Resolve(ctx context.Context, slug, ref string) (string, error) {
	if !s.Exists(slug) {
		return "", fmt.Errorf("repository %s: %w", slug, ErrNotFound)
	}
	if ref == "" {
		ref = Main
	}
	if !refRe.MatchString(ref) {
		return "", fmt.Errorf("ref %q: %w", ref, ErrNotFound)
	}
	candidates := []string{"refs/heads/" + ref, ref}
	for _, c := range candidates {
		out, err := s.bareString(ctx, slug, "rev-parse", "--verify", "--quiet", "--end-of-options", c+"^{commit}")
		if err == nil && out != "" {
			return out, nil
		}
	}
	if ref == Main {
		return "", nil
	}
	return "", fmt.Errorf("ref %q: %w", ref, ErrNotFound)
}

// Head returns the commit main points at ("" before the first commit).
func (s *Store) Head(ctx context.Context, slug string) (string, error) {
	return s.Resolve(ctx, slug, Main)
}

// Refs returns every branch and the commit it points at.
func (s *Store) Refs(ctx context.Context, slug string) (map[string]string, error) {
	out, err := s.bare(ctx, slug, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		sha, ref, ok := strings.Cut(line, " ")
		if ok {
			refs[strings.TrimPrefix(ref, "refs/heads/")] = sha
		}
	}
	return refs, nil
}

// File is one file of a tree.
type File struct {
	Path  string
	Bytes int
	Blob  string
}

// ListFiles lists the files at ref, optionally under a directory prefix; it returns the commit ref resolved to.
func (s *Store) ListFiles(ctx context.Context, slug, ref, prefix string) (string, []File, error) {
	commit, err := s.Resolve(ctx, slug, ref)
	if err != nil || commit == "" {
		return commit, []File{}, err
	}
	args := []string{"ls-tree", "-r", "-l", "-z", "--full-tree", commit}
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		args = append(args, "--", prefix)
	}
	out, err := s.bare(ctx, slug, args...)
	if err != nil {
		return "", nil, err
	}
	files := []File{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type object size
		if len(f) != 4 || f[1] != "blob" {
			continue
		}
		var n int
		_, _ = fmt.Sscan(f[3], &n)
		files = append(files, File{Path: path, Bytes: n, Blob: f[2]})
	}
	return commit, files, nil
}

// ReadFile returns a file's content at ref and the commit ref resolved to.
func (s *Store) ReadFile(ctx context.Context, slug, ref, path string) ([]byte, string, error) {
	commit, err := s.Resolve(ctx, slug, ref)
	if err != nil {
		return nil, "", err
	}
	if commit == "" || !safePath(path) {
		return nil, commit, fmt.Errorf("%s at %s: %w", path, ref, ErrNotFound)
	}
	out, err := s.bare(ctx, slug, "cat-file", "blob", commit+":"+path)
	if err != nil {
		return nil, commit, fmt.Errorf("%s at %s: %w", path, ref, ErrNotFound)
	}
	return out, commit, nil
}

// LogEntry is one commit of a history.
type LogEntry struct {
	SHA     string
	Subject string
	Author  string
	At      time.Time
}

// History returns the commits at ref that changed path (every commit when path is empty), newest first.
func (s *Store) History(ctx context.Context, slug, ref, path string, limit int) ([]LogEntry, error) {
	commit, err := s.Resolve(ctx, slug, ref)
	if err != nil || commit == "" {
		return []LogEntry{}, err
	}
	args := []string{"log", fmt.Sprintf("-n%d", max(limit, 1)), "--format=%H%x1f%s%x1f%an%x1f%aI%x1e", commit}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := s.bare(ctx, slug, args...)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

func parseLog(out []byte) []LogEntry {
	entries := []LogEntry{}
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) != 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, f[3])
		entries = append(entries, LogEntry{SHA: f[0], Subject: f[1], Author: f[2], At: at})
	}
	return entries
}

// safePath rejects paths that leave the repository or name git's own files.
func safePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\x00") || strings.Contains(p, "\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == ".git" {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- commits

// Status of a changed file.
const (
	Added    = "added"
	Modified = "modified"
	Deleted  = "deleted"
)

// FileChange is one changed file.
type FileChange struct {
	Path   string
	Status string
}

// Change is a set of file writes committed on one branch.
type Change struct {
	Branch  string            // main when empty
	Author  Signature         // the actor behind the change
	Message string            // the commit message
	Files   map[string][]byte // path → new content
	Delete  []string          // paths to remove
	// Replace, when set, removes every tracked file under these directory prefixes that Files does not write (a
	// directory rendered as a whole, such as .claude/skills).
	Replace []string
}

// Commit is the result of a change: the new commit and what it changed. An empty SHA means nothing changed.
type Commit struct {
	SHA     string
	Parent  string
	Branch  string
	Changes []FileChange
}

// Commit writes c in the working clone and pushes it to the bare repository. The branch must exist (main may be
// unborn: the first commit creates it). A change that changes nothing makes no commit.
func (s *Store) Commit(ctx context.Context, slug string, c Change) (Commit, error) {
	if err := checkSlug(slug); err != nil {
		return Commit{}, err
	}
	if c.Branch == "" {
		c.Branch = Main
	}
	for p := range c.Files {
		if !safePath(p) {
			return Commit{}, fmt.Errorf("repos: refusing to write %q", p)
		}
	}
	for _, p := range c.Delete {
		if !safePath(p) {
			return Commit{}, fmt.Errorf("repos: refusing to delete %q", p)
		}
	}
	defer s.locked(slug)()
	if !s.Exists(slug) {
		return Commit{}, fmt.Errorf("repository %s: %w", slug, ErrNotFound)
	}
	if err := s.ensureWork(ctx, slug); err != nil {
		return Commit{}, err
	}
	var last error
	for range 3 { // a push from outside between fetch and push is retried on the new tip
		res, err := s.commitOnce(ctx, slug, c)
		if err == nil {
			return res, nil
		}
		last = err
		if !errors.Is(err, errPushRejected) {
			return Commit{}, err
		}
	}
	return Commit{}, last
}

var errPushRejected = errors.New("push rejected")

func (s *Store) commitOnce(ctx context.Context, slug string, c Change) (Commit, error) {
	w := run{dir: s.WorkPath(slug)}
	if _, err := s.run(ctx, w, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return Commit{}, err
	}
	parent, err := s.Resolve(ctx, slug, c.Branch)
	if err != nil {
		return Commit{}, err
	}
	if parent == "" && c.Branch != Main {
		return Commit{}, fmt.Errorf("branch %s: %w", c.Branch, ErrNotFound)
	}
	// Start from exactly the branch tip (or nothing, for the first commit), whatever an earlier attempt left.
	if parent == "" {
		if _, err := s.run(ctx, w, "symbolic-ref", "HEAD", "refs/heads/"+c.Branch); err != nil {
			return Commit{}, err
		}
		if _, err := s.run(ctx, w, "read-tree", "--empty"); err != nil {
			return Commit{}, err
		}
	} else if _, err := s.run(ctx, w, "checkout", "--quiet", "--force", "-B", c.Branch, parent); err != nil {
		return Commit{}, err
	}
	if _, err := s.run(ctx, w, "clean", "-f", "-d", "-x", "-q"); err != nil {
		return Commit{}, err
	}
	for _, prefix := range c.Replace {
		if _, err := s.run(ctx, w, "rm", "-r", "-q", "--ignore-unmatch", "--", prefix); err != nil {
			return Commit{}, err
		}
	}
	for _, p := range c.Delete {
		if _, err := s.run(ctx, w, "rm", "-q", "--ignore-unmatch", "--", p); err != nil {
			return Commit{}, err
		}
	}
	for p, content := range c.Files {
		full := filepath.Join(s.WorkPath(slug), filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return Commit{}, fmt.Errorf("write %s: %w", p, err)
		}
		if err := os.WriteFile(full, content, 0o600); err != nil {
			return Commit{}, fmt.Errorf("write %s: %w", p, err)
		}
	}
	if _, err := s.run(ctx, w, "add", "-A", "."); err != nil {
		return Commit{}, err
	}
	status, err := s.run(ctx, w, "diff", "--cached", "--name-status", "-z", "--no-renames")
	if err != nil {
		return Commit{}, err
	}
	changes := parseNameStatus(status)
	if len(changes) == 0 {
		return Commit{SHA: "", Parent: parent, Branch: c.Branch, Changes: []FileChange{}}, nil
	}
	msg := c.Message
	if strings.TrimSpace(msg) == "" {
		msg = "update"
	}
	if _, err := s.run(ctx, run{dir: w.dir, env: authorEnv(c.Author)}, "commit", "--quiet", "--no-verify", "-m", msg); err != nil {
		return Commit{}, err
	}
	sha, err := s.run(ctx, w, "rev-parse", "HEAD")
	if err != nil {
		return Commit{}, err
	}
	head := strings.TrimSpace(string(sha))
	lease := "--force-with-lease=refs/heads/" + c.Branch + ":" + parent
	if _, err := s.run(ctx, w, "push", "--quiet", "--porcelain", lease, "origin", "HEAD:refs/heads/"+c.Branch); err != nil {
		return Commit{}, fmt.Errorf("%w: %w", errPushRejected, err)
	}
	return Commit{SHA: head, Parent: parent, Branch: c.Branch, Changes: changes}, nil
}
