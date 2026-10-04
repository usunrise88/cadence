package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/annotation"
	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// ---------------------------------------------------------------- annotation (phase 4 · stream A)
//
// batches.new|get|list|freeze, batchItems.list|get|accept, annotations.new, invitations.new|list, auth.accept,
// triage.accept|correct|reject and tracks.get. The domain lives in internal/annotation; reviewer invitations in
// internal/credentials. A reviewer's session reaches one batch (auth.Scope.Batch): reviewerOnly keeps it inside the
// batch's operations and the media of its items.

func (c commandResponse) VisitBatchesNewResponse(w http.ResponseWriter) error     { return c.write(w) }
func (c commandResponse) VisitBatchesFreezeResponse(w http.ResponseWriter) error  { return c.write(w) }
func (c commandResponse) VisitAnnotationsNewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitBatchItemsAcceptResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitInvitationsNewResponse(w http.ResponseWriter) error { return c.write(w) }
func (c commandResponse) VisitTriageAcceptResponse(w http.ResponseWriter) error   { return c.write(w) }
func (c commandResponse) VisitTriageCorrectResponse(w http.ResponseWriter) error  { return c.write(w) }
func (c commandResponse) VisitTriageRejectResponse(w http.ResponseWriter) error   { return c.write(w) }

func (s *Server) newAnnotation() *annotation.Service {
	svc := &annotation.Service{Pool: s.Pool, CAS: s.CAS, Defaults: s.defaultsDoc}
	if s.Projects != nil {
		svc.Repo = s.Projects.Repos()
	}
	return svc
}

// reviewerOnly keeps a reviewer's session (scope: one batch) inside that batch: its document, items and annotations,
// the media of its items (the handlers check the item's batch), sign-in state, sign-out and help. Everything else
// answers forbidden, so a reviewer reaches no other data whatever an operation's own checks are.
func (s *Server) reviewerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.PrincipalFromContext(r.Context()); ok && p.Scope.Reviewer() && !reviewerAllowed(r.Method, r.URL.Path, p.Scope.Batch) {
			s.writeProblem(w, r, problems.Forbidden.New("a reviewer's invitation opens batch %s only: its items and their audio", p.Scope.Batch))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func reviewerAllowed(method, path, batch string) bool {
	p := strings.TrimPrefix(path, APIPrefix)
	get, post := method == http.MethodGet || method == http.MethodHead, method == http.MethodPost
	switch {
	case p == "/auth" && get, p == "/auth:logout" && post, p == "/auth:accept" && post:
		return true
	case (p == "/help" || strings.HasPrefix(p, "/help/")) && get:
		return true
	case p == "/defaults" && get:
		return true // the audio view's settings (views.audio); configuration, no data
	case p == "/batches/"+batch && get, p == "/batches/"+batch+"/guidelines" && get:
		return true // the batch and the guidelines file it pinned (nothing else of the repository)
	case strings.HasPrefix(p, "/batches/"+batch+"/batch-items") && (get || post):
		return true
	case strings.HasPrefix(p, "/registry/utterances/bit_") && (get || post):
		return true // the media handlers check that the item is the reviewer's batch's
	}
	return false
}

// batchViewer is who reads or changes batch id: a reviewer of that batch, or a person or credential that reaches its
// project. It returns the batch's project.
func (s *Server) batchViewer(ctx context.Context, id string) (annotation.Viewer, string, error) {
	projectID, err := annotation.ProjectOf(ctx, s.Pool, id)
	if err != nil {
		return annotation.Viewer{}, "", err
	}
	// An approved request replays with its actor and scope but no credential, so both come from the context.
	actor, ok := auth.FromContext(ctx)
	scope, sok := auth.ScopeFromContext(ctx)
	if !ok || !sok {
		return annotation.Viewer{}, "", problems.Unauthenticated.New("sign in or open your invitation link")
	}
	v := annotation.Viewer{Actor: actor}
	if actor.Kind == auth.KindUser {
		v.UserID = actor.ID
		if p, ok := auth.PrincipalFromContext(ctx); ok && p.UserID != "" {
			v.UserID = p.UserID
		}
	}
	if scope.Reviewer() {
		if scope.Batch != id {
			return annotation.Viewer{}, "", problems.Forbidden.New("your invitation opens batch %s, not %s", scope.Batch, id)
		}
		v.Reviewer, v.Role = true, scope.BatchRole
		return v, projectID, nil
	}
	return v, projectID, auth.CheckProject(ctx, projectID)
}

// BatchesNew implements batches.new.
func (s *Server) BatchesNew(ctx context.Context, req api.BatchesNewRequestObject) (api.BatchesNewResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, p.ID)
	b := req.Body
	cmd := command(ctx, "batches.new", req.Params.IdempotencyKey, req.Params.DryRun)
	in := annotation.NewInput{ProjectID: p.ID, ProjectSlug: p.Slug, Name: b.Name, Description: deref(b.Description),
		Dataset: deref(b.Dataset), Segments: deref(b.Segments), Size: deref(b.Size), DoubleShare: b.DoubleShare,
		Guidelines: deref(b.Guidelines), DueAt: b.DueAt, GoldenSet: deref(b.GoldenSet), ContextS: b.ContextS, Actor: cmd.Actor}
	if b.Purpose != nil {
		in.Purpose = string(*b.Purpose)
	}
	if b.Role != nil {
		in.Role = string(*b.Role)
	}
	if b.Seed != nil {
		in.Seed = int64(*b.Seed)
	}
	if b.Stratify != nil {
		in.Stratify = []string{}
		for _, x := range *b.Stratify {
			in.Stratify = append(in.Stratify, string(x))
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		plan, err := s.annotation.Prepare(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			plan.Batch.Sample = plan.Items
			out, err := convert[api.Batch](plan.Batch)
			return commands.Result{Status: http.StatusOK, Body: out}, nil, err
		}
		v, drafts, err := s.annotation.Create(ctx, tx, plan)
		if err != nil {
			return commands.Result{}, nil, err
		}
		// The items' windows get their peaks now (media.peaks), so an annotator's first view does not compute them.
		peaks, err := s.media.EnqueueBatchPeaks(ctx, tx, v.ID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		drafts = append(drafts, peaks...)
		got, err := s.annotation.Get(ctx, tx, v.ID)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.Batch](got)
		return commands.Result{Status: http.StatusCreated, Body: out, ETag: commands.ETag(got.Rev)}, drafts, err
	})
}

