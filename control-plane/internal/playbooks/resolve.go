package playbooks

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Resolved are a playbook's inputs with a value each: Values in their JSON form (registry inputs as version ids, a
// multiple input as a list, an absent optional one as nil) and Text as the prompt and the notice name them.
type Resolved struct {
	Values map[string]any
	Text   map[string]string
}

// Bounds is the safe range of a defaultRef, when defaults.yaml gives one.
func Bounds(d *defaults.Defaults, ref string) (lo, hi *float64) {
	var node any = d.Document()
	for _, key := range strings.Split(ref, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, nil
		}
		node = m[key]
	}
	m, _ := node.(map[string]any)
	r, _ := m["range"].(map[string]any)
	if v, ok := number(r["min"]); ok {
		lo = &v
	}
	if v, ok := number(r["max"]); ok {
		hi = &v
	}
	return lo, hi
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

// Resolve gives every input of p a value: the given one (checked against its type and, for a defaultRef, the safe
// range), else its default from d, a project fact (prj's base model) or the project's adoption of a collection. prj
// may be nil (a listing without a project): project facts then stay unresolved. Problems answer validation-failed
// with one field error per input (/inputs/<name>).
func Resolve(ctx context.Context, q storage.Querier, d *defaults.Defaults, p Playbook, prj *projects.Project, given map[string]any) (Resolved, error) {
	return resolve(ctx, q, d, p, prj, given, false)
}

// ResolvePartial is Resolve for a listing: a required input nobody gave stays nil (named <name> in the text), so the
// estimate covers what is known.
func ResolvePartial(ctx context.Context, q storage.Querier, d *defaults.Defaults, p Playbook, prj *projects.Project) (Resolved, error) {
	return resolve(ctx, q, d, p, prj, nil, true)
}

