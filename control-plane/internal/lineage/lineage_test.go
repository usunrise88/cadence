package lineage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

func id(prefix string, n int) string {
	return fmt.Sprintf("%s_00000000-0000-7000-8000-%012d", prefix, n)
}

func TestRefsIn(t *testing.T) {
	ds, src1, src2, run, apr := id("ver", 1), id("src", 1), id("src", 2), id("run", 1), id("apr", 1)
	doc := fmt.Sprintf(`{"datasetVersionId":%q,"sourceIds":[%q,%q],"lineage":{"runId":%q,"datasetVersionIds":[%q]},
		"approvalId":%q,"note":"ver_not-an-id","n":3,"hash":"b3:00"}`, ds, src1, src2, run, ds, apr)
	got, err := RefsIn([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, r := range got {
		parts = append(parts, r.Relation+"="+r.ID[:3])
	}
	want := []string{"lineage.runId=run", "sourceIds=src", "sourceIds=src", "datasetVersionId=ver", "lineage.datasetVersionIds=ver"}
	slices.Sort(parts)
	slices.Sort(want)
	if strings.Join(parts, " ") != strings.Join(want, " ") {
		t.Fatalf("refs %v, want %v", parts, want)
	}
	if IsEntityID(apr) || !IsEntityID(ds) || IsEntityID("ver_x") {
		t.Fatal("IsEntityID")
	}
}

// fake is a source over an in-memory graph: edges from → to.
type fake struct {
	nodes map[string]Node
	edges [][3]string // from, to, relation
}

func (f fake) Describe(_ context.Context, _ storage.Querier, id string) (Node, bool, error) {
	n, ok := f.nodes[id]
	return n, ok, nil
}

func (f fake) Upstream(_ context.Context, _ storage.Querier, id string) ([]Ref, error) {
	var out []Ref
	for _, e := range f.edges {
		if e[1] == id {
			out = append(out, Ref{ID: e[0], Relation: e[2]})
		}
	}
	return out, nil
}

func (f fake) Downstream(_ context.Context, _ storage.Querier, id string) ([]Ref, error) {
	var out []Ref
	for _, e := range f.edges {
		if e[0] == id {
			out = append(out, Ref{ID: e[1], Relation: e[2]})
		}
	}
	return out, nil
}

func TestWalk(t *testing.T) {
	src, ds, gs, mix, run, ckp, model, prj, other := id("src", 1), id("ver", 1), id("ver", 2), id("mix", 1), id("run", 1),
		id("ckp", 1), id("ver", 3), id("prj", 1), id("prj", 2)
	f := fake{nodes: map[string]Node{
		src: {ID: src, Kind: "source"}, ds: {ID: ds, Kind: "dataset_version"}, gs: {ID: gs, Kind: "golden_set"},
		mix: {ID: mix, Kind: "mix", ProjectID: prj}, run: {ID: run, Kind: "run", ProjectID: prj},
		ckp: {ID: ckp, Kind: "checkpoint", ProjectID: prj}, model: {ID: model, Kind: "model"},
		prj: {ID: prj, Kind: "project", ProjectID: prj, Terminal: true}, other: {ID: other, Kind: "project", ProjectID: other, Terminal: true},
	}, edges: [][3]string{
		{src, ds, "sourceIds"}, {ds, gs, "datasetVersionId"}, {ds, mix, "group main"}, {mix, run, "mix r1"},
		{run, ckp, "run"}, {ckp, model, "checkpointId"}, {model, ckp, "cycle"}, {ds, prj, "adopted"}, {ds, other, "adopted"},
		{prj, model, "projectId"},
	}}
	g := &Graph{sources: []Source{f}}
	ctx := context.Background()

	res, err := g.Walk(ctx, nil, Walk{Root: ds, Depth: 10, Limit: 100,
		Visible: func(n Node) bool { return n.ProjectID != other }})
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	for _, n := range res.Nodes {
		dirs[n.ID] = n.Direction
	}
	for want, dir := range map[string]string{ds: Root, src: Upstream, gs: Downstream, mix: Downstream, run: Downstream,
		ckp: Downstream, model: Downstream, prj: Downstream} {
		if dirs[want] != dir {
			t.Errorf("%s: direction %q, want %q (%v)", want, dirs[want], dir, dirs)
		}
	}
	if _, ok := dirs[other]; ok || res.Hidden != 1 {
		t.Errorf("hidden project shown or not counted: hidden=%d", res.Hidden)
	}
	// The project is terminal: what names it (the model) is not reached through it, only through the checkpoint.
	for _, e := range res.Edges {
		if e.From == prj {
			t.Errorf("walked through a terminal project: %+v", e)
		}
	}

	up, err := g.Walk(ctx, nil, Walk{Root: model, Direction: Upstream, Depth: 2, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range up.Nodes {
		got = append(got, fmt.Sprintf("%s@%d", n.Kind, n.Distance))
	}
	slices.Sort(got)
	if strings.Join(got, " ") != "checkpoint@1 model@0 project@1 run@2" {
		t.Fatalf("upstream of the model at depth 2: %v", got)
	}

	small, err := g.Walk(ctx, nil, Walk{Root: ds, Depth: 10, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !small.Truncated || len(small.Nodes) != 3 {
		t.Fatalf("limit 3: %d nodes, truncated %v", len(small.Nodes), small.Truncated)
	}

	if _, err := g.Walk(ctx, nil, Walk{Root: id("ver", 99), Depth: 1}); err == nil {
		t.Fatal("an unknown root walked")
	}
}
