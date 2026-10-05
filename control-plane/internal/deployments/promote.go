package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// Check names (the contract's DeploymentCheck.name) and verdicts.
const (
	CheckTargetServes = "target-serves"
	CheckEngine       = "engine"
	CheckParity       = "parity"
	CheckBenchmark    = "benchmark"
	CheckShadow       = "shadow"
	CheckSlotFree     = "slot-free"
	CheckCanary       = "canary"
	CheckRollback     = "rollback"
	CheckNotPending   = "not-pending"

	Passed  = "passed"
	Failed  = "failed"
	Skipped = "skipped"
	Warning = "warning"
)

// BoostRequest is one boost list a promotion ships: lang/<locale>/boost/<domain>.txt of the project repository.
type BoostRequest struct {
	Locale string
	Domain string
	Ref    string
	Weight *float64
}

// PromoteInput is a deployments.promote request.
type PromoteInput struct {
	Stage        string
	Target       string
	Slot         string
	TrafficShare *float64
	BoostLists   *[]BoostRequest // nil keeps the deployment's decoding
	Reason       string
}

// Check is one promotion check with its verdict (the contract's DeploymentCheck).
type Check struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	ProblemType string `json:"problemType,omitempty"`
	Detail      string `json:"detail,omitempty"`

	err error
}

// Plan is a promotion or rollback checked and planned: the checks and the record's body (the contract's
// DeploymentPromotion, without deployment and record, which the approved request adds).
type Plan struct {
	DeploymentID string         `json:"deploymentId"`
	Kind         string         `json:"kind"`
	Stage        string         `json:"stage"`
	TargetID     string         `json:"targetId,omitempty"`
	TargetName   string         `json:"targetName,omitempty"`
	Slot         string         `json:"slot,omitempty"`
	TrafficShare *float64       `json:"trafficShare,omitempty"`
	ConfigOnly   bool           `json:"configOnly,omitempty"`
	Checks       []Check        `json:"checks"`
	Ready        bool           `json:"ready"`
	Body         map[string]any `json:"body,omitempty"`

	d        Deployment
	target   targets.Target
	decoding Decoding
	otherID  string // the slot's previous production (a promotion), or the deployment a rollback restores
}

// Refusal is the problem of the first failing check (nil when the plan is ready).
func (pl Plan) Refusal() error {
	for _, c := range pl.Checks {
		if c.State == Failed {
			return c.err
		}
	}
	return nil
}

func (pl *Plan) add(name string, err error, detail string) {
	c := Check{Name: name, State: Passed, Detail: detail}
	if err != nil {
		c.State, c.err = Failed, err
		if pe, ok := problems.As(err); ok {
			c.ProblemType, c.Detail = pe.Type.Slug, pe.Detail
		} else {
			c.Detail = err.Error()
		}
	}
	pl.Checks = append(pl.Checks, c)
}

func (pl *Plan) warn(name, detail string) {
	pl.Checks = append(pl.Checks, Check{Name: name, State: Warning, Detail: detail})
}

func (pl *Plan) skip(name, detail string) {
	pl.Checks = append(pl.Checks, Check{Name: name, State: Skipped, Detail: detail})
}

func (pl *Plan) finish() {
	pl.Ready = pl.Refusal() == nil
	if pl.Checks == nil {
		pl.Checks = []Check{}
	}
}

// deployed is what the checks and the record read of a deployment's model and export.
type deployed struct {
	model  evals.DeployModel
	export modelexports.Export
	label  string // model/<name>@<version>
	files  []modelexports.DeployableFile
	man    string
}

func (s *Service) deployedOf(ctx context.Context, q storage.Querier, d Deployment) (deployed, error) {
	m, x, found, err := s.Exports.ResolveExport(ctx, q, d.ProjectID, d.ModelVersionID, d.Profile, d.Format)
	if err != nil {
		return deployed{}, err
	}
	if !found || x.State != modelexports.StateExported || x.DeployableHash == "" {
		return deployed{}, problems.ExportMissing.New("%s has no exported deployable at %s in %s any more", m.Label(), d.Profile, d.Format)
	}
	_, files, man, err := s.Exports.DeployableFiles(x.DeployableHash)
	if err != nil {
		return deployed{}, err
	}
	return deployed{model: m, export: x, label: m.Version.Name + "@" + m.Version.Version, files: files, man: man}, nil
}

var modelNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ModelName is the versioned model name a slot serves a model version under on the production host: the slot and
// the version, e.g. asr-he-il-2026-11-02-ab12cd (the previous version stays loaded under its own name).
func ModelName(slot, version string) string {
	n := slot + "-" + modelNameUnsafe.ReplaceAllString(strings.ReplaceAll(version, ".", "-"), "-")
	if len(n) > 100 {
		n = n[:100]
	}
	return strings.TrimRight(n, "-.")
}

func (dp deployed) deployableBody(modelName string) map[string]any {
	files := make([]any, 0, len(dp.files))
	for _, f := range dp.files {
		files = append(files, map[string]any{"path": f.Path, "sha256": f.SHA256, "bytes": f.Bytes})
	}
	return map[string]any{"hash": dp.export.DeployableHash, "format": dp.export.Format, "profile": dp.export.Profile,
		"modelName": modelName, "manifestSha256": dp.man, "files": files}
}

