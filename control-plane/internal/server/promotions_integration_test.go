//go:build integration

package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
	"github.com/usunrise88/cadence/control-plane/templates"
)

type targetView struct {
	ID, Name, Kind, RepositoryPath, Endpoint, State string
	Slots                                           []string
	Rev                                             int
	Chain                                           *struct {
		Records, HeadSeq, Pending int
		HeadHash                  string
	}
	ApprovalID string `json:"approvalId"`
}

type recordView struct {
	ID, Kind, Hash, PrevHash, Signature, KeyID, Canonical, Slot, State, ClosedBy, RefersTo, DeploymentID string
	Seq, Rev                                                                                             int
	Verified                                                                                             bool
	Problems                                                                                             []string
	Body                                                                                                 map[string]any
	PublicKeyPem                                                                                         string
	Delivery                                                                                             *struct {
		State, ArtifactHash, Script, BundleURL, Error string
		Smoke                                         *struct{ Utterances, Required int }
	}
}

type chainView struct {
	TargetName string
	Intact     bool
	Problems   []string
	Items      []recordView
}

// deploySources gives the bundle builder the fixture's smoke set (stream D1 supplies the real one).
type deploySources struct {
	delivery.StoreSources
	smoke []delivery.SmokeItem
}

func (d deploySources) Smoke(context.Context, promotions.Record) (delivery.Smoke, error) {
	return delivery.Smoke{Items: d.smoke, Client: []byte("#!/bin/sh\ncat \"$3\"\n")}, nil
}

func startDeploy(t *testing.T) (*env, *delivery.Service) {
	t.Helper()
	dsvc := &delivery.Service{Templates: templates.FS}
	e := startWith(t, func(c *Config) {
		dsvc.CAS, dsvc.Jobs = c.CAS, c.Jobs
	}, func(pool *pgxpool.Pool, js *jobs.Service) {
		dsvc.Pool = pool
		dsvc.Register(js)
	})
	if _, created, err := (&promotions.Keyring{Secrets: e.admin.Secrets}).Ensure(context.Background(), e.pool, time.Now()); err != nil || !created {
		t.Fatalf("instance key: created %v, %v", created, err)
	}
	return e, dsvc
}

func (e *env) chain(target string) chainView {
	e.t.Helper()
	var c chainView
	e.ok(e.do("GET", "/api/deployment-targets/"+target+"/promotions", ""), 200, &c)
	return c
}

