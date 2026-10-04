package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Ingest, draft dataset versions and freezing (phase 4 · stream D): sources.new, datasets.preview, datasets.freeze,
// utterances.search. Registry data: no project, except the freeze's pipeline run, which runs in the project the
// draft was ingested in.

func (c commandResponse) VisitSourcesNewResponse(w http.ResponseWriter) error      { return c.write(w) }
func (c commandResponse) VisitDatasetsPreviewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitDatasetsFreezeResponse(w http.ResponseWriter) error  { return c.write(w) }

// SourcesNew implements sources.new.
func (s *Server) SourcesNew(ctx context.Context, req api.SourcesNewRequestObject) (api.SourcesNewResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	b := req.Body
	in := data.NewInput{Name: b.Name, Licence: b.Licence, Kind: string(b.Kind), Languages: deref(b.Languages), URL: deref(b.Url),
		Description: deref(b.Description)}
	cmd := command(ctx, "sources.new", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		src, drafts, err := data.New(ctx, tx, in, cmd.Actor, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		status := http.StatusCreated
		if cmd.DryRun {
			status = http.StatusOK
		}
		return commands.Result{Status: status, Body: apiSource(src), ETag: commands.ETag(src.Rev)}, drafts, nil
	})
}

// DatasetsPreview implements datasets.preview: hours per language and split after filters (metadata only).
func (s *Server) DatasetsPreview(ctx context.Context, req api.DatasetsPreviewRequestObject) (api.DatasetsPreviewResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	b := req.Body
	f := data.PreviewFilter{MinDuration: deref(b.MinDuration), MaxDuration: deref(b.MaxDuration),
		MinCharsPerSecond: deref(b.MinCharsPerSecond), MaxCharsPerSecond: deref(b.MaxCharsPerSecond),
		Languages: deref(b.Languages), Origins: deref(b.Origins)}
	for _, sp := range deref(b.Splits) {
		f.Splits = append(f.Splits, string(sp))
	}
	cmd := command(ctx, "datasets.preview", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		pv, err := data.PreviewVersion(ctx, tx, b.Version, f)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: pv}, nil, nil
	})
}

// DatasetsFreeze implements datasets.freeze: the leakage check, then the freeze pipeline run (dataset_freeze in mode
// cut) in the draft's project; its output hook makes the draft frozen. A dry run checks only.
func (s *Server) DatasetsFreeze(ctx context.Context, req api.DatasetsFreezeRequestObject) (api.DatasetsFreezeResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	id := req.Body.Version
	plan, err := data.PlanFreeze(ctx, s.Pool, id)
	if err != nil {
		return nil, err
	}
	if plan.ProjectID != "" {
		if err := auth.CheckProject(ctx, plan.ProjectID); err != nil {
			return nil, err
		}
		ctx = commands.WithProject(ctx, plan.ProjectID)
	}
	cmd := command(ctx, "datasets.freeze", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		plan, err := data.PlanFreeze(ctx, tx, id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		answer := func(runID string) (commands.Result, []events.Draft, error) {
			body, err := freezeView(ctx, tx, id, plan.GoldenSets, runID)
			return commands.Result{Status: http.StatusOK, Body: body}, nil, err
		}
		if plan.Frozen || plan.Running != "" {
			return answer(plan.Running)
		}
		// The cut writes 16 kHz PCM16 audio into the content store, accounted to the freezing project (dry run too).
		adding, err := data.CutBytes(ctx, tx, id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := cache.CheckQuota(ctx, tx, s.defaultsDoc(), plan.ProjectID, adding); err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			return answer("")
		}
		in := pipelines.StartInput{
			ProjectID: plan.ProjectID,
			Pipeline: &pipelines.Pipeline{Name: "dataset-freeze", Description: "Freeze a draft dataset version: cut its segments into the content store",
				Inputs: map[string]string{data.SegmentsType: data.SegmentsType},
				Steps: []pipelines.Step{{ID: "freeze", Kind: plan.Kind, In: map[string]string{data.SegmentsType: pipelines.InputsRef + data.SegmentsType},
					Params: plan.Params}}},
			Inputs: map[string]steps.ArtifactRef{data.SegmentsType: plan.Segments},
			Actor:  cmd.Actor,
		}
		r, drafts, err := s.Pipelines.Start(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := data.MarkFreezing(ctx, tx, id, r.ID, cmd.Actor, time.Now().UTC()); err != nil {
			return commands.Result{}, nil, err
		}
		for _, st := range r.Steps {
			if st.JobID != "" && st.State == pipelines.StepQueued {
				return commands.Result{Status: http.StatusAccepted, Body: api.JobAccepted{JobId: st.JobID}}, drafts, nil
			}
		}
		res, _, err := answer(r.ID) // the cut was reused by its input hash: frozen inside Start
		return res, drafts, err
	})
}

// freezeView is datasets.freeze's 200 body: the version as it is now and the leakage check's result.
func freezeView(ctx context.Context, q storage.Querier, id string, goldenSets int, runID string) (api.DatasetFreeze, error) {
	items, err := datasetVersions(ctx, q, registry.Filter{Kind: registry.KindDataset, IDs: []string{id}})
	if err != nil {
		return api.DatasetFreeze{}, err
	}
	v, err := one(items, registry.KindDataset, id)
	if err != nil {
		return api.DatasetFreeze{}, err
	}
	return api.DatasetFreeze{Version: v, Leakage: api.DatasetLeakage{Passed: true, GoldenSets: goldenSets}, PipelineRunId: optional(runID)}, nil
}

// UtterancesSearch implements utterances.search.
func (s *Server) UtterancesSearch(ctx context.Context, req api.UtterancesSearchRequestObject) (api.UtterancesSearchResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	p := req.Params
	f := data.UtteranceFilter{Source: deref(p.Source), Dataset: deref(p.Dataset), Language: deref(p.Language), After: deref(p.After),
		Limit: deref(p.Limit), Text: deref(p.Q), Speaker: deref(p.Speaker), Origin: deref(p.Origin),
		MinDuration: deref(p.MinDuration), MaxDuration: deref(p.MaxDuration)}
	if p.Split != nil {
		f.Split = string(*p.Split)
	}
	list, next, err := data.ListUtterances(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out := api.UtterancesSearch200JSONResponse{Items: make([]api.Utterance, 0, len(list)), Next: optional(next)}
	for _, u := range list {
		out.Items = append(out.Items, apiUtterance(u))
	}
	return out, nil
}

// sourceHistory renders a source's clearance and ingest history (sources.get only).
func sourceHistory(src data.Source, out *api.Source) {
	if src.Clearances != nil {
		cs := make([]api.SourceClearance, 0, len(src.Clearances))
		for _, c := range src.Clearances {
			change := api.SourceClearanceChange(c.Change)
			cs = append(cs, api.SourceClearance{Change: &change, Licence: c.Licence, TrainingCleared: c.TrainingCleared,
				Actor: apiActor(c.Actor), At: c.At})
		}
		out.Clearances = &cs
	}
	if src.Ingests != nil {
		is := make([]api.SourceIngest, 0, len(src.Ingests))
		for _, i := range src.Ingests {
			frozen := i.Frozen
			is = append(is, api.SourceIngest{DatasetVersionId: i.VersionID, PipelineRunId: optional(i.PipelineRunID),
				ProjectId: optional(i.ProjectID), StepKind: optional(i.StepKind), Frozen: &frozen, Utterances: i.Utterances,
				Hours: round3(i.Hours), At: i.At})
		}
		out.Ingests = &is
	}
}
