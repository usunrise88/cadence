package steps

import (
	"encoding/json"
	"sort"
	"strings"
)

// TypeDeployable is the artifact type a family's export role writes and its serve role loads (cadence.deployable/1,
// docs/spec/03-pipelines-defaults.md "Export, parity and benchmark").
const TypeDeployable = "deployable"

// ServedModelPrefix starts the name a deployable is loaded under on a staging server.
const ServedModelPrefix = "cadence-"

// Served is a model a step serves through a staging target (docs/spec/06-platform.md "Staging serving"): a step that
// consumes a deployable is served by the target its params name, under a versioned model name derived from the
// deployable's hash. The queue reserves its memory once per card however many leases use it, and the control plane
// counts its leases.
type Served struct {
	Input          string // the step's input name
	Model          string // the versioned model name on the server
	DeployableHash string
	Target         string // the target param as written (dtg_… or a name); "" means serving.default_target
	MemoryMB       int    // the reservation: the deployable's serving.memoryMb, else the step's memoryGb, else fallbackMB
}

// ServedModelName is the name the deployable with this hash is loaded under: the prefix and its first 16 hex digits.
func ServedModelName(hash string) string {
	h := strings.TrimPrefix(hash, "b3:")
	if len(h) > 16 {
		h = h[:16]
	}
	return ServedModelPrefix + h
}

// ServedModels lists the distinct models s serves, one per deployable it consumes (by input name). A deployable
// states its reservation in its metadata (serving.memoryMb, or memoryMb at the top); the step's memoryGb applies
// when it does not and the step serves one model, fallbackMB otherwise.
func ServedModels(s Spec, fallbackMB int) []Served {
	var names []string
	for n, in := range s.Inputs {
		if in.Type == TypeDeployable {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var p struct {
		Target string `json:"target"`
	}
	if len(s.Params) > 0 {
		_ = json.Unmarshal(s.Params, &p)
	}
	out := make([]Served, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		ref := s.Inputs[n]
		if seen[ref.Hash] {
			continue
		}
		seen[ref.Hash] = true
		sv := Served{Input: n, Model: ServedModelName(ref.Hash), DeployableHash: ref.Hash, Target: strings.TrimSpace(p.Target)}
		var meta struct {
			MemoryMB int `json:"memoryMb"`
			Serving  struct {
				MemoryMB int `json:"memoryMb"`
			} `json:"serving"`
		}
		if len(ref.Meta) > 0 {
			_ = json.Unmarshal(ref.Meta, &meta)
		}
		switch {
		case meta.Serving.MemoryMB > 0:
			sv.MemoryMB = meta.Serving.MemoryMB
		case meta.MemoryMB > 0:
			sv.MemoryMB = meta.MemoryMB
		case s.Resources.MemoryGB > 0 && len(names) == 1:
			sv.MemoryMB = int(s.Resources.MemoryGB * 1024)
		default:
			sv.MemoryMB = fallbackMB
		}
		out = append(out, sv)
	}
	return out
}

// ServedOf is the model of s with the smallest name (the one the queue keys the lease by, as serving_leases' least
// model) with the reservation of all of them (MemoryMB is their sum), or false when it serves none.
func ServedOf(s Spec, fallbackMB int) (Served, bool) {
	all := ServedModels(s, fallbackMB)
	if len(all) == 0 {
		return Served{}, false
	}
	first, total := all[0], 0
	for _, sv := range all {
		total += sv.MemoryMB
		if sv.Model < first.Model {
			first = sv
		}
	}
	first.MemoryMB = total
	return first, true
}
