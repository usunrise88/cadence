package pipelines

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/auxiliary"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// RefSpec is a parameter's x-cadence.registryRef: the parameter's value names a registry version of Kind (in v1
// only auxiliary), which must fill Role when Role is set.
type RefSpec struct {
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
}

// RegistryRefs returns the parameters of k whose x-cadence marks them as registry references.
func RegistryRefs(k Kind) map[string]RefSpec {
	var schema struct {
		Properties map[string]struct {
			XCadence struct {
				RegistryRef *RefSpec `json:"registryRef"`
			} `json:"x-cadence"`
		} `json:"properties"`
	}
	if len(k.Params) == 0 || json.Unmarshal(k.Params, &schema) != nil {
		return nil
	}
	out := map[string]RefSpec{}
	for name, prop := range schema.Properties {
		if r := prop.XCadence.RegistryRef; r != nil {
			out[name] = *r
		}
	}
	return out
}

// resolveRefs resolves each registry-reference parameter of a step (its resolved value names the version) for the
// project, through its data.lock when lock reads one (resolveRef). Problems (unknown collection, not adopted, wrong
// role, refused licence) are added to errs under prefix; other errors are returned.
func resolveRefs(ctx context.Context, q storage.Querier, k Kind, params map[string]any, projectID string, lock *lockSet, prefix string, errs *Errors) (map[string]steps.RegistryRef, error) {
	specs := RegistryRefs(k)
	if len(specs) == 0 {
		return nil, nil
	}
	out := map[string]steps.RegistryRef{}
	for _, name := range sortedKeys(specs) {
		spec := specs[name]
		ref, _ := params[name].(string)
		if ref == "" {
			errs.Add(prefix+"."+name, "%s names a registry %s; give a collection name, ver_… or @alias", k.Ref(), spec.Kind)
			continue
		}
		if spec.Kind != auxiliary.Kind {
			errs.Add(prefix+"."+name, "%s marks %s as a registry reference of kind %q; only %s references resolve", k.Ref(), name, spec.Kind, auxiliary.Kind)
			continue
		}
		r, err := resolveRef(ctx, q, projectID, spec.Role, ref, lock)
		if pe, ok := problems.As(err); ok {
			errs.Add(prefix+"."+name, "%s", pe.Detail)
			continue
		}
		if err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, nil
}

// lockSet is a project's data.lock at the commit a pipeline is read at (Engine.readLock), read at most once per plan
// and only when a step names a registry version.
type lockSet struct {
	read    func(ctx context.Context, q storage.Querier) ([]lockEntry, string, error)
	done    bool
	entries []lockEntry
	where   string
	err     error
}

func (l *lockSet) get(ctx context.Context, q storage.Querier) ([]lockEntry, string, error) {
	if !l.done {
		l.entries, l.where, l.err = l.read(ctx, q)
		l.done = true
	}
	return l.entries, l.where, l.err
}

// resolveRef resolves one registry reference of a step. A collection name the project's data.lock lists (at the
// commit the pipeline is read at) resolves to the newest locked version of it, so a checkout of the repository says
// which version ran (spec 02 "Lockfile"); a project without a repository or a lock, a collection the lock does not
// list, ver_… and @alias resolve through the adoptions (auxiliary.Resolve). Either way the version must be adopted
// (not-adopted), fill the role and allow commercial use of its outputs.
func resolveRef(ctx context.Context, q storage.Querier, projectID, role, ref string, lock *lockSet) (steps.RegistryRef, error) {
	if lock != nil && projectID != "" && !strings.HasPrefix(ref, "@") && !strings.HasPrefix(ref, "ver_") {
		entries, where, err := lock.get(ctx, q)
		if err != nil {
			return steps.RegistryRef{}, err
		}
		collection := registry.CollectionName(auxiliary.Kind, ref)
		if strings.HasPrefix(where, LockFile) && slices.ContainsFunc(entries, func(e lockEntry) bool {
			return e.Kind == auxiliary.Kind && e.Collection == collection
		}) {
			l, err := resolveLocked(ctx, q, projectID, auxiliary.Kind, ref, entries)
			if err != nil {
				return steps.RegistryRef{}, err
			}
			ref = l.VersionID
		}
	}
	r, _, err := auxiliary.Resolve(ctx, q, projectID, role, ref)
	return r, err
}

// hashParams is what a step's input hash covers of its parameters: the resolved values and, for each registry
// reference, the version it resolved to — a new version of the same collection (another revision, another endpoint)
// runs the step again — and, for a step that reads a mount, the fingerprint of the files it would read (changed files
// run it again). Steps without either hash their parameters as before.
func hashParams(s StepRow) map[string]any {
	if len(s.Auxiliaries) == 0 && s.MountFingerprint == "" {
		return s.Params
	}
	out := maps.Clone(s.Params)
	if len(s.Auxiliaries) > 0 {
		refs := map[string]string{}
		for name, r := range s.Auxiliaries {
			refs[name] = r.VersionID
		}
		out["$registryRefs"] = refs
	}
	if s.MountFingerprint != "" { // what the step reads from mounts (Engine.mountFingerprint)
		out["$mounts"] = s.MountFingerprint
	}
	return out
}
