package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/deployments"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/eviction"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Deployments and shadow replays (phase 5 · stream D4; internal/deployments). deployments.new is a draft any actor
// may take; deployments.promote and deployments.rollback run their checks before the policy is asked (a refusal is
// a 422 with its help page, never an approval), then wait for an approval for everyone (preset rule deployments), and
// the approved replay signs the record and queues its bundle in the deciding transaction. shadowReplays.new is GPU
// spend under the usual policy; the nightly replay runs as the system.

func (c commandResponse) VisitDeploymentsNewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitDeploymentsPromoteResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitDeploymentsRollbackResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitShadowReplaysNewResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// newDeployments wires the deployments service after the export, promotion, delivery and serving services: the
// promotion records' Stager, the deployment lanes of manual tests, and the shadow_report hook.
func (s *Server) newDeployments() {
	s.deployments = &deployments.Service{Pool: s.Pool, CAS: s.CAS, Engine: s.Pipelines, Exports: s.modelExports, Evals: s.evals,
		Promotions: s.promotions, Delivery: s.delivery, Serving: s.serving, Defaults: s.defaultsDoc, Log: s.Log,
		OpenMount: func(ctx context.Context, m mounts.Mount) (mounts.Reader, error) {
			var sec mounts.Secrets
			if s.Secrets != nil {
				sec = s.Secrets
			}
			return mounts.Open(ctx, m, sec, nil)
		}}
	if s.Projects != nil {
		s.deployments.Repo = s.Projects.Repos()
	}
	if s.Eviction != nil {
		ev := s.Eviction
		s.deployments.Evict = func(ctx context.Context, tx pgx.Tx, hashes []string) ([]events.Draft, error) {
			p := eviction.Plan{}
			for _, h := range hashes {
				p.Artifacts = append(p.Artifacts, eviction.Candidate{Hash: h})
			}
			_, drafts, err := ev.Enqueue(ctx, tx, p, "")
			return drafts, err
		}
	}
	s.promotions.Stages = s.deployments
	s.transcriptions.Deployments = s.deployments
	if s.StepHooks != nil {
		s.deployments.Install(s.StepHooks)
	}
}

// decodingSources ship a promotion's boost lists (its deployment step's decoding) in the bundle; a record no
// deployment appended keeps the store's answer.
type decodingSources struct {
	delivery.Sources
	pool storage.Querier
}

func (d decodingSources) Decoding(ctx context.Context, rec promotions.Record) ([]delivery.DecodingFile, error) {
	files, found, err := deployments.DecodingFiles(ctx, d.pool, rec)
	if err != nil || found {
		return files, err
	}
	return d.Sources.Decoding(ctx, rec)
}

// TickShadow starts the nightly shadow replays (a periodic job, every minute).
func (s *Server) TickShadow(ctx context.Context) error { return s.deployments.Tick(ctx) }

// SweepShadow clears the texts of shadow replays past deploy.shadow_artifact_retention_days (a daily periodic job).
func (s *Server) SweepShadow(ctx context.Context) error {
	n, err := s.deployments.Retention(ctx)
	if n > 0 {
		s.Log.InfoContext(ctx, "shadow replay texts past their retention cleared", "nights", n)
	}
	return err
}

// scopedDeployment reads a deployment and checks the request's scope reaches its project.
func (s *Server) scopedDeployment(ctx context.Context, q storage.Querier, id string) (deployments.Deployment, error) {
	d, err := deployments.Get(ctx, q, id)
	if err != nil {
		return d, err
	}
	return d, auth.CheckProject(ctx, d.ProjectID)
}

func (s *Server) apiDeployment(ctx context.Context, q storage.Querier, d deployments.Deployment) (api.Deployment, error) {
	v, err := s.deployments.View(ctx, q, d)
	if err != nil {
		return api.Deployment{}, err
	}
	return convert[api.Deployment](v)
}