// BatchesList implements batches.list.
func (s *Server) BatchesList(ctx context.Context, req api.BatchesListRequestObject) (api.BatchesListResponseObject, error) {
	p, err := scopedProject(ctx, s.Pool, req.P)
	if err != nil {
		return nil, err
	}
	state := ""
	if req.Params.State != nil {
		state = string(*req.Params.State)
	}
	list, err := s.annotation.List(ctx, s.Pool, p.ID, state, deref(req.Params.Limit))
	if err != nil {
		return nil, err
	}
	out, err := convert[api.BatchList](map[string]any{"items": list})
	if err != nil {
		return nil, err
	}
	return api.BatchesList200JSONResponse(out), nil
}

// BatchesGet implements batches.get.
func (s *Server) BatchesGet(ctx context.Context, req api.BatchesGetRequestObject) (api.BatchesGetResponseObject, error) {
	if _, _, err := s.batchViewer(ctx, req.Id); err != nil {
		return nil, err
	}
	b, err := s.annotation.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.Batch](b)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(b.Rev)
	return api.BatchesGet200JSONResponse{Body: out, Headers: api.BatchesGet200ResponseHeaders{ETag: &etag}}, nil
}

// BatchesFreeze implements batches.freeze: checked before the policy gates it (the admin is never asked to approve a
// freeze that would be refused), then an approval for everyone; the approved replay writes the draft and starts the
// cut.
func (s *Server) BatchesFreeze(ctx context.Context, req api.BatchesFreezeRequestObject) (api.BatchesFreezeResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	v, projectID, err := s.batchViewer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if v.Reviewer {
		return nil, problems.Forbidden.New("a reviewer annotates; the admin freezes the batch")
	}
	ctx = commands.WithProject(ctx, projectID)
	cmd := command(ctx, "batches.freeze", req.Params.IdempotencyKey, req.Params.DryRun)
	approval := commands.ReplayedApproval(ctx)
	if !cmd.DryRun && approval == "" {
		if _, err := s.annotation.CheckFreeze(ctx, s.Pool, req.Id, rev); err != nil {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		res, drafts, err := s.annotation.Freeze(ctx, tx, s.Pipelines, annotation.FreezeInput{BatchID: req.Id, Rev: rev,
			Actor: cmd.Actor, ApprovalID: approval, DryRun: cmd.DryRun})
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.BatchFreeze](res)
		return commands.Result{Status: http.StatusOK, Body: out}, drafts, err
	})
}

