package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Mounts, the local cache and dataset materialisation (phase 4 · stream M). Mounts are registry data: registering
// one is a registry-scope approval the admin decides (preset rule mount-registration, everyone); scans run in the
// control plane, health checks on a worker (mount_check@1). The cache is the content store: storage.get accounts it,
// datasets.evict and datasets.materialize move a dataset version's shards out of it and back.

func (c commandResponse) VisitMountsNewResponse(w http.ResponseWriter) error     { return c.write(w) }
func (c commandResponse) VisitMountsScanResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitMountsVerifyResponse(w http.ResponseWriter) error  { return c.write(w) }
func (c commandResponse) VisitDatasetsEvictResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitDatasetsMaterializeResponse(w http.ResponseWriter) error {
	return c.write(w)
}

func (s *Server) newMounts() *mounts.Service {
	svc := &mounts.Service{Pool: s.Pool, Leases: s.Leases, Log: s.Log, Defaults: s.defaultsDoc}
	if s.Secrets != nil {
		svc.Secrets = s.Secrets
	}
	return svc
}

func (s *Server) newCache() *cache.Service {
	svc := &cache.Service{Pool: s.Pool, CAS: s.CAS, Log: s.Log, Defaults: s.defaultsDoc}
	if s.Secrets != nil {
		svc.Secrets = s.Secrets
	}
	return svc
}

func ptrIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func apiMount(m mounts.Mount) api.Mount {
	uri := m.URIPrefix()
	h := m.Health
	out := api.Mount{
		Id: m.ID, Name: m.Name, Kind: api.MountKind(m.Kind), Root: m.Root, Endpoint: ptrIf(m.Endpoint),
		Region: ptrIf(m.Region), Revision: ptrIf(m.Revision), Credentials: ptrIf(m.Credentials), ReadOnly: m.ReadOnly,
		LicenceHint: m.LicenceHint, Description: m.Description, Uri: &uri, Utterances: m.Utterances, Copies: m.Copies,
		CopyBytes: m.CopyBytes, Rev: m.Rev, CreatedBy: apiActor(m.CreatedBy), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
		Health: api.MountHealth{State: api.MountHealthState(h.State), CheckedAt: h.CheckedAt, Host: ptrIf(h.Host),
			Reachable: h.Reachable, Writable: h.Writable, FreeBytes: h.FreeBytes, TotalBytes: h.TotalBytes,
			ThroughputMBps: h.ThroughputMBps, SampledBytes: h.SampledBytes, Detail: ptrIf(h.Detail), JobId: ptrIf(h.JobID)},
	}
	if inv := m.Inventory; inv != nil {
		entries := make([]api.MountEntry, 0, len(inv.Entries))
		for _, e := range inv.Entries {
			entries = append(entries, api.MountEntry{Path: e.Path, Files: e.Files, Bytes: e.Bytes})
		}
		out.Inventory = &api.MountInventory{ScannedAt: inv.ScannedAt, Path: ptrIf(inv.Path), Files: inv.Files,
			Bytes: inv.Bytes, Truncated: &inv.Truncated, Entries: entries, Blobs: inv.Blobs, BlobBytes: inv.BlobBytes,
			JobId: ptrIf(inv.JobID)}
	}
	return out
}

// MountsList implements mounts.list.
func (s *Server) MountsList(ctx context.Context, _ api.MountsListRequestObject) (api.MountsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	list, err := mounts.List(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	out := api.MountsList200JSONResponse{Items: make([]api.Mount, 0, len(list))}
	for _, m := range list {
		out.Items = append(out.Items, apiMount(m))
	}
	return out, nil
}

// MountsGet implements mounts.get.
func (s *Server) MountsGet(ctx context.Context, req api.MountsGetRequestObject) (api.MountsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	m, err := mounts.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(m.Rev)
	return api.MountsGet200JSONResponse{Body: apiMount(m), Headers: api.MountsGet200ResponseHeaders{ETag: &etag}}, nil
}

func mountInput(b *api.MountNew) mounts.NewInput {
	return mounts.NewInput{Name: b.Name, Kind: string(b.Kind), Root: b.Root, Endpoint: deref(b.Endpoint),
		Region: deref(b.Region), Revision: deref(b.Revision), Credentials: deref(b.Credentials),
		LicenceHint: deref(b.LicenceHint), Description: deref(b.Description), ReadOnly: b.ReadOnly}
}

// MountsNew implements mounts.new. The request is validated before the policy gates it (so the admin never
// approves a mount that would be refused; the check rolls back); the approved replay registers the mount (201) and
// queues its first health check.
func (s *Server) MountsNew(ctx context.Context, req api.MountsNewRequestObject) (api.MountsNewResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	cmd := command(ctx, mounts.Operation, req.Params.IdempotencyKey, req.Params.DryRun)
	in := mountInput(req.Body)
	approval := commands.ReplayedApproval(ctx)
	if !cmd.DryRun && approval == "" {
		if _, err := mounts.Validate(ctx, s.Pool, in); err != nil {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		if cmd.DryRun {
			m, err := mounts.Validate(ctx, tx, in)
			if err != nil {
				return commands.Result{}, nil, err
			}
			m.CreatedBy, m.Rev = cmd.Actor, 1
			return commands.Result{Status: http.StatusOK, Body: apiMount(m)}, nil, nil
		}
		m, drafts, err := mounts.Create(ctx, tx, in, cmd.Actor, approval, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		if s.Jobs != nil {
			if _, more, err := s.mounts.EnqueueCheck(ctx, tx, m); err == nil {
				drafts = append(drafts, more...)
				if m, err = mounts.Get(ctx, tx, m.ID); err != nil {
					return commands.Result{}, nil, err
				}
			} else {
				s.Log.WarnContext(ctx, "queue the first mount health check", "mount", m.Name, "err", err)
			}
		}
		return commands.Result{Status: http.StatusCreated, Body: apiMount(m), ETag: commands.ETag(m.Rev)}, drafts, nil
	})
}

// mountAction runs a mount command that queues a job: the mount at its revision, a dry run answering it as it is.
func (s *Server) mountAction(ctx context.Context, op, id, ifMatch, key string, dry *bool,
	enqueue func(context.Context, pgx.Tx, mounts.Mount) (string, []events.Draft, error)) (commandResponse, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return commandResponse{}, err
	}
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	cmd := command(ctx, op, key, dry)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		m, err := mounts.Get(ctx, tx, id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev("mount", rev, m.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: apiMount(m), ETag: commands.ETag(m.Rev)}, nil, nil
		}
		jobID, drafts, err := enqueue(ctx, tx, m)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: jobID}}, drafts, nil
	})
}

