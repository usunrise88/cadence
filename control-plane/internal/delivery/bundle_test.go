package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// The manifest of a model directory is sha256sum's output over its files sorted by path; this golden value is
// `cd dir && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while read f; do sha256sum "$f"; done | sha256sum`
// over the two files below, run on a Linux host.
func TestManifestSHA256MatchesSha256sum(t *testing.T) {
	files := []ManifestEntry{
		{Path: "z/b.txt", SHA256: sum("world\n")},
		{Path: "a.txt", SHA256: sum("hello\n")},
	}
	wantText := sum("hello\n") + "  a.txt\n" + sum("world\n") + "  z/b.txt\n"
	if got := ManifestText(files); got != wantText {
		t.Fatalf("manifest text\n%s", got)
	}
	if got := ManifestSHA256(files); got != "e35e47c3a0528f569070dbc1c08b96c88bc9979169c13521e35ca417ac81514e" {
		t.Errorf("manifest sha256 %s", got)
	}
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fixtureModel is a model directory the identity backend of the inference server loads on a CPU; its config names
// no model, so it loads under its versioned directory name.
var fixtureModel = map[string]string{
	"config.pbtxt": `backend: "identity"
max_batch_size: 0
input [ { name: "INPUT0", data_type: TYPE_STRING, dims: [ 1 ] } ]
output [ { name: "OUTPUT0", data_type: TYPE_STRING, dims: [ 1 ] } ]
instance_group [ { kind: KIND_CPU } ]
`,
	"1/README": "the identity backend reads no weights; this file keeps the version directory\n",
}

// fixtureClient is the smoke client of the fixture: it sends a smoke file's bytes through the identity model and
// prints what comes back (a family's real client streams the WAV through its served model).
const fixtureClient = `#!/bin/sh
# fixture smoke client: transcribe <server url> <model name> <wav>
set -eu
text=$(cat "$3")
curl -fsS -X POST "$1/v2/models/$2/infer" -H 'Content-Type: application/json' \
	-d "{\"inputs\":[{\"name\":\"INPUT0\",\"shape\":[1],\"datatype\":\"BYTES\",\"data\":[\"$text\"]}]}" |
	sed -n 's/.*"data":\["\([^"]*\)"\].*/\1/p'
`

type fakeSources struct {
	store *cas.Store
	smoke []SmokeItem
}

func (f fakeSources) ModelFiles(ctx context.Context, h string) ([]ModelFile, error) {
	return StoreSources{CAS: f.store}.ModelFiles(ctx, h)
}

func (f fakeSources) Smoke(context.Context, promotions.Record) (Smoke, error) {
	return Smoke{Items: f.smoke, Client: []byte(fixtureClient)}, nil
}

func (f fakeSources) Decoding(ctx context.Context, rec promotions.Record) ([]DecodingFile, error) {
	return StoreSources{CAS: f.store}.Decoding(ctx, rec)
}

type fixture struct {
	svc     *Service
	store   *cas.Store
	rec     promotions.Record
	chain   promotions.Chain
	key     promotions.Key
	pinPEM  string
	model   string
	targetT targets.Target
}

// newFixture builds a deployable in a temporary content store and a signed promotion record of it, chained after a
// genesis record, with a throwaway key (no key from any stand is ever committed).
func newFixture(t *testing.T) fixture {
	t.Helper()
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var (
		files   []cas.File
		entries []map[string]any
		mfs     []ManifestEntry
	)
	for p, body := range fixtureModel {
		h, err := store.PutBytes([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, cas.File{Path: "model/" + p, Hash: h, Size: int64(len(body))})
		entries = append(entries, map[string]any{"path": p, "sha256": sum(body), "bytes": len(body)})
		mfs = append(mfs, ManifestEntry{Path: p, SHA256: sum(body)})
	}
	manifest := ManifestSHA256(mfs)
	dj, _ := json.Marshal(map[string]any{"format": "test-identity", "profile": "80ms", "manifestSha256": manifest,
		"serving": map[string]any{"modelDir": "model"}, "files": entries})
	h, _ := store.PutBytes(dj)
	files = append(files, cas.File{Path: "deployable.json", Hash: h, Size: int64(len(dj))})
	deployable, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	var smoke []SmokeItem
	for i, text := range []string{"shalom", "boker-tov", "toda"} {
		h, _ := store.PutBytes([]byte(text))
		smoke = append(smoke, SmokeItem{Name: fmt.Sprintf("%02d.wav", i+1), Hash: h, Text: text})
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	key := promotions.Key{ID: promotions.KeyID(pub), Public: pub, Private: priv}
	target := targets.Target{ID: "dtg_0192f5a4-0000-7000-8000-0000000000aa", Name: "era-production", Kind: targets.KindDelivery,
		Config: targets.Config{Server: targets.Server{Kind: "triton", Version: "26.07"}, RepositoryPath: "/tmp/repo",
			Slots: []string{"asr-he-il"}, Serves: []targets.Serves{{Family: "fam", Formats: []string{"test-identity"}, Profiles: []string{"80ms"}}}}}
	model := "asr-he-il-2026-11-02-ab12cd"
	sign := func(seq int, prev, id, kind string, extra map[string]any) promotions.Record {
		body := map[string]any{"schema": promotions.Schema, "kind": kind, "id": id,
			"target": map[string]any{"id": target.ID, "name": target.Name, "seq": seq, "prevHash": prev},
			"key":    map[string]any{"id": key.ID, "alg": "Ed25519"}, "createdAt": "2026-11-03T09:12:44Z"}
		for k, v := range extra {
			body[k] = v
		}
		canon, err := promotions.CanonicalJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		r := promotions.Record{ID: id, TargetID: target.ID, Seq: seq, Kind: kind, Canonical: canon, Hash: promotions.Hash(canon),
			PrevHash: prev, Signature: promotions.Sign(priv, canon), KeyID: key.ID, CreatedAt: time.Now()}
		if s, ok := extra["slot"].(string); ok {
			r.Slot = s
		}
		return r
	}
	genesis := sign(1, promotions.GenesisPrevHash, "prm_0192f5a4-0000-7000-8000-000000000001", promotions.KindGenesis,
		map[string]any{"targetConfig": target.Payload()})
	promo := sign(2, genesis.Hash, "prm_0192f5a4-0000-7000-8000-000000000002", promotions.KindPromotion, map[string]any{
		"slot": "asr-he-il", "stage": "canary", "trafficShare": 0.05,
		"deployable": map[string]any{"hash": deployable, "format": "test-identity", "profile": "80ms", "modelName": model,
			"manifestSha256": manifest, "files": entries},
	})
	recs := []promotions.Record{genesis, promo}
	if intact, probs := promotions.VerifyChain(recs, map[string]ed25519.PublicKey{key.ID: pub}); !intact {
		t.Fatalf("fixture chain: %v", probs)
	}
	svc := &Service{CAS: store, Templates: templates.FS, Sources: fakeSources{store: store, smoke: smoke}}
	return fixture{svc: svc, store: store, rec: recs[1], chain: promotions.Chain{Target: target, Records: recs, Intact: true},
		key: key, pinPEM: promotions.PublicPEM(pub), model: model, targetT: target}
}

func TestBundleOfAPromotion(t *testing.T) {
	f := newFixture(t)
	files, v, err := f.svc.assemble(t.Context(), f.rec, f.chain, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if !v.ShipModel || v.SmokeTotal != 3 || v.SmokeRequired != 3 || v.TrafficShare != "0.05" || v.Stage != "canary" {
		t.Errorf("values %+v", v)
	}
	hash, _, err := Put(f.store, files)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDir(f.store, hash, dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"deliver.sh", "record.json", "record.sig", "instance.pub", "models/" + f.model + "/config.pbtxt",
		"models/" + f.model + "/1/README", "models/" + f.model + ".sha256", "smoke/01.wav", "smoke/manifest.tsv", "smoke/transcribe"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("bundle lacks %s", p)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "deliver.sh")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("deliver.sh mode %v", st.Mode())
	}
	rec, _ := os.ReadFile(filepath.Join(dir, "record.json"))
	if string(rec) != string(f.rec.Canonical) {
		t.Error("record.json is not the canonical record")
	}
	script, _ := os.ReadFile(filepath.Join(dir, "deliver.sh"))
	for _, want := range []string{"RECORD_ID='" + f.rec.ID + "'", "RECORD_HASH='" + f.rec.Hash + "'", "KEY_ID='" + f.key.ID + "'",
		"MODEL_NAME='" + f.model + "'", "SHIP_MODEL='1'", "SMOKE_REQUIRED='3'", "REPOSITORY_PATH='/tmp/repo'",
		"server_load() {", "CADENCE-RECEIPT 1"} {
		if !strings.Contains(string(script), want) {
			t.Errorf("deliver.sh lacks %q", want)
		}
	}
	// The shell test (scripts/test-delivery.sh) runs this bundle against a real inference server.
	if out := os.Getenv("CADENCE_DELIVERY_BUNDLE_OUT"); out != "" {
		if err := WriteDir(f.store, hash, filepath.Join(out, "bundle")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "pin.pem"), []byte(f.pinPEM), 0o644); err != nil {
			t.Fatal(err)
		}
		_, other, _ := ed25519.GenerateKey(nil)
		if err := os.WriteFile(filepath.Join(out, "other.pem"), []byte(promotions.PublicPEM(other.Public().(ed25519.PublicKey))), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "expected.txt"), []byte(f.rec.Hash+" "+v.ManifestSHA256+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigOnlyAndRollbackShipNoModel(t *testing.T) {
	f := newFixture(t)
	// A confirmed promotion of the same deployable on the slot: the next promotion of it is config-only.
	confirmed := f.chain.Records[1]
	confirmed.State = promotions.StateConfirmed
	next := f.rec
	next.Seq = 3
	chain := f.chain
	chain.Records = []promotions.Record{f.chain.Records[0], confirmed, next}
	_, v, err := f.svc.assemble(t.Context(), next, chain, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if v.ShipModel || v.SmokeTotal != 0 {
		t.Errorf("config-only promotion: ship %v smoke %d", v.ShipModel, v.SmokeTotal)
	}
	rb := f.rec
	rb.Kind = promotions.KindRollback
	if _, v, err := f.svc.assemble(t.Context(), rb, f.chain, f.key); err != nil || v.ShipModel || v.SmokeTotal != 0 || v.Kind != "rollback" {
		t.Errorf("a rollback restores the installed version without shipping it: %+v %v", v, err)
	}
}

func TestBundleRefusesWhatDoesNotMatchTheRecord(t *testing.T) {
	f := newFixture(t)
	bad := f.rec
	bad.Verified = false
	bad.Problems = []string{"the signature does not verify"}
	if _, _, err := f.svc.assemble(t.Context(), bad, f.chain, f.key); err == nil {
		t.Error("built a bundle for a record that does not verify")
	}
	// The deployable's files changed after the record was signed.
	f.svc.Sources = fakeSources{store: f.store, smoke: nil}
	if _, _, err := f.svc.assemble(t.Context(), f.rec, f.chain, f.key); err == nil || !strings.Contains(err.Error(), "smoke") {
		t.Errorf("an empty smoke set: %v", err)
	}
	f.svc.Sources = StoreSources{CAS: f.store}
	if _, _, err := f.svc.assemble(t.Context(), f.rec, f.chain, f.key); err == nil || !strings.Contains(err.Error(), "stream D1") {
		t.Errorf("the default smoke source: %v", err)
	}
	if _, err := checkModel([]ModelFile{{Path: "config.pbtxt", SHA256: sum("other")}}, map[string]any{"files": []any{
		map[string]any{"path": "config.pbtxt", "sha256": sum("x")}}}, "00"); err == nil {
		t.Error("a model file with another hash passed")
	}
	if _, err := checkModel([]ModelFile{{Path: "a b", SHA256: sum("x")}}, nil, "00"); err == nil {
		t.Error("an unsafe path passed")
	}
}

func TestRenderRefusesUnsafeValues(t *testing.T) {
	base := Values{RecordID: "prm_0192f5a4-0000-7000-8000-000000000002", RecordHash: strings.Repeat("a", 64), Kind: "promotion",
		KeyID: "ed25519:" + strings.Repeat("b", 32), Target: "era-production", Slot: "asr-he-il", Stage: "canary",
		TrafficShare: "0.05", ModelName: "m-1", ManifestSHA256: strings.Repeat("c", 64), RepositoryPath: "/opt/models",
		ServerKind: "triton", ShipModel: true, SmokeTotal: 1, SmokeRequired: 1}
	if _, err := Render(templates.FS, base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Values){
		"quote in slot":       func(v *Values) { v.Slot = "a'b" },
		"command in path":     func(v *Values) { v.RepositoryPath = "/opt/$(reboot)" },
		"space in model":      func(v *Values) { v.ModelName = "m 1" },
		"unknown server kind": func(v *Values) { v.ServerKind = "vllm" },
		"genesis":             func(v *Values) { v.Kind = "genesis" },
		"smoke over total":    func(v *Values) { v.SmokeRequired = 2 },
	} {
		v := base
		mutate(&v)
		if _, err := Render(templates.FS, v); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	if got := ServerKinds(templates.FS); len(got) == 0 || got[0] != "triton" {
		t.Errorf("server kinds %v", got)
	}
}

func TestSignedLinks(t *testing.T) {
	s := NewSigner([]byte("k"))
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	sig := s.Sign("prm_1", "usr_a", now.Unix()+60)
	if err := s.Verify("prm_1", "usr_a", now.Unix()+60, sig); err != nil {
		t.Error(err)
	}
	for _, c := range []struct {
		rec, viewer string
		exp         int64
	}{{"prm_2", "usr_a", now.Unix() + 60}, {"prm_1", "usr_b", now.Unix() + 60}, {"prm_1", "usr_a", now.Unix() + 61}} {
		if s.Verify(c.rec, c.viewer, c.exp, sig) == nil {
			t.Errorf("verified for %+v", c)
		}
	}
	old := s.Sign("prm_1", "usr_a", now.Unix()-1)
	if s.Verify("prm_1", "usr_a", now.Unix()-1, old) == nil {
		t.Error("an expired link verified")
	}
}
