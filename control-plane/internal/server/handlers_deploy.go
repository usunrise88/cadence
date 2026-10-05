package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// Deployment targets, promotion records and delivery bundles (phase 5 · stream D3; docs/spec/02-domain-projects-
// registry.md "Deployment entities"). Targets are registry data: creating or changing one is an approval the admin
// decides, for people too (preset rule deployment-targets); the approved replay runs the change, and a delivery
// target's chain gets its genesis or target-changed record in the same transaction. promotions.verify is a person's
// (rule delivery-is-for-people, and the handler refuses agents and keys itself).

func (c commandResponse) VisitDeploymentTargetsNewResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitDeploymentTargetsEditResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitDeploymentTargetsArchiveResponse(w http.ResponseWriter) error {
	return c.write(w)
}
func (c commandResponse) VisitPromotionsVerifyResponse(w http.ResponseWriter) error {
	return c.write(w)
}

// newDeploy wires the targets, promotions and delivery services: a delivery target's creation appends its genesis
// record, an edit of what records name appends target-changed.
func (s *Server) newDeploy() {
	keys := &promotions.Keyring{}
	if s.Secrets != nil {
		keys.Secrets = s.Secrets
	}
	// TODO(D4): set Stages to internal/deployments, whose Confirm moves a deployment to its record's stage and
	// whose Withdraw returns it; deployments.promote|rollback call s.promotions.Append and s.delivery.Build in the
	// transaction that decides the approval.
	s.promotions = &promotions.Service{Pool: s.Pool, Keys: keys, Defaults: s.defaultsDoc, Log: s.Log}
	s.targets = &targets.Service{ServerKinds: delivery.ServerKinds(templates.FS)}
	s.targets.OnCreated(func(ctx context.Context, tx pgx.Tx, t targets.Target) ([]events.Draft, error) {
		if t.Kind != targets.KindDelivery {
			return nil, nil
		}
		actor, approver := replayActors(ctx)
		_, drafts, err := s.promotions.Genesis(ctx, tx, t, actor, commands.ReplayedApproval(ctx), approver)
		return drafts, err
	})
	s.targets.OnChanged(func(ctx context.Context, tx pgx.Tx, before, after targets.Target, changed []string) ([]events.Draft, error) {
		if after.Kind != targets.KindDelivery || len(changed) == 0 {
			return nil, nil
		}
		actor, approver := replayActors(ctx)
		_, drafts, err := s.promotions.TargetChanged(ctx, tx, before, after, changed, actor, commands.ReplayedApproval(ctx), approver)
		return drafts, err
	})
	s.delivery = &delivery.Service{Pool: s.Pool, CAS: s.CAS, Jobs: s.Jobs, Templates: templates.FS,
		Sources: delivery.StoreSources{CAS: s.CAS}, Defaults: s.defaultsDoc, Log: s.Log}
	var key []byte
	if s.Secrets != nil {
		key = s.Secrets.DeriveKey("delivery-link")
	}
	s.deliveryLinks = delivery.NewSigner(key)
}

// replayActors are the requester (the command's actor) and, in an approved replay, the approver.
func replayActors(ctx context.Context) (auth.Actor, *auth.Actor) {
	actor, _ := auth.FromContext(ctx)
	if a, ok := commands.ReplayApprover(ctx); ok {
		return actor, &a
	}
	return actor, nil
}

// SweepPromotions withdraws promotions without a receipt after deploy.delivery_pending_days (a periodic job).
func (s *Server) SweepPromotions(ctx context.Context) error {
	n, err := s.promotions.WithdrawStale(ctx)
	if n > 0 {
		s.Log.InfoContext(ctx, "promotions withdrawn: no receipt in time", "count", n)
	}
	return err
}

func (s *Server) apiTarget(t targets.Target, head *promotions.Head) (api.DeploymentTarget, error) {
	var out api.DeploymentTarget
	if err := recode(t, &out); err != nil {
		return out, err
	}
	if t.Kind == targets.KindDelivery && head != nil {
		pending := head.Pending
		out.Chain = &api.DeploymentTargetChain{Records: head.Records, HeadSeq: head.Seq, HeadHash: head.Hash, Pending: &pending}
	}
	return out, nil
}

func apiKeys(keys []promotions.Key) []api.SigningKey {
	out := make([]api.SigningKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, api.SigningKey{Id: k.ID, Alg: api.SigningKeyAlg("Ed25519"), PublicKeyPem: promotions.PublicPEM(k.Public),
			State: api.SigningKeyState(k.State), CreatedAt: k.CreatedAt, RetiredAt: k.RetiredAt})
	}
	return out
}

