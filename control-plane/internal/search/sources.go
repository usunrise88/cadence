package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/experiments"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/mixes"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Document scopes: who may see a document (docs/spec/08-resolutions.md R1: a project token never sees other
// projects' work; registry hits need registry read; instance-wide work is the admin's; help is everyone's).
const (
	ScopeDocProject  = "project"
	ScopeDocRegistry = "registry"
	ScopeDocInstance = "instance"
	ScopeDocHelp     = "help"
)

// KindHelp is the kind of help article documents.
const KindHelp = "help_article"

// Document is one entry of the search index. Its JSON form is the contract's SearchHit (without the snippet).
type Document struct {
	Kind      string
	ID        string
	Scope     string // project | registry | instance | help
	ProjectID string // project work only
	Ref       string // what the UI opens: <kind>:<id>
	Title     string
	Text      string
	Tags      []string
	Status    string
	Lang      string
	Actor     *auth.Actor
	Numbers   map[string]float64
	UpdatedAt time.Time
}

// Loader returns the current documents of the entity an event names: usually one, sometimes more (a registry
// version also refreshes its collection). None means the entity is gone and its document is removed.
type Loader func(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error)

// Source registers one entity kind for indexing: events whose entity has this kind are loaded with Load.
type Source struct {
	Kind string
	// Aliases are extra spellings accepted by kind: (kind:dataset for dataset_version).
	Aliases []string
	Load    Loader
}

// Sources is the registration table of indexed kinds. A kind joins the index by adding a row here with a loader
// that reads the entity by id; its events (entity.<kind>.<id>) then feed the index, and kind:<kind> accepts it.
// Per-user state (workspaces, saved searches) and secrets-adjacent kinds (credentials, secrets) are never indexed.
func Sources() []Source {
	return []Source{
		{Kind: projects.Kind, Load: loadProject},
		{Kind: registry.KindBaseModel, Aliases: []string{"base-model", "basemodel"}, Load: loadVersion},
		{Kind: registry.KindDataset, Aliases: []string{"dataset", "dataset-version"}, Load: loadVersion},
		{Kind: registry.KindTemplate, Load: loadVersion},
		{Kind: registry.KindGoldenSet, Aliases: []string{"golden-set", "goldenset", "golden"}, Load: loadVersion},
		{Kind: registry.KindNormalizer, Load: loadVersion},
		{Kind: registry.KindModel, Load: loadVersion},
		{Kind: registry.CollectionKind, Aliases: []string{"collection"}, Load: loadCollection},
		{Kind: jobs.Kind, Load: loadJob},
		{Kind: approvals.Kind, Load: loadApproval},
		{Kind: mixes.Kind, Load: loadMix},
		{Kind: evals.Kind, Load: loadEval},
		{Kind: experiments.Kind, Load: loadExperiment},
	}
}

// Kinds maps every accepted kind: value to its kind, for Parse: the sources' kinds and aliases, plus help.
func Kinds(sources []Source) map[string]string {
	m := map[string]string{KindHelp: KindHelp, "help": KindHelp}
	for _, s := range sources {
		m[s.Kind] = s.Kind
		for _, a := range s.Aliases {
			m[a] = s.Kind
		}
	}
	return m
}

func isNotFound(err error) bool {
	var pe *problems.Error
	return errors.As(err, &pe) && pe.Type == problems.NotFound
}

func ref(kind, id string) string { return kind + ":" + id }

// langFromTags takes the locale of an entity from its first locale:<x> tag.
func langFromTags(tags []string) string {
	for _, t := range tags {
		if l, ok := strings.CutPrefix(t, "locale:"); ok {
			return l
		}
	}
	return ""
}

func loadProject(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	var (
		p        projects.Project
		archived *time.Time
	)
	err := q.QueryRow(ctx, `SELECT id, slug, name, description, archived_at, updated_at FROM projects WHERE id = $1`,
		ev.Entity.ID).Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &archived, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load project %s: %w", ev.Entity.ID, err)
	}
	status := "active"
	if archived != nil {
		status = "archived"
	}
	return []Document{{
		Kind: projects.Kind, ID: p.ID, Scope: ScopeDocProject, ProjectID: p.ID, Ref: ref(projects.Kind, p.Slug),
		Title: p.Name, Text: strings.TrimSpace(p.Slug + " " + p.Description), Status: status, UpdatedAt: p.UpdatedAt,
	}}, nil
}

