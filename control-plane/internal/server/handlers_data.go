package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
)

// Sources and utterances: the minimal data entities of imports (phase 2 · stream D, R18). Registry data: no project.

func (c commandResponse) VisitSourcesEditResponse(w http.ResponseWriter) error    { return c.write(w) }
func (c commandResponse) VisitSourcesArchiveResponse(w http.ResponseWriter) error { return c.write(w) }

// SourcesList implements sources.list.
func (s *Server) SourcesList(ctx context.Context, req api.SourcesListRequestObject) (api.SourcesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	f := data.SourceFilter{Archived: deref(req.Params.Archived), Language: deref(req.Params.Language)}
	if req.Params.Kind != nil {
		f.Kind = string(*req.Params.Kind)
	}
	list, err := data.ListSources(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out := api.SourcesList200JSONResponse{Items: make([]api.Source, 0, len(list))}
	for _, src := range list {
		out.Items = append(out.Items, apiSource(src))
	}
	return out, nil
}

// SourcesGet implements sources.get.
func (s *Server) SourcesGet(ctx context.Context, req api.SourcesGetRequestObject) (api.SourcesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	src, err := data.GetSource(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(src.Rev)
	return api.SourcesGet200JSONResponse{Body: apiSource(src), Headers: api.SourcesGet200ResponseHeaders{ETag: &etag}}, nil
}

// SourcesEdit implements sources.edit. Clearing a source for training is a person's decision: the default preset
// gates an agent's sources.edit (rule registry-changes), so the pipeline answers 202 with an approval before this
// runs for an agent.
func (s *Server) SourcesEdit(ctx context.Context, req api.SourcesEditRequestObject) (api.SourcesEditResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	in := data.EditInput{Description: req.Body.Description, Licence: req.Body.Licence, TrainingCleared: req.Body.TrainingCleared}
	cmd := command(ctx, "sources.edit", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		src, drafts, err := data.Edit(ctx, tx, req.Id, rev, in, cmd.Actor, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiSource(src), ETag: commands.ETag(src.Rev)}, drafts, nil
	})
}

// SourcesArchive implements sources.archive: the admin's (registry deletion is admin-only, spec 02).
func (s *Server) SourcesArchive(ctx context.Context, req api.SourcesArchiveRequestObject) (api.SourcesArchiveResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	return s.run(ctx, command(ctx, "sources.archive", req.Params.IdempotencyKey, req.Params.DryRun), func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		src, drafts, err := data.Archive(ctx, tx, req.Id, rev, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: apiSource(src), ETag: commands.ETag(src.Rev)}, drafts, nil
	})
}

// UtterancesList implements utterances.list.
func (s *Server) UtterancesList(ctx context.Context, req api.UtterancesListRequestObject) (api.UtterancesListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	f := data.UtteranceFilter{Source: deref(req.Params.Source), Dataset: deref(req.Params.Dataset), Language: deref(req.Params.Language),
		After: deref(req.Params.After), Limit: deref(req.Params.Limit)}
	if req.Params.Split != nil {
		f.Split = string(*req.Params.Split)
	}
	list, next, err := data.ListUtterances(ctx, s.Pool, f)
	if err != nil {
		return nil, err
	}
	out := api.UtterancesList200JSONResponse{Items: make([]api.Utterance, 0, len(list)), Next: optional(next)}
	for _, u := range list {
		out.Items = append(out.Items, apiUtterance(u))
	}
	return out, nil
}

// UtterancesGet implements utterances.get.
func (s *Server) UtterancesGet(ctx context.Context, req api.UtterancesGetRequestObject) (api.UtterancesGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	u, err := data.GetUtterance(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out := apiUtterance(u)
	uris, err := mounts.URIsOf(ctx, s.Pool, u.ID) // where the audio also lives (phase 4 · stream M)
	if err != nil {
		return nil, err
	}
	if len(uris) > 0 {
		out.Uris = &uris
	}
	return api.UtterancesGet200JSONResponse(out), nil
}

func apiSource(src data.Source) api.Source {
	out := api.Source{
		Id: src.ID, Name: src.Name, Description: src.Description, Licence: src.Licence, Kind: api.SourceKind(src.Kind),
		Languages: src.Languages, Url: src.URL, TrainingCleared: src.TrainingCleared, ClearedAt: src.ClearedAt,
		Archived: src.Archived, Rev: src.Rev, Utterances: src.Utterances, Hours: round3(src.Hours), Datasets: src.Datasets,
		CreatedBy: apiActor(src.CreatedBy), CreatedAt: src.CreatedAt, UpdatedAt: src.UpdatedAt,
	}
	if out.Languages == nil {
		out.Languages = []string{}
	}
	sourceHistory(src, &out)
	if out.Datasets == nil {
		out.Datasets = []string{}
	}
	if src.ClearedBy != nil {
		a := apiActor(*src.ClearedBy)
		out.ClearedBy = &a
	}
	return out
}

func apiUtterance(u data.Utterance) api.Utterance {
	out := api.Utterance{
		Id: u.ID, ContentHash: u.ContentHash, SourceId: u.SourceID, SourceName: u.SourceName, Duration: u.Duration,
		Language: u.Language, Speaker: u.Speaker, SampleRate: u.SampleRate, Channels: u.Channels, Bytes: u.Bytes,
		CreatedAt: u.CreatedAt, Transcripts: make([]api.Transcript, 0, len(u.Transcripts)),
	}
	if u.Split != "" {
		sp := api.DatasetSplitName(u.Split)
		out.Split = &sp
	}
	for _, t := range u.Transcripts {
		out.Transcripts = append(out.Transcripts, api.Transcript{Id: t.ID, Text: t.Text, Origin: t.Origin, Confidence: t.Confidence, CreatedAt: t.CreatedAt})
	}
	if u.Fingerprints != nil {
		fp := u.Fingerprints
		out.Fingerprints = &fp
	}
	if u.Datasets != nil {
		ms := make([]api.UtteranceMembership, 0, len(u.Datasets))
		for _, m := range u.Datasets {
			ms = append(ms, api.UtteranceMembership{DatasetVersionId: m.VersionID, Split: api.DatasetSplitName(m.Split), TranscriptId: m.TranscriptID})
		}
		out.Datasets = &ms
	}
	return out
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}
