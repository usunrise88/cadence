package pipelines

import (
	"context"
	"fmt"
	"slices"

	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// WarningNeedsMaterialize means a training step would read a dataset version the cache evicted. A dry run reports it with
// what datasets.materialize would copy back; the real call is refused (artifact-missing) until it is back.
const WarningNeedsMaterialize = "needs-materialize"

// Materialize is a dataset version a training step would read whose shards the cache evicted (the contract's
// NeedsMaterialize): the datasets.materialize plan for it.
type Materialize struct {
	VersionID  string         `json:"versionId"`
	Collection string         `json:"collection,omitempty"`
	Version    string         `json:"version,omitempty"`
	Artifact   string         `json:"artifact"`
	Input      string         `json:"input,omitempty"`
	Bytes      int64          `json:"bytes"`
	CopyBytes  int64          `json:"copyBytes"`
	Shards     int            `json:"shards"`
	CopyShards int            `json:"copyShards"`
	From       []cache.Source `json:"from"`
	Missing    int            `json:"missing"`
}

// MaterializeRefusal is the artifact-missing answer a real call gives when the plan needs a dataset version
// materialized first (needs-materialize), else nil: a dry run reports the warning, a real call is refused.
func (p Plan) MaterializeRefusal(ctx context.Context, q storage.Querier) error {
	if len(p.Materialize) == 0 {
		return nil
	}
	m := p.Materialize[0]
	return data.EvictedRefusal(ctx, q, data.Evicted{VersionID: m.VersionID, Artifact: m.Artifact})
}

// evicted lists the dataset versions input ref of a training step would read that the cache evicted, each with its
// datasets.materialize plan; any other reason training may not read ref is refused as before.
func (e *Engine) evicted(ctx context.Context, q storage.Querier, input string, ref steps.ArtifactRef) ([]Materialize, error) {
	ev, err := data.TrainableUnlessEvicted(ctx, q, e.o.CAS, ref)
	if err != nil {
		return nil, err
	}
	out := make([]Materialize, 0, len(ev))
	for _, x := range ev {
		m := Materialize{VersionID: x.VersionID, Artifact: x.Artifact, Input: input, From: []cache.Source{}}
		_ = q.QueryRow(ctx, `SELECT c.name, v.version FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
			WHERE v.id = $1`, x.VersionID).Scan(&m.Collection, &m.Version)
		p, err := cache.PlanMaterialize(ctx, q, e.o.CAS, x.VersionID)
		if _, isProblem := problems.As(err); err != nil && !isProblem {
			return nil, err
		}
		if err == nil {
			m.Bytes, m.CopyBytes, m.Shards, m.CopyShards, m.Missing = p.Bytes, p.CopyBytes, p.Shards, p.CopyShards, p.Missing
			m.From = append(m.From, p.From...)
		}
		out = append(out, m)
	}
	return out, nil
}

// materializeWarning is the plan warning for m read by step (kind).
func materializeWarning(step, kind string, m Materialize) Warning {
	name := m.VersionID
	if m.Collection != "" {
		name = fmt.Sprintf("%s %s (%s)", m.Collection, m.Version, m.VersionID)
	}
	msg := fmt.Sprintf("step %s would read %s, which the cache evicted: its shards are on a mount and training reads only what the cache holds. "+
		"Run datasets.materialize with versionId %s first (%s to copy back", step, name, m.VersionID, gigabytes(m.CopyBytes))
	if len(m.From) > 0 {
		msg += " from mount " + m.From[0].Mount
		for _, f := range m.From[1:] {
			msg += ", " + f.Mount
		}
	}
	msg += "); until then the real call is refused (artifact-missing)"
	if m.Missing > 0 {
		msg += fmt.Sprintf(". %d shard(s) are on no mount: materialize cannot bring them back", m.Missing)
	}
	return Warning{Code: WarningNeedsMaterialize, Step: step, Kind: kind, Message: msg, Materialize: &m}
}

func gigabytes(n int64) string {
	if n < 1e9 {
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%.2f GB", float64(n)/1e9)
}

// addMaterialize records m on the plan once per dataset version, with its warning.
func (p *Plan) addMaterialize(step, kind string, m Materialize) {
	if slices.ContainsFunc(p.Materialize, func(x Materialize) bool { return x.VersionID == m.VersionID }) {
		return
	}
	p.Materialize = append(p.Materialize, m)
	p.Warnings = append(p.Warnings, materializeWarning(step, kind, m))
}

// NeedsMaterialize lists, for run r that is not done, the dataset versions its unfinished training steps would read
// whose shards the cache evicted (pipelineRuns.get): a retry fails such a step at queue time until
// datasets.materialize brings them back. A step whose inputs are not resolved yet reads its run inputs only.
func (e *Engine) NeedsMaterialize(ctx context.Context, q storage.Querier, r Run) ([]Materialize, error) {
	out := []Materialize{}
	if r.State == RunDone {
		return out, nil
	}
	seen := map[string]bool{}
	for _, s := range r.Steps {
		if !trains(s.Resources) || finished(s.State) || (s.Error != nil && s.Error.Type == SkipPlanned) {
			continue
		}
		inputs := s.Inputs
		if len(inputs) == 0 {
			inputs = map[string]steps.ArtifactRef{}
			for name, wire := range s.Wiring {
				if w, ok := ParseWire(wire); ok && w.Input != "" {
					if ref, ok := r.Inputs[w.Input]; ok {
						inputs[name] = ref
					}
				}
			}
		}
		for _, name := range sortedKeys(inputs) {
			list, err := e.evicted(ctx, q, name, inputs[name])
			if _, isProblem := problems.As(err); isProblem {
				continue // refused for another reason: the step's own error says why
			}
			if err != nil {
				return nil, err
			}
			for _, m := range list {
				if !seen[m.VersionID] {
					seen[m.VersionID] = true
					out = append(out, m)
				}
			}
		}
	}
	return out, nil
}
