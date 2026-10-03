package pipelines

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/usunrise88/cadence/control-plane/internal/auxiliary"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
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
// project. Problems (unknown collection, not adopted, wrong role, refused licence) are added to errs under prefix;
// other errors are returned.
func resolveRefs(ctx context.Context, q storage.Querier, k Kind, params map[string]any, projectID, prefix string, errs *Errors) (map[string]steps.RegistryRef, error) {
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
		r, _, err := auxiliary.Resolve(ctx, q, projectID, spec.Role, ref)
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

// hashParams is what a step's input hash covers of its parameters: the resolved values and, for each registry
// reference, the version it resolved to — a new version of the same collection (another revision, another endpoint)
// runs the step again. Steps without references hash their parameters as before.
func hashParams(s StepRow) map[string]any {
	if len(s.Auxiliaries) == 0 {
		return s.Params
	}
	out := maps.Clone(s.Params)
	refs := map[string]string{}
	for name, r := range s.Auxiliaries {
		refs[name] = r.VersionID
	}
	out["$registryRefs"] = refs
	return out
}
