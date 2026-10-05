//go:build integration

package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/deployments"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

type depView struct {
	ID, Stage, State, TargetID, TargetName, Slot, ModelName, ExportID, ModelVersionID string
	Rev                                                                               int
	TrafficShare                                                                      *float64
	Shadow                                                                            *struct {
		Hours, MinHours float64
		Calls, Nights   int
		NextReplayAt    *time.Time
		Divergence      *struct{ Wer float64 }
		Against         *struct{ Kind, VersionID, Label string }
	}
	Pending *struct{ RecordID, Kind, Stage string }
	History []struct{ Kind, FromStage, ToStage, RecordID string }
}

type promotionView struct {
	Kind, Stage, Slot string
	Ready, ConfigOnly bool
	Checks            []struct{ Name, State, ProblemType, Detail string }
	Body              map[string]any
	Deployment        *depView
	Record            *struct{ ID, Hash, Kind string }
}

type replayView struct {
	ID, State, Reason, PipelineRunID, Night string
	Calls, Utterances                       int
	Hours                                   float64
	Divergence                              *struct{ Wer float64 }
	Against                                 *struct{ Kind, DecodedBy string }
	Worst                                   []struct{ Audio, Call, Candidate, Current string }
	TextsEvictedAt                          *time.Time
}

// wavOf is a 16-bit PCM WAV header for seconds of 8 kHz mono audio, without the samples (the control plane reads
// only the header to size a call).
func wavOf(seconds int) []byte {
	b := make([]byte, 44)
	copy(b[0:], "RIFF")
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 8000)
	binary.LittleEndian.PutUint32(b[28:], 16000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(seconds*16000))
	return b
}

// approveProject approves a project-scope approval as the admin and answers the replay's status and body.
func (e *env) approveProject(id, rule string) (int, json.RawMessage) {
	e.t.Helper()
	ap := e.approval(id)
	if ap.Rule != rule || ap.Scope != "project" {
		e.t.Fatalf("approval %+v, want rule %s in project scope", ap, rule)
	}
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+id+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.State != "approved" || decided.Result == nil {
		e.t.Fatalf("decided %+v", decided)
	}
	return decided.Result.Status, decided.Result.Body
}

func (e *env) deployment(id string) depView {
	e.t.Helper()
	var d depView
	e.ok(e.do("GET", "/api/deployments/"+id, ""), 200, &d)
	return d
}