// BatchItemsList implements batchItems.list.
func (s *Server) BatchItemsList(ctx context.Context, req api.BatchItemsListRequestObject) (api.BatchItemsListResponseObject, error) {
	v, _, err := s.batchViewer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	queue, state := annotation.QueueMine, ""
	if req.Params.Queue != nil {
		queue = string(*req.Params.Queue)
	}
	if req.Params.State != nil {
		state = string(*req.Params.State)
	}
	items, next, err := s.annotation.ListItems(ctx, s.Pool, req.Id, queue, state, deref(req.Params.Limit), v)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"items": items}
	if next != "" {
		body["next"] = next
	}
	out, err := convert[api.BatchItemList](body)
	if err != nil {
		return nil, err
	}
	return api.BatchItemsList200JSONResponse(out), nil
}

// BatchItemsGet implements batchItems.get.
func (s *Server) BatchItemsGet(ctx context.Context, req api.BatchItemsGetRequestObject) (api.BatchItemsGetResponseObject, error) {
	v, _, err := s.batchViewer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	it, err := s.annotation.GetItem(ctx, s.Pool, req.Id, req.Item, v)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.BatchItem](it)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(it.Rev)
	return api.BatchItemsGet200JSONResponse{Body: out, Headers: api.BatchItemsGet200ResponseHeaders{ETag: &etag}}, nil
}

func entitiesOf(list *[]api.EntitySpan) []annotation.Entity {
	if list == nil {
		return nil
	}
	out := make([]annotation.Entity, 0, len(*list))
	for _, e := range *list {
		out = append(out, annotation.Entity{Start: e.Start, End: e.End, Class: e.Class})
	}
	return out
}

func tagsOf(list *[]api.AnnotationTag) []string {
	if list == nil {
		return nil
	}
	out := make([]string, 0, len(*list))
	for _, t := range *list {
		out = append(out, string(t))
	}
	return out
}

// AnnotationsNew implements annotations.new.
func (s *Server) AnnotationsNew(ctx context.Context, req api.AnnotationsNewRequestObject) (api.AnnotationsNewResponseObject, error) {
	v, projectID, err := s.batchViewer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	cmd := command(ctx, "annotations.new", req.Params.IdempotencyKey, req.Params.DryRun)
	b := req.Body
	in := annotation.AnnotateInput{BatchID: req.Id, ItemID: req.Item, Viewer: v, Status: string(b.Status), Text: deref(b.Text),
		Tags: tagsOf(b.Tags), Entities: entitiesOf(b.Entities), Note: deref(b.Note)}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		it, drafts, err := s.annotation.Annotate(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.BatchItem](it)
		return commands.Result{Status: http.StatusCreated, Body: out, ETag: commands.ETag(it.Rev)}, drafts, err
	})
}

