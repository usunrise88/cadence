// Package lineage walks the lineage graph both ways (docs/spec/02-domain-projects-registry.md "Registry": a model
// version links to dataset versions, mix, recipe and base model; a dataset version links to its sources and
// pipeline run; every registry entry answers "used by"). Edges point from what was used to what used it: upstream
// of an entity is what it was built from, downstream what was built from it or uses it.
//
// The graph is assembled from Sources, each answering for what it knows. Registry versions need no code of their
// own (the convention): a version's payload that names an entity id — any JSON string <prefix>_<uuid> at any depth,
// such as datasetVersionId, sourceIds, lineage.runId, checkpointId — makes that entity upstream of the version, with
// the payload field as the edge's relation; the same rule answers "used by" from the other end (migration 0025
// indexes the ids every payload names). A new registry kind (golden_set, normalizer, model) is therefore in the graph
// as soon as its payload names what it was made from. Project work kept in tables (runs, checkpoints, mixes,
// pipeline runs; evals in phase 3) joins by a Source passed to New.
package lineage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Node is one entity of the graph.
type Node struct {
	ID        string
	Kind      string // a registry kind, source, run, checkpoint, mix, pipeline_run, pipeline_step, project, …
	Label     string
	ProjectID string // project work: whose it is (visibility)
	State     string
	// Terminal nodes are shown but not walked through (a project: everything it adopted is not lineage of each
	// other).
	Terminal bool
}

// Ref is a neighbour of a node and how they relate.
type Ref struct {
	ID       string
	Relation string
}

// Source answers for the entities it knows. Every source is asked about every node; it returns nothing (and
// ok=false from Describe) for ids that are not its.
type Source interface {
	// Describe returns the node for id when the source owns it.
	Describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error)
	// Upstream lists what id was built from.
	Upstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error)
	// Downstream lists what was built from id or uses it.
	Downstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error)
}

// Directions of a walk and of a node relative to the root.
const (
	Both       = "both"
	Upstream   = "upstream"
	Downstream = "downstream"
	Root       = "root"
)

// Walk is one request.
type Walk struct {
	Root      string
	Direction string // both (default), upstream, downstream
	Depth     int    // hops each way (≥ 1)
	Limit     int    // most nodes returned
	// Visible reports whether the caller may see a node; hidden nodes are counted, not returned or walked through.
	Visible func(Node) bool
}

// Walked is a node of a result.
type Walked struct {
	Node
	Direction string
	Distance  int
}

// Edge points from the entity that was used to the one that used it.
type Edge struct {
	From, To, Relation string
}

// Result is the graph around the root.
type Result struct {
	Root      string
	Nodes     []Walked
	Edges     []Edge
	Truncated bool
	Hidden    int
}

// Graph walks over its sources.
type Graph struct {
	sources []Source
}

// New returns a graph over the built-in sources (registry, data sources, project work, projects) and extra.
func New(extra ...Source) *Graph {
	return &Graph{sources: append([]Source{registrySource{}, dataSource{}, workSource{}, projectSource{}}, extra...)}
}