// MountsScan implements mounts.scan: a scan job in the control plane (it queues a health check when it ends).
func (s *Server) MountsScan(ctx context.Context, req api.MountsScanRequestObject) (api.MountsScanResponseObject, error) {
	path := ""
	if req.Body != nil {
		path = deref(req.Body.Path)
	}
	return s.mountAction(ctx, "mounts.scan", req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, m mounts.Mount) (string, []events.Draft, error) {
			j, drafts, err := s.mounts.EnqueueScan(ctx, tx, m, path)
			if err != nil {
				return "", nil, problems.ValidationFailed.New("%v", err)
			}
			return j.ID, drafts, nil
		})
}

// MountsVerify implements mounts.verify: the health check on a worker (an already queued one is answered).
func (s *Server) MountsVerify(ctx context.Context, req api.MountsVerifyRequestObject) (api.MountsVerifyResponseObject, error) {
	return s.mountAction(ctx, "mounts.verify", req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun,
		func(ctx context.Context, tx pgx.Tx, m mounts.Mount) (string, []events.Draft, error) {
			j, drafts, err := s.mounts.EnqueueCheck(ctx, tx, m)
			return j.ID, drafts, err
		})
}

// ---------------------------------------------------------------- the cache

// StorageGet implements storage.get.
func (s *Server) StorageGet(ctx context.Context, _ api.StorageGetRequestObject) (api.StorageGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	u, err := s.cache.Use(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	var out api.StorageGet200JSONResponse
	if err := recode(u, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// recode copies a domain value into its contract type through JSON (the field names are the contract's).
func recode(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := json.Unmarshal(b, to); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// datasetCacheAction runs datasets.evict or datasets.materialize: a dry run answers the plan; a blocked plan is
// refused (artifact-not-evictable, or artifact-missing when shards exist nowhere); otherwise the job is queued.
func (s *Server) datasetCacheAction(ctx context.Context, op, versionID, key string, dry *bool, evict bool) (commandResponse, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return commandResponse{}, err
	}
	cmd := command(ctx, op, key, dry)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		plan := s.cache.PlanMaterialize
		if evict {
			plan = s.cache.PlanEvict
		}
		p, err := plan(ctx, tx, versionID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: p}, nil, nil
		}
		if len(p.Blocked) > 0 {
			if evict {
				return commands.Result{}, nil, problems.ArtifactNotEvictable.New("dataset %s: %s", versionID, joinReasons(p.Blocked))
			}
			return commands.Result{}, nil, problems.ArtifactMissing.New("dataset %s: %s", versionID, joinReasons(p.Blocked))
		}
		enqueue := func() (string, []events.Draft, error) {
			j, d, err := s.cache.EnqueueMaterialize(ctx, tx, versionID)
			return j.ID, d, err
		}
		if evict {
			enqueue = func() (string, []events.Draft, error) {
				j, d, err := s.cache.EnqueueEvict(ctx, tx, []string{versionID}, "")
				return j.ID, d, err
			}
		}
		id, drafts, err := enqueue()
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: id}}, drafts, nil
	})
}

func joinReasons(r []string) string {
	out := ""
	for i, s := range r {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// DatasetsEvict implements datasets.evict.
func (s *Server) DatasetsEvict(ctx context.Context, req api.DatasetsEvictRequestObject) (api.DatasetsEvictResponseObject, error) {
	return s.datasetCacheAction(ctx, "datasets.evict", req.Body.VersionId, req.Params.IdempotencyKey, req.Params.DryRun, true)
}

// DatasetsMaterialize implements datasets.materialize.
func (s *Server) DatasetsMaterialize(ctx context.Context, req api.DatasetsMaterializeRequestObject) (api.DatasetsMaterializeResponseObject, error) {
	return s.datasetCacheAction(ctx, "datasets.materialize", req.Body.VersionId, req.Params.IdempotencyKey, req.Params.DryRun, false)
}