// BatchItemsAccept implements batchItems.accept (adjudication).
func (s *Server) BatchItemsAccept(ctx context.Context, req api.BatchItemsAcceptRequestObject) (api.BatchItemsAcceptResponseObject, error) {
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	v, projectID, err := s.batchViewer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	ctx = commands.WithProject(ctx, projectID)
	cmd := command(ctx, "batchItems.accept", req.Params.IdempotencyKey, req.Params.DryRun)
	b := req.Body
	in := annotation.AdjudicateInput{BatchID: req.Id, ItemID: req.Item, Rev: rev, Viewer: v, From: deref(b.From), Text: b.Text,
		Tags: tagsOf(b.Tags), Entities: entitiesOf(b.Entities), Exclude: deref(b.Exclude)}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		it, drafts, err := s.annotation.Adjudicate(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.BatchItem](it)
		return commands.Result{Status: http.StatusOK, Body: out, ETag: commands.ETag(it.Rev)}, drafts, err
	})
}

func apiInvitation(c credentials.Credential, name string) api.Invitation {
	inv := api.Invitation{Id: c.ID, BatchId: c.Subject, Role: api.InvitationRole(c.Scope.BatchRole), CreatedAt: c.CreatedAt,
		LastUsedAt: c.LastUsedAt, RevokedAt: c.RevokedAt}
	inv.Reviewer.Id, inv.Reviewer.Name = c.UserID, name
	if c.ExpiresAt != nil {
		inv.ExpiresAt = *c.ExpiresAt
	}
	return inv
}

// InvitationsList implements invitations.list (admin).
func (s *Server) InvitationsList(ctx context.Context, req api.InvitationsListRequestObject) (api.InvitationsListResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	if _, err := annotation.ProjectOf(ctx, s.Pool, req.Id); err != nil {
		return nil, err
	}
	list, err := credentials.Invitations(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	out := api.InvitationsList200JSONResponse{Items: make([]api.Invitation, 0, len(list))}
	for _, c := range list {
		name := ""
		if u, err := credentials.GetUser(ctx, s.Pool, c.UserID); err == nil {
			name = u.Name
		}
		out.Items = append(out.Items, apiInvitation(c, name))
	}
	return out, nil
}

// InvitationsNew implements invitations.new (admin): the reviewer and a link that opens this batch only.
func (s *Server) InvitationsNew(ctx context.Context, req api.InvitationsNewRequestObject) (api.InvitationsNewResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	b, err := s.annotation.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if b.State != annotation.StateOpen {
		return nil, problems.BatchClosed.New("batch %s is %s; reviewers are invited to open batches", b.Name, b.State)
	}
	ctx = commands.WithProject(ctx, b.ProjectID)
	cmd := command(ctx, "invitations.new", req.Params.IdempotencyKey, req.Params.DryRun)
	role := credentials.RoleAnnotator
	if req.Body.Role != nil {
		role = string(*req.Body.Role)
	}
	exp := credentials.InvitationExpiry(time.Now(), b.DueAt, s.defaultsDoc().Annotation.InvitationMaxDays.Value, req.Body.ExpiresAt)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		u, err := credentials.EnsureReviewer(ctx, tx, req.Body.Name)
		if err != nil {
			return commands.Result{}, nil, err
		}
		token, c, err := credentials.NewInvitation(ctx, tx, u.ID, u.Name, b.ID, role, exp)
		if err != nil {
			return commands.Result{}, nil, err
		}
		inv := apiInvitation(c, u.Name)
		if cmd.DryRun {
			return commands.Result{Status: http.StatusOK, Body: inv}, nil, nil
		}
		out := api.InvitationCreated{Id: inv.Id, BatchId: inv.BatchId, Reviewer: inv.Reviewer, Role: api.InvitationCreatedRole(inv.Role), ExpiresAt: inv.ExpiresAt,
			CreatedAt: inv.CreatedAt, Token: token, Url: "/#invitation=" + token}
		ev := events.Draft{Topic: annotation.Topic(b.ID), Type: "annotation_batch.reviewer_invited", ProjectID: b.ProjectID,
			Entity:  &events.EntityRef{Kind: annotation.EntityKind, ID: b.ID, Rev: b.Rev},
			Payload: map[string]any{"batchId": b.ID, "reviewer": map[string]string{"id": u.ID, "name": u.Name}, "role": role, "invitationId": c.ID}}
		return commands.Result{Status: http.StatusCreated, Body: out}, []events.Draft{ev}, nil
	})
}