// DeploymentTargetsList implements deploymentTargets.list.
func (s *Server) DeploymentTargetsList(ctx context.Context, req api.DeploymentTargetsListRequestObject) (api.DeploymentTargetsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	all := req.Params.State != nil && *req.Params.State == "all"
	list, err := targets.List(ctx, s.Pool, all)
	if err != nil {
		return nil, err
	}
	heads, err := promotions.Heads(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	keys, err := promotions.Keys(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	out := api.DeploymentTargetsList200JSONResponse{Items: make([]api.DeploymentTarget, 0, len(list)), SigningKeys: apiKeys(keys)}
	for _, t := range list {
		var head *promotions.Head
		if h, ok := heads[t.ID]; ok {
			head = &h
		}
		at, err := s.apiTarget(t, head)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, at)
	}
	if err := s.withServing(ctx, s.Pool, out.Items); err != nil { // staging health and served models (stream D2)
		return nil, err
	}
	return out, nil
}

// DeploymentTargetsGet implements deploymentTargets.get.
func (s *Server) DeploymentTargetsGet(ctx context.Context, req api.DeploymentTargetsGetRequestObject) (api.DeploymentTargetsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	t, err := targets.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	at, err := s.targetWithHead(ctx, s.Pool, t)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(t.Rev)
	return api.DeploymentTargetsGet200JSONResponse{Body: at, Headers: api.DeploymentTargetsGet200ResponseHeaders{ETag: &etag}}, nil
}

func targetConfig(serves *[]api.DeploymentTargetServes, server *api.DeploymentTargetServer, endpoint, repo *string,
	slots *[]string, concurrency *int, cardClass *string, boost *api.DeploymentTargetBoost) targets.Config {
	c := targets.Config{Endpoint: deref(endpoint), RepositoryPath: deref(repo), Concurrency: deref(concurrency), CardClass: deref(cardClass)}
	if serves != nil {
		for _, sv := range *serves {
			c.Serves = append(c.Serves, targets.Serves{Family: sv.Family, Formats: sv.Formats, Profiles: sv.Profiles})
		}
	}
	if server != nil {
		c.Server = targets.Server{Kind: server.Kind, Version: server.Version}
	}
	if slots != nil {
		c.Slots = *slots
	}
	if boost != nil {
		c.Boost = &targets.Boost{Static: boost.Static, Dynamic: boost.Dynamic, MaxTermsPerCall: boost.MaxTermsPerCall}
	}
	return c
}

// DeploymentTargetsNew implements deploymentTargets.new. The request is validated before the policy gates it (so
// the admin never approves a target that would be refused); the approved replay creates it (201).
func (s *Server) DeploymentTargetsNew(ctx context.Context, req api.DeploymentTargetsNewRequestObject) (api.DeploymentTargetsNewResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	b := req.Body
	in := targets.Input{Name: b.Name, Kind: string(b.Kind), Description: deref(b.Description),
		Config: targetConfig(b.Serves, &b.Server, b.Endpoint, b.RepositoryPath, b.Slots, b.Concurrency, b.CardClass, b.Boost)}
	cmd := command(ctx, targets.OpNew, req.Params.IdempotencyKey, req.Params.DryRun)
	approval := commands.ReplayedApproval(ctx)
	if !cmd.DryRun && approval == "" {
		if _, err := s.targets.Validate(ctx, s.Pool, in); err != nil {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		if cmd.DryRun {
			t, err := s.targets.Validate(ctx, tx, in)
			if err != nil {
				return commands.Result{}, nil, err
			}
			t.CreatedBy, t.CreatedAt, t.UpdatedAt = cmd.Actor, time.Now().UTC(), time.Now().UTC()
			at, err := s.apiTarget(t, nil)
			return commands.Result{Status: http.StatusOK, Body: at}, nil, err
		}
		t, drafts, err := s.targets.Create(ctx, tx, in, cmd.Actor, approval, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		at, err := s.targetWithHead(ctx, tx, t)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusCreated, Body: at, ETag: commands.ETag(t.Rev)}, drafts, nil
	})
}

// targetWithHead answers a target with its chain head as q sees it.
func (s *Server) targetWithHead(ctx context.Context, q storage.Querier, t targets.Target) (api.DeploymentTarget, error) {
	heads, err := promotions.Heads(ctx, q)
	if err != nil {
		return api.DeploymentTarget{}, err
	}
	var head *promotions.Head
	if h, ok := heads[t.ID]; ok {
		head = &h
	}
	at, err := s.apiTarget(t, head)
	if err != nil {
		return at, err
	}
	one := []api.DeploymentTarget{at}
	if err := s.withServing(ctx, q, one); err != nil { // staging health and served models (stream D2)
		return at, err
	}
	return one[0], nil
}

// DeploymentTargetsEdit implements deploymentTargets.edit (approval for everyone; a delivery target's chain gets a
// target-changed record when something a record names changed).
func (s *Server) DeploymentTargetsEdit(ctx context.Context, req api.DeploymentTargetsEditRequestObject) (api.DeploymentTargetsEditResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	b := req.Body
	e := targets.Edit{Description: b.Description, Server: nil, Endpoint: b.Endpoint, RepositoryPath: b.RepositoryPath,
		Slots: b.Slots, Concurrency: b.Concurrency, CardClass: b.CardClass}
	if b.Serves != nil {
		c := targetConfig(b.Serves, nil, nil, nil, nil, nil, nil, nil)
		serves := c.Serves
		if serves == nil {
			serves = []targets.Serves{}
		}
		e.Serves = &serves
	}
	if b.Server != nil {
		e.Server = &targets.Server{Kind: b.Server.Kind, Version: b.Server.Version}
	}
	if b.Boost != nil {
		e.Boost = &targets.Boost{Static: b.Boost.Static, Dynamic: b.Boost.Dynamic, MaxTermsPerCall: b.Boost.MaxTermsPerCall}
	}
	cmd := command(ctx, targets.OpEdit, req.Params.IdempotencyKey, req.Params.DryRun)
	if !cmd.DryRun && commands.ReplayedApproval(ctx) == "" {
		t, err := targets.Get(ctx, s.Pool, req.Id)
		if err != nil {
			return nil, err
		}
		if err := commands.CheckRev("deployment target", rev, t.Rev); err != nil {
			return nil, err
		}
		if _, _, err := s.targets.Apply(t, e); err != nil {
			return nil, err
		}
	}
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		t, err := targets.Lock(ctx, tx, req.Id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev("deployment target", rev, t.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		n, changed, err := s.targets.Apply(t, e)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if cmd.DryRun {
			at, err := s.targetWithHead(ctx, tx, n)
			return commands.Result{Status: http.StatusOK, Body: at, ETag: commands.ETag(t.Rev)}, nil, err
		}
		saved, drafts, err := s.targets.Save(ctx, tx, t, n, changed, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		at, err := s.targetWithHead(ctx, tx, saved)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: at, ETag: commands.ETag(saved.Rev)}, drafts, nil
	})
}

// DeploymentTargetsArchive implements deploymentTargets.archive (the admin's; agents meet the preset's no-deletes).
func (s *Server) DeploymentTargetsArchive(ctx context.Context, req api.DeploymentTargetsArchiveRequestObject) (api.DeploymentTargetsArchiveResponseObject, error) {
	if err := auth.CheckAll(ctx); err != nil {
		return nil, err
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	cmd := command(ctx, targets.OpArchive, req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		t, err := targets.Lock(ctx, tx, req.Id)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if err := commands.CheckRev("deployment target", rev, t.Rev); err != nil {
			return commands.Result{}, nil, err
		}
		a, drafts, err := targets.Archive(ctx, tx, t, time.Now().UTC())
		if err != nil {
			return commands.Result{}, nil, err
		}
		at, err := s.targetWithHead(ctx, tx, a)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: at, ETag: commands.ETag(a.Rev)}, drafts, nil
	})
}

