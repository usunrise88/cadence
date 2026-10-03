package pipelines

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Departure is a parameter a step runs with that differs from its default (docs/spec/03-pipelines-defaults.md
// "Defaults": "peak LR 5e-4, default 2e-4"); Default is absent when the parameter has none.
type Departure struct {
	Param   string `json:"param"`
	Value   any    `json:"value"`
	Default any    `json:"default,omitempty"`
}

// paramProblem is one parameter problem, relative to the step.
type paramProblem struct {
	param   string
	message string
}

// resolveParams applies a step kind's defaults to the parameters a pipeline wrote and the run overrides:
// x-cadence.defaultRef (a defaults.yaml key, R11) wins over x-cadence.default, which wins over the schema's
// default. It returns the resolved parameters, the departures from defaults and every problem: unknown
// parameters, required ones without a value, an unresolvable defaultRef, and values outside the kind's JSON
// Schema or its x-cadence range.
func resolveParams(k Kind, written, overrides map[string]any, d *defaults.Defaults) (map[string]any, []Departure, []paramProblem) {
	var probs []paramProblem
	schema := &jsonschema.Schema{Type: "object"}
	if len(bytes.TrimSpace(k.Params)) > 0 && string(k.Params) != "null" {
		schema = &jsonschema.Schema{}
		if err := json.Unmarshal(k.Params, schema); err != nil {
			return nil, nil, []paramProblem{{"", fmt.Sprintf("step kind %s publishes a parameter schema that does not parse: %v", k.Ref(), err)}}
		}
	}
	set := map[string]any{}
	for name, v := range written {
		set[name] = v
	}
	for name, v := range overrides {
		set[name] = v
	}
	resolved := map[string]any{}
	var deps []Departure
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	for _, name := range sortedKeys(schema.Properties) {
		prop := schema.Properties[name]
		def, hasDef, err := defaultOf(prop, d)
		if err != nil {
			probs = append(probs, paramProblem{name, err.Error()})
		}
		v, isSet := set[name]
		switch {
		case isSet:
			nv, err := jsonValue(v)
			if err != nil {
				probs = append(probs, paramProblem{name, err.Error()})
				continue
			}
			resolved[name] = nv
			if !hasDef || !sameJSON(nv, def) {
				dep := Departure{Param: name, Value: nv}
				if hasDef {
					dep.Default = def
				}
				deps = append(deps, dep)
			}
			if msg := checkRange(prop, nv); msg != "" {
				probs = append(probs, paramProblem{name, msg})
			}
		case hasDef:
			resolved[name] = def
			// A default is checked too: an empty default with a pattern or a minimum length means "give a value"
			// (dataset_import's source_name and licence, R18), and the plan says so before a worker is involved.
			if msg := checkRange(prop, def); msg != "" {
				probs = append(probs, paramProblem{name, msg + " (the default; set a value)"})
			}
		case required[name]:
			probs = append(probs, paramProblem{name, fmt.Sprintf("%s needs a value: the parameter is required and has no default", k.Ref())})
		}
	}
	for _, name := range sortedKeys(set) {
		if _, known := schema.Properties[name]; !known {
			probs = append(probs, paramProblem{name, fmt.Sprintf("%s has no parameter %q (it has %s)", k.Ref(), name, strings.Join(sortedKeys(schema.Properties), ", "))})
		}
	}
	if len(probs) == 0 {
		rs, err := schema.Resolve(nil)
		if err != nil {
			probs = append(probs, paramProblem{"", fmt.Sprintf("step kind %s publishes an invalid parameter schema: %v", k.Ref(), err)})
		} else if err := rs.Validate(resolved); err != nil {
			probs = append(probs, paramProblem{"", fmt.Sprintf("the parameters do not fit %s: %v", k.Ref(), err)})
		}
	}
	return resolved, deps, probs
}

// ResolveParams is what a step of kind k runs with when a pipeline writes written: the written values over the
// defaults (as resolveParams; a facade hashes it, e.g. an eval's decoding). The error names the first problem; the
// map holds what resolved anyway.
func ResolveParams(k Kind, written map[string]any, d *defaults.Defaults) (map[string]any, error) {
	resolved, _, probs := resolveParams(k, written, nil, d)
	if len(probs) > 0 {
		return resolved, fmt.Errorf("%s: %s: %s", k.Ref(), probs[0].param, probs[0].message)
	}
	return resolved, nil
}

// xCadence returns a property's x-cadence metadata.
func xCadence(prop *jsonschema.Schema) map[string]any {
	if prop == nil || prop.Extra == nil {
		return nil
	}
	m, _ := prop.Extra["x-cadence"].(map[string]any)
	return m
}

// defaultOf returns a parameter's default: the defaults.yaml value its defaultRef names, else x-cadence.default,
// else the schema's default.
func defaultOf(prop *jsonschema.Schema, d *defaults.Defaults) (any, bool, error) {
	xc := xCadence(prop)
	if ref, _ := xc["defaultRef"].(string); ref != "" {
		v, ok := d.Lookup(ref)
		if !ok {
			return nil, false, fmt.Errorf("its defaultRef %q does not resolve in defaults.yaml", ref)
		}
		nv, err := jsonValue(v)
		return nv, err == nil, err
	}
	if v, ok := xc["default"]; ok {
		nv, err := jsonValue(v)
		return nv, err == nil, err
	}
	if len(prop.Default) > 0 {
		var v any
		if err := json.Unmarshal(prop.Default, &v); err != nil {
			return nil, false, fmt.Errorf("its schema default does not parse: %w", err)
		}
		return v, true, nil
	}
	return nil, false, nil
}

// checkRange applies x-cadence.range when it is a mapping of bounds (min/minimum, max/maximum, values/enum,
// minLength, maxLength, pattern); a prose range is documentation only.
func checkRange(prop *jsonschema.Schema, v any) string {
	r, ok := xCadence(prop)["range"].(map[string]any)
	if !ok {
		return ""
	}
	num := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			if f, ok := r[k].(float64); ok {
				return f, true
			}
		}
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		if lo, ok := num("min", "minimum"); ok && x < lo {
			return fmt.Sprintf("%v is below the safe minimum %v", x, lo)
		}
		if hi, ok := num("max", "maximum"); ok && x > hi {
			return fmt.Sprintf("%v is above the safe maximum %v", x, hi)
		}
	case string:
		n := float64(utf8.RuneCountInString(x))
		if lo, ok := num("minLength"); ok && n < lo {
			return fmt.Sprintf("%q is shorter than %v characters", x, lo)
		}
		if hi, ok := num("maxLength"); ok && n > hi {
			return fmt.Sprintf("%q is longer than %v characters", x, hi)
		}
		if pat, ok := r["pattern"].(string); ok {
			re, err := regexp.Compile(pat)
			if err != nil {
				return fmt.Sprintf("the step kind's pattern %q does not compile: %v", pat, err)
			}
			if !re.MatchString(x) {
				return fmt.Sprintf("%q does not match %s", x, pat)
			}
		}
	}
	for _, key := range []string{"values", "enum"} {
		allowed, ok := r[key].([]any)
		if !ok {
			continue
		}
		if !slices.ContainsFunc(allowed, func(a any) bool { return sameJSON(a, v) }) {
			return fmt.Sprintf("%v is not one of %v", v, allowed)
		}
	}
	return ""
}

// jsonValue turns a decoded value (YAML ints, nested maps) into its JSON form.
func jsonValue(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("not a JSON value: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func sameJSON(a, b any) bool {
	if fa, ok := a.(float64); ok {
		if fb, ok := b.(float64); ok {
			return fa == fb || math.Abs(fa-fb) <= 1e-12*math.Max(math.Abs(fa), math.Abs(fb))
		}
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// InputHash identifies a step's work: its kind and version, the runtime version that publishes the kind (the
// runtime registry version id, which changes with the runtime image), resolved parameters and input hashes. A
// finished step with the same hash in the same project produced what this one would, so it is reused (input-hash
// idempotence); after a runtime upgrade the step runs again on the new runtime.
func InputHash(kind, version, runtimeVersionID string, params map[string]any, inputs map[string]steps.ArtifactRef) (string, error) {
	in := map[string]string{}
	for name, ref := range inputs {
		in[name] = ref.Hash
	}
	b, err := json.Marshal(map[string]any{"kind": kind, "version": version, "runtime": runtimeVersionID, "params": params,
		"inputs": in})
	if err != nil {
		return "", fmt.Errorf("input hash: %w", err)
	}
	return cas.Hash(b), nil
}
