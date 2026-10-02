package server

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/lineage"
)

// Lineage both ways (phase 3 · stream L; docs/spec/02-domain-projects-registry.md "Registry").

// RegistryLineage implements registry.lineage. Project work the credential cannot see and, without registry read,
// registry entries are left out of the graph and counted in hidden; a root the caller cannot see is not found.
func (s *Server) RegistryLineage(ctx context.Context, req api.RegistryLineageRequestObject) (api.RegistryLineageResponseObject, error) {
	sc, ok := auth.ScopeFromContext(ctx)
	if !ok {
		sc = auth.FullScope()
	}
	w := lineage.Walk{Root: req.Id, Direction: lineage.Both, Depth: 3, Limit: 200,
		Visible: func(n lineage.Node) bool {
			if n.ProjectID != "" {
				return sc.AllowsProject(n.ProjectID)
			}
			return sc.AllowsRegistryRead()
		}}
	if req.Params.Direction != nil {
		w.Direction = string(*req.Params.Direction)
	}
	if req.Params.Depth != nil {
		w.Depth = *req.Params.Depth
	}
	if req.Params.Limit != nil {
		w.Limit = *req.Params.Limit
	}
	g := s.Lineage
	if g == nil {
		g = lineage.New()
	}
	res, err := g.Walk(ctx, s.Pool, w)
	if err != nil {
		return nil, err
	}
	out := api.RegistryLineage200JSONResponse{Root: res.Root, Nodes: make([]api.LineageNode, 0, len(res.Nodes)),
		Edges: make([]api.LineageEdge, 0, len(res.Edges)), Truncated: res.Truncated, Hidden: res.Hidden}
	for _, n := range res.Nodes {
		out.Nodes = append(out.Nodes, api.LineageNode{Id: n.ID, Kind: n.Kind, Label: n.Label, ProjectId: optional(n.ProjectID),
			State: optional(n.State), Direction: api.LineageNodeDirection(n.Direction), Distance: n.Distance})
	}
	for _, e := range res.Edges {
		out.Edges = append(out.Edges, api.LineageEdge{From: e.From, To: e.To, Relation: e.Relation})
	}
	return out, nil
}
