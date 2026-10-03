package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Golden sets and scoring normalizers (phase 3 · stream G). Registry data: no project; freezing a golden set is a
// registry-scope approval the admin decides (preset rule golden-set-freeze, everyone).

func (c commandResponse) VisitGoldenSetsFreezeResponse(w http.ResponseWriter) error {
	return c.write(w)
}

func normalizerVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.NormalizerVersion, error) {
	common, payloads, used, err := versions[api.NormalizerPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.NormalizerVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.NormalizerVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, Normalizer: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// NormalizersList implements normalizers.list.
func (s *Server) NormalizersList(ctx context.Context, req api.NormalizersListRequestObject) (api.NormalizersListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := normalizerVersions(ctx, s.Pool, versionFilter(registry.KindNormalizer, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.NormalizersList200JSONResponse{Items: items}, nil
}

// NormalizersGet implements normalizers.get.
func (s *Server) NormalizersGet(ctx context.Context, req api.NormalizersGetRequestObject) (api.NormalizersGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := normalizerVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindNormalizer, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindNormalizer, req.Id)
	if err != nil {
		return nil, err
	}
	return api.NormalizersGet200JSONResponse(v), nil
}

func goldenSetVersions(ctx context.Context, q storage.Querier, f registry.Filter) ([]api.GoldenSetVersion, error) {
	common, payloads, used, err := versions[api.GoldenSetPayload](ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := make([]api.GoldenSetVersion, 0, len(common))
	for i, v := range common {
		out = append(out, api.GoldenSetVersion{
			Actor: v.Actor, CollectionId: v.CollectionId, CreatedAt: v.CreatedAt, Fingerprint: v.Fingerprint, Id: v.Id,
			Kind: v.Kind, Licence: v.Licence, Name: v.Name, State: v.State, Tags: v.Tags, UpdatedAt: v.UpdatedAt,
			Version: v.Version, GoldenSet: payloads[i], UsedBy: used[i],
		})
	}
	return out, nil
}

// GoldenSetsList implements goldenSets.list.
func (s *Server) GoldenSetsList(ctx context.Context, req api.GoldenSetsListRequestObject) (api.GoldenSetsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := goldenSetVersions(ctx, s.Pool, versionFilter(registry.KindGoldenSet, req.Params.Collection, req.Params.State))
	if err != nil {
		return nil, err
	}
	return api.GoldenSetsList200JSONResponse{Items: items}, nil
}

// GoldenSetsGet implements goldenSets.get.
func (s *Server) GoldenSetsGet(ctx context.Context, req api.GoldenSetsGetRequestObject) (api.GoldenSetsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	items, err := goldenSetVersions(ctx, s.Pool, registry.Filter{Kind: registry.KindGoldenSet, IDs: []string{req.Id}})
	if err != nil {
		return nil, err
	}
	v, err := one(items, registry.KindGoldenSet, req.Id)
	if err != nil {
		return nil, err
	}
	return api.GoldenSetsGet200JSONResponse(v), nil
}

// GoldenSetsFreeze implements goldenSets.freeze. The request is checked before the policy gates it, so the admin is
// never asked to approve a freeze that would be refused (the check rolls back; the approved replay checks again in
// its own transaction). A dry run answers the would-be version; the approved request registers it (201), or answers
// the version the same content was already frozen as (200).
func (s *Server) GoldenSetsFreeze(ctx context.Context, req api.GoldenSetsFreezeRequestObject) (api.GoldenSetsFreezeResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, goldensets.Operation, req.Params.IdempotencyKey, req.Params.DryRun)
	in := goldensets.FreezeInput{
		Dataset: req.Body.DatasetVersionId, Normalizer: deref(req.Body.NormalizerVersionId), Name: deref(req.Body.Name),
		Domain: deref(req.Body.Domain), Actor: cmd.Actor, ApprovalID: commands.ReplayedApproval(ctx),
	}
	if req.Body.Groups != nil {
		in.Groups = string(*req.Body.Groups)
	}
	if !cmd.DryRun && in.ApprovalID == "" {
		if err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := goldensets.Prepare(ctx, tx, in, s.defaultsDoc()); err != nil {
				return err
			}
			return errCheckOnly
		}); !errors.Is(err, errCheckOnly) {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		v, created, drafts, err := goldensets.Freeze(ctx, tx, in, s.defaultsDoc(), time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		items, err := goldenSetVersions(ctx, tx, registry.Filter{Kind: registry.KindGoldenSet, IDs: []string{v.ID}})
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := one(items, registry.KindGoldenSet, v.ID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		return commands.Result{Status: status, Body: out}, drafts, nil
	})
}
