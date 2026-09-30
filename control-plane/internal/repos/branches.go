package repos

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch is one branch other than main, measured against main.
type Branch struct {
	Name      string
	Head      string
	Subject   string
	UpdatedAt time.Time
	Ahead     int // commits on the branch that main lacks
	Behind    int // commits on main that the branch lacks
}

// Kind is what Cadence uses the branch for: session, sync or other.
func (b Branch) Kind() string { return BranchKind(b.Name) }

// BranchKind classifies a branch name.
func BranchKind(name string) string {
	switch {
	case strings.HasPrefix(name, SessionPrefix):
		return "session"
	case strings.HasPrefix(name, SyncPrefix):
		return "sync"
	}
	return "other"
}

// SessionBranch is the branch of an agent session: session/<id>.
func SessionBranch(sessionID string) string { return SessionPrefix + sessionID }

// Branches lists the branches that main has not merged (open branches), most recently updated first; all, when
// set, keeps merged ones too.
func (s *Store) Branches(ctx context.Context, slug string, all bool) ([]Branch, error) {
	if !s.Exists(slug) {
		return nil, fmt.Errorf("repository %s: %w", slug, ErrNotFound)
	}
	out, err := s.bare(ctx, slug, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:strip=2)%1f%(objectname)%1f%(subject)%1f%(committerdate:iso-strict)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	main, err := s.Head(ctx, slug)
	if err != nil {
		return nil, err
	}
	branches := []Branch{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 || f[0] == Main {
			continue
		}
		b := Branch{Name: f[0], Head: f[1], Subject: f[2]}
		b.UpdatedAt, _ = time.Parse(time.RFC3339, f[3])
		if main != "" {
			b.Behind, b.Ahead, err = s.aheadBehind(ctx, slug, main, b.Head)
			if err != nil {
				return nil, err
			}
		} else {
			b.Ahead, err = s.count(ctx, slug, b.Head)
			if err != nil {
				return nil, err
			}
		}
		if all || b.Ahead > 0 {
			branches = append(branches, b)
		}
	}
	return branches, nil
}

// Branch returns one branch measured against main.
func (s *Store) Branch(ctx context.Context, slug, name string) (Branch, error) {
	list, err := s.Branches(ctx, slug, true)
	if err != nil {
		return Branch{}, err
	}
	for _, b := range list {
		if b.Name == name {
			return b, nil
		}
	}
	return Branch{}, fmt.Errorf("branch %s: %w", name, ErrNotFound)
}

func (s *Store) aheadBehind(ctx context.Context, slug, left, right string) (int, int, error) {
	out, err := s.bareString(ctx, slug, "rev-list", "--left-right", "--count", left+"..."+right)
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("rev-list --count: unexpected %q", out)
	}
	l, _ := strconv.Atoi(f[0])
	r, _ := strconv.Atoi(f[1])
	return l, r, nil
}

func (s *Store) count(ctx context.Context, slug, rev string) (int, error) {
	out, err := s.bareString(ctx, slug, "rev-list", "--count", rev)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// CreateBranch creates name at from (main when empty) and returns its commit; an existing name is ErrExists.
// The agent-session stream creates session/<id> this way before the host clones it.
func (s *Store) CreateBranch(ctx context.Context, slug, name, from string) (string, error) {
	if err := s.CheckBranchName(ctx, name); err != nil {
		return "", err
	}
	if name == Main {
		return "", fmt.Errorf("branch %s: %w", name, ErrExists)
	}
	defer s.locked(slug)()
	sha, err := s.Resolve(ctx, slug, from)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", fmt.Errorf("%s has no commits yet: %w", Main, ErrNotFound)
	}
	if _, err := s.bare(ctx, slug, "update-ref", "--create-reflog", "refs/heads/"+name, sha, zeroOID); err != nil {
		if _, rerr := s.Resolve(ctx, slug, name); rerr == nil {
			return "", fmt.Errorf("branch %s: %w", name, ErrExists)
		}
		return "", err
	}
	return sha, nil
}