func loadVersion(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{IDs: []string{ev.Entity.ID}})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	v := list[0]
	c, err := registry.GetCollection(ctx, q, v.CollectionID)
	if err != nil {
		return nil, err
	}
	actor := v.Actor
	doc := Document{
		Kind: v.Kind, ID: v.ID, Scope: ScopeDocRegistry, Ref: ref(v.Kind, v.ID),
		Title: v.Name + " " + v.Version,
		Text:  joinText(v.Name, v.Version, c.Description, v.Licence, v.Fingerprint, payloadText(v.Payload)),
		Tags:  v.Tags, Status: v.State, Lang: langFromTags(v.Tags), Actor: &actor,
		Numbers: payloadNumbers(v.Payload), UpdatedAt: v.UpdatedAt,
	}
	return []Document{doc, collectionDoc(c)}, nil
}

func loadCollection(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	c, err := registry.GetCollection(ctx, q, ev.Entity.ID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []Document{collectionDoc(c)}, nil
}

func collectionDoc(c registry.Collection) Document {
	d := Document{
		Kind: registry.CollectionKind, ID: c.ID, Scope: ScopeDocRegistry, Ref: ref(registry.CollectionKind, c.ID),
		Title: c.Name, Text: joinText(c.Name, c.Kind, c.Description, c.Licence), Tags: c.Tags,
		Lang: langFromTags(c.Tags), Numbers: map[string]float64{}, UpdatedAt: c.CreatedAt,
	}
	if c.Latest != nil {
		d.Status = c.Latest.State
		d.Text = joinText(d.Text, c.Latest.Version)
		if c.Latest.CreatedAt.After(d.UpdatedAt) {
			d.UpdatedAt = c.Latest.CreatedAt
		}
	}
	return d
}

func loadJob(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	j, err := jobs.Get(ctx, q, ev.Entity.ID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	actor := j.Actor
	d := Document{
		Kind: jobs.Kind, ID: j.ID, Scope: ScopeDocInstance, ProjectID: j.ProjectID, Ref: ref(jobs.Kind, j.ID),
		Title: j.Kind + " job", Text: joinText(j.ID, j.Kind, j.Message, j.Error), Status: j.State, Actor: &actor,
		Numbers: map[string]float64{"progress": j.Progress, "rev": float64(j.Rev)}, UpdatedAt: j.UpdatedAt,
	}
	if j.ProjectID != "" {
		d.Scope = ScopeDocProject
	}
	return []Document{d}, nil
}

func loadApproval(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	a, err := approvals.Get(ctx, q, ev.Entity.ID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	actor := a.Actor
	updated := a.CreatedAt
	if a.DecidedAt != nil {
		updated = *a.DecidedAt
	}
	d := Document{
		Kind: approvals.Kind, ID: a.ID, Scope: ScopeDocInstance, ProjectID: a.ProjectID, Ref: ref(approvals.Kind, a.ID),
		Title: a.Operation + " approval", Text: joinText(a.ID, a.Operation, a.Reason, a.Rule, a.Request.Path, a.Note),
		Status: a.State, Actor: &actor, Numbers: map[string]float64{"rev": float64(a.Rev)}, UpdatedAt: updated,
	}
	if a.ProjectID != "" {
		d.Scope = ScopeDocProject
	}
	return []Document{d}, nil
}

// loadMix indexes a mix at its current revision: project work, found by its name, description, group names and the
// dataset versions it samples. Draft and presence events on the mix's topic reload it too (harmless).
func loadMix(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	m, err := mixes.Get(ctx, q, ev.Entity.ID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parts := []string{m.Name, m.Description}
	for _, g := range m.Groups {
		parts = append(parts, g.Name)
		parts = append(parts, g.Datasets...)
	}
	actor := m.UpdatedBy
	return []Document{{
		Kind: mixes.Kind, ID: m.ID, Scope: ScopeDocProject, ProjectID: m.ProjectID, Ref: ref(mixes.Kind, m.ID),
		Title: m.Name, Text: joinText(parts...), Status: "active", Actor: &actor,
		Numbers: map[string]float64{"rev": float64(m.Rev)}, UpdatedAt: m.UpdatedAt,
	}}, nil
}

// loadEval indexes an eval: project work, found by its subject and baseline, its golden sets, profiles and its gate
// verdict. Its status is the eval's; a gated eval also carries a verdict:<passed|failed|…> tag.
func loadEval(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	e, err := evals.Get(ctx, q, ev.Entity.ID)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parts := []string{e.ID, e.Subject.Label, e.Subject.ID, e.Baseline.Label, e.Baseline.ID}
	lang := ""
	for _, g := range e.GoldenSets {
		parts = append(parts, g.Name, g.Version, g.Locale, g.Domain)
		if lang == "" {
			lang = g.Locale
		}
	}
	for _, p := range e.Profiles {
		parts = append(parts, p.Name)
	}
	var tags []string
	var gate struct {
		Verdict string `json:"verdict"`
	}
	if len(e.Gate) > 0 && json.Unmarshal(e.Gate, &gate) == nil && gate.Verdict != "" {
		tags = append(tags, "verdict:"+gate.Verdict)
	}
	title := "Eval of " + orElse(e.Subject.Label, e.Subject.ID)
	if b := orElse(e.Baseline.Label, e.Baseline.ID); b != "" {
		title += " vs " + b
	}
	actor := e.Actor
	return []Document{{
		Kind: evals.Kind, ID: e.ID, Scope: ScopeDocProject, ProjectID: e.ProjectID, Ref: ref(evals.Kind, e.ID),
		Title: title, Text: joinText(parts...), Tags: tags, Status: e.Status, Lang: lang, Actor: &actor,
		Numbers: map[string]float64{"rev": float64(e.Rev)}, UpdatedAt: e.UpdatedAt,
	}}, nil
}

// loadExperiment indexes an experiment: project work, found by its name, question and tag.
func loadExperiment(ctx context.Context, q storage.Querier, ev events.Record) ([]Document, error) {
	var (
		id, projectID, name, question, tag string
		actor                              auth.Actor
		rev                                int
		updated                            time.Time
	)
	err := q.QueryRow(ctx, `SELECT id, project_id, name, question, tag, actor, rev, updated_at FROM experiments WHERE id = $1`,
		ev.Entity.ID).Scan(&id, &projectID, &name, &question, &tag, &actor, &rev, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load experiment %s: %w", ev.Entity.ID, err)
	}
	var tags []string
	if tag != "" {
		tags = []string{"tag:" + tag}
	}
	return []Document{{
		Kind: experiments.Kind, ID: id, Scope: ScopeDocProject, ProjectID: projectID, Ref: ref(experiments.Kind, id),
		Title: name, Text: joinText(id, tag, question), Tags: tags, Status: "active", Actor: &actor,
		Numbers: map[string]float64{"rev": float64(rev)}, UpdatedAt: updated,
	}}, nil
}

func orElse(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func joinText(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// payloadText collects the string values of a JSON payload (sources, subsets, descriptions), sorted by key path
// so the text is stable.
func payloadText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if len(x) <= 200 {
				parts = append(parts, x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k])
			}
		}
	}
	walk(v)
	return strings.Join(parts, " ")
}

// payloadNumbers takes the top-level numeric fields of a payload that the query language compares (hours, wer, …).
func payloadNumbers(raw json.RawMessage) map[string]float64 {
	out := map[string]float64{}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for _, f := range NumericFields() {
		if n, ok := m[f].(float64); ok {
			out[f] = n
		}
	}
	return out
}