func (dp deployed) modelBody() map[string]any {
	m := map[string]any{"versionId": dp.model.Version.ID, "version": dp.label, "family": dp.model.Family.Name}
	if dp.model.Payload.WeightsHash != "" {
		m["weightsHash"] = dp.model.Payload.WeightsHash
	}
	return m
}

func decodingBody(dec Decoding) map[string]any {
	lists := make([]any, 0, len(dec.BoostLists))
	for _, b := range dec.BoostLists {
		lists = append(lists, map[string]any{"locale": b.Locale, "domain": b.Domain, "sha256": b.SHA256, "weight": b.Weight})
	}
	return map[string]any{"boostLists": lists}
}

func ptrsOK(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) }

// slotDeployments reads the live deployments of a delivery target's slot (other than skip).
func slotDeployments(ctx context.Context, q storage.Querier, targetID, slot, skip string) ([]Deployment, error) {
	return query(ctx, q, `target_id = $1 AND slot = $2 AND id <> $3 AND state IN ('active', 'pending-delivery')
		ORDER BY updated_at DESC, id DESC`, targetID, slot, skip)
}

// pendingOnSlot names a promotion or rollback of the slot that waits for its receipt, by another deployment.
func pendingOnSlot(ctx context.Context, q storage.Querier, targetID, slot, skip string) (string, string, error) {
	var rec, dep string
	err := q.QueryRow(ctx, `SELECT s.record_id, s.deployment_id FROM deployment_steps s JOIN deployments d ON d.id = s.deployment_id
		WHERE s.kind IN ('promotion', 'rollback') AND s.target_id = $1 AND s.slot = $2 AND d.pending_record_id = s.record_id
			AND d.id <> $3 LIMIT 1`, targetID, slot, skip).Scan(&rec, &dep)
	if noRows(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("look up pending promotions of slot %s: %w", slot, err)
	}
	return rec, dep, nil
}

