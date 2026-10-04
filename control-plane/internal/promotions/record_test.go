package promotions

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The golden record was canonicalized by Python's json (sorted keys, no whitespace, ensure_ascii off: JCS for this
// data), hashed by sha256sum and signed by `openssl pkeyutl -sign -rawin` with the RFC 8032 test-1 key — tools
// independent of this package. Ed25519 is deterministic, so Go must produce the same bytes.
const (
	goldenSeed      = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	goldenPublicHex = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	goldenKeyID     = "ed25519:21fe31dfa154a261626bf854046fd227"
	goldenCanonical = `{"createdAt":"2026-11-03T09:12:44Z","deployable":{"files":[{"bytes":120,"path":"config.pbtxt","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}],"format":"triton-onnx-cache-aware","hash":"b3:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","manifestSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","modelName":"asr-he-il-2026-11-02-ab12cd","profile":"80ms"},"id":"prm_0192f5a4-0000-7000-8000-000000000001","key":{"alg":"Ed25519","id":"ed25519:21fe31dfa154a261626bf854046fd227"},"kind":"promotion","reason":"Ünïcode “quotes”, a tab\tand a bell\u0007","schema":"cadence.promotion/1","slot":"asr-he-il","stage":"canary","target":{"id":"dtg_0192f5a4-0000-7000-8000-0000000000aa","name":"era-production","prevHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","seq":2},"trafficShare":0.05}`
	goldenHash      = "9d274ee82d2b1671e7715195ba470c9dea18713d78ecf7ccf5910151afcad61b"
	goldenSignature = "yqpzVQKG1O1Q+QuKvYiijfHDNmNIV+uuiEDrDFgsHkt/KCpAyEfmZ8NGr8ciu1qOp+uaNWccWgwfKV3AQ1L3AQ=="
	goldenPublicPEM = "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=\n-----END PUBLIC KEY-----\n"
)

func goldenKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(goldenSeed)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestGoldenRecordCanonicalHashAndSignature(t *testing.T) {
	priv := goldenKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != goldenPublicHex {
		t.Fatalf("public key %x", pub)
	}
	if got := KeyID(pub); got != goldenKeyID {
		t.Errorf("KeyID = %s, want %s", got, goldenKeyID)
	}
	if got := PublicPEM(pub); got != goldenPublicPEM {
		t.Errorf("PublicPEM =\n%s", got)
	}
	if back, err := ParsePublicPEM(goldenPublicPEM); err != nil || !back.Equal(pub) {
		t.Errorf("ParsePublicPEM: %v", err)
	}
	// The same record built as Go values canonicalizes to the golden text.
	body := map[string]any{
		"schema": Schema, "kind": KindPromotion, "id": "prm_0192f5a4-0000-7000-8000-000000000001",
		"target": map[string]any{"id": "dtg_0192f5a4-0000-7000-8000-0000000000aa", "name": "era-production", "seq": 2,
			"prevHash": strings.Repeat("a", 64)},
		"slot": "asr-he-il", "stage": "canary", "trafficShare": 0.05,
		"deployable": map[string]any{"hash": "b3:" + strings.Repeat("b", 64), "format": "triton-onnx-cache-aware",
			"profile": "80ms", "modelName": "asr-he-il-2026-11-02-ab12cd", "manifestSha256": strings.Repeat("c", 64),
			"files": []map[string]any{{"path": "config.pbtxt", "sha256": strings.Repeat("d", 64), "bytes": 120}}},
		"reason":    "Ünïcode “quotes”, a tab\tand a bell\a",
		"key":       map[string]any{"id": goldenKeyID, "alg": "Ed25519"},
		"createdAt": "2026-11-03T09:12:44Z",
	}
	canon, err := CanonicalJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != goldenCanonical {
		t.Fatalf("canonical form\n got %s\nwant %s", canon, goldenCanonical)
	}
	if got := Hash(canon); got != goldenHash {
		t.Errorf("hash %s, want %s", got, goldenHash)
	}
	if got := Sign(priv, canon); got != goldenSignature {
		t.Errorf("signature %s, want %s (openssl)", got, goldenSignature)
	}
	if !CheckSignature(pub, canon, goldenSignature) {
		t.Error("the openssl signature does not verify")
	}
	if CheckSignature(pub, append([]byte(nil), canon[:len(canon)-1]...), goldenSignature) {
		t.Error("a signature verified over changed bytes")
	}
}

