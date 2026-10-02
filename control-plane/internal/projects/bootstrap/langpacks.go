package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Language packs in the project repository (docs/spec/03-pipelines-defaults.md "Language packs and hot words"):
// langpacks.edit and boost.edit commit through WritePack; projects.sync offers starter pack updates through
// packUpdates.

// LangpackBranchPrefix starts the branch an agent's pack edit lands on under the draft policy language_pack: draft.
const LangpackBranchPrefix = "langpack/"

// PackWrite is one change of a language pack.
type PackWrite struct {
	Locale string
	// Expect is the pack's version from langpacks.get (the last commit that changed it; If-Match).
	Expect string
	// Files are written and Delete removed, by path inside the pack (normalizer.yaml, boost/names.txt).
	Files   map[string][]byte
	Delete  []string
	Message string
	// Draft commits on a new branch langpack/<locale>-<date> for a person to accept, instead of main.
	Draft  bool
	Limits langpacks.Limits
}

// PackCommit is where a pack change landed: main, or a draft branch, at commit (empty for a dry run or no change).
type PackCommit struct {
	Branch string
	Commit string
	Pack   langpacks.Pack // the pack as written (or, for a dry run, as it would be)
	Base   string         // the pack's version before the change
}

// WritePack checks a pack change against the pack on main and the pack's file shapes and commits it as actor. The
// pack must exist (a project gets its packs at bootstrap and from projects.sync); a pack changed since Expect
// answers 412; a dry run checks everything and commits nothing.
func (s *Service) WritePack(ctx context.Context, tx pgx.Tx, p projects.Project, w PackWrite, actor auth.Actor, dryRun bool) (PackCommit, []events.Draft, error) {
	if err := writable(p); err != nil {
		return PackCommit{}, nil, err
	}
	cur, err := langpacks.ReadPack(ctx, s.o.Repos, p.Slug, repos.Main, w.Locale)
	if err != nil {
		return PackCommit{}, nil, repoProblem(err)
	}
	if w.Expect == "" || !strings.HasPrefix(cur.SHA, w.Expect) {
		return PackCommit{}, nil, problems.PreconditionFailed.New("the %s language pack was last changed in %.12s, not %s; re-read it (langpacks.get)", cur.Locale, cur.SHA, w.Expect)
	}
	next := langpacks.Pack{Locale: cur.Locale, Files: maps.Clone(cur.Files)}
	var fields []problems.FieldError
	for rel, b := range w.Files {
		if !safeRel(rel) {
			fields = append(fields, problems.FieldError{Path: "files", Message: fmt.Sprintf("%q is not a path inside the pack", rel)})
			continue
		}
		next.Files[rel] = b
	}
	for _, rel := range w.Delete {
		if _, ok := next.Files[rel]; !ok {
			fields = append(fields, problems.FieldError{Path: "files", Message: fmt.Sprintf("%s is not in the pack; nothing to delete", rel)})
		}
		delete(next.Files, rel)
	}
	if len(fields) == 0 {
		fields = next.Check(w.Limits)
	}
	if len(fields) > 0 {
		e := problems.Validation(fields)
		e.Detail = "the language pack would not check out: " + fields[0].Path + ": " + fields[0].Message
		return PackCommit{}, nil, e
	}
	out := PackCommit{Pack: next, Base: cur.SHA}
	if dryRun {
		return out, nil, nil
	}
	dir := langpacks.PackPath(cur.Locale)
	change := repos.Change{Author: Signature(actor), Message: w.Message, Files: layout.Files{}}
	if change.Message == "" {
		change.Message = "language pack " + cur.Locale + ": " + summary(w)
	}
	for rel, b := range w.Files {
		change.Files[dir+"/"+rel] = b
	}
	for _, rel := range w.Delete {
		change.Delete = append(change.Delete, dir+"/"+rel)
	}
	if w.Draft {
		name, err := s.newBranch(ctx, p.Slug, LangpackBranchPrefix+strings.ToLower(cur.Locale)+"-"+s.o.Now().UTC().Format(time.DateOnly), cur.Commit)
		if err != nil {
			return PackCommit{}, nil, err
		}
		change.Branch = name
		c, err := s.o.Repos.Commit(ctx, p.Slug, change)
		if err != nil {
			_ = s.o.Repos.DeleteBranch(context.WithoutCancel(ctx), p.Slug, name, "")
			return PackCommit{}, nil, repoProblem(err)
		}
		out.Branch, out.Commit = name, c.SHA
		return out, RecipeEvents(p.ID, name, c.SHA, c.Changes), nil
	}
	c, err := s.o.Repos.Commit(ctx, p.Slug, change)
	if err != nil {
		return PackCommit{}, nil, repoProblem(err)
	}
	out.Branch, out.Commit = repos.Main, c.SHA
	if c.SHA == "" {
		return out, nil, nil
	}
	drafts := RecipeEvents(p.ID, repos.Main, c.SHA, c.Changes)
	more, err := s.mirror(ctx, tx, p)
	if err != nil {
		return PackCommit{}, nil, err
	}
	return out, append(drafts, more...), nil
}

