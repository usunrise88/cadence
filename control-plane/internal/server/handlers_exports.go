package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/exports"
)

// Interoperability (phase 4 · stream I): datasets.export starts an export of a frozen dataset version as a pipeline
// run in a project; exports.list and exports.get read the exports a project ran. A Hub push is gated for everyone
// (preset rule hub-export matches the body's format, which the handler names to the policy as a parameter).

func (c commandResponse) VisitDatasetsExportResponse(w http.ResponseWriter) error { return c.write(w) }

func exportRequest(b *api.DatasetExportRequest) exports.Request {
	return exports.Request{Version: b.Version, Format: string(b.Format), Project: deref(b.Project), Target: deref(b.Target),
		HubRepo: deref(b.HubRepo), HubPrivate: b.HubPrivate}
}

func apiExportPlan(pl exports.Plan) api.DatasetExportPlan {
	return api.DatasetExportPlan{VersionId: pl.Version.ID, Collection: pl.Version.Name, Version: pl.Version.Version,
		Format: api.DatasetExportFormat(pl.Format), ProjectId: pl.ProjectID, Project: pl.Project, Target: pl.Target,
		StepKind: pl.StepKind, Utterances: pl.Utterances, Hours: round3(pl.Hours), Bytes: pl.Bytes, Licence: pl.Licence,
		Sources: pl.Sources, Approval: pl.Approval, Copies: pl.Copies}
}

func apiExport(x exports.Export) api.DatasetExport {
	out := api.DatasetExport{Id: x.ID, VersionId: optional(x.VersionID), Collection: optional(x.Collection),
		Version: optional(x.Version), ProjectId: x.ProjectID, Format: api.DatasetExportFormat(x.Format), Target: x.Target,
		State: api.DatasetExportState(x.State), PipelineRunId: optional(x.PipelineRunID), StepKind: optional(x.StepKind),
		Artifact: optional(x.Artifact), Files: x.Files, Bytes: x.Bytes, Copies: x.Copies, Error: optional(x.Error),
		CreatedBy: apiActor(x.CreatedBy), ApprovalId: optional(x.ApprovalID), CreatedAt: x.CreatedAt, FinishedAt: x.FinishedAt,
		Rev: x.Rev, JobId: optional(x.JobID), Commit: optional(x.Commit)}
	if x.Format == exports.FormatProject {
		n := x.Versions
		out.Versions = &n
	}
	sample := make([]api.DatasetExportFile, 0, len(x.Sample))
	for _, f := range x.Sample {
		sample = append(sample, api.DatasetExportFile{Path: f.Path, Hash: optional(f.Hash), Bytes: f.Bytes})
	}
	out.Sample = &sample
	if h := x.Hub; h != nil {
		out.Hub = &api.DatasetExportHub{Repo: h.Repo, Commit: optional(h.Commit), Url: optional(h.URL), Private: h.Private}
	}
	return out
}

// DatasetsExport implements datasets.export: the plan (a dry run answers it), then the export's pipeline run in the
// plan's project. The plan is checked before the policy gates a Hub push, so the admin never approves an export that
// would be refused.
func (s *Server) DatasetsExport(ctx context.Context, req api.DatasetsExportRequestObject) (api.DatasetsExportResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	in := exportRequest(req.Body)
	plan, err := exports.PlanExport(ctx, s.Pool, s.defaultsDoc(), in)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, plan.ProjectID); err != nil {
		return nil, err
	}
	if !plan.Approval {
		// Work in the project; a Hub push stays a registry command, so its approval is the admin's (registry scope).
		ctx = commands.WithProject(ctx, plan.ProjectID)
	}
	cmd := command(ctx, exports.Operation, req.Params.IdempotencyKey, req.Params.DryRun)
	if cmd.PathParams == nil {
		cmd.PathParams = map[string]string{}
	}
	cmd.PathParams["format"] = in.Format // policy rules match it (hub-export)
	approval := commands.ReplayedApproval(ctx)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		plan, err := exports.PlanExport(ctx, tx, s.defaultsDoc(), in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: apiExportPlan(plan)}, nil, nil
		}
		x, drafts, err := exports.Start(ctx, tx, s.Pipelines, s.CAS, plan, cmd.Actor, approval)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: apiExport(x), ETag: commands.ETag(x.Rev)}, drafts, nil
	})
}

// ExportsList implements exports.list.
func (s *Server) ExportsList(ctx context.Context, req api.ExportsListRequestObject) (api.ExportsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	list, err := exports.List(ctx, s.Pool, p.ID, deref(req.Params.Version), deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out := api.ExportsList200JSONResponse{Items: make([]api.DatasetExport, 0, len(list))}
	for _, x := range list {
		out.Items = append(out.Items, apiExport(x))
	}
	return out, nil
}

// ExportsGet implements exports.get.
func (s *Server) ExportsGet(ctx context.Context, req api.ExportsGetRequestObject) (api.ExportsGetResponseObject, error) {
	x, err := exports.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, x.ProjectID); err != nil {
		return nil, err
	}
	etag := commands.ETag(x.Rev)
	return api.ExportsGet200JSONResponse{Body: apiExport(x), Headers: api.ExportsGet200ResponseHeaders{ETag: &etag}}, nil
}
