package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// data.lock read by pipelines (docs/spec/02-domain-projects-registry.md "Registry": "Lockfile: Cadence writes data.lock
// in the project repository listing every registry version the project depends on, so a checkout of the repository
// says exactly which data a run used"). A step parameter marked x-cadence.registry: <registry kind> (any kind but
// source, which "no licence, no ingest" covers) names a registry version: a collection name, @alias or ver_…. The
// engine resolves it through data.lock at the commit the pipeline is read at — the bundled templates and projects
// without a repository through the project's adoptions, which data.lock is written from — and refuses a version the
// lock does not list (not-adopted). The pipeline file names the collection and data.lock the version, both in one
// commit; the plan reports what each parameter resolved to (PlanStep.Locked).

// LockFile is where a project repository keeps its lockfile (layout.DataLock).
const LockFile = "data.lock"

// Locked is the contract's LockedReference.
type Locked struct {
	Param      string `json:"param"`
	Ref        string `json:"ref"`
	VersionID  string `json:"versionId"`
	Version    string `json:"version"`
	Collection string `json:"collection"`
}

// lockEntry is one resolved entry of data.lock.
type lockEntry struct {
	Kind       string `yaml:"kind"`
	Collection string `yaml:"collection"`
	Version    string `yaml:"version"`
	ID         string `yaml:"id"`
}

