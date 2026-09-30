package drafts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Change is one changed field of a draft against its base, as a JSON pointer (the contract's DraftChange). Before
// is absent when the field was added, After when it was removed.
type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Diff lists the fields that differ between two JSON documents. Objects are compared key by key and arrays of
// objects element by element; an array of scalars that differs is one change, as is a value whose type changed.
func Diff(before, after json.RawMessage) ([]Change, error) {
	var b, a any
	if err := decode(before, &b); err != nil {
		return nil, fmt.Errorf("diff: decode base: %w", err)
	}
	if err := decode(after, &a); err != nil {
		return nil, fmt.Errorf("diff: decode draft: %w", err)
	}
	out := []Change{}
	diff("", b, a, &out)
	return out, nil
}

func decode(doc json.RawMessage, v *any) error {
	if len(bytes.TrimSpace(doc)) == 0 {
		*v = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	return dec.Decode(v)
}

func diff(path string, b, a any, out *[]Change) {
	switch bv := b.(type) {
	case map[string]any:
		if av, ok := a.(map[string]any); ok {
			keys := make([]string, 0, len(bv)+len(av))
			for k := range bv {
				keys = append(keys, k)
			}
			for k := range av {
				if _, seen := bv[k]; !seen {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := path + "/" + escape(k)
				x, inB := bv[k]
				y, inA := av[k]
				switch {
				case !inA:
					*out = append(*out, Change{Path: p, Before: x})
				case !inB:
					*out = append(*out, Change{Path: p, After: y})
				default:
					diff(p, x, y, out)
				}
			}
			return
		}
	case []any:
		if av, ok := a.([]any); ok && objects(bv) && objects(av) {
			n := max(len(bv), len(av))
			for i := range n {
				p := path + "/" + strconv.Itoa(i)
				switch {
				case i >= len(av):
					*out = append(*out, Change{Path: p, Before: bv[i]})
				case i >= len(bv):
					*out = append(*out, Change{Path: p, After: av[i]})
				default:
					diff(p, bv[i], av[i], out)
				}
			}
			return
		}
	}
	if !equal(b, a) {
		*out = append(*out, Change{Path: path, Before: b, After: a})
	}
}

// objects reports whether every element of xs is an object (an empty list counts).
func objects(xs []any) bool {
	for _, x := range xs {
		if _, ok := x.(map[string]any); !ok {
			return false
		}
	}
	return true
}

func equal(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// Rebase carries a draft over to a newer revision of its entity: the result is current with every top-level field
// the draft changed against its base taken from the draft. Fields the draft left alone keep the newer revision's
// values, so a person's edit made in between is not undone by accepting the draft later.
func Rebase(base, draft, current json.RawMessage) (json.RawMessage, error) {
	var b, d, c map[string]json.RawMessage
	for _, x := range []struct {
		doc  json.RawMessage
		into *map[string]json.RawMessage
	}{{base, &b}, {draft, &d}, {current, &c}} {
		if err := json.Unmarshal(x.doc, x.into); err != nil {
			return nil, fmt.Errorf("rebase: %w", err)
		}
	}
	out := make(map[string]json.RawMessage, len(c))
	for k, v := range c {
		out[k] = v
	}
	for k, v := range d {
		if bv, ok := b[k]; !ok || !sameJSON(bv, v) {
			out[k] = v
		}
	}
	for k := range b {
		if _, kept := d[k]; !kept {
			delete(out, k) // the draft removed the field
		}
	}
	res, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("rebase: %w", err)
	}
	return res, nil
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if decode(a, &x) != nil || decode(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return equal(x, y)
}
