package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

func jsonUnmarshal(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- projects.new

// Wizard is the body of projects.new: every field but Name may be empty and takes its default from defaults.yaml.
type Wizard struct {
	Slug, Name, Description string
	Locales                 []string
	Domain                  string
	BaseModel               string // ver_… or a base-model collection name
	Driver, Model           string
	PermissionPreset        string
	InstructionsTemplate    string
	Repository              RepoArgs
	Private                 *bool
	GPUHoursPerDay          *float64
	AgentTokensPerDay       *int64
}

// Plan is a wizard with every default applied and every choice checked.
type Plan struct {
	Project    projects.NewInput
	Profile    projects.AgentProfile
	Repository RepoArgs
}

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// SlugFrom derives a slug from a project name: lower case, runs of other characters become one dash.
func SlugFrom(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s != "" && (s[0] < 'a' || s[0] > 'z') {
		s = "p-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// PlanProject applies the defaults to a wizard and checks the result: base model frozen, preset, instructions
// template, driver and model, repository and its secret.
func (s *Service) PlanProject(ctx context.Context, q storage.Querier, w Wizard) (Plan, error) {
	d := defaults.Get()
	var fields []problems.FieldError
	bad := func(path, format string, a ...any) {
		fields = append(fields, problems.FieldError{Path: path, Message: fmt.Sprintf(format, a...)})
	}
	slug := w.Slug
	if slug == "" {
		slug = SlugFrom(w.Name)
	}
	if !slugRe.MatchString(slug) {
		bad("slug", "cannot derive a slug from %q; send one (lowercase letters, digits and dashes, 3–40 characters)", w.Name)
	}
	locales := w.Locales
	if len(locales) == 0 {
		locales = []string{d.Wizard.Locale.Value}
	}
	domain := w.Domain
	if domain == "" {
		domain = d.Wizard.Domain.Value
	}
	budgets := projects.DefaultBudgets()
	if w.GPUHoursPerDay != nil {
		budgets.GPUHoursPerDay = *w.GPUHoursPerDay
	}
	if w.AgentTokensPerDay != nil {
		budgets.AgentTokensPerDay = *w.AgentTokensPerDay
	}
	profile := projects.AgentProfile{
		Driver: or(w.Driver, d.Wizard.Driver.Value), PermissionPreset: or(w.PermissionPreset, d.Wizard.PermissionPreset.Value),
		InstructionsTemplate: or(w.InstructionsTemplate, d.Wizard.InstructionsTemplate.Value),
		AutoMerge:            d.Wizard.AutoMerge.Value, DraftPolicy: d.Wizard.DraftPolicy.Value,
	}
	profile.Model = or(w.Model, d.Wizard.ModelFor(profile.Driver).Value)
	for _, check := range []error{CheckModel(profile.Driver, profile.Model), s.CheckPreset(profile.PermissionPreset),
		s.CheckInstructions(profile.InstructionsTemplate, false)} {
		var pe *problems.Error
		if errors.As(check, &pe) && pe.Type == problems.ValidationFailed {
			fields = append(fields, pe.Errors...)
		} else if check != nil {
			return Plan{}, check
		}
	}
	ref := or(w.BaseModel, d.Wizard.BaseModel.Value)
	base, err := registry.Resolve(ctx, q, "", registry.KindBaseModel, ref)
	var pe *problems.Error
	switch {
	case errors.As(err, &pe) && pe.Type == problems.NotFound:
		bad("baseModel", "no frozen base model %q in the registry (baseModels.list)", ref)
	case err != nil:
		return Plan{}, err
	case base.State != registry.StateFrozen:
		bad("baseModel", "%s %s is %s; choose a frozen version", base.Name, base.Version, base.State)
	}
	repo := w.Repository
	repo.Kind = or(repo.Kind, d.Wizard.Repository.Value)
	if w.Private == nil {
		repo.Private = true
	} else {
		repo.Private = *w.Private
	}
	remote := ""
	switch repo.Kind {
	case projects.RepoInternal:
		if repo.URL != "" || repo.Secret != "" {
			bad("repository", "the internal repository takes no url or secret")
		}
	case projects.RepoURL:
		if err := s.checkLinkURL(repo.URL); err != nil {
			bad("repository.url", "%v", err)
		}
		remote = repo.URL
	case projects.RepoGitHub:
		if repo.Secret == "" {
			bad("repository.secret", "a GitHub repository is created with a stored token: name the secret (secrets.new, kind github)")
		}
	default:
		bad("repository.kind", "unknown repository kind %q", repo.Kind)
	}
	if repo.Secret != "" {
		var exists bool
		if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM secrets WHERE name = $1)", repo.Secret).Scan(&exists); err != nil {
			return Plan{}, fmt.Errorf("check secret: %w", err)
		}
		if !exists {
			bad("repository.secret", "no stored secret named %q (secrets.list)", repo.Secret)
		}
	}
	if len(fields) > 0 {
		e := problems.Validation(fields)
		e.Detail = "the wizard's choices do not check out: " + fields[0].Path + ": " + fields[0].Message
		return Plan{}, e
	}
	if slugRe.MatchString(slug) && s.o.Repos.Exists(slug) {
		return Plan{}, problems.Conflict.New("a repository for slug %q already exists on the server; pick another slug", slug)
	}
	return Plan{
		Project: projects.NewInput{Slug: slug, Name: w.Name, Description: w.Description, Locales: locales, Domain: domain,
			BaseModelVersionID: base.ID, RepoKind: repo.Kind, Remote: remote, Secret: repo.Secret, Budgets: budgets,
			State: projects.StateBootstrapping},
		Profile: profile, Repository: repo,
	}, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Service) checkLinkURL(raw string) error {
	if raw == "" {
		return errors.New("name the repository to link")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.User != nil {
		return errors.New("put credentials in a stored secret, not in the URL")
	}
	switch {
	case u.Scheme == "https" && u.Host != "":
		return nil
	case s.o.AllowLocalRemotes && (u.Scheme == "file" || u.Scheme == ""):
		return nil
	}
	return errors.New("link an https:// repository")
}

// Created is what projects.new wrote besides the project row: the agent profile and the base model adoption.
func (s *Service) Created(ctx context.Context, tx pgx.Tx, p projects.Project, plan Plan, actor auth.Actor) error {
	a := plan.Profile
	a.ProjectID = p.ID
	if _, err := projects.CreateAgentProfile(ctx, tx, a); err != nil {
		return err
	}
	if plan.Project.BaseModelVersionID != "" {
		if _, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{plan.Project.BaseModelVersionID}, actor); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- facts: projects.edit and adoptions

// CommitFacts re-renders the files the project's facts appear in (project.yaml, AGENTS.md unless custom,
// data.lock) and commits what changed to main. Projects without a repository are skipped; in a dry run nothing is
// committed.
func (s *Service) CommitFacts(ctx context.Context, tx pgx.Tx, p projects.Project, actor auth.Actor, message string, dryRun bool) ([]events.Draft, error) {
	if dryRun || writable(p) != nil {
		return nil, nil
	}
	a, err := projects.GetAgentProfile(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	f, _, err := s.facts(ctx, tx, p, a)
	if err != nil {
		return nil, err
	}
	files, err := s.render.Render(f)
	if err != nil {
		return nil, err
	}
	_, drafts, err := s.commitMain(ctx, tx, p, actor, message, files.Pick(layout.ProjectYAML, layout.AgentsMD, layout.DataLock))
	return drafts, err
}

// ---------------------------------------------------------------- agent profile

// ProfileFiles are the files an agent profile renders, as committed on main (rendered when the project has no
// repository yet).
var ProfileFiles = []string{layout.ClaudeSettings, layout.OpencodeJSON, layout.AgentsMD, layout.ClaudeMD}

// Files returns the committed profile files of a project (or, before the bootstrap wrote them, the rendering).
func (s *Service) Files(ctx context.Context, q storage.Querier, p projects.Project, a projects.AgentProfile) (layout.Files, error) {
	out := layout.Files{}
	if p.Repository != nil && s.o.Repos.Exists(p.Slug) {
		for _, path := range ProfileFiles {
			if b, _, err := s.o.Repos.ReadFile(ctx, p.Slug, repos.Main, path); err == nil {
				out[path] = b
			}
		}
		if len(out) == len(ProfileFiles) {
			return out, nil
		}
	}
	f, _, err := s.facts(ctx, q, p, a)
	if err != nil {
		return nil, err
	}
	rendered, err := s.render.Render(f)
	if err != nil {
		return nil, err
	}
	for p, b := range rendered.Pick(ProfileFiles...) {
		if _, ok := out[p]; !ok {
			out[p] = b
		}
	}
	return out, nil
}

// EditProfile applies an agentProfile.edit to the profile at revision rev: it re-renders the agent config files,
// AGENTS.md (unless custom; agentsMd, when set, is written as is and makes the instructions custom), CLAUDE.md,
// project.yaml and data.lock, commits them to main unless dryRun, and saves the profile at the next revision.
func (s *Service) EditProfile(ctx context.Context, tx pgx.Tx, p projects.Project, rev int, edit projects.ProfileEdit,
	agentsMd *string, actor auth.Actor, dryRun bool) (projects.AgentProfile, layout.Files, []events.Draft, error) {
	if err := writable(p); err != nil {
		return projects.AgentProfile{}, nil, nil, err
	}
	cur, err := projects.LockAgentProfile(ctx, tx, p.ID, rev)
	if err != nil {
		return projects.AgentProfile{}, nil, nil, err
	}
	next := edit.Apply(cur)
	if agentsMd != nil {
		next.InstructionsTemplate = layout.CustomInstructions
	}
	if edit.Driver != nil && edit.Model == nil && *edit.Driver != cur.Driver {
		next.Model = defaults.Get().Wizard.ModelFor(next.Driver).Value // a new driver starts from its default model
	}
	if err := errors.Join(CheckModel(next.Driver, next.Model), s.CheckPreset(next.PermissionPreset),
		s.CheckInstructions(next.InstructionsTemplate, agentsMd != nil || cur.InstructionsTemplate == layout.CustomInstructions)); err != nil {
		var pe *problems.Error
		if errors.As(err, &pe) {
			return projects.AgentProfile{}, nil, nil, pe
		}
		return projects.AgentProfile{}, nil, nil, err
	}
	f, ids, err := s.facts(ctx, tx, p, next)
	if err != nil {
		return projects.AgentProfile{}, nil, nil, err
	}
	rendered, err := s.render.Render(f)
	if err != nil {
		return projects.AgentProfile{}, nil, nil, err
	}
	files := rendered.Pick(append([]string{layout.ProjectYAML, layout.DataLock}, ProfileFiles...)...)
	if agentsMd != nil {
		files[layout.AgentsMD] = []byte(*agentsMd)
	}
	var (
		commit string
		drafts []events.Draft
	)
	if !dryRun {
		c, d, err := s.commitMain(ctx, tx, p, actor, "agent profile: "+profileSummary(cur, next, agentsMd != nil), files)
		if err != nil {
			return projects.AgentProfile{}, nil, nil, err
		}
		commit, drafts = c.SHA, d
		if _, err := registry.AdoptQuietly(ctx, tx, p.ID, ids, actor); err != nil {
			return projects.AgentProfile{}, nil, nil, err
		}
	}
	saved, more, err := projects.SaveAgentProfile(ctx, tx, next, commit)
	if err != nil {
		return projects.AgentProfile{}, nil, nil, err
	}
	out := files.Pick(ProfileFiles...)
	if _, ok := out[layout.AgentsMD]; !ok {
		if b, _, err := s.o.Repos.ReadFile(ctx, p.Slug, repos.Main, layout.AgentsMD); err == nil {
			out[layout.AgentsMD] = b
		}
	}
	return saved, out, append(more, drafts...), nil
}

func profileSummary(cur, next projects.AgentProfile, agents bool) string {
	var parts []string
	if cur.Driver != next.Driver || cur.Model != next.Model {
		parts = append(parts, next.Driver+" "+next.Model)
	}
	if cur.PermissionPreset != next.PermissionPreset {
		parts = append(parts, "preset "+next.PermissionPreset)
	}
	if agents {
		parts = append(parts, "AGENTS.md edited")
	} else if cur.InstructionsTemplate != next.InstructionsTemplate {
		parts = append(parts, "instructions "+next.InstructionsTemplate)
	}
	if cur.AutoMerge != next.AutoMerge {
		parts = append(parts, "auto-merge "+next.AutoMerge)
	}
	if len(parts) == 0 {
		return "re-rendered"
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------- notes

// Note is a learning filed in NOTES.md.
type Note struct {
	Date   string
	Text   string
	Commit string
}

// AddNote appends text to NOTES.md under today's date and commits it (not in a dry run).
func (s *Service) AddNote(ctx context.Context, tx pgx.Tx, p projects.Project, text string, actor auth.Actor, dryRun bool) (Note, []events.Draft, error) {
	if err := writable(p); err != nil {
		return Note{}, nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Note{}, nil, problems.Validation([]problems.FieldError{{Path: "text", Message: "the note is empty"}})
	}
	day := s.o.Now().UTC()
	n := Note{Date: day.Format(time.DateOnly), Text: text}
	if dryRun {
		return n, nil, nil
	}
	cur, _, err := s.o.Repos.ReadFile(ctx, p.Slug, repos.Main, layout.NotesMD)
	if err != nil && !errors.Is(err, repos.ErrNotFound) {
		return Note{}, nil, repoProblem(err)
	}
	subject, _, _ := strings.Cut(text, "\n")
	if len(subject) > 60 {
		subject = subject[:60] + "…"
	}
	c, drafts, err := s.commitMain(ctx, tx, p, actor, "note: "+subject,
		layout.Files{layout.NotesMD: layout.AppendNote(cur, day, text)})
	if err != nil {
		return Note{}, nil, err
	}
	n.Commit = c.SHA
	return n, drafts, nil
}

// ---------------------------------------------------------------- template sync

// SyncResult is what projects.sync found.
type SyncResult struct {
	UpToDate bool
	Base     string
	Branch   string
	Commit   string
	Changes  []repos.FileChange
}

// templateOwned are the files a sync re-renders; .claude/skills is replaced as a whole (a skill Cadence dropped
// goes too), pipelines only file by file (a project's own pipelines stay).
var templateOwned = []string{layout.AgentsMD, layout.ClaudeMD, layout.DataLock, layout.ClaudeSettings, layout.OpencodeJSON,
	layout.SkillsDir + "/", layout.PipelinesDir + "/"}

// Sync re-renders the template-owned files with the current template versions and, when anything differs from
// main, commits the update on a new branch sync/<date> (a draft to accept with branches.accept). A dry run only
// reports the changes.
func (s *Service) Sync(ctx context.Context, tx pgx.Tx, p projects.Project, actor auth.Actor, dryRun bool) (SyncResult, error) {
	if err := writable(p); err != nil {
		return SyncResult{}, err
	}
	a, err := projects.GetAgentProfile(ctx, tx, p.ID)
	if err != nil {
		return SyncResult{}, err
	}
	f, _, err := s.facts(ctx, tx, p, a)
	if err != nil {
		return SyncResult{}, err
	}
	rendered, err := s.render.Render(f)
	if err != nil {
		return SyncResult{}, err
	}
	files := rendered.Pick(templateOwned...)
	base, err := s.o.Repos.Head(ctx, p.Slug)
	if err != nil {
		return SyncResult{}, repoProblem(err)
	}
	changes, err := s.diffWith(ctx, p.Slug, base, files)
	if err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{UpToDate: len(changes) == 0, Base: base, Changes: changes}
	if res.UpToDate || dryRun {
		return res, nil
	}
	name := repos.SyncPrefix + s.o.Now().UTC().Format(time.DateOnly)
	for i := 2; ; i++ {
		if _, err := s.o.Repos.Resolve(ctx, p.Slug, name); errors.Is(err, repos.ErrNotFound) {
			break
		}
		name = fmt.Sprintf("%s%s-%d", repos.SyncPrefix, s.o.Now().UTC().Format(time.DateOnly), i)
	}
	if _, err := s.o.Repos.CreateBranch(ctx, p.Slug, name, base); err != nil {
		return SyncResult{}, repoProblem(err)
	}
	c, err := s.o.Repos.Commit(ctx, p.Slug, repos.Change{Branch: name, Author: Signature(actor),
		Message: "sync templates and skills with Cadence", Files: files, Replace: []string{layout.SkillsDir}})
	if err != nil {
		_ = s.o.Repos.DeleteBranch(context.WithoutCancel(ctx), p.Slug, name, "")
		return SyncResult{}, repoProblem(err)
	}
	res.Branch, res.Commit, res.Changes = name, c.SHA, c.Changes
	return res, nil
}

// diffWith lists how files differ from the tree at base: added, modified and, under .claude/skills, deleted.
func (s *Service) diffWith(ctx context.Context, slug, base string, files layout.Files) ([]repos.FileChange, error) {
	_, tree, err := s.o.Repos.ListFiles(ctx, slug, base, "")
	if err != nil {
		return nil, repoProblem(err)
	}
	have := map[string]bool{}
	for _, f := range tree {
		have[f.Path] = true
	}
	var out []repos.FileChange
	for _, path := range files.Paths() {
		if !have[path] {
			out = append(out, repos.FileChange{Path: path, Status: repos.Added})
			continue
		}
		cur, _, err := s.o.Repos.ReadFile(ctx, slug, base, path)
		if err != nil {
			return nil, repoProblem(err)
		}
		if !bytes.Equal(cur, files[path]) {
			out = append(out, repos.FileChange{Path: path, Status: repos.Modified})
		}
	}
	for _, f := range tree {
		if strings.HasPrefix(f.Path, layout.SkillsDir+"/") && files[f.Path] == nil {
			out = append(out, repos.FileChange{Path: f.Path, Status: repos.Deleted})
		}
	}
	slices.SortFunc(out, func(a, b repos.FileChange) int { return strings.Compare(a.Path, b.Path) })
	if out == nil {
		out = []repos.FileChange{}
	}
	return out, nil
}

// ---------------------------------------------------------------- branches

// Merge merges a branch into main as actor — the primitive behind branches.accept and, for session branches,
// agentSessions.accept: fast-forward when main has not moved, else a merge commit; *repos.ConflictError leaves main
// as it was (merge-conflict). It then adopts the registry versions the merged data.lock names, mirrors main to the
// remote and returns the recipe.{path} events of what main gained. expect, when set, is the branch head the caller
// reviewed.
func (s *Service) Merge(ctx context.Context, tx pgx.Tx, p projects.Project, branch, expect string, actor auth.Actor) (repos.Merge, []events.Draft, error) {
	if err := writable(p); err != nil {
		return repos.Merge{}, nil, err
	}
	m, err := s.o.Repos.MergeBranch(ctx, p.Slug, branch, expect, Signature(actor), "Merge branch '"+branch+"'")
	if err != nil {
		return repos.Merge{}, nil, repoProblem(err)
	}
	drafts := RecipeEvents(p.ID, repos.Main, m.Main, m.Changes)
	if m.Main != m.Before {
		if err := s.adoptLocked(ctx, tx, p, m.Main, actor); err != nil {
			return repos.Merge{}, nil, err
		}
		more, err := s.mirror(ctx, tx, p)
		if err != nil {
			return repos.Merge{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	return m, drafts, nil
}

// adoptLocked adopts every registry version data.lock names at commit.
func (s *Service) adoptLocked(ctx context.Context, tx pgx.Tx, p projects.Project, commit string, actor auth.Actor) error {
	b, _, err := s.o.Repos.ReadFile(ctx, p.Slug, commit, layout.DataLock)
	if err != nil {
		return nil //nolint:nilerr // a repository without data.lock pins nothing
	}
	var lock struct {
		Resolved  []struct{ ID string } `yaml:"resolved"`
		Templates []struct{ ID string } `yaml:"templates"`
	}
	if err := yaml.Unmarshal(b, &lock); err != nil {
		return nil //nolint:nilerr // a hand-broken data.lock is shown in the Recipe panel, not fatal to a merge
	}
	var ids []string
	for _, l := range append(lock.Resolved, lock.Templates...) {
		if strings.HasPrefix(l.ID, "ver_") {
			ids = append(ids, l.ID)
		}
	}
	_, err = registry.AdoptQuietly(ctx, tx, p.ID, ids, actor)
	return err
}

// CheckDraftBranch fails for branches branches.accept and branches.revert do not handle.
func CheckDraftBranch(name string) error {
	switch {
	case name == repos.Main:
		return problems.Conflict.New("main is the project branch; it is not merged or discarded")
	case repos.BranchKind(name) == "session":
		return problems.Conflict.New("%s is an agent session's branch: accept or discard it with agentSessions.accept or agentSessions.revert", name)
	}
	return nil
}

// Discard deletes a branch whose head is expect (when set).
func (s *Service) Discard(ctx context.Context, p projects.Project, branch, expect string) (repos.Branch, error) {
	if err := writable(p); err != nil {
		return repos.Branch{}, err
	}
	b, err := s.o.Repos.Branch(ctx, p.Slug, branch)
	if err != nil {
		return repos.Branch{}, repoProblem(err)
	}
	if expect != "" && expect != b.Head {
		return repos.Branch{}, problems.PreconditionFailed.New("branch %s is at %s, not %s; re-read it (branches.get)", branch, b.Head, expect)
	}
	return b, repoProblem(s.o.Repos.DeleteBranch(ctx, p.Slug, branch, b.Head))
}

// ---------------------------------------------------------------- archive, pushes, retention

// Archived removes an archived project's working clone and worktrees; the bare repository stays, read-only.
func (s *Service) Archived(p projects.Project) error {
	if p.Repository == nil {
		return nil
	}
	return s.o.Repos.RemoveWorkingState(p.Slug)
}

// Pushed turns a push from outside into events: recipe.{path} for every file each moved branch changed, as the
// pusher; a moved main is mirrored to the project's remote.
func (s *Service) Pushed(ctx context.Context, slug string, actor auth.Actor, moved map[string][2]string) error {
	p, err := projects.Get(ctx, s.o.Pool, slug)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.o.Pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		names := make([]string, 0, len(moved))
		for n := range moved {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, name := range names {
			m := moved[name]
			if m[1] == "" {
				continue // a deleted branch changes no file
			}
			changes, err := s.o.Repos.Changes(ctx, slug, m[0], m[1])
			if err != nil {
				return err
			}
			if m[0] == "" && name != repos.Main {
				// A new branch: what it changes against main, not against nothing.
				if head, err := s.o.Repos.Head(ctx, slug); err == nil && head != "" {
					if base, err := s.o.Repos.Changes(ctx, slug, head, m[1]); err == nil {
						changes = base
					}
				}
			}
			drafts = append(drafts, RecipeEvents(p.ID, name, m[1], changes)...)
			if name == repos.Main {
				more, err := s.mirror(ctx, tx, p)
				if err != nil {
					return err
				}
				drafts = append(drafts, more...)
			}
		}
		return events.Append(ctx, tx, actor, nil, drafts)
	})
}

// PruneBranches deletes, in every project, the branches merged more than repos.BranchRetention ago.
func (s *Service) PruneBranches(ctx context.Context) (int, error) {
	list, err := projects.List(ctx, s.o.Pool, true)
	if err != nil {
		return 0, err
	}
	n := 0
	cutoff := s.o.Now().Add(-repos.BranchRetention)
	for _, p := range list {
		if p.Repository == nil || !s.o.Repos.Exists(p.Slug) {
			continue
		}
		deleted, err := s.o.Repos.PruneMerged(ctx, p.Slug, cutoff)
		if err != nil {
			return n, fmt.Errorf("prune %s: %w", p.Slug, err)
		}
		n += len(deleted)
	}
	return n, nil
}
