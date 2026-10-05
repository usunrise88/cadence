package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/auxiliary"
	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Registry, compute, secrets, defaults and policies (phase 1 · stream B); runs.* live in handlers_runs.go.

func (c commandResponse) VisitProjectsAdoptResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitAliasesSetResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitComputeEditResponse(w http.ResponseWriter) error   { return c.write(w) }
func (c commandResponse) VisitSecretsNewResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitPoliciesEditResponse(w http.ResponseWriter) error  { return c.write(w) }

// defaultsDoc returns the defaults the server runs with.
func (s *Server) defaultsDoc() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults
	}
	return defaults.Get()
}

// run executes fn as cmd and returns its response.
func (s *Server) run(ctx context.Context, cmd commands.Command, fn commands.Func) (commandResponse, error) {
	resp, err := s.Pipeline.Run(ctx, cmd, fn)
	return commandResponse(resp), err
}

// ---------------------------------------------------------------- registry reads

// RegistrySearch implements registry.search.
func (s *Server) RegistrySearch(ctx context.Context, req api.RegistrySearchRequestObject) (api.RegistrySearchResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	base := registry.Filter{Limit: 100}
	if req.Params.Limit != nil {
		base.Limit = *req.Params.Limit
	}
	if req.Params.Kind != nil {
		base.Kind = string(*req.Params.Kind)
	}
	if req.Params.Project != nil {
		p, err := projects.Get(ctx, s.Pool, *req.Params.Project)
		if err != nil {
			return nil, err
		}
		base.ProjectID = p.ID
	}
	f := registry.Search(base, deref(req.Params.Q))
	f.HideArchived = f.State == "" // archived versions show only when state:archived is asked (versions.archive)
	list, err := registry.ListVersions(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	counts, err := registry.CountKinds(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out := api.RegistrySearch200JSONResponse{Items: make([]api.RegistryVersion, 0, len(list)), Kinds: make([]api.RegistryKindCount, 0, len(counts))}
	for _, v := range list {
		out.Items = append(out.Items, apiVersion(v))
	}
	for _, k := range counts {
		out.Kinds = append(out.Kinds, api.RegistryKindCount{Kind: api.RegistryKind(k.Kind), Count: k.Count})
	}
	return out, nil
}

// CollectionsList implements collections.list.
func (s *Server) CollectionsList(ctx context.Context, req api.CollectionsListRequestObject) (api.CollectionsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	var kind string
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	list, err := registry.ListCollections(ctx, s.Pool, kind, deref(req.Params.Tag))
	if err != nil {
		return nil, err
	}
	out := api.CollectionsList200JSONResponse{Items: make([]api.Collection, 0, len(list))}
	for _, c := range list {
		out.Items = append(out.Items, apiCollection(c))
	}
	return out, nil
}

// CollectionsGet implements collections.get.
func (s *Server) CollectionsGet(ctx context.Context, req api.CollectionsGetRequestObject) (api.CollectionsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	c, err := registry.GetCollection(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	return api.CollectionsGet200JSONResponse(apiCollection(c)), nil
}

// versions lists versions of kind and decodes each payload into P, with the projects that use each.
func versions[P any](ctx context.Context, q storage.Querier, f registry.Filter) ([]api.RegistryVersion, []P, [][]api.UsedBy, error) {
	list, err := registry.ListVersions(ctx, q, f)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := make([]string, 0, len(list))
	for _, v := range list {
		ids = append(ids, v.ID)
	}
	used, err := registry.UsedByOf(ctx, q, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	common := make([]api.RegistryVersion, 0, len(list))
	payloads := make([]P, 0, len(list))
	usedBy := make([][]api.UsedBy, 0, len(list))
	for _, v := range list {
		var p P
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return nil, nil, nil, fmt.Errorf("decode %s payload of %s: %w", v.Kind, v.ID, err)
		}
		u := make([]api.UsedBy, 0, len(used[v.ID]))
		for _, x := range used[v.ID] {
			u = append(u, api.UsedBy{ProjectId: x.ProjectID, ProjectSlug: x.ProjectSlug, AdoptedAt: x.AdoptedAt, Aliases: x.Aliases})
		}
		common, payloads, usedBy = append(common, apiVersion(v)), append(payloads, p), append(usedBy, u)
	}
	return common, payloads, usedBy, nil
}

func versionFilter(kind string, collection *string, state *api.VersionState) registry.Filter {
	f := registry.Filter{Kind: kind, Collection: deref(collection)}
	if state != nil {
		f.State = string(*state)
	}
	return f
}

// one returns the single version of a list asked by id, or not-found.
func one[T any](items []T, kind, id string) (T, error) {
	if len(items) == 0 {
		var zero T
		return zero, problems.NotFound.New("no %s version %q in the registry", registry.Noun(kind), id)
	}
	return items[0], nil
}

func baseModelVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.BaseModelVersion, error) {
	common, payloads, used, err := versions[api.BaseModelPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.BaseModelVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.BaseModelVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, BaseModel: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// BaseModelsList implements baseModels.list.
func (s *Server) BaseModelsList(ctx context.Context, req api.BaseModelsListRequestObject) (api.BaseModelsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := baseModelVersions(ctx, s.Pool, versionFilter(registry.KindBaseModel, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.BaseModelsList200JSONResponse{Items: items}, nil
}

// BaseModelsGet implements baseModels.get.
func (s *Server) BaseModelsGet(ctx context.Context, req api.BaseModelsGetRequestObject) (api.BaseModelsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := baseModelVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindBaseModel, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindBaseModel, req.Id)
	if err != nil {
		return nil, err
	}
	return api.BaseModelsGet200JSONResponse(v), nil
}

func datasetVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.DatasetVersion, error) {
	common, payloads, used, err := versions[api.DatasetPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.DatasetVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.DatasetVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, Dataset: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// DatasetsList implements datasets.list.
func (s *Server) DatasetsList(ctx context.Context, req api.DatasetsListRequestObject) (api.DatasetsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := datasetVersions(ctx, s.Pool, versionFilter(registry.KindDataset, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.DatasetsList200JSONResponse{Items: items}, nil
}

// DatasetsGet implements datasets.get.
func (s *Server) DatasetsGet(ctx context.Context, req api.DatasetsGetRequestObject) (api.DatasetsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := datasetVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindDataset, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindDataset, req.Id)
	if err != nil {
		return nil, err
	}
	if err := overlayShards(ctx, s.Pool, &v); err != nil {
		return nil, err
	}
	return api.DatasetsGet200JSONResponse(v), nil
}

// overlayShards replaces the shard location and pin the payload recorded when the version froze (cas, unpinned)
// with the cache's state now (cache.LiveState): the payload is immutable, the cache is not.
func overlayShards(ctx context.Context, q storage.Querier, v *api.DatasetVersion) error {
	if v.Dataset.Shards == nil || len(*v.Dataset.Shards) == 0 {
		return nil
	}
	live, ok, err := cache.LiveState(ctx, q, v.Id)
	if err != nil || !ok {
		return err
	}
	for i := range *v.Dataset.Shards {
		sh := &(*v.Dataset.Shards)[i]
		sh.Location, sh.Pinned = live.Location(sh.Hash), len(live.Pinned) > 0
	}
	return nil
}

func templateVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.TemplateVersion, error) {
	common, payloads, used, err := versions[api.TemplatePayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.TemplateVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.TemplateVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, Template: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// TemplatesList implements templates.list.
func (s *Server) TemplatesList(ctx context.Context, req api.TemplatesListRequestObject) (api.TemplatesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	f := versionFilter(registry.KindTemplate, req.Params.Collection, req.Params.State)
	if req.Params.TemplateKind != nil {
		f.TemplateKind = string(*req.Params.TemplateKind)
	}
	items, err := templateVersions(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	return api.TemplatesList200JSONResponse{Items: items}, nil
}

// TemplatesGet implements templates.get.
func (s *Server) TemplatesGet(ctx context.Context, req api.TemplatesGetRequestObject) (api.TemplatesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := templateVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindTemplate, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindTemplate, req.Id)
	if err != nil {
		return nil, err
	}
	return api.TemplatesGet200JSONResponse(v), nil
}

func apiVersion(v registry.Version) api.RegistryVersion {
	tags := v.Tags
	if tags == nil {
		tags = []string{}
	}
	return api.RegistryVersion{
		Kind: api.RegistryKind(v.Kind), Id: v.ID, CollectionId: v.CollectionID, Name: v.Name, Version: v.Version,
		State: api.VersionState(v.State), Tags: tags, Licence: v.Licence, Fingerprint: v.Fingerprint,
		Actor: apiActor(v.Actor), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

func apiSummary(s registry.Summary) api.VersionSummary {
	return api.VersionSummary{Id: s.ID, Version: s.Version, State: api.VersionState(s.State), CreatedAt: s.CreatedAt}
}

func apiCollection(c registry.Collection) api.Collection {
	out := api.Collection{
		Id: c.ID, Kind: api.RegistryKind(c.Kind), Name: c.Name, Description: c.Description, Tags: c.Tags,
		Licence: c.Licence, VersionCount: c.VersionCount, CreatedAt: c.CreatedAt,
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if c.Latest != nil {
		l := apiSummary(*c.Latest)
		out.Latest = &l
	}
	if c.Versions != nil {
		vs := make([]api.VersionSummary, 0, len(c.Versions))
		for _, v := range c.Versions {
			vs = append(vs, apiSummary(v))
		}
		out.Versions = &vs
	}
	return out
}

// ---------------------------------------------------------------- adoptions and aliases

// ProjectsAdopt implements projects.adopt.
func (s *Server) ProjectsAdopt(ctx context.Context, req api.ProjectsAdoptRequestObject) (api.ProjectsAdoptResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	pr, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, pr.ID); err != nil {
		return nil, err
	}
	// An auxiliary model (R26) is adopted by a registry-scope approval the admin decides, for everyone: the policy
	// engine sees the version's kind (preset rule auxiliary-adoption) and no project, and a licence that forbids
	// commercial use of the outputs is refused before anyone is asked.
	v, err := registry.GetVersion(ctx, s.Pool, "", req.Body.Version)
	auxiliaryAdoption := err == nil && v.Kind == registry.KindAuxiliary
	if auxiliaryAdoption {
		if err := auxiliary.CheckAdoption(v); err != nil {
			return nil, err
		}
	} else {
		ctx = commands.WithProject(ctx, pr.ID) // project work: the policy engine, audit and approvals see the project
	}
	cmd := command(ctx, "projects.adopt", req.Params.IdempotencyKey, req.Params.DryRun)
	if auxiliaryAdoption {
		if cmd.PathParams == nil {
			cmd.PathParams = map[string]string{}
		}
		cmd.PathParams["kind"] = registry.KindAuxiliary
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		var purpose string
		if req.Body.Purpose != nil {
			purpose = string(*req.Body.Purpose)
		}
		a, p, drafts, err := registry.Adopt(ctx, tx, req.P, rev, req.Body.Version, purpose, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := auxiliary.CheckAdoption(a.Version); err != nil {
			return commands.Result{}, nil, err
		}
		// Adopting a golden set re-runs the leakage check against what the project trained on (spec 02).
		if err := goldensets.CheckAdoption(ctx, tx, p.ID, a.Version); err != nil {
			return commands.Result{}, nil, err
		}
		if s.Projects != nil { // data.lock lists every adopted version
			more, err := s.Projects.CommitFacts(ctx, tx, p, cmd.Actor, "adopt "+a.Version.Name+" "+a.Version.Version, cmd.DryRun)
			if err != nil {
				return commands.Result{}, nil, err
			}
			drafts = append(drafts, more...)
		}
		return commands.Result{Status: http.StatusOK, Body: apiAdoption(a), ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

func apiAdoption(a registry.Adoption) api.Adoption {
	aliases := a.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	return api.Adoption{ProjectId: a.ProjectID, Version: apiVersion(a.Version), Actor: apiActor(a.Actor), AdoptedAt: a.AdoptedAt, Aliases: aliases}
}

// AdoptionsList implements adoptions.list.
func (s *Server) AdoptionsList(ctx context.Context, req api.AdoptionsListRequestObject) (api.AdoptionsListResponseObject, error) {
	p, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return nil, err
	}
	var kind string
	if req.Params.Kind != nil {
		kind = string(*req.Params.Kind)
	}
	list, err := registry.ListAdoptions(ctx, s.Pool, p.ID, kind)
	if err != nil {
		return nil, err
	}
	out := api.AdoptionsList200JSONResponse{Items: make([]api.Adoption, 0, len(list))}
	for _, a := range list {
		out.Items = append(out.Items, apiAdoption(a))
	}
	return out, nil
}

// AliasesList implements aliases.list.
func (s *Server) AliasesList(ctx context.Context, req api.AliasesListRequestObject) (api.AliasesListResponseObject, error) {
	p, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return nil, err
	}
	list, err := registry.ListAliases(ctx, s.Pool, p.ID)
	if err != nil {
		return nil, err
	}
	out := api.AliasesList200JSONResponse{Items: make([]api.Alias, 0, len(list))}
	for _, a := range list {
		out.Items = append(out.Items, apiAlias(a))
	}
	return out, nil
}

// AliasesGet implements aliases.get.
func (s *Server) AliasesGet(ctx context.Context, req api.AliasesGetRequestObject) (api.AliasesGetResponseObject, error) {
	p, err := projects.Get(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, p.ID); err != nil {
		return nil, err
	}
	a, err := registry.GetAlias(ctx, s.Pool, p.ID, req.Name)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(a.Rev)
	return api.AliasesGet200JSONResponse{Body: apiAlias(a), Headers: api.AliasesGet200ResponseHeaders{ETag: &etag}}, nil
}

// AliasesSet implements aliases.set. production answers reserved-alias (R8). Moving baseline is gated: the
// contract marks it (x-cadence.gatedWhen) and registry.Gated names the rule; until the policy engine lands in the
// command pipeline it answers like any other alias.
func (s *Server) AliasesSet(ctx context.Context, req api.AliasesSetRequestObject) (api.AliasesSetResponseObject, error) {
	var ifMatch *int
	if req.Params.IfMatch != nil {
		rev, err := commands.ParseIfMatch(*req.Params.IfMatch)
		if err != nil {
			return nil, err
		}
		ifMatch = &rev
	}
	// Aliases are project work: a gated move of baseline is a project-scope approval, not a registry one.
	ctx, err := s.withProject(ctx, req.P)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, "aliases.set", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, req.P)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := auth.CheckProject(ctx, p.ID); err != nil {
			return commands.Result{}, nil, err
		}
		a, drafts, err := registry.SetAlias(ctx, tx, p.ID, req.Name, ifMatch, req.Body.Version, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiAlias(a), ETag: commands.ETag(a.Rev)}, drafts, nil
	})
}

func apiAlias(a registry.Alias) api.Alias {
	return api.Alias{
		Id: a.ID, Name: a.Name, ProjectId: a.ProjectID, Version: apiVersion(a.Version),
		Reserved: api.AliasReserved(registry.Reservation(a.Name)), Rev: a.Rev, Actor: apiActor(a.Actor), UpdatedAt: a.UpdatedAt,
	}
}

// ---------------------------------------------------------------- compute

// ComputeList implements compute.list.
func (s *Server) ComputeList(ctx context.Context, _ api.ComputeListRequestObject) (api.ComputeListResponseObject, error) {
	list, err := compute.List(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	if err := compute.WithTelemetry(ctx, s.Pool, list); err != nil {
		return nil, err
	}
	out := api.ComputeList200JSONResponse{Items: make([]api.ComputeHost, 0, len(list))}
	for _, h := range list {
		out.Items = append(out.Items, apiHost(h))
	}
	return out, nil
}

// ComputeGet implements compute.get.
func (s *Server) ComputeGet(ctx context.Context, req api.ComputeGetRequestObject) (api.ComputeGetResponseObject, error) {
	h, err := compute.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	hosts := []compute.Host{h}
	if err := compute.WithTelemetry(ctx, s.Pool, hosts); err != nil {
		return nil, err
	}
	h = hosts[0]
	etag := commands.ETag(h.Rev)
	return api.ComputeGet200JSONResponse{Body: apiHost(h), Headers: api.ComputeGet200ResponseHeaders{ETag: &etag}}, nil
}

// ComputeEdit implements compute.edit.
func (s *Server) ComputeEdit(ctx context.Context, req api.ComputeEditRequestObject) (api.ComputeEditResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	in := compute.EditInput{Description: req.Body.Description}
	for _, c := range deref(req.Body.Cards) {
		e := compute.CardEdit{Index: c.Index, Name: c.Name, CardClass: c.CardClass, MemoryGB: c.MemoryGb, MemoryCapGB: c.MemoryCapGb,
			ServingReserveGB: c.ServingReserveGb}
		if c.AllowedJobKinds != nil {
			kinds := make([]string, 0, len(*c.AllowedJobKinds))
			for _, k := range *c.AllowedJobKinds {
				kinds = append(kinds, string(k))
			}
			e.AllowedJobKinds = &kinds
		}
		if c.Windows != nil {
			ws := compute.Windows{}
			for kind, list := range *c.Windows {
				for _, w := range list {
					days := make([]string, 0, len(w.Days))
					for _, d := range w.Days {
						days = append(days, string(d))
					}
					ws[kind] = append(ws[kind], compute.Window{Days: days, Start: w.Start, End: w.End, Timezone: deref(w.Timezone)})
				}
				if ws[kind] == nil {
					ws[kind] = []compute.Window{}
				}
			}
			e.Windows = &ws
		}
		in.Cards = append(in.Cards, e)
	}
	return s.run(ctx, command(ctx, "compute.edit", req.Params.IdempotencyKey, req.Params.DryRun), func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		h, drafts, err := compute.Edit(ctx, tx, req.Id, rev, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiHost(h), ETag: commands.ETag(h.Rev)}, drafts, nil
	})
}

func apiHost(h compute.Host) api.ComputeHost {
	out := api.ComputeHost{
		Id: h.ID, Name: h.Name, Description: h.Description, Rev: h.Rev, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
		Cards:  make([]api.ComputeCard, 0, len(h.Cards)),
		Health: api.ComputeHealth{State: api.ComputeHealthState(h.Health.State), CheckedAt: h.Health.CheckedAt, Detail: optional(h.Health.Detail)},
	}
	for _, c := range h.Cards {
		kinds := make([]api.JobKind, 0, len(c.AllowedJobKinds))
		for _, k := range c.AllowedJobKinds {
			kinds = append(kinds, api.JobKind(k))
		}
		card := api.ComputeCard{
			Index: c.Index, Name: c.Name, CardClass: c.CardClass, MemoryGb: c.MemoryGB, MemoryCapGb: c.MemoryCapGB, AllowedJobKinds: kinds,
			ServingReserveGb: optionalFloat(c.ServingReserveGB),
		}
		if len(c.Windows) > 0 {
			ws := api.AvailabilityWindows{}
			for kind, list := range c.Windows {
				for _, w := range list {
					days := make([]api.AvailabilityWindowDays, 0, len(w.Days))
					for _, d := range w.Days {
						days = append(days, api.AvailabilityWindowDays(d))
					}
					ws[kind] = append(ws[kind], api.AvailabilityWindow{Days: days, Start: w.Start, End: w.End, Timezone: optional(w.Timezone)})
				}
			}
			card.Windows = &ws
		}
		if t := c.Telemetry; t != nil {
			card.Telemetry = &api.CardTelemetryReport{
				Index: t.Index, Name: optional(t.Name), MemoryTotalMb: t.MemoryTotalMB, MemoryUsedMb: t.MemoryUsedMB,
				Utilization: f32(t.Utilization), TemperatureC: f32(t.TemperatureC), PowerW: f32(t.PowerW), ReportedAt: t.ReportedAt,
			}
		}
		out.Cards = append(out.Cards, card)
	}
	return out
}

// ---------------------------------------------------------------- secrets

// SecretsList implements secrets.list: names, kinds, scopes and last use; never values.
func (s *Server) SecretsList(ctx context.Context, _ api.SecretsListRequestObject) (api.SecretsListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	list, err := secrets.List(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	out := api.SecretsList200JSONResponse{Items: make([]api.Secret, 0, len(list))}
	for _, sec := range list {
		out.Items = append(out.Items, apiSecret(sec))
	}
	return out, nil
}

// SecretsNew implements secrets.new. The value goes to the encrypted store and nowhere else: the response, the
// event and the idempotency record carry only metadata, and the request fingerprint replaces the value with a MAC
// under the master key.
func (s *Server) SecretsNew(ctx context.Context, req api.SecretsNewRequestObject) (api.SecretsNewResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	if s.Secrets == nil {
		return nil, fmt.Errorf("secrets.new: no secret store configured")
	}
	value := []byte(deref(req.Body.Value))
	if len(value) == 0 {
		return nil, problems.Validation([]problems.FieldError{{Path: "/value", Message: "required"}})
	}
	if req.Body.Kind == api.SecretKind(promotions.SecretKind) {
		return nil, problems.Validation([]problems.FieldError{{Path: "/kind",
			Message: "signing keys are generated by Cadence (cadence admin rotate-signing-key), never stored by hand"}})
	}
	in := secrets.NewInput{Name: req.Body.Name, Kind: string(req.Body.Kind), Scope: deref(req.Body.Scope), Value: value}
	cmd := command(ctx, "secrets.new", req.Params.IdempotencyKey, req.Params.DryRun)
	cmd.RequestHash = secretRequestHash(in, s.Secrets.RequestMAC(value))
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		sec, drafts, err := s.Secrets.Create(ctx, tx, in, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: apiSecret(sec), ETag: commands.ETag(sec.Rev)}, drafts, nil
	})
}

func secretRequestHash(in secrets.NewInput, mac string) string {
	body, _ := json.Marshal(map[string]string{"name": in.Name, "kind": in.Kind, "scope": in.Scope, "valueMac": mac})
	return commands.HashRequest(http.MethodPost, "/secrets", nil, "", body)
}

func apiSecret(sec secrets.Secret) api.Secret {
	return api.Secret{
		Id: sec.ID, Name: sec.Name, Kind: api.SecretKind(sec.Kind), Scope: sec.Scope, Rev: sec.Rev,
		Actor: apiActor(sec.Actor), CreatedAt: sec.CreatedAt, LastUsedAt: sec.LastUsedAt,
	}
}

// ---------------------------------------------------------------- defaults and policies

// DefaultsGet implements defaults.get; the ETag is the defaults' version.
func (s *Server) DefaultsGet(_ context.Context, _ api.DefaultsGetRequestObject) (api.DefaultsGetResponseObject, error) {
	d := s.defaultsDoc()
	raw, err := d.JSON()
	if err != nil {
		return nil, err
	}
	var body api.Defaults
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("defaults.yaml does not match the contract's Defaults: %w", err)
	}
	etag := strconv.Quote(strconv.Itoa(d.Version))
	return api.DefaultsGet200JSONResponse{Body: body, Headers: api.DefaultsGet200ResponseHeaders{ETag: &etag}}, nil
}

// PoliciesGet implements policies.get.
func (s *Server) PoliciesGet(ctx context.Context, _ api.PoliciesGetRequestObject) (api.PoliciesGetResponseObject, error) {
	p, err := policies.Get(ctx, s.Pool, s.defaultsDoc())
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(p.Rev)
	return api.PoliciesGet200JSONResponse{Body: apiPolicies(p), Headers: api.PoliciesGet200ResponseHeaders{ETag: &etag}}, nil
}

// PoliciesEdit implements policies.edit.
func (s *Server) PoliciesEdit(ctx context.Context, req api.PoliciesEditRequestObject) (api.PoliciesEditResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	var in policies.EditInput
	if b := req.Body.Budgets; b != nil {
		in.GPUHoursPerProjectPerDay, in.AgentTurnsPerSession = b.GpuHoursPerProjectPerDay, b.AgentTurnsPerSession
	}
	in.Timezone = req.Body.Timezone
	return s.run(ctx, command(ctx, "policies.edit", req.Params.IdempotencyKey, req.Params.DryRun), func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, drafts, err := policies.Edit(ctx, tx, rev, in, s.defaultsDoc())
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiPolicies(p), ETag: commands.ETag(p.Rev)}, drafts, nil
	})
}

func apiPolicies(p policies.Policies) api.Policies {
	return api.Policies{
		Rev: p.Rev, UpdatedAt: p.UpdatedAt, Departures: p.Departures, Timezone: p.Timezone,
		Budgets: api.PolicyBudgets{GpuHoursPerProjectPerDay: p.Budgets.GPUHoursPerProjectPerDay, AgentTurnsPerSession: p.Budgets.AgentTurnsPerSession},
	}
}