// PlanPromotion runs a promotion's checks (02 "Checks before an approval is even asked") and plans its record. An
// error is a request that cannot be planned at all; a failing check is in the plan (Refusal).
func (s *Service) PlanPromotion(ctx context.Context, q storage.Querier, d Deployment, in PromoteInput) (Plan, error) {
	dflt := s.defaults().Deploy
	if in.Stage != StageCanary && in.Stage != StageProduction {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/stage", Message: "promote to canary or production"}})
	}
	if d.Stage == StageRetired || !d.Live() {
		return Plan{}, problems.Conflict.New("deployment %s is %s (%s): it is never promoted again; deploy the model again (deployments.new)", d.ID, d.Stage, d.State)
	}
	pl := Plan{DeploymentID: d.ID, Kind: promotions.KindPromotion, Stage: in.Stage, d: d}
	tref := strings.TrimSpace(in.Target)
	if tref == "" && d.Stage != StageShadow {
		tref = d.TargetID
	}
	if tref == "" {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/target", Message: "name the delivery target (dtg_… or name) the shadow is promoted to"}})
	}
	t, err := targets.Get(ctx, q, tref)
	if err != nil {
		if isNotFound(err) {
			return Plan{}, problems.Validation([]problems.FieldError{{Path: "/target", Message: fmt.Sprintf("no deployment target %q (deploymentTargets.list)", tref)}})
		}
		return Plan{}, err
	}
	if t.Kind != targets.KindDelivery {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/target",
			Message: fmt.Sprintf("%s is a %s target; canary and production go to a delivery target", t.Name, t.Kind)}})
	}
	if t.State != targets.StateActive {
		return Plan{}, problems.Conflict.New("deployment target %s is archived; it takes no promotions", t.Name)
	}
	slot := strings.TrimSpace(in.Slot)
	if slot == "" && d.TargetID == t.ID {
		slot = d.Slot
	}
	if slot == "" {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/slot", Message: fmt.Sprintf("name the slot of %s (its slots: %s)", t.Name, strings.Join(t.Slots, ", "))}})
	}
	if !t.HasSlot(slot) {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/slot", Message: fmt.Sprintf("%s has no slot %q (its slots: %s)", t.Name, slot, strings.Join(t.Slots, ", "))}})
	}
	pl.target, pl.TargetID, pl.TargetName, pl.Slot = t, t.ID, t.Name, slot
	if in.Stage == StageCanary {
		share := dflt.CanaryShare.Value
		if in.TrafficShare != nil {
			share = *in.TrafficShare
		} else if d.Stage == StageCanary && d.TrafficShare != nil {
			share = *d.TrafficShare
		}
		pl.TrafficShare = &share
	} else if in.TrafficShare != nil {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/trafficShare", Message: "production takes the slot's whole traffic; trafficShare is a canary's"}})
	}
	pl.decoding = d.Decoding.norm()
	if in.BoostLists != nil {
		if pl.decoding, err = s.renderBoosts(ctx, q, d.ProjectID, *in.BoostLists); err != nil {
			return Plan{}, err
		}
	}
	if len(pl.decoding.BoostLists) > 0 && t.Boost != nil && t.Boost.Static != nil && !*t.Boost.Static {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/decoding/boostLists",
			Message: fmt.Sprintf("%s takes no static boost lists (boost.static is false)", t.Name)}})
	}
	sameSlot := d.Stage != StageShadow && d.TargetID == t.ID && d.Slot == slot
	pl.ConfigOnly = sameSlot && d.Stage == in.Stage
	if pl.ConfigOnly && pl.decoding.same(d.Decoding) && (in.Stage != StageCanary || (d.TrafficShare != nil && *d.TrafficShare == *pl.TrafficShare)) {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/decoding",
			Message: fmt.Sprintf("%s is already the %s of %s/%s with this decoding: nothing would change", d.ID, d.Stage, t.Name, slot)}})
	}
	if in.Stage == StageCanary && !pl.ConfigOnly && d.Stage != StageShadow {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/stage",
			Message: fmt.Sprintf("a canary is promoted from a shadow deployment; %s is a %s on %s", d.ID, d.Stage, d.Slot)}})
	}
	dp, err := s.deployedOf(ctx, q, d)
	if err != nil {
		return Plan{}, err
	}

	// The deployment itself must not wait for a receipt already.
	if d.State == StatePendingDelivery {
		pl.add(CheckNotPending, problems.Conflict.New("deployment %s waits for the receipt of %s; confirm it (promotions.verify) or let it be withdrawn first", d.ID, d.PendingRecordID), "")
	} else {
		pl.add(CheckNotPending, nil, "")
	}
	switch {
	case pl.ConfigOnly:
		pl.skip(CheckTargetServes, "config-only: the slot already runs this deployable")
	case in.Stage == StageCanary:
		s.checkTarget(&pl, t, dp)
		s.checkParity(ctx, q, &pl, dp)
		s.checkBenchmark(ctx, q, &pl, t, dp)
		if d.Shadow.Hours+1e-9 >= dflt.ShadowMinHours.Value {
			pl.add(CheckShadow, nil, fmt.Sprintf("%.1f h of replayed calls (%d nights)", d.Shadow.Hours, d.Shadow.Nights))
		} else {
			pl.add(CheckShadow, problems.ShadowVolumeShort.New("the shadow replayed %.1f h of calls over %d night(s); a canary needs %.0f h (deploy.shadow_min_hours)",
				d.Shadow.Hours, d.Shadow.Nights, dflt.ShadowMinHours.Value), "")
		}
	default: // production
		if d.Stage == StageCanary && sameSlot && d.State == StateActive {
			pl.add(CheckCanary, nil, fmt.Sprintf("%s is the confirmed canary of %s/%s", dp.label, t.Name, slot))
		} else {
			pl.add(CheckCanary, problems.CanaryRequired.New("%s is not the confirmed canary of %s/%s (it is a %s on %s): promote it to canary first, and confirm the delivery",
				dp.label, t.Name, slot, d.Stage, orDash(d.Slot)), "")
		}
		s.checkTarget(&pl, t, dp)
	}
	// The slot: no other canary (a canary promotion) and no promotion waiting for its receipt.
	if err := s.checkSlot(ctx, q, &pl, d, t, slot, in.Stage == StageCanary && !pl.ConfigOnly); err != nil {
		return Plan{}, err
	}

	modelName := d.ModelName
	if modelName == "" || !sameSlot {
		modelName = ModelName(slot, dp.model.Version.Version)
	}
	body := map[string]any{"stage": in.Stage, "model": dp.modelBody(), "deployable": dp.deployableBody(modelName),
		"decoding": decodingBody(pl.decoding)}
	if pl.TrafficShare != nil {
		body["trafficShare"] = *pl.TrafficShare
	}
	prods, err := slotDeployments(ctx, q, t.ID, slot, d.ID)
	if err != nil {
		return Plan{}, err
	}
	for _, p := range prods {
		if p.Stage == StageProduction {
			pl.otherID = p.ID
			body["previous"] = map[string]any{"versionId": p.ModelVersionID, "modelName": p.ModelName}
			break
		}
	}
	body["evidence"] = s.evidence(ctx, q, d, dp, t)
	pl.Body = body
	pl.finish()
	return pl, nil
}

func orDash(s string) string {
	if s == "" {
		return "no slot"
	}
	return s
}