func resolve(ctx context.Context, q storage.Querier, d *defaults.Defaults, p Playbook, prj *projects.Project, given map[string]any, partial bool) (Resolved, error) {
	out := Resolved{Values: map[string]any{}, Text: map[string]string{}}
	var fields []problems.FieldError
	fail := func(name, format string, args ...any) {
		fields = append(fields, problems.FieldError{Path: "/inputs/" + name, Message: fmt.Sprintf(format, args...)})
	}
	projectID := ""
	if prj != nil {
		projectID = prj.ID
	}
	for name := range given {
		if _, ok := p.Input(name); !ok {
			fail(name, "the playbook %s has no input %q", p.Name, name)
		}
	}
	for _, in := range p.Inputs {
		v, has := given[in.Name]
		if has && !empty(v) {
			val, text, err := resolveGiven(ctx, q, d, projectID, in, v)
			if err != nil {
				fail(in.Name, "%v", err)
				continue
			}
			out.Values[in.Name], out.Text[in.Name] = val, text
			continue
		}
		switch {
		case in.Required && partial:
			out.Values[in.Name], out.Text[in.Name] = nil, "<"+in.Name+">"
		case in.Required:
			fail(in.Name, "required: %s", in.Description)
		case in.DefaultRef != "":
			dv, _ := d.Lookup(in.DefaultRef)
			out.Values[in.Name], out.Text[in.Name] = dv, fmt.Sprint(dv)
		case in.From == FromProject:
			if prj == nil || prj.BaseModel == nil {
				out.Values[in.Name], out.Text[in.Name] = nil, "the project's base model"
				continue
			}
			bm := prj.BaseModel
			out.Values[in.Name], out.Text[in.Name] = bm.VersionID, fmt.Sprintf("%s %s (%s)", bm.Name, bm.Version, bm.VersionID)
		case in.From == FromAdoption:
			out.Values[in.Name], out.Text[in.Name] = nil, "none (the project has not adopted "+in.Collection+")"
			if prj == nil {
				out.Text[in.Name] = "the project's " + in.Collection + " when adopted"
				continue
			}
			kind := registry.KindDataset
			if in.Type == TypeBaseModel {
				kind = registry.KindBaseModel
			}
			list, err := registry.ListAdoptions(ctx, q, projectID, kind)
			if err != nil {
				return Resolved{}, err
			}
			for _, a := range list {
				if a.Version.Name == in.Collection {
					val := any(a.Version.ID)
					if in.Multiple {
						val = []any{a.Version.ID}
					}
					out.Values[in.Name], out.Text[in.Name] = val, versionText(a.Version)
				}
			}
		}
	}
	if len(fields) > 0 {
		return Resolved{}, problems.Validation(fields)
	}
	return out, nil
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func versionText(v registry.Version) string {
	return fmt.Sprintf("%s %s (%s)", v.Name, v.Version, v.ID)
}

func resolveGiven(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, in Input, v any) (any, string, error) {
	switch in.Type {
	case TypeDataset, TypeBaseModel:
		kind := registry.KindDataset
		if in.Type == TypeBaseModel {
			kind = registry.KindBaseModel
		}
		var refs []string
		switch x := v.(type) {
		case string:
			refs = []string{x}
		case []any:
			for _, e := range x {
				s, ok := e.(string)
				if !ok {
					return nil, "", fmt.Errorf("a list of references (ver_…, a collection name or @alias)")
				}
				refs = append(refs, s)
			}
		default:
			return nil, "", fmt.Errorf("a reference (ver_…, a collection name or @alias)")
		}
		if len(refs) > 1 && !in.Multiple {
			return nil, "", fmt.Errorf("one reference, not %d", len(refs))
		}
		ids := make([]any, 0, len(refs))
		texts := make([]string, 0, len(refs))
		for _, ref := range refs {
			ver, err := registry.Resolve(ctx, q, projectID, kind, strings.TrimSpace(ref))
			if err != nil {
				pe, ok := problems.As(err)
				if ok {
					return nil, "", fmt.Errorf("%s", pe.Detail)
				}
				return nil, "", err
			}
			ids = append(ids, ver.ID)
			texts = append(texts, versionText(ver))
		}
		if in.Multiple {
			return ids, strings.Join(texts, ", "), nil
		}
		return ids[0], texts[0], nil
	case TypeInteger, TypeNumber:
		f, ok := number(v)
		if !ok {
			return nil, "", fmt.Errorf("a number")
		}
		if in.Type == TypeInteger && f != math.Trunc(f) {
			return nil, "", fmt.Errorf("a whole number")
		}
		if in.DefaultRef != "" {
			lo, hi := Bounds(d, in.DefaultRef)
			if (lo != nil && f < *lo) || (hi != nil && f > *hi) {
				return nil, "", fmt.Errorf("%v is outside the safe range %s of defaults.yaml %s", f, rangeText(lo, hi), in.DefaultRef)
			}
		}
		if in.Type == TypeInteger {
			return int(f), fmt.Sprint(int(f)), nil
		}
		return f, fmt.Sprint(f), nil
	case TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, "", fmt.Errorf("true or false")
		}
		return b, fmt.Sprint(b), nil
	default:
		s, ok := v.(string)
		if !ok {
			return nil, "", fmt.Errorf("text")
		}
		return s, s, nil
	}
}

func rangeText(lo, hi *float64) string {
	l, h := "−∞", "∞"
	if lo != nil {
		l = fmt.Sprint(*lo)
	}
	if hi != nil {
		h = fmt.Sprint(*hi)
	}
	return "[" + l + ", " + h + "]"
}

// With resolves a step's `with` against the inputs: $inputs.<name> takes the input's value, lists are flattened and
// values that are nil (an optional input without one) are dropped.
func With(s Step, r Resolved) map[string]any {
	out := make(map[string]any, len(s.With))
	for k, v := range s.With {
		if x := withValue(v, r); x != nil {
			out[k] = x
		}
	}
	return out
}

func withValue(v any, r Resolved) any {
	switch x := v.(type) {
	case string:
		if m := refRe.FindStringSubmatch(x); m != nil {
			return r.Values[m[1]]
		}
		return x
	case []any:
		var out []any
		for _, e := range x {
			switch y := withValue(e, r).(type) {
			case nil:
			case []any:
				out = append(out, y...)
			default:
				out = append(out, y)
			}
		}
		return out
	}
	return v
}