// idRe is a Cadence entity id: a three-letter prefix and a UUID (the same pattern migration 0025 indexes).
var idRe = regexp.MustCompile(`^([a-z]{3})_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// notLineage are id prefixes that never carry lineage: who did something, approvals, jobs and leases, commands,
// audit rows, credentials and secrets, drafts, notifications, workspaces and views, collections, utterances and
// transcripts (a dataset version stands for those).
var notLineage = []string{"act", "als", "apr", "aud", "bkp", "cmd", "cmp", "crd", "cdk", "cst", "drf", "job", "lse",
	"ntf", "reg", "sec", "ses", "usr", "vew", "wrk", "wsp", "utt", "trn"}

// kindOf names the kind of an id no source describes, by its prefix.
var kindOf = map[string]string{
	"ver": "registry_version", "src": "source", "plr": "pipeline_run", "pls": "pipeline_step", "run": "run",
	"ckp": "checkpoint", "mix": "mix", "prj": "project", "evl": "eval", "evc": "eval_cell", "erc": "eval_record",
	"exp": "experiment", "swp": "sweep", "trs": "transcription",
}

// IsEntityID reports whether s is an entity id lineage follows.
func IsEntityID(s string) bool {
	m := idRe.FindStringSubmatch(s)
	return m != nil && !slices.Contains(notLineage, m[1])
}

// RefsIn returns every entity id a JSON document names as a string, with the path of the field that names it
// (array positions left out: lineage.datasetVersionIds), sorted and without repeats. It is the payload convention
// registry versions follow, exported for sources whose rows hold JSON documents.
func RefsIn(doc []byte) ([]Ref, error) {
	if len(doc) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("lineage: read document: %w", err)
	}
	seen := map[Ref]bool{}
	var out []Ref
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, e := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, e)
			}
		case []any:
			for _, e := range t {
				walk(path, e)
			}
		case string:
			if IsEntityID(t) {
				r := Ref{ID: t, Relation: path}
				if !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
	}
	walk("", v)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Relation < out[j].Relation
	})
	return out, nil
}

// describe asks the sources for id; an id none owns is a bare node named by its prefix.
func (g *Graph) describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error) {
	for _, s := range g.sources {
		n, ok, err := s.Describe(ctx, q, id)
		if err != nil {
			return Node{}, false, err
		}
		if ok {
			return n, true, nil
		}
	}
	if m := idRe.FindStringSubmatch(id); m != nil {
		kind := kindOf[m[1]]
		if kind == "" {
			kind = "entity"
		}
		return Node{ID: id, Kind: kind, Label: id}, false, nil
	}
	return Node{}, false, nil
}

func (g *Graph) neighbours(ctx context.Context, q storage.Querier, id, dir string) ([]Ref, error) {
	var out []Ref
	seen := map[Ref]bool{}
	for _, s := range g.sources {
		var (
			refs []Ref
			err  error
		)
		if dir == Upstream {
			refs, err = s.Upstream(ctx, q, id)
		} else {
			refs, err = s.Downstream(ctx, q, id)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if r.ID != id && !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// Walk walks from the root up to Depth hops each way, breadth first. Every node appears once (at its first, nearest
// sighting); a cycle stops where it meets a node already seen; the walk stops adding nodes at Limit.
func (g *Graph) Walk(ctx context.Context, q storage.Querier, w Walk) (Result, error) {
	if w.Depth < 1 {
		w.Depth = 1
	}
	if w.Limit < 1 {
		w.Limit = 200
	}
	visible := w.Visible
	if visible == nil {
		visible = func(Node) bool { return true }
	}
	root, owned, err := g.describe(ctx, q, w.Root)
	if err != nil {
		return Result{}, err
	}
	if !owned || !visible(root) {
		return Result{}, problems.NotFound.New("no entity %q in the lineage graph (registry versions ver_…, sources src_…, runs run_…, checkpoints ckp_…, mixes mix_…, pipeline runs plr_…)", w.Root)
	}
	res := Result{Root: w.Root, Nodes: []Walked{{Node: root, Direction: Root}}, Edges: []Edge{}}
	nodes := map[string]bool{w.Root: true}
	terminal := map[string]bool{w.Root: false}
	hidden := map[string]bool{}
	edges := map[Edge]bool{}
	dirs := []string{Upstream, Downstream}
	switch w.Direction {
	case Upstream:
		dirs = []string{Upstream}
	case Downstream:
		dirs = []string{Downstream}
	}
	for _, dir := range dirs {
		type item struct {
			id       string
			distance int
		}
		queue := []item{{w.Root, 0}}
		expanded := map[string]bool{}
		for len(queue) > 0 {
			it := queue[0]
			queue = queue[1:]
			if it.distance >= w.Depth || expanded[it.id] {
				continue
			}
			expanded[it.id] = true
			refs, err := g.neighbours(ctx, q, it.id, dir)
			if err != nil {
				return Result{}, err
			}
			for _, r := range refs {
				if hidden[r.ID] {
					continue
				}
				if !nodes[r.ID] {
					if len(res.Nodes) >= w.Limit {
						res.Truncated = true
						continue
					}
					n, _, err := g.describe(ctx, q, r.ID)
					if err != nil {
						return Result{}, err
					}
					if n.ID == "" {
						continue
					}
					if !visible(n) {
						hidden[r.ID] = true
						continue
					}
					nodes[r.ID], terminal[r.ID] = true, n.Terminal
					res.Nodes = append(res.Nodes, Walked{Node: n, Direction: dir, Distance: it.distance + 1})
					if !n.Terminal {
						queue = append(queue, item{r.ID, it.distance + 1})
					}
				} else if !expanded[r.ID] && !terminal[r.ID] && r.ID != w.Root {
					queue = append(queue, item{r.ID, it.distance + 1})
				}
				e := Edge{From: r.ID, To: it.id, Relation: r.Relation}
				if dir == Downstream {
					e = Edge{From: it.id, To: r.ID, Relation: r.Relation}
				}
				if !edges[e] {
					edges[e] = true
					res.Edges = append(res.Edges, e)
				}
			}
		}
	}
	res.Hidden = len(hidden)
	return res, nil
}