// engineOf is what deployable.json's serving says the engine was built for.
type engineOf struct {
	Server struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"server"`
	Engine struct {
		Kind      string `json:"kind"`
		Version   string `json:"version"`
		CardClass string `json:"cardClass"`
	} `json:"engine"`
}

// checkTarget: the target serves the family, format and profile (R46), and its server and card class are the ones
// the engine was built for — an engine runs only on its GPU architecture and server release (07 "Open questions").
func (s *Service) checkTarget(pl *Plan, t targets.Target, dp deployed) {
	fam, x := dp.model.Family.Name, dp.export
	if t.Serving(fam, x.Format, x.Profile) {
		pl.add(CheckTargetServes, nil, fmt.Sprintf("%s serves %s in %s at %s", t.Name, fam, x.Format, x.Profile))
	} else {
		pl.add(CheckTargetServes, problems.TargetDoesNotServe.New("target %s does not serve %s in %s at %s (deploymentTargets.edit adds it to serves)",
			t.Name, fam, x.Format, x.Profile), "")
	}
	var e engineOf
	if len(x.Deployable) > 0 {
		_ = json.Unmarshal(x.Deployable, &e)
	}
	// The server kind is the format's (serves.formats); the release and the card class are the engine's own.
	var why []string
	if e.Server.Version != "" && t.Server.Version != "" && e.Server.Version != t.Server.Version {
		why = append(why, fmt.Sprintf("it was built for server %s, the target runs %s", e.Server.Version, t.Server.Version))
	}
	if e.Engine.CardClass != "" && t.CardClass != "" && e.Engine.CardClass != t.CardClass {
		why = append(why, fmt.Sprintf("its engine was built on %s, the target's card class is %s", e.Engine.CardClass, t.CardClass))
	}
	switch {
	case len(why) > 0:
		pl.add(CheckEngine, problems.TargetDoesNotServe.New("the export's engine will not load on %s: %s; export on a staging target of that card class and server release",
			t.Name, strings.Join(why, "; ")), "")
	case t.CardClass == "" || e.Engine.CardClass == "":
		pl.warn(CheckEngine, fmt.Sprintf("the card class is not known on both sides (engine %q, target %q): the engine may not load there", e.Engine.CardClass, t.CardClass))
	default:
		pl.add(CheckEngine, nil, fmt.Sprintf("engine for %s, server %s %s", e.Engine.CardClass, t.Server.Kind, t.Server.Version))
	}
}

func (s *Service) checkParity(ctx context.Context, q storage.Querier, pl *Plan, dp deployed) {
	pv, err := modelexports.LatestParityView(ctx, q, dp.export.ID)
	switch {
	case err != nil:
		pl.add(CheckParity, err, "")
	case pv == nil:
		pl.add(CheckParity, problems.ParityFailed.New("export %s has no parity check; run models.parity first", dp.export.ID), "")
	case pv.State == "pending":
		pl.add(CheckParity, problems.ParityFailed.New("the parity check of export %s is still running (pipeline run %s)", dp.export.ID, pv.PipelineRunID), "")
	case pv.State != "passed":
		why := strings.Join(pv.Reasons, "; ")
		if why == "" {
			why = pv.Error
		}
		pl.add(CheckParity, problems.ParityFailed.New("the parity check of export %s failed: %s", dp.export.ID, why), "")
	default:
		detail := "passed"
		if pv.WERDelta != nil && pv.IdenticalShare != nil {
			detail = fmt.Sprintf("ΔWER %+.4f, %.1f%% identical", *pv.WERDelta, 100**pv.IdenticalShare)
		}
		pl.add(CheckParity, nil, detail)
	}
}

// benchmarkAt is the newest finished benchmark of the export whose verdict is taken at conc streams.
func benchmarkAt(list []modelexports.BenchmarkView, conc int) (*modelexports.BenchmarkView, bool) {
	running := false
	for i := range list {
		b := list[i]
		if b.Streams != conc {
			continue
		}
		switch b.State {
		case "running":
			running = true
		case "done":
			return &list[i], running
		}
	}
	return nil, running
}

func (s *Service) concurrency(t targets.Target) int {
	if t.Concurrency > 0 {
		return t.Concurrency
	}
	return s.defaults().Deploy.TargetConcurrency.Value
}

func (s *Service) checkBenchmark(ctx context.Context, q storage.Querier, pl *Plan, t targets.Target, dp deployed) {
	list, err := modelexports.LatestBenchmarks(ctx, q, dp.export.ID)
	if err != nil {
		pl.add(CheckBenchmark, err, "")
		return
	}
	conc := s.concurrency(t)
	b, running := benchmarkAt(list, conc)
	switch {
	case b == nil && running:
		pl.add(CheckBenchmark, problems.BenchmarkMissing.New("the benchmark of export %s at %d streams is still running", dp.export.ID, conc), "")
	case b == nil:
		pl.add(CheckBenchmark, problems.BenchmarkMissing.New("export %s has no finished benchmark at %s's concurrency (%d streams); run models.benchmark with target %s",
			dp.export.ID, t.Name, conc, t.Name), "")
	case b.Verdict == "passed":
		detail := fmt.Sprintf("p95 chunk latency within %.0f ms at %d streams", b.BudgetMs, conc)
		if ptrsOK(b.P95ChunkLatencyMs) {
			detail = fmt.Sprintf("p95 %.1f ms ≤ %.0f ms at %d streams", *b.P95ChunkLatencyMs, b.BudgetMs, conc)
		}
		pl.add(CheckBenchmark, nil, detail)
		if t.CardClass != "" && b.CardClass != "" && b.CardClass != t.CardClass {
			pl.warn(CheckBenchmark, fmt.Sprintf("measured on %s, the target's card class is %s: production latency may differ", b.CardClass, t.CardClass))
		}
	case b.Verdict == "inconclusive":
		pl.add(CheckBenchmark, problems.BenchmarkMissing.New("the newest benchmark of export %s at %d streams is inconclusive: processes outside Cadence used the card; benchmark again on a quiet card",
			dp.export.ID, conc), "")
	default:
		p95 := "over budget"
		if ptrsOK(b.P95ChunkLatencyMs) {
			p95 = fmt.Sprintf("p95 %.1f ms", *b.P95ChunkLatencyMs)
		}
		pl.add(CheckBenchmark, problems.LatencyBudgetExceeded.New("export %s at %d streams: %s, the budget is %.0f ms (deploy.latency_budget_over_chunk_ms)",
			dp.export.ID, conc, p95, b.BudgetMs), "")
	}
}

// checkSlot: no promotion of the slot waits for its receipt, and (for a canary) no other canary runs on it.
func (s *Service) checkSlot(ctx context.Context, q storage.Querier, pl *Plan, d Deployment, t targets.Target, slot string, canary bool) error {
	rec, dep, err := pendingOnSlot(ctx, q, t.ID, slot, d.ID)
	if err != nil {
		return err
	}
	if rec != "" {
		pl.add(CheckSlotFree, problems.Conflict.New("slot %s/%s has a promotion waiting for its receipt (%s of %s); confirm or withdraw it first", t.Name, slot, rec, dep), "")
		return nil
	}
	if canary {
		others, err := slotDeployments(ctx, q, t.ID, slot, d.ID)
		if err != nil {
			return err
		}
		for _, o := range others {
			if o.Stage == StageCanary {
				pl.add(CheckSlotFree, problems.Conflict.New("slot %s/%s already runs the canary %s; roll it back or promote it first", t.Name, slot, o.ID), "")
				return nil
			}
		}
	}
	pl.add(CheckSlotFree, nil, "")
	return nil
}

// evidence is what the record cites for the approver: the gate, the parity check, the benchmark and the shadow.
func (s *Service) evidence(ctx context.Context, q storage.Querier, d Deployment, dp deployed, t targets.Target) map[string]any {
	ev := map[string]any{}
	gate := map[string]any{}
	if dp.model.Payload.EvalID != "" {
		gate["evalId"] = dp.model.Payload.EvalID
	}
	if dp.model.Payload.Gate.Verdict != "" {
		gate["verdict"] = dp.model.Payload.Gate.Verdict
	}
	if dp.model.Payload.Gate.GatesSHA != "" {
		gate["gatesSha"] = dp.model.Payload.Gate.GatesSHA
	}
	if len(gate) > 0 {
		ev["gate"] = gate
	}
	if pv, err := modelexports.LatestParityView(ctx, q, dp.export.ID); err == nil && pv != nil && pv.State == "passed" {
		p := map[string]any{"reportHash": pv.ReportHash}
		if ptrsOK(pv.WERDelta) {
			p["werDelta"] = *pv.WERDelta
		}
		if ptrsOK(pv.IdenticalShare) {
			p["identicalShare"] = *pv.IdenticalShare
		}
		ev["parity"] = p
	}
	if list, err := modelexports.LatestBenchmarks(ctx, q, dp.export.ID); err == nil {
		if b, _ := benchmarkAt(list, s.concurrency(t)); b != nil {
			bm := map[string]any{"reportHash": b.ReportHash, "streams": b.Streams, "budgetMs": b.BudgetMs}
			if ptrsOK(b.P95TimeToFinalMs) {
				bm["p95TimeToFinalMs"] = *b.P95TimeToFinalMs
			}
			if b.CardClass != "" {
				bm["cardClass"] = b.CardClass
			}
			ev["benchmark"] = bm
		}
	}
	sh := map[string]any{"hours": math.Round(d.Shadow.Hours*100) / 100}
	if d.Shadow.Divergence != nil && !math.IsNaN(d.Shadow.Divergence.WER) {
		sh["divergence"] = d.Shadow.Divergence.WER
	}
	ev["shadow"] = sh
	return ev
}

var (
	localeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,35}$`)
	domainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	refRe    = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
)