func (e *env) target(ref string) targetView {
	e.t.Helper()
	var tv targetView
	e.ok(e.do("GET", "/api/deployment-targets/"+ref, ""), 200, &tv)
	return tv
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const deliveryTarget = `{"name":"era-production","kind":"delivery","description":"Эра's Triton",
	"serves":[{"family":"fam","formats":["test-identity"],"profiles":["80ms"]}],"server":{"kind":"triton","version":"26.07"},
	"repositoryPath":"/opt/era/triton/models","slots":["asr-he-il"],"concurrency":32,"cardClass":"blackwell-96gb"}`

func TestDeploymentTargetsAndPromotionChain(t *testing.T) {
	e, dsvc := startDeploy(t)
	ctx := context.Background()

	// The staging target is seeded from defaults.yaml; it has no chain.
	if _, err := targets.Seed(ctx, e.pool, "staging", "http://triton:8000", targets.Server{Kind: "triton", Version: "26.07"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st := e.target("staging"); st.Kind != "staging" || st.Endpoint != "http://triton:8000" || st.Chain != nil {
		t.Fatalf("staging %+v", st)
	}
	if c := e.chain("staging"); !c.Intact || len(c.Items) != 0 {
		t.Fatalf("staging chain %+v", c)
	}

	// A delivery target never has an endpoint: refused before any approval. A dry run validates.
	expectProblem(t, e.do("POST", "/api/deployment-targets", strings.Replace(deliveryTarget, `"slots"`, `"endpoint":"http://era:8000","slots"`, 1),
		"Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/deployment-targets", strings.Replace(deliveryTarget, `"triton"`, `"unknown-server"`, 1),
		"Idempotency-Key", e.key()), 422, "validation-failed")
	var dry targetView
	e.ok(e.do("POST", "/api/deployment-targets?dryRun=true", deliveryTarget, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Name != "era-production" || dry.Chain != nil || e.count("SELECT count(*) FROM deployment_targets") != 1 {
		t.Fatalf("dry run %+v", dry)
	}

	// Creating a target is an approval for everyone, agents and people; the admin's approval creates it with its
	// genesis record, naming the requester and the approver.
	var acc accepted
	e.ok(e.agent("POST", "/api/deployment-targets", deliveryTarget, "Idempotency-Key", e.key()), 202, &acc)
	if a := e.approval(acc.ApprovalID); a.Rule != "deployment-targets" || a.Scope != "registry" {
		t.Fatalf("agent approval %+v", a)
	}
	e.ok(e.do("POST", "/api/deployment-targets", deliveryTarget, "Idempotency-Key", e.key()), 202, &acc)
	status, body := e.approveReplay(acc.ApprovalID, "deployment-targets")
	if status != http.StatusCreated {
		t.Fatalf("replay %d %s", status, body)
	}
	tg := e.target("era-production")
	if tg.Kind != "delivery" || tg.Chain == nil || tg.Chain.Records != 1 || tg.ApprovalID != acc.ApprovalID {
		t.Fatalf("target %+v", tg)
	}
	c := e.chain("era-production")
	if !c.Intact || len(c.Items) != 1 || c.Items[0].Kind != "genesis" || !c.Items[0].Verified || c.Items[0].PrevHash != strings.Repeat("0", 64) {
		t.Fatalf("genesis %+v", c)
	}
	g := c.Items[0].Body
	if appr, _ := g["approval"].(map[string]any); appr["id"] != acc.ApprovalID || appr["approver"].(map[string]any)["id"] != "usr_admin" {
		t.Errorf("genesis approval %v", g["approval"])
	}
	if cfg, _ := g["targetConfig"].(map[string]any); cfg["repositoryPath"] != "/opt/era/triton/models" || g["publicKeyPem"] == "" {
		t.Errorf("genesis payload %v", g)
	}
	var list struct {
		Items       []targetView
		SigningKeys []struct{ ID, State, PublicKeyPem string }
	}
	e.ok(e.do("GET", "/api/deployment-targets", ""), 200, &list)
	if len(list.Items) != 2 || len(list.SigningKeys) != 1 || list.SigningKeys[0].ID != c.Items[0].KeyID ||
		!strings.HasPrefix(list.SigningKeys[0].PublicKeyPem, "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("list %+v", list)
	}

	// An edit of what records name is an approval too and appends target-changed; a description alone does not.
	e.ok(e.do("PATCH", "/api/deployment-targets/era-production", `{"slots":["asr-he-il","asr-sr"]}`, "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 202, &acc)
	if status, body := e.approveReplay(acc.ApprovalID, "deployment-targets"); status != 200 {
		t.Fatalf("edit replay %d %s", status, body)
	}
	e.ok(e.do("PATCH", "/api/deployment-targets/era-production", `{"description":"production"}`, "Idempotency-Key", e.key(),
		"If-Match", `"2"`), 202, &acc)
	e.approveReplay(acc.ApprovalID, "deployment-targets")
	expectProblem(t, e.do("PATCH", "/api/deployment-targets/era-production", `{"description":"x"}`, "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 412, "precondition-failed")
	c = e.chain("era-production")
	if len(c.Items) != 2 || c.Items[1].Kind != "target-changed" || !c.Intact {
		t.Fatalf("after edits %+v", c)
	}
	tg = e.target("era-production")

	// Stream D4 appends a promotion record in the transaction that decides its approval and queues the bundle; here
	// the test does both for a fixture deployable.
	p := e.newProject("hebrew")
	deployable, manifest, files, smoke := fixtureDeployable(t, e.admin.CAS)
	dsvc.Sources = deploySources{StoreSources: delivery.StoreSources{CAS: e.admin.CAS}, smoke: smoke}
	approver := auth.DevActor()
	promote := func(slot string) promotions.Record {
		var rec promotions.Record
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			r, drafts, err := e.admin.promotions.Append(ctx, tx, promotions.AppendInput{Kind: promotions.KindPromotion,
				TargetID: tg.ID, Slot: slot, ProjectID: p.ID, DeploymentID: "dep_test", Actor: testAgent,
				ApprovalID: "apr_test", Approver: &approver, DecidedAt: time.Now(), Reason: "beats production on telephony",
				Body: map[string]any{"stage": "canary", "trafficShare": 0.05,
					"model":      map[string]any{"versionId": "ver_test", "version": "model/hebrew@2026-11-02.ab12cd"},
					"deployable": map[string]any{"hash": deployable, "format": "test-identity", "profile": "80ms", "modelName": "asr-he-il-2026-11-02-ab12cd", "manifestSha256": manifest, "files": files}}})
			if err != nil {
				return err
			}
			_, more, err := dsvc.Build(ctx, tx, r.ID)
			if err != nil {
				return err
			}
			rec = r
			return events.Append(ctx, tx, testAgent, nil, append(drafts, more...))
		}); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	rec := promote("asr-he-il")
	var job string
	if err := e.pool.QueryRow(ctx, "SELECT job_id FROM promotion_deliveries WHERE record_id = $1", rec.ID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	e.waitJob(job, "done")

	var got recordView
	resp := e.ok(e.do("GET", "/api/promotions/"+rec.ID, ""), 200, &got)
	if got.State != "pending" || !got.Verified || got.Rev != 1 || resp.Header.Get("ETag") != `"1"` || got.Delivery == nil ||
		got.Delivery.State != "ready" || got.Delivery.Smoke == nil || got.Delivery.Smoke.Required != 3 ||
		!strings.Contains(got.Delivery.Script, "RECORD_HASH='"+rec.Hash+"'") || got.Delivery.BundleURL == "" || got.PublicKeyPem == "" {
		t.Fatalf("promotions.get %+v", got)
	}
	if appr := got.Body["approval"].(map[string]any); appr["approver"].(map[string]any)["id"] != "usr_admin" ||
		got.Body["requestedBy"].(map[string]any)["actor"] != "agent" || got.Body["project"].(map[string]any)["slug"] != "hebrew" {
		t.Errorf("record body %v", got.Body)
	}

	// The bundle downloads through its signed link: deliver.sh executable, record.json the canonical record.
	bundle := e.do("GET", strings.TrimPrefix(got.Delivery.BundleURL, ""), "")
	if bundle.StatusCode != 200 || !strings.Contains(bundle.Header.Get("Content-Disposition"), rec.ID+".tar.gz") {
		t.Fatalf("download %d", bundle.StatusCode)
	}
	entries := untar(t, bundle.Body)
	if entries[rec.ID+"/record.json"].body != string(rec.Canonical) || entries[rec.ID+"/deliver.sh"].mode != 0o755 ||
		entries[rec.ID+"/models/asr-he-il-2026-11-02-ab12cd/config.pbtxt"].body == "" {
		t.Fatalf("bundle entries %v", keys(entries))
	}
	expectProblem(t, e.do("GET", strings.Replace(got.Delivery.BundleURL, "sig=", "sig=x", 1), ""), 403, "delivery-link-invalid")

	// Agents get no link and no bundle, and never confirm a delivery.
	var asAgent recordView
	e.ok(e.agent("GET", "/api/promotions/"+rec.ID, ""), 200, &asAgent)
	if asAgent.Delivery == nil || asAgent.Delivery.BundleURL != "" {
		t.Errorf("an agent got a bundle link: %+v", asAgent.Delivery)
	}
	expectProblem(t, e.agent("GET", "/api/promotions/"+rec.ID+"/delivery", ""), 403, "forbidden")
	receipt := func(served, smoke string) string {
		return fmt.Sprintf(`{"receipt":"deliver.sh: smoke check: 3/3\nCADENCE-RECEIPT 1 %s %s %s era-asr-01 2026-11-03T10:00:00Z\n"}`, rec.Hash, served, smoke)
	}
	expectProblem(t, e.agent("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 403, "forbidden")

	// A receipt that does not match changes nothing.
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(strings.Repeat("e", 64), "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 422, "promotion-receipt-mismatch")
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "2/3"), "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 422, "promotion-receipt-mismatch")
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "4/4"), "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 422, "promotion-receipt-mismatch")
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", strings.Replace(receipt(manifest, "3/3"), rec.Hash, strings.Repeat("f", 64), 1),
		"Idempotency-Key", e.key(), "If-Match", `"1"`), 422, "promotion-receipt-mismatch")
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"2"`), 412, "precondition-failed")
	var dryConf recordView
	e.ok(e.do("POST", "/api/promotions/"+rec.ID+":verify?dryRun=true", receipt(manifest, "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"1"`), 200, &dryConf)
	if dryConf.State != "confirmed" || len(e.chain("era-production").Items) != 3 {
		t.Fatalf("dry run confirmed %+v", dryConf)
	}

	// The real receipt appends a signed confirmation naming the person; the record is confirmed.
	var conf recordView
	e.ok(e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "3/3"), "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &conf)
	if conf.State != "confirmed" || conf.Rev != 2 || conf.ClosedBy == "" {
		t.Fatalf("confirmed %+v", conf)
	}
	c = e.chain("era-production")
	last := c.Items[len(c.Items)-1]
	if !c.Intact || last.Kind != "confirmation" || last.RefersTo != rec.ID || !last.Verified ||
		last.Body["confirmedBy"].(map[string]any)["id"] != "usr_admin" || !strings.HasPrefix(last.Body["receipt"].(string), "CADENCE-RECEIPT 1 "+rec.Hash) ||
		last.DeploymentID != "dep_test" {
		t.Fatalf("confirmation %+v", last)
	}
	expectProblem(t, e.do("POST", "/api/promotions/"+rec.ID+":verify", receipt(manifest, "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"2"`), 422, "promotion-receipt-mismatch")
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'deploy.dep_test' AND type = 'promotion.confirmed'"); n != 1 {
		t.Errorf("promotion.confirmed on deploy.dep_test: %d", n)
	}

	// A promotion without a receipt is withdrawn after deploy.delivery_pending_days.
	e.admin.promotions.Now = func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }
	stale := promote("asr-he-il")
	e.admin.promotions.Now = nil
	if n, err := e.admin.promotions.WithdrawStale(ctx); err != nil || n != 1 {
		t.Fatalf("withdrawn %d, %v", n, err)
	}
	e.ok(e.do("GET", "/api/promotions/"+stale.ID, ""), 200, &got)
	if got.State != "withdrawn" {
		t.Fatalf("stale promotion %+v", got)
	}
	expectProblem(t, e.do("POST", "/api/promotions/"+stale.ID+":verify", receipt(manifest, "3/3"), "Idempotency-Key", e.key(),
		"If-Match", `"2"`), 422, "promotion-receipt-mismatch")

	// Key rotation: a key-rotation record signed by the old key; later records are signed by the new one.
	var rotated []promotions.Record
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		_, _, rotated, _, err = e.admin.promotions.Rotate(ctx, tx, auth.DevActor(), "yearly rotation", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	after := promote("asr-sr")
	c = e.chain("era-production")
	if !c.Intact || len(rotated) != 1 || after.KeyID == rotated[0].KeyID || c.Items[len(c.Items)-1].KeyID != after.KeyID {
		t.Fatalf("after rotation %+v", c.Problems)
	}
	e.ok(e.do("GET", "/api/deployment-targets", ""), 200, &list)
	if len(list.SigningKeys) != 2 || list.SigningKeys[0].State != "current" || list.SigningKeys[0].ID != after.KeyID {
		t.Fatalf("keys %+v", list.SigningKeys)
	}

	// Records are append-only; an edit made behind the trigger's back shows on the next read.
	if _, err := e.pool.Exec(ctx, "UPDATE promotion_records SET slot = 'x' WHERE id = $1", rec.ID); err == nil {
		t.Fatal("a record was updated")
	}
	if _, err := e.pool.Exec(ctx, "DELETE FROM promotion_records WHERE id = $1", rec.ID); err == nil {
		t.Fatal("a record was deleted")
	}
	if _, err := e.pool.Exec(ctx, `ALTER TABLE promotion_records DISABLE TRIGGER promotion_records_no_update;
		UPDATE promotion_records SET canonical = replace(canonical, '"trafficShare":0.05', '"trafficShare":0.5') WHERE id = '`+rec.ID+`';
		ALTER TABLE promotion_records ENABLE TRIGGER promotion_records_no_update`); err != nil {
		t.Fatal(err)
	}
	c = e.chain("era-production")
	var tampered recordView
	for _, r := range c.Items {
		if r.ID == rec.ID {
			tampered = r
		}
	}
	if c.Intact || tampered.Verified || !strings.Contains(strings.Join(tampered.Problems, ";"), "does not hash") {
		t.Fatalf("tampered chain intact %v, record %+v", c.Intact, tampered.Problems)
	}

	// Archiving is the admin's; agents meet no-deletes.
	expectProblem(t, e.agent("POST", "/api/deployment-targets/era-production:archive", "", "Idempotency-Key", e.key(), "If-Match", `"3"`), 403, "policy-denied")
	var arch targetView
	e.ok(e.do("POST", "/api/deployment-targets/era-production:archive", "", "Idempotency-Key", e.key(), "If-Match", `"3"`), 200, &arch)
	if arch.State != "archived" || len(e.chain("era-production").Items) != len(c.Items) {
		t.Fatalf("archived %+v", arch)
	}
}

// fixtureDeployable stores a cadence.deployable/1 directory with an identity-backend model and three smoke files.
func fixtureDeployable(t *testing.T, store *cas.Store) (string, string, []map[string]any, []delivery.SmokeItem) {
	t.Helper()
	model := map[string]string{"config.pbtxt": "backend: \"identity\"\nmax_batch_size: 0\n", "1/README": "version directory\n"}
	var (
		files   []cas.File
		entries []map[string]any
		mfs     []delivery.ManifestEntry
	)
	for p, b := range model {
		h, err := store.PutBytes([]byte(b))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, cas.File{Path: "model/" + p, Hash: h, Size: int64(len(b))})
		entries = append(entries, map[string]any{"path": p, "sha256": sha([]byte(b)), "bytes": len(b)})
		mfs = append(mfs, delivery.ManifestEntry{Path: p, SHA256: sha([]byte(b))})
	}
	manifest := delivery.ManifestSHA256(mfs)
	dj, _ := json.Marshal(map[string]any{"format": "test-identity", "manifestSha256": manifest, "serving": map[string]any{"modelDir": "model"}})
	h, _ := store.PutBytes(dj)
	files = append(files, cas.File{Path: "deployable.json", Hash: h, Size: int64(len(dj))})
	dep, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	var smoke []delivery.SmokeItem
	for i, text := range []string{"shalom", "boker tov", "toda"} {
		h, _ := store.PutBytes([]byte(text))
		smoke = append(smoke, delivery.SmokeItem{Name: fmt.Sprintf("%02d.wav", i+1), Hash: h, Text: text})
	}
	return dep, manifest, entries, smoke
}

type tarEntry struct {
	mode int64
	body string
}

func untar(t *testing.T, r io.ReadCloser) map[string]tarEntry {
	t.Helper()
	defer func() { _ = r.Close() }()
	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]tarEntry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = tarEntry{mode: h.Mode, body: string(b)}
	}
}

func keys(m map[string]tarEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