// reviewerStatus is a reviewer's scope as AuthStatus.reviewer.
func (s *Server) reviewerStatus(ctx context.Context, p auth.Principal) *api.ReviewerScope {
	if !p.Scope.Reviewer() {
		return nil
	}
	projectID, err := annotation.ProjectOf(ctx, s.Pool, p.Scope.Batch)
	if err != nil {
		return nil
	}
	rs := &api.ReviewerScope{BatchId: p.Scope.Batch, ProjectId: projectID, Role: api.ReviewerScopeRole(p.Scope.BatchRole)}
	if c, err := credentials.Get(ctx, s.Pool, p.CredentialID); err == nil {
		rs.ExpiresAt = c.ExpiresAt
	}
	return rs
}

// AuthAccept implements auth.accept: an invitation link becomes a reviewer's session for its batch.
func (s *Server) AuthAccept(ctx context.Context, req api.AuthAcceptRequestObject) (api.AuthAcceptResponseObject, error) {
	client := auth.ClientFrom(ctx)
	if retry, ok := s.LoginLimiter.Check("ip:" + client.IP); !ok {
		e := problems.RateLimited.New("too many failed sign-ins; try again in %d seconds", int(retry.Seconds())+1)
		e.RetryAfter = int(retry.Seconds()) + 1
		return nil, e
	}
	var (
		cookie string
		user   credentials.User
		c      credentials.Credential
	)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		name := "Reviewer browser"
		if client.IP != "" {
			name += " at " + client.IP
		}
		token, cred, u, err := credentials.Redeem(ctx, tx, req.Body.Token, name)
		if err != nil {
			return err
		}
		cookie, user, c = auth.SessionCookie(token, client.Secure).String(), u, cred
		return nil
	})
	if pe, ok := problems.As(err); ok && pe.Type == problems.InvitationInvalid {
		s.LoginLimiter.Record("ip:" + client.IP)
	}
	if err != nil {
		return nil, err
	}
	p := auth.Principal{Actor: user.Actor(), Scope: c.Scope, CredentialID: c.ID, CredentialKind: c.Kind, UserID: user.ID}
	a := apiActor(p.Actor)
	st := api.AuthStatus{Actor: &a, Reviewer: s.reviewerStatus(ctx, p)}
	s.Log.InfoContext(ctx, "reviewer signed in", "user", user.ID, "batch", c.Scope.Batch, "ip", client.IP)
	return api.AuthAccept200JSONResponse{Body: st, Headers: api.AuthAccept200ResponseHeaders{SetCookie: &cookie}}, nil
}

// ---------------------------------------------------------------- triage resolutions

func (s *Server) resolveTriage(ctx context.Context, id, ifMatch, key string, dryRun *bool, op string, in annotation.ResolveInput) (commandResponse, error) {
	rev, err := commands.ParseIfMatch(ifMatch)
	if err != nil {
		return commandResponse{}, err
	}
	projectID, err := annotation.TriageProject(ctx, s.Pool, id)
	if err != nil {
		return commandResponse{}, err
	}
	if err := auth.CheckProject(ctx, projectID); err != nil {
		return commandResponse{}, err
	}
	ctx = commands.WithProject(ctx, projectID)
	cmd := command(ctx, op, key, dryRun)
	in.ID, in.Rev, in.Actor = id, rev, cmd.Actor
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		it, drafts, err := s.annotation.Resolve(ctx, tx, in)
		if err != nil {
			return commands.Result{}, nil, err
		}
		out, err := convert[api.TriageItem](it)
		return commands.Result{Status: http.StatusOK, Body: out, ETag: commands.ETag(it.Rev)}, drafts, err
	})
}

// TriageAccept implements triage.accept.
func (s *Server) TriageAccept(ctx context.Context, req api.TriageAcceptRequestObject) (api.TriageAcceptResponseObject, error) {
	in := annotation.ResolveInput{State: annotation.TriageAccepted}
	if req.Body != nil {
		in.Tags = tagsOf(req.Body.Tags)
	}
	return s.resolveTriage(ctx, req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun, "triage.accept", in)
}