// DeleteBranch removes a branch (and its merged marker); expect, when set, must be its current head.
func (s *Store) DeleteBranch(ctx context.Context, slug, name, expect string) error {
	if name == Main {
		return errors.New("repos: main cannot be deleted")
	}
	defer s.locked(slug)()
	return s.deleteBranch(ctx, slug, name, expect)
}

func (s *Store) deleteBranch(ctx context.Context, slug, name, expect string) error {
	head, err := s.bareString(ctx, slug, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil || head == "" {
		return fmt.Errorf("branch %s: %w", name, ErrNotFound)
	}
	if expect != "" && expect != head {
		return fmt.Errorf("branch %s is at %s, not %s: %w", name, short(head), short(expect), ErrStale)
	}
	if _, err := s.bare(ctx, slug, "update-ref", "-d", "refs/heads/"+name, head); err != nil {
		return err
	}
	markers, err := s.markers(ctx, slug)
	if err != nil {
		return err
	}
	for _, m := range markers {
		if m.branch == name {
			if _, err := s.bare(ctx, slug, "update-ref", "-d", m.ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- diff

// FileDiff is one file of a branch's diff against main.
type FileDiff struct {
	Path      string
	Status    string
	Additions int
	Deletions int
	Binary    bool
}

// Diff is a branch compared with main.
type Diff struct {
	Branch      Branch
	Main        string
	Base        string // merge base
	FastForward bool   // main is the merge base: the branch applies as a fast-forward
	Conflicts   []string
	Files       []FileDiff
	Patch       string
	Truncated   bool
}

// MaxPatch is the size a diff's patch is cut at.
const MaxPatch = 512 << 10

// DiffBranch compares a branch with main: the files and patch of main...branch (what the branch changed since it
// left main) and the files a merge would conflict on (git merge-tree, nothing is written).
func (s *Store) DiffBranch(ctx context.Context, slug, name string) (Diff, error) {
	b, err := s.Branch(ctx, slug, name)
	if err != nil {
		return Diff{}, err
	}
	main, err := s.Head(ctx, slug)
	if err != nil {
		return Diff{}, err
	}
	d := Diff{Branch: b, Main: main, Conflicts: []string{}, Files: []FileDiff{}}
	if main == "" {
		d.FastForward = true
		return d, nil
	}
	if d.Base, err = s.bareString(ctx, slug, "merge-base", main, b.Head); err != nil && exitCode(err) != 1 {
		return Diff{}, err
	}
	d.FastForward = d.Base == main
	from := d.Base
	if from == "" {
		from = emptyTree
	}
	if d.Files, err = s.diffFiles(ctx, slug, from, b.Head); err != nil {
		return Diff{}, err
	}
	patch, err := s.bare(ctx, slug, "diff", "--no-color", "--no-renames", from, b.Head)
	if err != nil {
		return Diff{}, err
	}
	if len(patch) > MaxPatch {
		patch, d.Truncated = patch[:MaxPatch], true
	}
	d.Patch = string(patch)
	if !d.FastForward {
		_, conflicts, err := s.mergeTree(ctx, slug, main, b.Head)
		if err != nil {
			return Diff{}, err
		}
		d.Conflicts = conflicts
	}
	return d, nil
}

// emptyTree is git's well-known empty tree.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (s *Store) diffFiles(ctx context.Context, slug, from, to string) ([]FileDiff, error) {
	ns, err := s.bare(ctx, slug, "diff", "--name-status", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	num, err := s.bare(ctx, slug, "diff", "--numstat", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	stats := map[string][3]int{} // additions, deletions, binary
	for _, rec := range strings.Split(string(num), "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		var st [3]int
		if f[0] == "-" {
			st[2] = 1
		} else {
			st[0], _ = strconv.Atoi(f[0])
			st[1], _ = strconv.Atoi(f[1])
		}
		stats[f[2]] = st
	}
	files := []FileDiff{}
	for _, c := range parseNameStatus(ns) {
		st := stats[c.Path]
		files = append(files, FileDiff{Path: c.Path, Status: c.Status, Additions: st[0], Deletions: st[1], Binary: st[2] == 1})
	}
	return files, nil
}

// Changes lists what changed between two commits; from may be empty (everything in to is added).
func (s *Store) Changes(ctx context.Context, slug, from, to string) ([]FileChange, error) {
	if from == "" || from == zeroOID {
		from = emptyTree
	}
	if to == "" || to == zeroOID {
		to = emptyTree
	}
	out, err := s.bare(ctx, slug, "diff", "--name-status", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// parseNameStatus reads `git diff --name-status -z --no-renames`: status NUL path NUL …
func parseNameStatus(out []byte) []FileChange {
	parts := strings.Split(string(out), "\x00")
	changes := []FileChange{}
	for i := 0; i+1 < len(parts); i += 2 {
		st := ""
		switch {
		case strings.HasPrefix(parts[i], "A"):
			st = Added
		case strings.HasPrefix(parts[i], "D"):
			st = Deleted
		case parts[i] == "":
			continue
		default:
			st = Modified
		}
		changes = append(changes, FileChange{Path: parts[i+1], Status: st})
	}
	sort.Slice(changes, func(a, b int) bool { return changes[a].Path < changes[b].Path })
	return changes
}

// mergeTree runs a merge of ours and theirs without touching any ref or working tree: it returns the merged tree,
// or the conflicting files.
func (s *Store) mergeTree(ctx context.Context, slug, ours, theirs string) (string, []string, error) {
	out, err := s.bare(ctx, slug, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", ours, theirs)
	switch exitCode(err) {
	case -1:
		if err != nil {
			return "", nil, err
		}
	case 1: // conflicts: tree NUL file NUL file NUL …
	default:
		return "", nil, err
	}
	parts := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if err == nil {
		return strings.TrimSpace(parts[0]), []string{}, nil
	}
	seen := map[string]bool{}
	files := []string{}
	for _, p := range parts[1:] {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return "", files, nil
}

// ---------------------------------------------------------------- merge

// Merge is the result of merging a branch into main.
type Merge struct {
	Branch      string
	Head        string // the branch head that was merged
	Before      string // main before
	Main        string // main after
	FastForward bool
	Changes     []FileChange // what main gained
}

// MergeBranch merges a branch into main: a fast-forward when main has not moved since the branch left it,
// otherwise a merge commit (git's ort merge, computed without a working tree). A conflict returns *ConflictError
// and changes nothing; expect, when set, must be the branch head the caller reviewed (ErrStale otherwise). The
// branch stays, with a marker that PruneMerged uses to delete it after the retention.
func (s *Store) MergeBranch(ctx context.Context, slug, name, expect string, author Signature, message string) (Merge, error) {
	if name == Main {
		return Merge{}, errors.New("repos: main cannot be merged into itself")
	}
	defer s.locked(slug)()
	b, err := s.Branch(ctx, slug, name)
	if err != nil {
		return Merge{}, err
	}
	if expect != "" && expect != b.Head {
		return Merge{}, fmt.Errorf("branch %s is at %s, not %s: %w", name, short(b.Head), short(expect), ErrStale)
	}
	main, err := s.Head(ctx, slug)
	if err != nil {
		return Merge{}, err
	}
	m := Merge{Branch: name, Head: b.Head, Before: main}
	base := ""
	if main != "" {
		if base, err = s.bareString(ctx, slug, "merge-base", main, b.Head); err != nil && exitCode(err) != 1 {
			return Merge{}, err
		}
	}
	switch {
	case main == "" || base == main:
		m.FastForward, m.Main = true, b.Head
	case base == b.Head: // already merged: nothing to do
		m.Main = main
	default:
		tree, conflicts, err := s.mergeTree(ctx, slug, main, b.Head)
		if err != nil {
			return Merge{}, err
		}
		if len(conflicts) > 0 {
			return Merge{}, &ConflictError{Branch: name, Files: conflicts}
		}
		if message == "" {
			message = "Merge branch '" + name + "'"
		}
		commit, err := s.run(ctx, run{dir: s.BarePath(slug), env: authorEnv(author)},
			"commit-tree", tree, "-p", main, "-p", b.Head, "-m", message)
		if err != nil {
			return Merge{}, err
		}
		m.Main = strings.TrimSpace(string(commit))
	}
	if m.Main != main {
		old := main
		if old == "" {
			old = zeroOID
		}
		if _, err := s.bare(ctx, slug, "update-ref", "-m", "merge "+name, "refs/heads/"+Main, m.Main, old); err != nil {
			return Merge{}, fmt.Errorf("move %s: %w", Main, errors.Join(ErrStale, err))
		}
	}
	if m.Changes, err = s.Changes(ctx, slug, main, m.Main); err != nil {
		return Merge{}, err
	}
	marker := fmt.Sprintf("%s%d/%s", mergedRefs, s.now().Unix(), name)
	if _, err := s.bare(ctx, slug, "update-ref", marker, b.Head); err != nil {
		return Merge{}, err
	}
	return m, nil
}

type marker struct {
	ref    string
	branch string
	at     time.Time
}

func (s *Store) markers(ctx context.Context, slug string) ([]marker, error) {
	out, err := s.bareString(ctx, slug, "for-each-ref", "--format=%(refname)", mergedRefs)
	if err != nil {
		return nil, err
	}
	var ms []marker
	for _, ref := range strings.Fields(out) {
		rest := strings.TrimPrefix(ref, mergedRefs)
		ts, branch, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		ms = append(ms, marker{ref: ref, branch: branch, at: time.Unix(sec, 0)})
	}
	return ms, nil
}

// BranchRetention is how long a merged branch is kept (docs/spec/05-agents.md "Worktree, drafts and merge").
const BranchRetention = 30 * 24 * time.Hour

// PruneMerged deletes the branches merged before cutoff whose head has not moved since the merge, and their
// markers; it returns the deleted branch names.
func (s *Store) PruneMerged(ctx context.Context, slug string, cutoff time.Time) ([]string, error) {
	defer s.locked(slug)()
	ms, err := s.markers(ctx, slug)
	if err != nil {
		return nil, err
	}
	deleted := []string{}
	for _, m := range ms {
		if !m.at.Before(cutoff) {
			continue
		}
		merged, _ := s.bareString(ctx, slug, "rev-parse", m.ref)
		head, _ := s.bareString(ctx, slug, "rev-parse", "--verify", "--quiet", "refs/heads/"+m.branch)
		if head != "" && head == merged {
			if _, err := s.bare(ctx, slug, "update-ref", "-d", "refs/heads/"+m.branch, head); err != nil {
				return deleted, err
			}
			deleted = append(deleted, m.branch)
		}
		if _, err := s.bare(ctx, slug, "update-ref", "-d", m.ref); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// ---------------------------------------------------------------- remotes

// PushRemote mirrors main to the remote (GitHub or a linked repository). It never forces: when the remote moved
// on its own the push fails and main stays as it is here.
func (s *Store) PushRemote(ctx context.Context, slug string, remote Remote) error {
	if err := checkRemoteURL(remote.URL); err != nil {
		return err
	}
	head, err := s.Head(ctx, slug)
	if err != nil || head == "" {
		return err
	}
	if _, err := s.run(ctx, run{dir: s.BarePath(slug), env: remoteEnv(remote)},
		"push", "--porcelain", "--", remote.URL, "refs/heads/"+Main+":refs/heads/"+Main); err != nil {
		return fmt.Errorf("push to %s: %w", redact(remote.URL), err)
	}
	return nil
}

// checkRemoteURL accepts https:// URLs (and file:// and plain paths, which tests and local mirrors use); ssh and
// the ext:: transports are refused.
func checkRemoteURL(raw string) error {
	if raw == "" {
		return errors.New("repos: the remote URL is empty")
	}
	if strings.HasPrefix(raw, "-") || strings.Contains(raw, "::") {
		return fmt.Errorf("repos: unsupported remote %q", redact(raw))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("repos: remote URL: %w", err)
	}
	switch u.Scheme {
	case "https", "http", "file", "":
		return nil
	}
	return fmt.Errorf("repos: unsupported remote scheme %q (use https)", u.Scheme)
}

// redact removes credentials from a URL for messages.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