// renderBoosts reads the boost lists of a promotion from the project repository and stores each as a boost_list
// artifact ({terms, weight}, the language pack's rendering): what the bundle ships as decoding configuration.
func (s *Service) renderBoosts(ctx context.Context, q storage.Querier, projectID string, reqs []BoostRequest) (Decoding, error) {
	out := Decoding{BoostLists: []BoostList{}}
	if len(reqs) == 0 {
		return out, nil
	}
	p, err := projects.GetByID(ctx, q, projectID)
	if err != nil {
		return out, err
	}
	if s.Repo == nil || !s.Repo.Exists(p.Slug) || s.CAS == nil {
		return out, problems.Validation([]problems.FieldError{{Path: "/decoding/boostLists", Message: "the project has no repository to read boost lists from"}})
	}
	seen := map[string]bool{}
	for i, r := range reqs {
		at := fmt.Sprintf("/decoding/boostLists/%d", i)
		ref := strings.TrimSpace(r.Ref)
		if ref == "" {
			ref = "main"
		}
		if !localeRe.MatchString(r.Locale) || !domainRe.MatchString(r.Domain) || !refRe.MatchString(ref) {
			return out, problems.Validation([]problems.FieldError{{Path: at, Message: fmt.Sprintf("%q / %q @%s is not a locale, a boost list and a commit", r.Locale, r.Domain, ref)}})
		}
		key := r.Locale + "/" + r.Domain
		if seen[key] {
			return out, problems.Validation([]problems.FieldError{{Path: at, Message: key + " is listed twice"}})
		}
		seen[key] = true
		path := "lang/" + r.Locale + "/" + langpacks.BoostPath(r.Domain)
		b, commit, err := s.Repo.ReadFile(ctx, p.Slug, ref, path)
		if err != nil {
			return out, problems.Validation([]problems.FieldError{{Path: at, Message: fmt.Sprintf("%s cannot be read at %s: %v", path, ref, err)}})
		}
		bl, err := langpacks.ParseBoost(r.Domain, b, s.defaults().Langpacks.BoostMaxTerms.Value)
		if err != nil {
			return out, problems.Validation([]problems.FieldError{{Path: at, Message: fmt.Sprintf("%s: %v", path, err)}})
		}
		if r.Weight != nil {
			bl.Weight = *r.Weight
		}
		h, err := s.CAS.PutBytes(bl.Artifact())
		if err != nil {
			return out, err
		}
		out.BoostLists = append(out.BoostLists, BoostList{Locale: r.Locale, Domain: r.Domain, Hash: h, SHA256: bl.SHA256(),
			Weight: bl.Weight, Terms: len(bl.Terms), Commit: commit})
	}
	return out, nil
}

