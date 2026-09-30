package defaults

import "strings"

// Lookup returns the value a defaultRef points at (docs/spec/08-resolutions.md R11): ref is a dotted path of
// defaults.yaml keys (training.steps) ending at a default, the mapping that carries `value`. ok is false when the
// path does not exist or ends anywhere else.
func (d *Defaults) Lookup(ref string) (any, bool) {
	if d == nil || ref == "" {
		return nil, false
	}
	var node any = d.document
	for _, key := range strings.Split(ref, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		if node, ok = m[key]; !ok {
			return nil, false
		}
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m["value"]
	return v, ok
}