// chain builds a signed chain of n records: genesis, then promotions and a confirmation of the first promotion.
func chain(t *testing.T, priv ed25519.PrivateKey, kinds ...string) []Record {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	var recs []Record
	prev := GenesisPrevHash
	for i, kind := range kinds {
		id := fmt.Sprintf("prm_0192f5a4-0000-7000-8000-%012d", i+1)
		body := map[string]any{"schema": Schema, "kind": kind, "id": id,
			"target": map[string]any{"id": "dtg_t", "name": "era-production", "seq": i + 1, "prevHash": prev},
			"key":    map[string]any{"id": KeyID(pub), "alg": "Ed25519"}, "createdAt": "2026-11-03T09:12:44Z"}
		r := Record{ID: id, TargetID: "dtg_t", Seq: i + 1, Kind: kind, PrevHash: prev, KeyID: KeyID(pub), CreatedAt: time.Now()}
		if Pends(kind) {
			body["slot"], r.Slot = "asr-he-il", "asr-he-il"
		}
		if Closes(kind) {
			r.RefersTo = recs[1].ID // the first promotion
			body["closes"] = map[string]any{"id": r.RefersTo}
		}
		canon, err := CanonicalJSON(body)
		if err != nil {
			t.Fatal(err)
		}
		r.Canonical, r.Hash, r.Signature = canon, Hash(canon), Sign(priv, canon)
		prev = r.Hash
		recs = append(recs, r)
	}
	return recs
}

func TestVerifyChainHoldsAndClosesPromotions(t *testing.T) {
	priv := goldenKey(t)
	keys := map[string]ed25519.PublicKey{KeyID(priv.Public().(ed25519.PublicKey)): priv.Public().(ed25519.PublicKey)}
	recs := chain(t, priv, KindGenesis, KindPromotion, KindConfirmation, KindPromotion)
	intact, probs := VerifyChain(recs, keys)
	if !intact || len(probs) > 0 {
		t.Fatalf("intact chain refused: %v", probs)
	}
	for _, r := range recs {
		if !r.Verified {
			t.Errorf("record %d: %v", r.Seq, r.Problems)
		}
	}
	if recs[1].State != StateConfirmed || recs[1].ClosedBy != recs[2].ID || recs[1].Rev() != 2 {
		t.Errorf("first promotion: state %s closed by %s", recs[1].State, recs[1].ClosedBy)
	}
	if recs[3].State != StatePending || recs[3].Rev() != 1 {
		t.Errorf("second promotion: state %s", recs[3].State)
	}
}

func TestVerifyChainDetectsTampering(t *testing.T) {
	priv := goldenKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	keys := map[string]ed25519.PublicKey{KeyID(pub): pub}
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	otherPub := otherPriv.Public().(ed25519.PublicKey)
	cases := []struct {
		name   string
		tamper func([]Record) []Record
		bad    int    // the record that must fail
		want   string // part of its problem
	}{
		{"edited body, hash kept", func(r []Record) []Record {
			r[1].Canonical = []byte(strings.Replace(string(r[1].Canonical), "asr-he-il", "asr-he-xx", 1))
			return r
		}, 1, "does not hash"},
		{"edited body, rehashed", func(r []Record) []Record {
			r[1].Canonical = []byte(strings.Replace(string(r[1].Canonical), "asr-he-il", "asr-he-xx", 1))
			r[1].Hash = Hash(r[1].Canonical)
			return r
		}, 1, "signature does not verify"},
		{"edited, rehashed and re-signed by another key", func(r []Record) []Record {
			r[1].Canonical = []byte(strings.Replace(string(r[1].Canonical), "asr-he-il", "asr-he-xx", 1))
			r[1].Hash, r[1].Signature = Hash(r[1].Canonical), Sign(otherPriv, r[1].Canonical)
			return r
		}, 1, "signature does not verify"},
		{"edited and re-signed: the next link breaks", func(r []Record) []Record {
			r[1].Canonical = []byte(strings.Replace(string(r[1].Canonical), "asr-he-il", "asr-he-xx", 1))
			r[1].Hash, r[1].Signature = Hash(r[1].Canonical), Sign(priv, r[1].Canonical)
			return r
		}, 2, "prevHash"},
		{"record removed", func(r []Record) []Record { return append(r[:1], r[2:]...) }, 1, "seq 3 where 2"},
		{"records swapped", func(r []Record) []Record { r[1], r[2] = r[2], r[1]; return r }, 1, "seq 3 where 2"},
		{"not canonical", func(r []Record) []Record {
			r[1].Canonical = append([]byte(" "), r[1].Canonical...)
			r[1].Hash, r[1].Signature = Hash(r[1].Canonical), Sign(priv, r[1].Canonical)
			return r
		}, 1, "not in canonical form"},
		{"column disagrees with body", func(r []Record) []Record { r[1].Slot = "asr-sr"; return r }, 1, "slot"},
		{"unknown key", func(r []Record) []Record {
			delete(keys, KeyID(pub))
			keys[KeyID(otherPub)] = otherPub
			return r
		}, 0, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys = map[string]ed25519.PublicKey{KeyID(pub): pub}
			recs := c.tamper(chain(t, priv, KindGenesis, KindPromotion, KindPromotion, KindPromotion))
			intact, _ := VerifyChain(recs, keys)
			if intact {
				t.Fatal("tampered chain verified")
			}
			r := recs[c.bad]
			if r.Verified || !strings.Contains(strings.Join(r.Problems, "; "), c.want) {
				t.Errorf("record %d: verified %v, problems %v (want %q)", c.bad, r.Verified, r.Problems, c.want)
			}
		})
	}
}