func summary(w PackWrite) string {
	var parts []string
	for rel := range w.Files {
		parts = append(parts, "edit "+rel)
	}
	for _, rel := range w.Delete {
		parts = append(parts, "delete "+rel)
	}
	if len(parts) > 3 {
		return fmt.Sprintf("%d files", len(parts))
	}
	return strings.Join(parts, ", ")
}

// safeRel accepts a relative path inside a pack: no leading slash, no empty, . or .. segment.
func safeRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".git") {
			return false
		}
	}
	return path.Clean(rel) == rel
}

// newBranch creates a branch named name (name-2, name-3, … when taken) at from.
func (s *Service) newBranch(ctx context.Context, slug, name, from string) (string, error) {
	try := name
	for i := 2; ; i++ {
		if _, err := s.o.Repos.Resolve(ctx, slug, try); errors.Is(err, repos.ErrNotFound) {
			break
		}
		try = fmt.Sprintf("%s-%d", name, i)
	}
	if _, err := s.o.Repos.CreateBranch(ctx, slug, try, from); err != nil {
		return "", repoProblem(err)
	}
	return try, nil
}

// packUpdates chooses the language pack files a sync offers, three-way: rendered holds the starter pack files of
// the project's locales (lang/<locale>/…), and data.lock at base names the pack version the project last took.
//
//   - A file the project lacks is added when its whole pack is missing, or when the locked version did not have it
//     (Cadence added it since); a file the project deleted is not brought back.
//   - A file that differs is updated only when its content is one Cadence shipped (any registered version of the
//     pack's template): the project never edited it. A file the project edited stays as it is.
func (s *Service) packUpdates(ctx context.Context, q storage.Querier, p projects.Project, base string, rendered layout.Files) (layout.Files, error) {
	out := layout.Files{}
	if len(rendered) == 0 {
		return out, nil
	}
	_, tree, err := s.o.Repos.ListFiles(ctx, p.Slug, base, layout.LangDir)
	if err != nil {
		return nil, repoProblem(err)
	}
	have := map[string]bool{}
	for _, f := range tree {
		have[f.Path] = true
	}
	locked := s.lockedTemplates(ctx, p.Slug, base)
	for _, locale := range p.Locales {
		pack, ok := s.render.PackFor(locale)
		if !ok {
			continue
		}
		prefix := layout.LangDir + "/" + locale + "/"
		exists := false
		for f := range have {
			exists = exists || strings.HasPrefix(f, prefix)
		}
		shipped, took, err := s.shippedPack(ctx, q, pack, locked)
		if err != nil {
			return nil, err
		}
		for fp, b := range rendered {
			rel, ok := strings.CutPrefix(fp, prefix)
			if !ok {
				continue
			}
			if !have[fp] {
				if !exists || !took[rel] {
					out[fp] = b
				}
				continue
			}
			cur, _, err := s.o.Repos.ReadFile(ctx, p.Slug, base, fp)
			if err != nil {
				return nil, repoProblem(err)
			}
			if bytes.Equal(cur, b) {
				continue
			}
			sum := sha256.Sum256(cur)
			if shipped[rel][hex.EncodeToString(sum[:])] {
				out[fp] = b
			}
		}
	}
	return out, nil
}

// lockedTemplates reads the template versions data.lock names at commit: collection → version id.
func (s *Service) lockedTemplates(ctx context.Context, slug, commit string) map[string]string {
	out := map[string]string{}
	b, _, err := s.o.Repos.ReadFile(ctx, slug, commit, layout.DataLock)
	if err != nil {
		return out
	}
	var lock struct {
		Templates []struct {
			Collection string `yaml:"collection"`
			ID         string `yaml:"id"`
		} `yaml:"templates"`
	}
	if yaml.Unmarshal(b, &lock) != nil {
		return out
	}
	for _, t := range lock.Templates {
		out[t.Collection] = t.ID
	}
	return out
}

// shippedPack returns, per path inside the starter pack, the sha256 of every content Cadence registered for it, and
// the paths of the version the project took (locked: data.lock's template versions).
func (s *Service) shippedPack(ctx context.Context, q storage.Querier, pack string, locked map[string]string) (map[string]map[string]bool, map[string]bool, error) {
	shipped, took := map[string]map[string]bool{}, map[string]bool{}
	unit := path.Join(layout.LangDir, pack)
	name, ok := s.templates[unit]
	if !ok {
		return shipped, took, nil
	}
	versions, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindTemplate, Collection: name})
	if err != nil {
		return nil, nil, err
	}
	for _, v := range versions {
		var payload registry.TemplatePayload
		if err := jsonUnmarshal(v.Payload, &payload); err != nil {
			return nil, nil, err
		}
		for _, f := range payload.Files {
			rel := strings.TrimPrefix(f.Path, unit+"/")
			if shipped[rel] == nil {
				shipped[rel] = map[string]bool{}
			}
			shipped[rel][f.SHA256] = true
			if v.ID == locked[name] {
				took[rel] = true
			}
		}
	}
	return shipped, took, nil
}