// PlanRollback runs a rollback's check and plans its record: the slot returns to its confirmed earlier production
// version, which stayed loaded on the production host.
func (s *Service) PlanRollback(ctx context.Context, q storage.Querier, d Deployment) (Plan, error) {
	if d.Stage != StageCanary && d.Stage != StageProduction {
		return Plan{}, problems.Conflict.New("deployment %s is a %s: only a canary or production deployment rolls back", d.ID, d.Stage)
	}
	if !d.Live() {
		return Plan{}, problems.Conflict.New("deployment %s is %s", d.ID, d.State)
	}
	t, err := targets.Get(ctx, q, d.TargetID)
	if err != nil {
		return Plan{}, err
	}
	pl := Plan{DeploymentID: d.ID, Kind: promotions.KindRollback, Stage: StageProduction, d: d, target: t, TargetID: t.ID,
		TargetName: t.Name, Slot: d.Slot}
	if d.State == StatePendingDelivery {
		pl.add(CheckNotPending, problems.Conflict.New("deployment %s waits for the receipt of %s; confirm it or let it be withdrawn first", d.ID, d.PendingRecordID), "")
	} else {
		pl.add(CheckNotPending, nil, "")
	}
	restored, err := s.restorable(ctx, q, d)
	if err != nil {
		return Plan{}, err
	}
	if restored == nil {
		pl.add(CheckRollback, problems.RollbackUnavailable.New("slot %s/%s has no confirmed earlier production version still loaded to return to", t.Name, d.Slot), "")
		pl.decoding = Decoding{}.norm()
		pl.Body = map[string]any{"stage": StageProduction}
		if err := s.checkSlot(ctx, q, &pl, d, t, d.Slot, false); err != nil {
			return Plan{}, err
		}
		pl.finish()
		return pl, nil
	}
	rp, err := s.deployedOf(ctx, q, *restored)
	if err != nil {
		return Plan{}, err
	}
	pl.add(CheckRollback, nil, fmt.Sprintf("%s returns to %s (%s)", d.Slot, rp.label, restored.ModelName))
	if err := s.checkSlot(ctx, q, &pl, d, t, d.Slot, false); err != nil {
		return Plan{}, err
	}
	pl.otherID = restored.ID
	pl.decoding = restored.Decoding.norm()
	name := restored.ModelName
	if name == "" {
		name = ModelName(d.Slot, rp.model.Version.Version)
	}
	pl.Body = map[string]any{"stage": StageProduction, "model": rp.modelBody(), "deployable": rp.deployableBody(name),
		"decoding": decodingBody(pl.decoding), "replaces": map[string]any{"versionId": d.ModelVersionID, "modelName": d.ModelName}}
	pl.finish()
	return pl, nil
}

