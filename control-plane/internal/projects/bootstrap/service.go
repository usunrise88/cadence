// Package bootstrap sets up and keeps a project's repository: the bootstrap job that projects.new queues (create
// or link the repository, render and commit the templates, adopt the versions data.lock names, create the default
// workspaces), and the commands that commit to the repository afterwards — agent profile changes, facts from
// projects.edit, notes, template syncs, merges of draft branches, pushes from outside, archive and the merged-branch
// retention (docs/spec/02-domain-projects-registry.md "Project wizard"; docs/spec/08-resolutions.md R1, R7, R10).
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/layout"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// Options wire a Service.
type Options struct {
	Pool    *pgxpool.Pool
	Repos   *repos.Store
	Secrets *secrets.Store // reads tokens of github and private url repositories; nil refuses them
	GitHub  repos.GitHub
	Log     *slog.Logger
	// Templates is the templates tree; the embedded one when nil.
	Templates fs.FS
	// AllowLocalRemotes accepts file:// and path remotes for the url repository kind (tests). Production links
	// https repositories only: a local path would copy the server's own files into a project.
	AllowLocalRemotes bool
	Now               func() time.Time
}

// Service owns project repositories on behalf of the API and the job runner.
type Service struct {
	o         Options
	render    layout.Renderer
	templates map[string]string // unit path in the templates tree → registry collection name
}