func TestVerifyChainFollowsKeyRotation(t *testing.T) {
	oldPriv := goldenKey(t)
	oldPub := oldPriv.Public().(ed25519.PublicKey)
	newPub, newPriv, _ := ed25519.GenerateKey(nil)
	recs := chain(t, oldPriv, KindGenesis, KindKeyRotation)
	// The rotation names the new key; the record after it must be signed by the new key.
	var body map[string]any
	_ = json.Unmarshal(recs[1].Canonical, &body)
	body["newKey"] = map[string]any{"id": KeyID(newPub), "alg": "Ed25519"}
	canon, _ := CanonicalJSON(body)
	recs[1].Canonical, recs[1].Hash, recs[1].Signature = canon, Hash(canon), Sign(oldPriv, canon)
	next := func(priv ed25519.PrivateKey) Record {
		pub := priv.Public().(ed25519.PublicKey)
		b := map[string]any{"schema": Schema, "kind": KindPromotion, "id": "prm_3", "slot": "asr-he-il",
			"target": map[string]any{"id": "dtg_t", "name": "era-production", "seq": 3, "prevHash": recs[1].Hash},
			"key":    map[string]any{"id": KeyID(pub), "alg": "Ed25519"}}
		c, _ := CanonicalJSON(b)
		return Record{ID: "prm_3", TargetID: "dtg_t", Seq: 3, Kind: KindPromotion, Slot: "asr-he-il", PrevHash: recs[1].Hash,
			KeyID: KeyID(pub), Canonical: c, Hash: Hash(c), Signature: Sign(priv, c)}
	}
	keys := map[string]ed25519.PublicKey{KeyID(oldPub): oldPub, KeyID(newPub): newPub}
	good := append(append([]Record(nil), recs...), next(newPriv))
	if intact, probs := VerifyChain(good, keys); !intact {
		t.Fatalf("rotated chain refused: %v %v", probs, good[2].Problems)
	}
	stale := append(append([]Record(nil), recs...), next(oldPriv))
	if intact, _ := VerifyChain(stale, keys); intact || !strings.Contains(strings.Join(stale[2].Problems, ";"), "while the chain is under") {
		t.Errorf("a record signed by the retired key verified: %v", stale[2].Problems)
	}
}

func TestSmokeRequired(t *testing.T) {
	for _, c := range []struct {
		total int
		share float64
		want  int
	}{{20, 0.995, 20}, {200, 0.995, 199}, {10, 0.9, 9}, {0, 0.995, 0}, {1, 0.9, 1}} {
		if got := SmokeRequired(c.total, c.share); got != c.want {
			t.Errorf("SmokeRequired(%d, %v) = %d, want %d", c.total, c.share, got, c.want)
		}
	}
}