// DeploymentsList implements deployments.list.
func (s *Server) DeploymentsList(ctx context.Context, req api.DeploymentsListRequestObject) (api.DeploymentsListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	f := deployments.Filter{All: req.Params.State != nil && *req.Params.State == "all"}
	if req.Params.Stage != nil {
		f.Stage = string(*req.Params.Stage)
	}
	if ref := refOr(req.Params.Version); ref != "" {
		v, err := registry.Resolve(ctx, s.Pool, p.ID, registry.KindModel, ref)
		if err != nil {
			return nil, err
		}
		f.ModelVersionID = v.ID
	}
	list, err := deployments.List(ctx, s.Pool, p.ID, f)
	if err != nil {
		return nil, err
	}
	out := api.DeploymentsList200JSONResponse{Items: make([]api.Deployment, 0, len(list))}
	for _, d := range list {
		ad, err := s.apiDeployment(ctx, s.Pool, d)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, ad)
	}
	return out, nil
}

// DeploymentsGet implements deployments.get.
func (s *Server) DeploymentsGet(ctx context.Context, req api.DeploymentsGetRequestObject) (api.DeploymentsGetResponseObject, error) {
	d, err := s.scopedDeployment(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	ad, err := s.apiDeployment(ctx, s.Pool, d)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(d.Rev)
	return api.DeploymentsGet200JSONResponse{Body: ad, Headers: api.DeploymentsGet200ResponseHeaders{ETag: &etag}}, nil
}

// DeploymentsNew implements deployments.new: a shadow deployment on the staging target.
func (s *Server) DeploymentsNew(ctx context.Context, req api.DeploymentsNewRequestObject) (api.DeploymentsNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	in := deployments.NewInput{ProjectID: p.ID}
	if b := req.Body; b != nil {
		in.Version, in.Profile, in.Format, in.Against = b.Version, refOr(b.Profile), refOr(b.Format), refOr(b.Against)
		in.Replay = deployments.ReplayConfig{Mount: b.Replay.Mount, Path: deref(b.Replay.Path), Source: b.Replay.Source,
			Language: deref(b.Replay.Language)}
		if b.Replay.ChannelRoles != nil {
			for _, r := range *b.Replay.ChannelRoles {
				in.Replay.ChannelRoles = append(in.Replay.ChannelRoles, string(r))
			}
		}
	}
	cmd := command(ctx, deployments.OpNew, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		in.Actor = cmd.Actor
		d, err := s.deployments.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			v, err := s.deployments.View(ctx, tx, d)
			if err != nil {
				return commands.Result{}, nil, err
			}
			return commands.Result{Status: http.StatusOK, Body: v}, nil, nil
		}
		d, drafts, err := s.deployments.Create(ctx, tx, d)
		if err != nil {
			return commands.Result{}, nil, err
		}
		v, err := s.deployments.View(ctx, tx, d)
		return commands.Result{Status: http.StatusCreated, Body: v, ETag: commands.ETag(d.Rev)}, drafts, err
	})
}

func promoteInput(b *api.DeploymentPromote) deployments.PromoteInput {
	if b == nil {
		return deployments.PromoteInput{}
	}
	in := deployments.PromoteInput{Stage: string(b.Stage), Target: refOr(b.Target), Slot: refOr(b.Slot), Reason: strings.TrimSpace(b.Reason),
		TrafficShare: b.TrafficShare}
	if b.Decoding != nil && b.Decoding.BoostLists != nil {
		lists := make([]deployments.BoostRequest, 0, len(*b.Decoding.BoostLists))
		for _, l := range *b.Decoding.BoostLists {
			lists = append(lists, deployments.BoostRequest{Locale: l.Locale, Domain: l.Domain, Ref: deref(l.Ref), Weight: l.Weight})
		}
		in.BoostLists = &lists
	}
	return in
}