// New returns a service over the embedded presets and the MCP tool manifest (the permission rules render from it).
func New(o Options) (*Service, error) {
	if o.Templates == nil {
		o.Templates = templates.FS
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	presets, err := policy.EmbeddedPresets()
	if err != nil {
		return nil, err
	}
	tools, err := Tools()
	if err != nil {
		return nil, err
	}
	inputs, err := registry.TemplateInputs(o.Templates)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, in := range inputs {
		var p registry.TemplatePayload
		if err := jsonUnmarshal(in.Payload, &p); err != nil {
			return nil, err
		}
		names[p.Path] = in.Name
	}
	return &Service{o: o, render: layout.Renderer{Tree: o.Templates, Presets: presets, Tools: tools}, templates: names}, nil
}

// SetSecrets sets the secret store after construction (tests build it together with the server); call it before
// any job runs.
func (s *Service) SetSecrets(st *secrets.Store) { s.o.Secrets = st }

// SetGitHub replaces the GitHub client (tests point it at a fake API); call it before any job runs.
func (s *Service) SetGitHub(g repos.GitHub) { s.o.GitHub = g }

// Repos is the repository store.
func (s *Service) Repos() *repos.Store { return s.o.Repos }

// Renderer renders repository files.
func (s *Service) Renderer() layout.Renderer { return s.render }

// Tools lists the MCP tools with their verb class, as the permission renderer needs them.
func Tools() ([]policy.Operation, error) {
	m, err := mcp.LoadManifest()
	if err != nil {
		return nil, err
	}
	ops := make([]policy.Operation, 0, len(m.Tools))
	for _, t := range m.Tools {
		vc := "mutate"
		if t.Annotations.ReadOnlyHint {
			vc = "read"
		}
		ops = append(ops, policy.Operation{Name: t.Name, VerbClass: vc})
	}
	return ops, nil
}

// ---------------------------------------------------------------- validation of wizard and profile choices

// CheckPreset fails unless name is a permission preset.
func (s *Service) CheckPreset(name string) error {
	if _, ok := s.render.Presets[name]; !ok {
		return problems.Validation([]problems.FieldError{{Path: "permissionPreset",
			Message: fmt.Sprintf("no permission preset %q; templates.list?templateKind=preset lists them", name)}})
	}
	return nil
}

// CheckInstructions fails unless name is an instructions template (custom is set only by writing AGENTS.md).
func (s *Service) CheckInstructions(name string, customOK bool) error {
	if name == layout.CustomInstructions && customOK {
		return nil
	}
	names, err := s.render.Instructions()
	if err != nil {
		return err
	}
	if !slices.Contains(names, name) {
		return problems.Validation([]problems.FieldError{{Path: "instructionsTemplate",
			Message: fmt.Sprintf("no instructions template %q (have %s); custom is set by editing AGENTS.md", name, strings.Join(names, ", "))}})
	}
	return nil
}

// CheckModel fails unless model suits driver: a Claude Code alias or claude-* id, or opencode's provider/model.
func CheckModel(driver, model string) error {
	bad := func(msg string) error {
		return problems.Validation([]problems.FieldError{{Path: "model", Message: msg}})
	}
	switch driver {
	case projects.DriverClaudeCode:
		w := defaults.Get().Wizard.ClaudeCodeModel
		if !w.Range.Allows(model) && !strings.HasPrefix(model, "claude-") {
			return bad(fmt.Sprintf("%q is not a Claude Code model: use an alias (%s) or a claude-… model id", model, strings.Join(w.Range.Values, ", ")))
		}
	case projects.DriverOpencode:
		if prov, name, ok := strings.Cut(model, "/"); !ok || prov == "" || name == "" {
			return bad(fmt.Sprintf("%q is not an opencode model: use provider/model, e.g. %s", model, defaults.Get().Wizard.OpencodeModel.Value))
		}
	default:
		return problems.Validation([]problems.FieldError{{Path: "driver", Message: fmt.Sprintf("no agent driver %q", driver)}})
	}
	return nil
}

// ---------------------------------------------------------------- facts

// facts gathers what the templates render from; ids are the template versions the rendered files come from.
func (s *Service) facts(ctx context.Context, q storage.Querier, p projects.Project, a projects.AgentProfile) (layout.Facts, []string, error) {
	f := layout.Facts{
		Name: p.Name, Slug: p.Slug, Description: p.Description, Locales: p.Locales, Domain: p.Domain,
		Budgets: layout.Budgets{GPUHoursPerDay: p.Budgets.GPUHoursPerDay, AgentTokensPerDay: p.Budgets.AgentTokensPerDay},
		Agent: layout.Agent{Driver: a.Driver, Model: a.Model, PermissionPreset: a.PermissionPreset,
			InstructionsTemplate: a.InstructionsTemplate, AutoMerge: a.AutoMerge, DraftPolicy: a.DraftPolicy},
		Lock: []layout.Locked{}, Templates: []layout.Locked{},
	}
	f.Agent.OpencodeModel = defaults.Get().Wizard.OpencodeModel.Value
	if a.Driver == projects.DriverOpencode {
		f.Agent.OpencodeModel = a.Model
	}
	if p.Repository != nil {
		f.Repo = layout.Repo{Kind: p.Repository.Kind, CloneURL: p.Repository.CloneURL, Remote: p.Repository.Remote}
	}
	if p.BaseModel != nil {
		v, err := registry.GetVersion(ctx, q, registry.KindBaseModel, p.BaseModel.VersionID)
		if err != nil {
			return layout.Facts{}, nil, err
		}
		var payload struct {
			HFRepo   string `json:"hfRepo"`
			Revision string `json:"revision"`
			Licence  string `json:"licence"`
		}
		if err := jsonUnmarshal(v.Payload, &payload); err != nil {
			return layout.Facts{}, nil, err
		}
		f.BaseModel = layout.BaseModel{VersionID: v.ID, Collection: v.Name, Version: v.Version, Repo: payload.HFRepo,
			Revision: payload.Revision, Licence: payload.Licence}
	}
	adopted, err := registry.ListAdoptions(ctx, q, p.ID, "")
	if err != nil {
		return layout.Facts{}, nil, err
	}
	for _, ad := range adopted {
		if ad.Version.Kind == registry.KindTemplate {
			continue
		}
		f.Lock = append(f.Lock, layout.Locked{Kind: ad.Version.Kind, Collection: ad.Version.Name, Version: ad.Version.Version, ID: ad.Version.ID})
	}
	var ids []string
	for _, name := range s.templateCollections(a) {
		v, err := registry.Latest(ctx, q, registry.KindTemplate, name)
		var pe *problems.Error
		if errors.As(err, &pe) && pe.Type == problems.NotFound {
			continue // a registry that was never seeded (the bundled templates register at start)
		}
		if err != nil {
			return layout.Facts{}, nil, err
		}
		f.Templates = append(f.Templates, layout.Locked{Kind: v.Kind, Collection: v.Name, Version: v.Version, ID: v.ID})
		ids = append(ids, v.ID)
	}
	return f, ids, nil
}

// templateCollections names the registry template collections a project's files come from.
func (s *Service) templateCollections(a projects.AgentProfile) []string {
	var out []string
	for path, name := range s.templates {
		dir, rest, _ := strings.Cut(path, "/")
		switch {
		case dir == "skills", dir == "pipelines", dir == "agent-config":
		case dir == "presets" && strings.TrimSuffix(rest, ".yaml") == a.PermissionPreset:
		case dir == "instructions" && rest == a.InstructionsTemplate:
		default:
			continue
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// ---------------------------------------------------------------- commits, mirroring and events

// Signature is the git author of an actor's change.
func Signature(a auth.Actor) repos.Signature {
	name := a.Name
	if name == "" {
		name = a.ID
	}
	if a.Kind == auth.KindAgent && a.SessionID != "" {
		name += " (session " + a.SessionID + ")"
	}
	return repos.Signature{Name: name, Email: a.ID + "@cadence.local"}
}

// remote returns the project's remote with its token, or ok=false for an internal repository.
func (s *Service) remote(ctx context.Context, p projects.Project) (repos.Remote, bool, error) {
	if p.Repository == nil || p.Repository.Remote == "" {
		return repos.Remote{}, false, nil
	}
	r := repos.Remote{URL: p.Repository.Remote}
	if p.Repository.Secret != "" {
		tok, err := s.token(ctx, p.Repository.Secret)
		if err != nil {
			return repos.Remote{}, true, err
		}
		r.Token = tok
	}
	return r, true, nil
}

func (s *Service) token(ctx context.Context, name string) (string, error) {
	if s.o.Secrets == nil {
		return "", errors.New("the secret store is not configured")
	}
	b, err := s.o.Secrets.Read(ctx, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// mirror pushes main to the project's remote, if it has one, and records the outcome on the project (an event
// when it changes). Push failures never fail the command: main here is the truth, the remote catches up.
func (s *Service) mirror(ctx context.Context, tx pgx.Tx, p projects.Project) ([]events.Draft, error) {
	r, ok, err := s.remote(ctx, p)
	if !ok {
		return nil, nil
	}
	if err == nil {
		err = s.o.Repos.PushRemote(ctx, p.Slug, r)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
		s.o.Log.WarnContext(ctx, "push to the project's remote failed", "project", p.Slug, "err", err)
	}
	_, drafts, err := projects.SetPushError(ctx, tx, p.ID, msg)
	return drafts, err
}

// RecipeEvents are the recipe.{path} events of one commit (docs/spec/06-platform.md "Topic scheme").
func RecipeEvents(projectID, branch, commit string, changes []repos.FileChange) []events.Draft {
	out := make([]events.Draft, 0, len(changes))
	for _, c := range changes {
		out = append(out, events.Draft{
			Topic: "recipe." + c.Path, Type: "recipe.changed", ProjectID: projectID,
			Payload: map[string]any{"path": c.Path, "branch": branch, "commit": commit, "status": c.Status},
		})
	}
	return out
}

// commitMain commits files to main as actor, mirrors to the remote and returns the commit with its events.
func (s *Service) commitMain(ctx context.Context, tx pgx.Tx, p projects.Project, actor auth.Actor, message string,
	files layout.Files, replace ...string) (repos.Commit, []events.Draft, error) {
	c, err := s.o.Repos.Commit(ctx, p.Slug, repos.Change{Author: Signature(actor), Message: message, Files: files, Replace: replace})
	if err != nil {
		return repos.Commit{}, nil, repoProblem(err)
	}
	if c.SHA == "" {
		return c, nil, nil
	}
	drafts := RecipeEvents(p.ID, repos.Main, c.SHA, c.Changes)
	more, err := s.mirror(ctx, tx, p)
	if err != nil {
		return c, nil, err
	}
	return c, append(drafts, more...), nil
}

// writable fails unless the project has a repository it may commit to.
func writable(p projects.Project) error {
	switch {
	case p.Archived():
		return problems.Conflict.New("project %q is archived; its repository is read-only", p.Slug)
	case p.Repository == nil:
		return problems.Conflict.New("project %q has no repository (it was created before the project wizard)", p.Slug)
	case p.State == projects.StateBootstrapping:
		return problems.Conflict.New("project %q is still bootstrapping; wait for job %s (jobs.wait)", p.Slug, p.BootstrapJobID)
	case p.State == projects.StateFailed:
		return problems.Conflict.New("project %q failed to bootstrap: %s", p.Slug, p.BootstrapError)
	}
	return nil
}

// repoProblem maps repository errors to problems: missing things are not-found, a moved branch is
// precondition-failed, a conflict is merge-conflict, anything else repository-unavailable.
func repoProblem(err error) error {
	var ce *repos.ConflictError
	var pe *problems.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return err
	case errors.As(err, &ce):
		return problems.MergeConflict.New("%s: resolve it on the branch (or discard the branch) and try again", ce.Error())
	case errors.Is(err, repos.ErrNotFound):
		return problems.NotFound.New("%v", err)
	case errors.Is(err, repos.ErrStale):
		return problems.PreconditionFailed.New("%v; re-read the branch (branches.get) and retry", err)
	case errors.Is(err, repos.ErrExists):
		return problems.Conflict.New("%v", err)
	}
	return fmt.Errorf("%w: %w", problems.RepositoryUnavailable.New("the project repository could not be updated: %v", err), err)
}