// TriageCorrect implements triage.correct.
func (s *Server) TriageCorrect(ctx context.Context, req api.TriageCorrectRequestObject) (api.TriageCorrectResponseObject, error) {
	in := annotation.ResolveInput{State: annotation.TriageCorrected, Text: req.Body.Text, Tags: tagsOf(req.Body.Tags)}
	return s.resolveTriage(ctx, req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun, "triage.correct", in)
}

// TriageReject implements triage.reject.
func (s *Server) TriageReject(ctx context.Context, req api.TriageRejectRequestObject) (api.TriageRejectResponseObject, error) {
	in := annotation.ResolveInput{State: annotation.TriageRejected}
	if req.Body != nil {
		in.Reason = deref(req.Body.Reason)
	}
	return s.resolveTriage(ctx, req.Id, req.Params.IfMatch, req.Params.IdempotencyKey, req.Params.DryRun, "triage.reject", in)
}

// ---------------------------------------------------------------- media of annotation and triage items

// mediaFor is the person a media request about u is for: people only, as mediaViewer; an annotation item's audio
// to the reviewers of its batch and to the people of its project, a triage item's to its project's, an utterance's to
// anyone who reads the registry (never a reviewer).
func (s *Server) mediaFor(ctx context.Context, u media.Utterance) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, problems.Unauthenticated.New("sign in to hear audio, or open a signed link")
	}
	if p.Actor.Kind == auth.KindAgent || p.CredentialKind == credentials.KindAgent {
		return auth.Principal{}, problems.Forbidden.New("agents read no raw audio (docs/spec/05-agents.md \"What the agent sees\"); test a model with evals.new and read its scores")
	}
	if p.Actor.Kind != auth.KindUser {
		return auth.Principal{}, problems.Forbidden.New("audio is for people: sign in to hear it (an API key, worker or host token reads none; docs/spec/06-platform.md \"Media\")")
	}
	switch {
	case p.Scope.Reviewer():
		if u.BatchID == "" || u.BatchID != p.Scope.Batch {
			return auth.Principal{}, problems.Forbidden.New("your invitation plays the items of batch %s only", p.Scope.Batch)
		}
		return p, nil
	case u.ProjectID != "":
		return p, auth.CheckProject(ctx, u.ProjectID)
	}
	return p, auth.CheckRegistryRead(ctx)
}

// TracksGet implements tracks.get.
func (s *Server) TracksGet(ctx context.Context, req api.TracksGetRequestObject) (api.TracksGetResponseObject, error) {
	u, err := media.Lookup(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if _, err := s.mediaFor(ctx, u); err != nil {
		return nil, err
	}
	d := s.defaultsDoc().Annotation
	prm := media.TrackParams{HopMs: 10, MarginDB: d.VADMarginDB.Value, FloorDB: d.VADFloorDB.Value,
		MinSilenceMs: d.VADMinSilenceMs.Value, BandwidthFloorDB: d.BandwidthFloorDB.Value}
	if req.Params.HopMs != nil {
		if *req.Params.HopMs%10 != 0 {
			return nil, problems.BadRequest.New("hopMs must be a multiple of 10; got %d", *req.Params.HopMs)
		}
		prm.HopMs = *req.Params.HopMs
	}
	release, err := s.mediaSlot()
	if err != nil {
		return nil, err
	}
	defer release()
	t, err := s.media.Tracks(u, prm)
	if err != nil {
		return nil, err
	}
	out, err := convert[api.AudioTracks](t)
	if err != nil {
		return nil, err
	}
	return api.TracksGet200JSONResponse(out), nil
}

// mediaSlot takes one of the media conversion slots for an analysis (media-busy when none is free).
func (s *Server) mediaSlot() (func(), error) {
	if s.media.Conversions == nil {
		return func() {}, nil
	}
	select {
	case s.media.Conversions <- struct{}{}:
		return func() { <-s.media.Conversions }, nil
	default:
		pe := problems.MediaBusy.New("%d audio conversions are running (media.max_conversions); try again in a moment", cap(s.media.Conversions))
		pe.RetryAfter = 2
		return nil, pe
	}
}