// DeploymentsPromote implements deployments.promote.
func (s *Server) DeploymentsPromote(ctx context.Context, req api.DeploymentsPromoteRequestObject) (api.DeploymentsPromoteResponseObject, error) {
	in := promoteInput(req.Body)
	r, err := s.promote(ctx, req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun, deployments.OpPromote, in.Reason,
		func(ctx context.Context, q storage.Querier, d deployments.Deployment) (deployments.Plan, error) {
			return s.deployments.PlanPromotion(ctx, q, d, in)
		})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeploymentsRollback implements deployments.rollback.
func (s *Server) DeploymentsRollback(ctx context.Context, req api.DeploymentsRollbackRequestObject) (api.DeploymentsRollbackResponseObject, error) {
	reason := ""
	if req.Body != nil {
		reason = strings.TrimSpace(req.Body.Reason)
	}
	r, err := s.promote(ctx, req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun, deployments.OpRollback, reason,
		func(ctx context.Context, q storage.Querier, d deployments.Deployment) (deployments.Plan, error) {
			return s.deployments.PlanRollback(ctx, q, d)
		})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// promote runs a promotion or rollback: the checks outside the command (a refusal never becomes an approval), then
// the command — a dry run answers every check, the approved replay signs and queues the bundle.
func (s *Server) promote(ctx context.Context, id string, ifMatch api.IfMatch, key api.IdempotencyKey, dryRun *api.DryRun, op, reason string,
	plan func(context.Context, storage.Querier, deployments.Deployment) (deployments.Plan, error)) (commandResponse, error) {
	d, err := s.scopedDeployment(ctx, s.Pool, id)
	if err != nil {
		return commandResponse{}, err
	}
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	ctx = commands.WithProject(ctx, d.ProjectID)
	cmd := command(ctx, op, key, dryRun)
	if !cmd.DryRun && commands.ReplayedApproval(ctx) == "" {
		if err := commands.CheckRev("deployment", rev, d.Rev); err != nil {
			return commandResponse{}, err
		}
		pl, err := plan(ctx, s.Pool, d)
		if err != nil {
			return commandResponse{}, err
		}
		if err := pl.Refusal(); err != nil {
			return commandResponse{}, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		d, err := deployments.Lock(ctx, tx, id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev("deployment", rev, d.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		pl, err := plan(ctx, tx, d)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			out, err := convert[api.DeploymentPromotion](pl)
			return commands.Result{Status: http.StatusOK, Body: out, ETag: commands.ETag(d.Rev)}, nil, err
		}
		actor, approver := replayActors(ctx)
		nd, rec, drafts, err := s.deployments.Apply(ctx, tx, pl, reason, actor, commands.ReplayedApproval(ctx), approver)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.DeploymentPromotion](pl)
		if err != nil {
			return commands.Result{}, nil, err
		}
		ad, err := s.apiDeployment(ctx, tx, nd)
		if err != nil {
			return commands.Result{}, nil, err
		}
		ar, err := s.apiRecord(ctx, rec, false)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out.Deployment, out.Record = &ad, &ar
		return commands.Result{Status: http.StatusOK, Body: out, ETag: commands.ETag(nd.Rev)}, drafts, nil
	})
}

// ShadowReplaysList implements shadowReplays.list.
func (s *Server) ShadowReplaysList(ctx context.Context, req api.ShadowReplaysListRequestObject) (api.ShadowReplaysListResponseObject, error) {
	d, err := s.scopedDeployment(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	list, err := deployments.Replays(ctx, s.Pool, d.ID, deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	items, err := convert[[]api.ShadowReplay](list)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []api.ShadowReplay{}
	}
	return api.ShadowReplaysList200JSONResponse{Items: items}, nil
}

// ShadowReplaysGet implements shadowReplays.get.
func (s *Server) ShadowReplaysGet(ctx context.Context, req api.ShadowReplaysGetRequestObject) (api.ShadowReplaysGetResponseObject, error) {
	r, err := deployments.GetReplay(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if err := auth.CheckProject(ctx, r.ProjectID); err != nil {
		return nil, err
	}
	out, err := convert[api.ShadowReplay](r)
	if err != nil {
		return nil, err
	}
	return api.ShadowReplaysGet200JSONResponse(out), nil
}

// ShadowReplaysNew implements shadowReplays.new: a replay now, weighed against the GPU budget like any spend.
func (s *Server) ShadowReplaysNew(ctx context.Context, req api.ShadowReplaysNewRequestObject) (api.ShadowReplaysNewResponseObject, error) {
	d, err := s.scopedDeployment(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, d.ProjectID)
	actor, _ := auth.FromContext(ctx)
	ctx, weighed := weigh(ctx, func() (deployments.Replay, error) { return s.deployments.PlanReplay(ctx, s.Pool, d, actor) })
	cmd := command(ctx, deployments.OpReplayNew, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		d, err := deployments.Lock(ctx, tx, d.ID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			r, err := s.deployments.PlanReplay(ctx, tx, d, cmd.Actor)
			return commands.Result{Status: http.StatusOK, Body: r}, nil, err
		}
		if !weighed {
			return commands.Result{}, nil, unweighed(cmd.Operation)
		}
		r, drafts, err := s.deployments.StartReplay(ctx, tx, d, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: r}, drafts, nil
	})
}