// ---------------------------------------------------------------- promotion records

// person reports whether the request is a person's own (a user's session, not an agent or a key).
func person(ctx context.Context) bool {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		a, ok := auth.FromContext(ctx)
		return ok && a.Kind == auth.KindUser
	}
	return p.Actor.Kind == auth.KindUser && p.CredentialKind != credentials.KindAgent
}

func (s *Server) apiRecord(ctx context.Context, r promotions.Record, withDelivery bool) (api.PromotionRecord, error) {
	out := api.PromotionRecord{
		Id: r.ID, TargetId: r.TargetID, Seq: r.Seq, Kind: api.PromotionRecordKind(r.Kind), Hash: r.Hash, PrevHash: r.PrevHash,
		Signature: r.Signature, KeyId: r.KeyID, Canonical: string(r.Canonical), Body: r.Body, Verified: r.Verified,
		CreatedAt: r.CreatedAt, Rev: r.Rev(), Slot: ptrIf(r.Slot), ProjectId: ptrIf(r.ProjectID),
		DeploymentId: ptrIf(r.DeploymentID), RefersTo: ptrIf(r.RefersTo), ClosedBy: ptrIf(r.ClosedBy),
	}
	if out.Body == nil {
		out.Body = map[string]any{}
	}
	if len(r.Problems) > 0 {
		p := r.Problems
		out.Problems = &p
	}
	if r.State != "" {
		st := api.PromotionRecordState(r.State)
		out.State = &st
	}
	if !withDelivery {
		return out, nil
	}
	if k, err := promotions.KeyByID(ctx, s.Pool, r.KeyID); err == nil {
		pemText := promotions.PublicPEM(k.Public)
		out.PublicKeyPem = &pemText
	}
	d, err := delivery.Get(ctx, s.Pool, r.ID)
	if err != nil || d == nil {
		return out, err
	}
	pd := api.PromotionDelivery{State: api.PromotionDeliveryState(d.State), JobId: ptrIf(d.JobID),
		ArtifactHash: ptrIf(d.ArtifactHash), Error: ptrIf(d.Error), Script: ptrIf(d.Script)}
	if d.SmokeTotal != nil && d.SmokeRequired != nil {
		pd.Smoke = &struct {
			Required   int `json:"required"`
			Utterances int `json:"utterances"`
		}{Required: *d.SmokeRequired, Utterances: *d.SmokeTotal}
	}
	// A signed download link for people only: delivery is for people (05 "Guardrails").
	if a, ok := auth.FromContext(ctx); ok && d.State == delivery.StateReady && person(ctx) {
		ttl := time.Duration(s.defaultsDoc().Media.SignedLinkTTLSeconds.Value) * time.Second
		exp := s.deliveryLinks.Now().Add(ttl).Truncate(time.Second)
		q := url.Values{}
		q.Set("viewer", a.ID)
		q.Set("exp", strconv.FormatInt(exp.Unix(), 10))
		q.Set("sig", s.deliveryLinks.Sign(r.ID, a.ID, exp.Unix()))
		link := APIPrefix + "/promotions/" + url.PathEscape(r.ID) + "/delivery?" + q.Encode()
		pd.BundleUrl, pd.BundleUrlExpiresAt = &link, &exp
	}
	out.Delivery = &pd
	return out, nil
}