// readLock returns the registry versions the project's data.lock lists at commit (main when commit is empty), or —
// for a project without a repository, or a repository without data.lock — its adoptions. where names the source in
// messages.
func (e *Engine) readLock(ctx context.Context, q storage.Querier, p projects.Project, commit string) ([]lockEntry, string, error) {
	if e.hasRepo(p) {
		ref := commit
		if ref == "" {
			ref = DefaultRef
		}
		b, at, err := e.o.Repos.ReadFile(ctx, p.Slug, ref, LockFile)
		if err == nil {
			var doc struct {
				Resolved []lockEntry `yaml:"resolved"`
			}
			if err := yaml.Unmarshal(b, &doc); err != nil {
				return nil, "", problems.PipelineInvalid.New("data.lock at %s does not parse (%v); it is written by Cadence (projects.adopt), never by hand", short(at), err)
			}
			return doc.Resolved, "data.lock at " + short(at), nil
		}
	}
	adopted, err := registry.ListAdoptions(ctx, q, p.ID, "")
	if err != nil {
		return nil, "", err
	}
	out := make([]lockEntry, 0, len(adopted))
	for _, a := range adopted {
		out = append(out, lockEntry{Kind: a.Version.Kind, Collection: a.Version.Name, Version: a.Version.Version, ID: a.Version.ID})
	}
	return out, "the project's adoptions", nil
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// registryParams returns, in name order, the parameters of a kind's schema marked x-cadence.registry with a registry
// kind (not source) and that kind.
func registryParams(k Kind) [][2]string {
	if len(k.Params) == 0 || string(k.Params) == "null" {
		return nil
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(k.Params, &schema); err != nil {
		return nil // Plan has reported it
	}
	var out [][2]string
	for _, name := range sortedKeys(schema.Properties) {
		reg, _ := xCadence(schema.Properties[name])["registry"].(string)
		if reg != "" && reg != RegistrySource {
			out = append(out, [2]string{name, reg})
		}
	}
	return out
}

// lock resolves every registry-marked parameter of plan through the project's lockfile (see above) and records the
// results on its steps. commit is the commit the pipeline was read at ("" for a bundled template: main).
func (e *Engine) lock(ctx context.Context, q storage.Querier, p projects.Project, commit string, plan *Plan) error {
	var (
		entries []lockEntry
		where   string
		read    bool
	)
	for i := range plan.Steps {
		ps := &plan.Steps[i]
		for _, pr := range registryParams(ps.Kind) {
			name, kind := pr[0], pr[1]
			ref, _ := ps.Params[name].(string)
			if ref = strings.TrimSpace(ref); ref == "" {
				continue // an optional reference left empty
			}
			if !read {
				var err error
				if entries, where, err = e.readLock(ctx, q, p, commit); err != nil {
					return err
				}
				read = true
			}
			l, err := resolveLocked(ctx, q, p.ID, kind, ref, entries)
			if err != nil {
				if pe, ok := problems.As(err); ok {
					pe.Detail = fmt.Sprintf("step %s (%s), parameter %s: %s (%s)", ps.Step, ps.Kind.Ref(), name, pe.Detail, where)
				}
				return err
			}
			l.Param = name
			ps.Locked = append(ps.Locked, l)
		}
	}
	return nil
}

// resolveLocked finds the locked version ref names: ver_… must be listed, @alias must point at a listed version, and a
// collection name (bare names take the kind's prefix: dataset/…) resolves to the newest listed version of it.
func resolveLocked(ctx context.Context, q storage.Querier, projectID, kind, ref string, entries []lockEntry) (Locked, error) {
	match := func(e lockEntry) bool { return e.Kind == kind || kind == "" }
	switch {
	case strings.HasPrefix(ref, "@"):
		a, err := registry.GetAlias(ctx, q, projectID, strings.TrimPrefix(ref, "@"))
		if err != nil {
			return Locked{}, err
		}
		for _, e := range entries {
			if e.ID == a.Version.ID && match(e) {
				return Locked{Ref: ref, VersionID: e.ID, Version: e.Version, Collection: e.Collection}, nil
			}
		}
		return Locked{}, problems.NotAdopted.New("%s points at %s %s, which the lock does not list as a %s", ref, a.Version.Name, a.Version.Version, registry.Noun(kind))
	case strings.HasPrefix(ref, "ver_"):
		for _, e := range entries {
			if e.ID == ref && match(e) {
				return Locked{Ref: ref, VersionID: e.ID, Version: e.Version, Collection: e.Collection}, nil
			}
		}
		return Locked{}, problems.NotAdopted.New("%s is not a %s the project adopted; adopt it first (projects.adopt), which writes it into data.lock", ref, registry.Noun(kind))
	}
	collection := registry.CollectionName(kind, ref)
	var listed []lockEntry
	for _, e := range entries {
		if e.Collection == collection && match(e) {
			listed = append(listed, e)
		}
	}
	if len(listed) == 0 {
		return Locked{}, problems.NotAdopted.New("the project adopted no version of %s; adopt one (registry.search, then projects.adopt), which writes it into data.lock", collection)
	}
	best, err := newestLocked(ctx, q, listed)
	if err != nil {
		return Locked{}, err
	}
	return Locked{Ref: ref, VersionID: best.ID, Version: best.Version, Collection: best.Collection}, nil
}

// newestLocked is the most recently created of the locked versions of one collection. Versions are ordered by when
// the registry created them, not by their names: two versions of a day (2026-10-03.<sha>) differ only in a hash.
// An entry the registry does not know (a hand-edited lock) loses to every known one.
func newestLocked(ctx context.Context, q storage.Querier, listed []lockEntry) (lockEntry, error) {
	if len(listed) == 1 || q == nil { // nothing to order, or no registry to ask (unit tests): the greatest name
		best := listed[0]
		for _, e := range listed[1:] {
			if e.Version > best.Version {
				best = e
			}
		}
		return best, nil
	}
	ids := make([]string, 0, len(listed))
	for _, e := range listed {
		ids = append(ids, e.ID)
	}
	var id string
	err := q.QueryRow(ctx, "SELECT id FROM registry_versions WHERE id = ANY($1) ORDER BY created_at DESC, id DESC LIMIT 1", ids).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return listed[len(listed)-1], nil
	case err != nil:
		return lockEntry{}, fmt.Errorf("order locked versions: %w", err)
	}
	for _, e := range listed {
		if e.ID == id {
			return e, nil
		}
	}
	return listed[len(listed)-1], nil
}