// promoteApproved asks for a promotion or rollback, approves it and answers the signed record.
func (e *env) promoteApproved(depID, verb, body string, rev int) promotionView {
	e.t.Helper()
	var acc accepted
	e.ok(e.do("POST", "/api/deployments/"+depID+":"+verb, body, "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, rev)), 202, &acc)
	status, raw := e.approveProject(acc.ApprovalID, "deployments")
	if status != 200 {
		e.t.Fatalf("%s replay %d %s", verb, status, raw)
	}
	var pv promotionView
	if err := json.Unmarshal(raw, &pv); err != nil {
		e.t.Fatal(err)
	}
	if pv.Record == nil || pv.Deployment == nil || pv.Deployment.State != "pending-delivery" {
		e.t.Fatalf("%s %+v", verb, pv)
	}
	return pv
}

// deliver waits for a record's bundle and confirms it with the receipt deliver.sh would print.
func (e *env) deliver(recordID string, smoke string) recordView {
	e.t.Helper()
	var job string
	if err := e.pool.QueryRow(context.Background(), "SELECT job_id FROM promotion_deliveries WHERE record_id = $1", recordID).Scan(&job); err != nil {
		e.t.Fatal(err)
	}
	e.waitJob(job, "done")
	var rec recordView
	e.ok(e.do("GET", "/api/promotions/"+recordID, ""), 200, &rec)
	if rec.Delivery == nil || rec.Delivery.State != "ready" {
		e.t.Fatalf("delivery of %s %+v", recordID, rec.Delivery)
	}
	man := rec.Body["deployable"].(map[string]any)["manifestSha256"].(string)
	receipt := fmt.Sprintf(`{"receipt":"CADENCE-RECEIPT 1 %s %s %s era-asr-01 2026-11-03T10:00:00Z"}`, rec.Hash, man, smoke)
	var conf recordView
	e.ok(e.do("POST", "/api/promotions/"+recordID+":verify", receipt, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &conf)
	if conf.State != "confirmed" {
		e.t.Fatalf("confirmed %+v", conf)
	}
	return rec
}

func TestDeploymentsShadowPromotionAndRollback(t *testing.T) {
	e, dsvc := startDeploy(t) // the bundle builder's job kind, and the instance key
	dsvc.Sources, dsvc.Defaults = e.admin.deliverySources(), e.admin.defaultsDoc
	e.admin.deployments.Delivery = dsvc
	ctx := context.Background()
	for _, reg := range []func(context.Context) error{
		func(c context.Context) error { return pipelinestest.RegisterTraining(c, e.pool) },
		func(c context.Context) error { return pipelinestest.RegisterEvaluation(c, e.pool) },
		func(c context.Context) error { return pipelinestest.RegisterDeploy(c, e.pool) },
		func(c context.Context) error { return pipelinestest.RegisterShadow(c, e.pool) },
	} {
		if err := reg(ctx); err != nil {
			t.Fatal(err)
		}
	}
	p := e.newProject("dep")
	gs, _, err := pipelinestest.RegisterDirGoldenSet(ctx, e.pool, e.admin.CAS, "fx-dep", "he-IL", []string{"one two", "three four"})
	if err != nil {
		t.Fatal(err)
	}
	serves := targets.Serves{Family: pipelinestest.FamilyName, Formats: []string{pipelinestest.FixtureFormat}, Profiles: []string{"80ms", "offline"}}
	if _, err := targets.Seed(ctx, e.pool, "staging", "http://triton:8000", targets.Server{Kind: "triton", Version: "26.08"}, time.Now(), serves); err != nil {
		t.Fatal(err)
	}
	base, err := registry.Latest(ctx, e.pool, registry.KindBaseModel, pipelinestest.BaseModel)
	if err != nil {
		t.Fatal(err)
	}
	register := func(name string) registry.Version {
		ckpt, err := e.admin.CAS.PutBytes([]byte("fixture checkpoint of " + name))
		if err != nil {
			t.Fatal(err)
		}
		var v registry.Version
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			b, _ := json.Marshal(map[string]any{"checkpointId": "ckp_" + name, "weightsHash": "b3:w" + name, "checkpointHash": ckpt,
				"familyId": pipelinestest.FamilyName, "baseModelVersionId": base.ID, "projectId": p.ID, "evalId": "evl_" + name,
				"gate": map[string]any{"verdict": "passed", "gatesSha": "abc"}, "lineage": map[string]any{"datasetVersionIds": []string{}}})
			var err error
			v, _, _, err = registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindModel, Name: "model/" + name, Payload: b,
				Freeze: true, Actor: auth.DevActor()}, time.Now())
			if err != nil {
				return err
			}
			_, err = registry.AdoptQuietly(ctx, tx, p.ID, []string{v.ID, gs}, auth.DevActor())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	ready := func(model string) {
		body := `{"version":"model/` + model + `"}`
		var run mexPlanView
		e.ok(e.do("POST", "/api/projects/dep/models:export", body, "Idempotency-Key", e.key()), 201, &run)
		e.waitPipelineRun(run.PipelineRun.ID, "done")
		var par checkPlanView
		e.ok(e.do("POST", "/api/projects/dep/models:parity", body, "Idempotency-Key", e.key()), 201, &par)
		e.waitPipelineRun(par.PipelineRun.ID, "done")
		bench := `{"version":"model/` + model + `","streams":[1],"target":"era-production"}`
		var bp checkPlanView
		e.ok(e.do("POST", "/api/projects/dep/models:benchmark", bench, "Idempotency-Key", e.key()), 201, &bp)
		e.waitPipelineRun(bp.PipelineRun.ID, "done")
	}

	// The delivery target (an approval the admin decides) and two model versions ready for it.
	target := `{"name":"era-production","kind":"delivery","serves":[{"family":"` + pipelinestest.FamilyName + `","formats":["` +
		pipelinestest.FixtureFormat + `"],"profiles":["80ms"]}],"server":{"kind":"triton","version":"26.08"},
		"repositoryPath":"/opt/era/models","slots":["asr-he-il"],"concurrency":32}`
	var acc accepted
	e.ok(e.do("POST", "/api/deployment-targets", target, "Idempotency-Key", e.key()), 202, &acc)
	if status, body := e.approveReplay(acc.ApprovalID, "deployment-targets"); status != 201 {
		t.Fatalf("target %d %s", status, body)
	}
	m1 := register("dep")
	ready("dep")

	// The calls mount: three calls of 10 s each, the newest last written; a registered source.
	root := t.TempDir()
	callsDir := filepath.Join(root, "calls-synth", "rev1", "calls")
	if err := os.MkdirAll(callsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"c1.wav", "c2.wav", "c3.wav"} {
		f := filepath.Join(callsDir, name)
		if err := os.WriteFile(f, wavOf(10), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, created_by) VALUES ('mnt_corpora', 'corpora', 'local', $1,
		'{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"calls-synth","licence":"CC-BY-4.0","kind":"synthetic","languages":["he-IL"]}`,
		"Idempotency-Key", e.key()), 201, nil)

	// deployments.new: a shadow on the staging target, allowed for agents; one per export.
	newBody := `{"version":"model/dep","replay":{"mount":"corpora","path":"calls-synth/rev1/calls","source":"calls-synth","language":"he-IL"},
		"against":"` + pipelinestest.BaseModel + `"}`
	expectProblem(t, e.agent("POST", "/api/projects/dep/deployments", strings.Replace(newBody, `"source":"calls-synth"`, `"source":"nope"`, 1),
		"Idempotency-Key", e.key()), 422, "source-unlicensed")
	var dry depView
	e.ok(e.agent("POST", "/api/projects/dep/deployments?dryRun=true", newBody, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Stage != "shadow" || e.count("SELECT count(*) FROM deployments") != 0 {
		t.Fatalf("dry run %+v", dry)
	}
	var d1 depView
	e.ok(e.agent("POST", "/api/projects/dep/deployments", newBody, "Idempotency-Key", e.key()), 201, &d1)
	if d1.Stage != "shadow" || d1.State != "active" || d1.TargetName != "staging" || d1.ModelVersionID != m1.ID || d1.Shadow == nil ||
		d1.Shadow.MinHours != 20 || d1.Shadow.NextReplayAt == nil || d1.Shadow.Against == nil || d1.Shadow.Against.Kind != "base_model" {
		t.Fatalf("shadow deployment %+v", d1)
	}
	expectProblem(t, e.do("POST", "/api/projects/dep/deployments", newBody, "Idempotency-Key", e.key()), 409, "conflict")

	// A manual test resolves the deployment through the staging target.
	td, err := e.admin.deployments.Resolve(ctx, e.pool, p.ID, d1.ID)
	if err != nil || td.TargetID == "" || td.Deployable.Hash == "" || td.Profile != "80ms" {
		t.Fatalf("resolve %+v (%v)", td, err)
	}

	// Before any replay the canary is refused on shadow volume; the dry run answers every check.
	canary := `{"stage":"canary","target":"era-production","slot":"asr-he-il","reason":"beats the baseline on calls"}`
	var pl promotionView
	e.ok(e.do("POST", "/api/deployments/"+d1.ID+":promote?dryRun=true", canary, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &pl)
	checks := map[string]string{}
	for _, c := range pl.Checks {
		if c.State == "failed" {
			checks[c.Name] = c.ProblemType
		} else if _, seen := checks[c.Name]; !seen {
			checks[c.Name] = c.State
		}
	}
	if pl.Ready || checks["shadow"] != "shadow-volume-short" || checks["parity"] != "passed" || checks["benchmark"] != "passed" ||
		checks["target-serves"] != "passed" {
		t.Fatalf("checks %+v", pl.Checks)
	}
	expectProblem(t, e.do("POST", "/api/deployments/"+d1.ID+":promote", canary, "Idempotency-Key", e.key(), "If-Match", `"1"`), 422, "shadow-volume-short")
	expectProblem(t, e.agent("POST", "/api/deployments/"+d1.ID+":promote", canary, "Idempotency-Key", e.key(), "If-Match", `"1"`), 422, "shadow-volume-short")

	// A replay now: the three calls, newest first; both models through the staging server or the family's decoder.
	var rp replayView
	e.ok(e.do("POST", "/api/deployments/"+d1.ID+"/shadow-replays?dryRun=true", "", "Idempotency-Key", e.key()), 200, &rp)
	if rp.Calls != 3 || rp.State != "planned" || rp.Against == nil || rp.Against.DecodedBy != "transcribe" {
		t.Fatalf("replay plan %+v", rp)
	}
	e.ok(e.do("POST", "/api/deployments/"+d1.ID+"/shadow-replays", "", "Idempotency-Key", e.key()), 201, &rp)
	e.waitPipelineRun(rp.PipelineRunID, "done")
	if c := e.leases.CallsOf("candidate"); len(c) != 1 || c[0].Spec.Resources.JobKind != "shadow" {
		t.Fatalf("candidate %+v", c)
	}
	var idx map[string]any
	_ = json.Unmarshal(e.leases.CallsOf("index")[0].Spec.Params, &idx)
	if files, _ := idx["files"].([]any); len(files) != 3 || files[0] != "c3.wav" || idx["path"] != "mount://corpora/calls-synth/rev1/calls" {
		t.Fatalf("index params %+v", idx)
	}
	var night replayView
	e.ok(e.do("GET", "/api/shadow-replays/"+rp.ID, ""), 200, &night)
	if night.State != "done" || night.Calls != 3 || night.Divergence == nil || night.Divergence.Wer != pipelinestest.ShadowDivergence ||
		len(night.Worst) != 1 || night.Worst[0].Current != "other words" {
		t.Fatalf("night %+v", night)
	}
	worstAudio := night.Worst[0].Audio
	if dur, ok, err := deployments.SegmentAudio(ctx, e.pool, worstAudio); err != nil || !ok || dur != pipelinestest.ShadowSegmentSeconds {
		t.Fatalf("segment audio %v %v %v", dur, ok, err)
	}
	d1 = e.deployment(d1.ID)
	if d1.Shadow.Hours < 20 || d1.Shadow.Calls != 3 || d1.Shadow.Nights != 1 || d1.Rev != 1 {
		t.Fatalf("shadow totals %+v", d1.Shadow)
	}
	// Nothing new to replay: refused; the nightly tick records the night as skipped, once.
	expectProblem(t, e.do("POST", "/api/deployments/"+d1.ID+"/shadow-replays", "", "Idempotency-Key", e.key()), 409, "conflict")
	e.admin.deployments.Now = func() time.Time { return time.Date(2031, 1, 1, 12, 0, 0, 0, time.UTC) }
	for range 2 {
		if err := e.admin.deployments.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count("SELECT count(*) FROM shadow_replays WHERE trigger = 'nightly' AND state = 'skipped'"); n != 1 {
		t.Fatalf("%d skipped nights", n)
	}
	e.admin.deployments.Now = nil

	// The canary: an approval for people too, decided in the confirm modal; then a signed record and its bundle.
	pv := e.promoteApproved(d1.ID, "promote", canary, 1)
	if pv.Record.Kind != "promotion" || pv.Body["stage"] != "canary" || pv.Body["trafficShare"] != 0.05 ||
		pv.Body["deployable"].(map[string]any)["modelName"] == "" {
		t.Fatalf("canary record %+v", pv)
	}
	expectProblem(t, e.do("POST", "/api/deployments/"+d1.ID+":promote", canary, "Idempotency-Key", e.key(), "If-Match", `"2"`), 409, "conflict")
	e.deliver(pv.Record.ID, "2/2")
	d1 = e.deployment(d1.ID)
	if d1.Stage != "canary" || d1.State != "active" || d1.TargetName != "era-production" || d1.Slot != "asr-he-il" ||
		d1.TrafficShare == nil || *d1.TrafficShare != 0.05 || !strings.HasPrefix(d1.ModelName, "asr-he-il-") || d1.Pending != nil {
		t.Fatalf("canary %+v", d1)
	}

	// A config-only promotion: the canary's share grows; no model ships, the smoke check is empty.
	pv = e.promoteApproved(d1.ID, "promote", `{"stage":"canary","trafficShare":0.2,"reason":"signals are clean"}`, d1.Rev)
	if !pv.ConfigOnly {
		t.Fatalf("config-only %+v", pv)
	}
	e.deliver(pv.Record.ID, "0/0")
	d1 = e.deployment(d1.ID)
	if *d1.TrafficShare != 0.2 {
		t.Fatalf("share %+v", d1)
	}

	// Production needs the slot's confirmed canary: a shadow of another model is refused.
	pv = e.promoteApproved(d1.ID, "promote", `{"stage":"production","reason":"canary held a week"}`, d1.Rev)
	e.deliver(pv.Record.ID, "0/0")
	d1 = e.deployment(d1.ID)
	if d1.Stage != "production" || d1.TrafficShare != nil {
		t.Fatalf("production %+v", d1)
	}
	expectProblem(t, e.do("POST", "/api/deployments/"+d1.ID+":rollback", `{"reason":"x"}`, "Idempotency-Key", e.key(),
		"If-Match", fmt.Sprintf(`"%d"`, d1.Rev)), 422, "rollback-unavailable")

	// A config-only promotion of the boost lists: the record names the list by its SHA-256, the bundle ships it as
	// decoding configuration and no model.
	if _, err := e.repos.Repos().Commit(ctx, "dep", repos.Change{Message: "boost list", Author: repos.Signature{Name: "admin", Email: "usr_admin@cadence.local"},
		Files: map[string][]byte{"lang/he-IL/boost/names.txt": []byte("# weight: 2\nCadence\nshalom\n")}}); err != nil {
		t.Fatal(err)
	}
	pv = e.promoteApproved(d1.ID, "promote", `{"stage":"production","decoding":{"boostLists":[{"locale":"he-IL","domain":"names"}]},"reason":"names"}`, d1.Rev)
	lists := pv.Body["decoding"].(map[string]any)["boostLists"].([]any)
	if !pv.ConfigOnly || len(lists) != 1 || lists[0].(map[string]any)["sha256"] == "" {
		t.Fatalf("boost promotion %+v", pv.Body["decoding"])
	}
	rec := e.deliver(pv.Record.ID, "0/0")
	if !strings.Contains(rec.Delivery.Script, "decoding/he-IL.names.json") || strings.Contains(rec.Delivery.Script, "SHIP_MODEL='1'") {
		t.Fatalf("boost bundle script %s", rec.Delivery.Script)
	}
	d1 = e.deployment(d1.ID)

	// A second model: shadow (its own replay of the same calls), canary on the slot, then rolled back to the first.
	register("dep2")
	ready("dep2")
	var d2 depView
	newBody2 := `{"version":"model/dep2","replay":{"mount":"corpora","path":"calls-synth/rev1/calls","source":"calls-synth","language":"he-IL"}}`
	e.ok(e.do("POST", "/api/projects/dep/deployments", newBody2, "Idempotency-Key", e.key()), 201, &d2)
	if d2.Shadow.Against == nil || d2.Shadow.Against.Kind != "model" {
		t.Fatalf("against production %+v", d2.Shadow)
	}
	expectProblem(t, e.do("POST", "/api/deployments/"+d2.ID+":promote", `{"stage":"production","target":"era-production","slot":"asr-he-il","reason":"x"}`,
		"Idempotency-Key", e.key(), "If-Match", `"1"`), 422, "canary-required")
	e.ok(e.do("POST", "/api/deployments/"+d2.ID+"/shadow-replays", "", "Idempotency-Key", e.key()), 201, &rp)
	if rp.Against == nil || rp.Against.DecodedBy != "serve" {
		t.Fatalf("second replay %+v", rp)
	}
	e.waitPipelineRun(rp.PipelineRunID, "done")
	pv = e.promoteApproved(d2.ID, "promote", canary, 1)
	if prev, _ := pv.Body["previous"].(map[string]any); prev["versionId"] != m1.ID {
		t.Fatalf("previous %+v", pv.Body["previous"])
	}
	e.deliver(pv.Record.ID, "2/2")
	d2 = e.deployment(d2.ID)
	pv = e.promoteApproved(d2.ID, "rollback", `{"reason":"names regressed"}`, d2.Rev)
	if pv.Record.Kind != "rollback" || pv.Body["deployable"].(map[string]any)["modelName"] != d1.ModelName {
		t.Fatalf("rollback %+v", pv.Body)
	}
	e.deliver(pv.Record.ID, "0/0")
	d2, d1 = e.deployment(d2.ID), e.deployment(d1.ID)
	if d2.Stage != "retired" || d2.State != "rolled-back" || d1.Stage != "production" || d1.State != "active" {
		t.Fatalf("after rollback %+v / %+v", d2, d1)
	}

	// The chain holds every step, and the deployment events went out.
	if c := e.chain("era-production"); !c.Intact || len(c.Items) != 13 {
		t.Fatalf("chain intact %v, %d records", c.Intact, len(c.Items))
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'deployment.stage_changed'"); n < 5 {
		t.Fatalf("%d stage changes", n)
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'shadow.replayed'"); n < 2 {
		t.Fatalf("%d shadow.replayed", n)
	}

	// Retention: the nights' texts and segment audio go; the summaries stay.
	e.admin.deployments.Now = func() time.Time { return time.Now().Add(200 * 24 * time.Hour) }
	if n, err := e.admin.deployments.Retention(ctx); err != nil || n < 2 {
		t.Fatalf("retention %d %v", n, err)
	}
	e.admin.deployments.Now = nil
	var kept replayView
	e.ok(e.do("GET", "/api/shadow-replays/"+night.ID, ""), 200, &kept)
	if kept.TextsEvictedAt == nil || len(kept.Worst) != 0 || kept.Calls != 3 || kept.Divergence == nil {
		t.Fatalf("after retention %+v", kept)
	}
	if _, ok, _ := deployments.SegmentAudio(ctx, e.pool, worstAudio); ok {
		t.Fatal("segment audio kept")
	}
}