// PromotionsList implements promotions.list: the target's chain, every record verified again.
func (s *Server) PromotionsList(ctx context.Context, req api.PromotionsListRequestObject) (api.PromotionsListResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	t, err := targets.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	c, err := promotions.LoadChain(ctx, s.Pool, t)
	if err != nil {
		return nil, err
	}
	projectID := ""
	if p := deref(req.Params.Project); p != "" {
		pr, err := projects.Get(ctx, s.Pool, p)
		if err != nil {
			return nil, err
		}
		projectID = pr.ID
	}
	out := api.PromotionsList200JSONResponse{TargetId: t.ID, TargetName: t.Name, Intact: c.Intact, Items: []api.PromotionRecord{}}
	if len(c.Problems) > 0 {
		p := c.Problems
		out.Problems = &p
	}
	for _, r := range c.Records {
		if (projectID != "" && r.ProjectID != projectID) || (deref(req.Params.Slot) != "" && r.Slot != *req.Params.Slot) {
			continue
		}
		ar, err := s.apiRecord(ctx, r, false)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, ar)
	}
	return out, nil
}

// PromotionsGet implements promotions.get.
func (s *Server) PromotionsGet(ctx context.Context, req api.PromotionsGetRequestObject) (api.PromotionsGetResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	r, _, err := promotions.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	ar, err := s.apiRecord(ctx, r, true)
	if err != nil {
		return nil, err
	}
	etag := commands.ETag(r.Rev())
	return api.PromotionsGet200JSONResponse{Body: ar, Headers: api.PromotionsGet200ResponseHeaders{ETag: &etag}}, nil
}