// restorable is the deployment a rollback of d returns its slot to: a canary's slot keeps its production version;
// a production deployment's is the one its confirmation retired.
func (s *Service) restorable(ctx context.Context, q storage.Querier, d Deployment) (*Deployment, error) {
	if d.Stage == StageCanary {
		others, err := slotDeployments(ctx, q, d.TargetID, d.Slot, d.ID)
		if err != nil {
			return nil, err
		}
		for _, o := range others {
			if o.Stage == StageProduction && o.State == StateActive {
				return &o, nil
			}
		}
		return nil, nil
	}
	var id string
	err := q.QueryRow(ctx, `SELECT s.deployment_id FROM deployment_steps s JOIN deployments p ON p.id = s.deployment_id
		WHERE s.kind = 'retired' AND s.other_id = $1 AND p.state = 'retired' AND p.target_id = $2 AND p.slot = $3
		ORDER BY s.created_at DESC LIMIT 1`, d.ID, d.TargetID, d.Slot).Scan(&id)
	if noRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the version %s replaced: %w", d.ID, err)
	}
	r, err := Get(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Apply signs a planned promotion or rollback (in the transaction that decides its approval): it appends the record
// to the target's chain, queues its delivery bundle, records the step and leaves the deployment pending delivery.
func (s *Service) Apply(ctx context.Context, tx pgx.Tx, pl Plan, reason string, actor auth.Actor, approvalID string, approver *auth.Actor) (Deployment, promotions.Record, []events.Draft, error) {
	if err := pl.Refusal(); err != nil {
		return Deployment{}, promotions.Record{}, nil, err
	}
	if approvalID == "" {
		return Deployment{}, promotions.Record{}, nil, problems.PolicyDenied.New("a %s record is signed only in an approved request (rule deployments)", pl.Kind)
	}
	d := pl.d
	rec, drafts, err := s.Promotions.Append(ctx, tx, promotions.AppendInput{Kind: pl.Kind, TargetID: pl.TargetID, Slot: pl.Slot,
		ProjectID: d.ProjectID, DeploymentID: d.ID, Actor: actor, ApprovalID: approvalID, Approver: approver, DecidedAt: s.now(),
		Reason: reason, Body: pl.Body})
	if err != nil {
		return Deployment{}, promotions.Record{}, nil, err
	}
	if s.Delivery != nil {
		_, more, err := s.Delivery.Build(ctx, tx, rec.ID)
		if err != nil {
			return Deployment{}, promotions.Record{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	dec := pl.decoding.norm()
	if err := s.addStep(ctx, tx, d.ID, Step{Kind: pl.Kind, FromStage: d.Stage, ToStage: pl.Stage, RecordID: rec.ID, TargetID: pl.TargetID,
		Slot: pl.Slot, TrafficShare: pl.TrafficShare, Decoding: &dec, OtherID: pl.otherID, Reason: reason, ApprovalID: approvalID,
		Actor: actor}); err != nil {
		return Deployment{}, promotions.Record{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET state = 'pending-delivery', pending_record_id = $2, rev = rev + 1,
		updated_at = $3 WHERE id = $1`, d.ID, rec.ID, s.now()); err != nil {
		return Deployment{}, promotions.Record{}, nil, fmt.Errorf("mark %s pending delivery: %w", d.ID, err)
	}
	out, err := Get(ctx, tx, d.ID)
	if err != nil {
		return Deployment{}, promotions.Record{}, nil, err
	}
	return out, rec, drafts, nil
}

// Confirm implements promotions.Stager: the confirmed record's step moves the deployment to its stage. A production
// promotion retires the slot's earlier production deployment (which stays loaded for a rollback); a rollback retires
// the deployment and restores the one it returns the slot to.
func (s *Service) Confirm(ctx context.Context, tx pgx.Tx, promotion, confirmation promotions.Record) ([]events.Draft, error) {
	st, depID, found, err := stepOfRecord(ctx, tx, promotion.ID)
	if err != nil || !found {
		return nil, err
	}
	d, err := Lock(ctx, tx, depID)
	if err != nil {
		return nil, err
	}
	if d.PendingRecordID != promotion.ID {
		return nil, nil
	}
	actor := confirmer(confirmation)
	now := s.now()
	var drafts []events.Draft
	if err := s.addStep(ctx, tx, d.ID, Step{Kind: StepConfirmation, FromStage: d.Stage, ToStage: st.ToStage, RecordID: confirmation.ID,
		TargetID: st.TargetID, Slot: st.Slot, Actor: actor}); err != nil {
		return nil, err
	}
	from := d.Stage
	switch st.Kind {
	case StepPromotion:
		dep, _ := promotion.Body["deployable"].(map[string]any)
		name, _ := dep["modelName"].(string)
		var share *float64
		if st.ToStage == StageCanary {
			share = st.TrafficShare
		}
		dec := Decoding{}.norm()
		if st.Decoding != nil {
			dec = st.Decoding.norm()
		}
		b, err := json.Marshal(dec)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE deployments SET stage = $2, target_id = $3, slot = $4, traffic_share = $5, decoding = $6,
				model_name = nullif($7, ''), state = 'active', pending_record_id = NULL, rev = rev + 1, updated_at = $8 WHERE id = $1`,
			d.ID, st.ToStage, st.TargetID, st.Slot, share, b, name, now); err != nil {
			return nil, fmt.Errorf("move %s to %s: %w", d.ID, st.ToStage, err)
		}
		if st.ToStage == StageProduction && from != StageProduction {
			others, err := slotDeployments(ctx, tx, st.TargetID, st.Slot, d.ID)
			if err != nil {
				return nil, err
			}
			for _, o := range others {
				if o.Stage != StageProduction {
					continue
				}
				if _, err := tx.Exec(ctx, `UPDATE deployments SET stage = 'retired', state = 'retired', traffic_share = NULL,
					rev = rev + 1, updated_at = $2 WHERE id = $1`, o.ID, now); err != nil {
					return nil, fmt.Errorf("retire %s: %w", o.ID, err)
				}
				if err := s.addStep(ctx, tx, o.ID, Step{Kind: StepRetired, FromStage: StageProduction, ToStage: StageRetired,
					TargetID: st.TargetID, Slot: st.Slot, OtherID: d.ID, Reason: "replaced by " + d.ID, Actor: actor}); err != nil {
					return nil, err
				}
				if r, err := Get(ctx, tx, o.ID); err == nil {
					drafts = append(drafts, draft(r, EventStageChanged, map[string]any{"from": StageProduction, "replacedBy": d.ID}))
				}
			}
		}
	case StepRollback:
		if _, err := tx.Exec(ctx, `UPDATE deployments SET stage = 'retired', state = 'rolled-back', traffic_share = NULL,
			pending_record_id = NULL, rev = rev + 1, updated_at = $2 WHERE id = $1`, d.ID, now); err != nil {
			return nil, fmt.Errorf("roll back %s: %w", d.ID, err)
		}
		if st.OtherID != "" {
			r, err := Lock(ctx, tx, st.OtherID)
			if err != nil {
				return nil, err
			}
			if r.Stage != StageProduction || r.State != StateActive {
				if _, err := tx.Exec(ctx, `UPDATE deployments SET stage = 'production', state = 'active', rev = rev + 1, updated_at = $2
					WHERE id = $1`, r.ID, now); err != nil {
					return nil, fmt.Errorf("restore %s: %w", r.ID, err)
				}
				if err := s.addStep(ctx, tx, r.ID, Step{Kind: StepRestored, FromStage: r.Stage, ToStage: StageProduction,
					TargetID: r.TargetID, Slot: r.Slot, OtherID: d.ID, Reason: "rollback of " + d.ID, Actor: actor}); err != nil {
					return nil, err
				}
				if r2, err := Get(ctx, tx, r.ID); err == nil {
					drafts = append(drafts, draft(r2, EventStageChanged, map[string]any{"from": r.Stage, "restoredBy": confirmation.ID}))
				}
			}
		}
	default:
		return nil, fmt.Errorf("deployments: record %s closes a %s step", promotion.ID, st.Kind)
	}
	out, err := Get(ctx, tx, d.ID)
	if err != nil {
		return nil, err
	}
	return append(drafts, draft(out, EventStageChanged, map[string]any{"from": from, "recordId": promotion.ID,
		"confirmation": confirmation.ID})), nil
}

// Withdraw implements promotions.Stager: a promotion or rollback without a receipt in time leaves the deployment
// where it was.
func (s *Service) Withdraw(ctx context.Context, tx pgx.Tx, promotion, withdrawal promotions.Record) ([]events.Draft, error) {
	st, depID, found, err := stepOfRecord(ctx, tx, promotion.ID)
	if err != nil || !found {
		return nil, err
	}
	d, err := Lock(ctx, tx, depID)
	if err != nil {
		return nil, err
	}
	if d.PendingRecordID != promotion.ID {
		return nil, nil
	}
	if err := s.addStep(ctx, tx, d.ID, Step{Kind: StepWithdrawal, FromStage: d.Stage, ToStage: d.Stage, RecordID: withdrawal.ID,
		TargetID: st.TargetID, Slot: st.Slot, Reason: "no receipt in time", Actor: promotions.System}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET state = 'active', pending_record_id = NULL, rev = rev + 1, updated_at = $2
		WHERE id = $1`, d.ID, s.now()); err != nil {
		return nil, fmt.Errorf("return %s from pending delivery: %w", d.ID, err)
	}
	out, err := Get(ctx, tx, d.ID)
	if err != nil {
		return nil, err
	}
	return []events.Draft{draft(out, EventStageChanged, map[string]any{"from": d.Stage, "withdrawn": promotion.ID})}, nil
}

// confirmer is the person a confirmation record names.
func confirmer(rec promotions.Record) auth.Actor {
	a := auth.Actor{Kind: auth.KindUser}
	if c, ok := rec.Body["confirmedBy"].(map[string]any); ok {
		a.ID, _ = c["id"].(string)
		a.Name, _ = c["name"].(string)
	}
	return a
}

// DecodingFiles are the boost lists a record ships (its step's decoding), as delivery bundle files; found is false
// for a record no deployment step appended.
func DecodingFiles(ctx context.Context, q storage.Querier, rec promotions.Record) ([]delivery.DecodingFile, bool, error) {
	st, _, found, err := stepOfRecord(ctx, q, rec.ID)
	if err != nil || !found {
		return nil, false, err
	}
	var out []delivery.DecodingFile
	if st.Decoding == nil {
		return out, true, nil
	}
	for _, b := range st.Decoding.BoostLists {
		if b.Hash == "" {
			return nil, true, fmt.Errorf("boost list %s/%s of record %s has no stored content", b.Locale, b.Domain, rec.ID)
		}
		out = append(out, delivery.DecodingFile{File: b.Locale + "." + b.Domain + ".json", Hash: b.Hash, SHA256: b.SHA256})
	}
	return out, true, nil
}