// PromotionsVerify implements promotions.verify: a person pastes the receipt; a signed confirmation record is
// appended when it holds (promotion-receipt-mismatch otherwise, nothing changed).
func (s *Server) PromotionsVerify(ctx context.Context, req api.PromotionsVerifyRequestObject) (api.PromotionsVerifyResponseObject, error) {
	if err := auth.CheckRegistryRead(ctx); err != nil {
		return nil, err
	}
	if !person(ctx) {
		return nil, problems.Forbidden.New("a delivery is confirmed by the person who ran its script on the production host: agents, API keys, worker and host tokens never confirm one (rule delivery-is-for-people)")
	}
	rev, err := commands.ParseIfMatch(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	var projectID string
	if err := s.Pool.QueryRow(ctx, "SELECT coalesce(project_id, '') FROM promotion_records WHERE id = $1", req.Id).Scan(&projectID); err == nil && projectID != "" {
		ctx = commands.WithProject(ctx, projectID)
	}
	cmd := command(ctx, "promotions.verify", req.Params.IdempotencyKey, req.Params.DryRun)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		rec, _, drafts, err := s.promotions.Confirm(ctx, tx, req.Id, req.Body.Receipt, rev, cmd.Actor)
		if err != nil {
			return commands.Result{}, nil, err
		}
		ar, err := s.apiRecord(ctx, rec, false)
		if err != nil {
			return commands.Result{}, nil, err
		}
		return commands.Result{Status: http.StatusOK, Body: ar, ETag: commands.ETag(rec.Rev())}, drafts, nil
	})
}

// ---------------------------------------------------------------- the bundle download (tag media)

// bundleResponse streams a bundle as an attachment.
type bundleResponse struct {
	name string
	body io.Reader
}

func (b bundleResponse) VisitDeliveryGetResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", b.name+".tar.gz"))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, b.body)
	if c, ok := b.body.(io.Closer); ok {
		_ = c.Close()
	}
	return err
}

var deliveryLinkPath = regexp.MustCompile(`^/promotions/[^/]+/delivery$`)

// signedDeliveryRequest reports a GET of delivery.get that carries a link signature: it may proceed without a
// principal and is checked by the handler (publicRequest).
func signedDeliveryRequest(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Query().Get("sig") != "" &&
		deliveryLinkPath.MatchString(strings.TrimPrefix(r.URL.Path, APIPrefix))
}

// DeliveryGet implements delivery.get: people only, with a session or a signed link; every download is audited.
func (s *Server) DeliveryGet(ctx context.Context, req api.DeliveryGetRequestObject) (api.DeliveryGetResponseObject, error) {
	var actor auth.Actor
	if p, ok := auth.PrincipalFromContext(ctx); ok && (p.Actor.Kind != auth.KindUser || p.CredentialKind == credentials.KindAgent) {
		return nil, problems.Forbidden.New("delivery bundles are for people: agents, API keys, worker and host tokens download none")
	}
	if req.Params.Sig != nil {
		if req.Params.Viewer == nil || req.Params.Exp == nil {
			return nil, problems.DeliveryLinkInvalid.New("a signed link carries viewer, exp and sig")
		}
		if err := s.deliveryLinks.Verify(req.Id, *req.Params.Viewer, *req.Params.Exp, *req.Params.Sig); err != nil {
			return nil, err
		}
		actor = auth.Actor{Kind: auth.KindUser, ID: *req.Params.Viewer}
	} else {
		a, ok := auth.FromContext(ctx)
		if !ok || !person(ctx) {
			return nil, problems.Unauthenticated.New("sign in to download a delivery bundle, or open a signed link from promotions.get")
		}
		if err := auth.CheckRegistryRead(ctx); err != nil {
			return nil, err
		}
		actor = a
	}
	d, err := delivery.Get(ctx, s.Pool, req.Id)
	if err != nil {
		return nil, err
	}
	if d == nil || d.State != delivery.StateReady || d.ArtifactHash == "" {
		return nil, problems.NotFound.New("promotion record %s has no finished delivery bundle", req.Id)
	}
	if s.CAS == nil {
		return nil, problems.Internal.New("no content store is configured")
	}
	detail := map[string]any{"recordId": req.Id, "artifact": d.ArtifactHash}
	if err := audit.Write(context.WithoutCancel(ctx), s.Pool, audit.Entry{Operation: "delivery.get", Actor: actor,
		Outcome: audit.OutcomeOK, Status: http.StatusOK, Detail: detail}); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(delivery.WriteTar(pw, s.CAS, d.ArtifactHash, req.Id))
	}()
	return bundleResponse{name: req.Id, body: pr}, nil
}
