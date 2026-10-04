// Package promotions keeps the signed promotion records of delivery targets (docs/spec/02-domain-projects-registry.md
// "Promotion records", R33): JSON in RFC 8785 canonical form, hashed with SHA-256, signed with the instance's Ed25519
// key, append-only and chained per delivery target from a genesis record. It also checks the receipts delivery
// scripts print (promotions.verify) and withdraws promotions that never got one.
package promotions

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Schema is the record format.
const Schema = "cadence.promotion/1"

// Record kinds.
const (
	KindGenesis       = "genesis"
	KindPromotion     = "promotion"
	KindRollback      = "rollback"
	KindConfirmation  = "confirmation"
	KindWithdrawal    = "withdrawal"
	KindTargetChanged = "target-changed"
	KindKeyRotation   = "key-rotation"
)

// States of a promotion or rollback record.
const (
	StatePending   = "pending"
	StateConfirmed = "confirmed"
	StateWithdrawn = "withdrawn"
)

// GenesisPrevHash is the prevHash of a chain's first record.
var GenesisPrevHash = strings.Repeat("0", 64)

// Record is one stored promotion record; the fields after CreatedAt are computed on read.
type Record struct {
	ID           string
	TargetID     string
	Seq          int
	Kind         string
	Canonical    []byte
	Hash         string
	PrevHash     string
	Signature    string
	KeyID        string
	Slot         string
	ProjectID    string
	DeploymentID string
	RefersTo     string
	CreatedAt    time.Time

	Body     map[string]any
	Verified bool
	Problems []string
	State    string // promotions and rollbacks: pending, confirmed or withdrawn
	ClosedBy string // the confirmation or withdrawal that closed it
}

// Closes reports whether the kind closes a pending promotion or rollback.
func Closes(kind string) bool { return kind == KindConfirmation || kind == KindWithdrawal }

// Pends reports whether the kind waits for a receipt.
func Pends(kind string) bool { return kind == KindPromotion || kind == KindRollback }

// Hash is the hex SHA-256 of a canonical record.
func Hash(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Sign returns the base64 Ed25519 signature over the 32 bytes of the canonical record's SHA-256: what the delivery
// script checks with openssl pkeyutl -verify -rawin over `openssl dgst -sha256 -binary record.json`.
func Sign(priv ed25519.PrivateKey, canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:]))
}

// CheckSignature verifies sig (base64) over the canonical record's SHA-256 with pub.
func CheckSignature(pub ed25519.PublicKey, canonical []byte, sig string) bool {
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(raw) != ed25519.SignatureSize || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sum := sha256.Sum256(canonical)
	return ed25519.Verify(pub, sum[:], raw)
}

// str reads a string at a dotted path of a parsed body ("" when absent or not a string).
func str(body map[string]any, path string) string {
	var cur any = body
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}

// num reads an integer at a dotted path (-1 when absent).
func num(body map[string]any, path string) int {
	var cur any = body
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return -1
		}
		cur = m[k]
	}
	switch n := cur.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return -1
		}
		return int(i)
	}
	return -1
}

// check verifies one record on its own: its canonical text is canonical and hashes to Hash, the signature holds
// under pub, and the body agrees with the columns. It returns the problems found (none: verified) and the parsed
// body.
func check(r Record, pub ed25519.PublicKey) (map[string]any, []string) {
	var probs []string
	canon, err := Canonicalize(r.Canonical)
	switch {
	case err != nil:
		probs = append(probs, "the stored record is not JSON: "+err.Error())
	case !bytes.Equal(canon, r.Canonical):
		probs = append(probs, "the stored record is not in canonical form")
	}
	if Hash(r.Canonical) != r.Hash {
		probs = append(probs, "the record does not hash to its recorded hash")
	}
	if pub == nil {
		probs = append(probs, fmt.Sprintf("signing key %s is unknown", r.KeyID))
	} else {
		if KeyID(pub) != r.KeyID {
			probs = append(probs, fmt.Sprintf("the key that verifies it is %s, not %s", KeyID(pub), r.KeyID))
		}
		if !CheckSignature(pub, r.Canonical, r.Signature) {
			probs = append(probs, "the signature does not verify")
		}
	}
	var body map[string]any
	if err := json.Unmarshal(r.Canonical, &body); err != nil {
		return nil, append(probs, "the record is not a JSON object")
	}
	for _, c := range []struct{ path, want string }{
		{"schema", Schema}, {"kind", r.Kind}, {"id", r.ID}, {"target.id", r.TargetID},
		{"target.prevHash", r.PrevHash}, {"key.id", r.KeyID}, {"key.alg", "Ed25519"},
	} {
		if got := str(body, c.path); got != c.want {
			probs = append(probs, fmt.Sprintf("the body's %s is %q, not %q", c.path, got, c.want))
		}
	}
	if got := num(body, "target.seq"); got != r.Seq {
		probs = append(probs, fmt.Sprintf("the body's target.seq is %d, not %d", got, r.Seq))
	}
	if r.Slot != "" && str(body, "slot") != r.Slot {
		probs = append(probs, fmt.Sprintf("the body's slot is %q, not %q", str(body, "slot"), r.Slot))
	}
	if Closes(r.Kind) && str(body, "closes.id") != r.RefersTo {
		probs = append(probs, fmt.Sprintf("the body closes %q, not %q", str(body, "closes.id"), r.RefersTo))
	}
	return body, probs
}

// VerifyChain checks a delivery target's chain, oldest first, in place: each record on its own (check), seq from 1
// without a gap, prevHash linking each record to the one before, a genesis first and nowhere else, and every record
// signed by the key the chain is under (the genesis key, then each key-rotation's new key). It sets Verified,
// Problems and Body on every record and the State and ClosedBy of promotions and rollbacks, and reports whether
// the whole chain holds with the problems that are not about one record.
func VerifyChain(recs []Record, keys map[string]ed25519.PublicKey) (bool, []string) {
	var chainProbs []string
	chainKey := ""
	byID := map[string]int{}
	for i := range recs {
		r := &recs[i]
		body, probs := check(*r, keys[r.KeyID])
		r.Body = body
		wantPrev := GenesisPrevHash
		if i > 0 {
			wantPrev = recs[i-1].Hash
		}
		if r.Seq != i+1 {
			probs = append(probs, fmt.Sprintf("seq %d where %d was expected (a record is missing or out of place)", r.Seq, i+1))
		}
		if r.PrevHash != wantPrev {
			probs = append(probs, "prevHash is not the previous record's hash (a record before it was removed or changed)")
		}
		switch {
		case i == 0 && r.Kind != KindGenesis:
			probs = append(probs, "the chain does not start with a genesis record")
		case i > 0 && r.Kind == KindGenesis:
			probs = append(probs, "a second genesis record")
		}
		if i == 0 {
			chainKey = r.KeyID
		} else if r.KeyID != chainKey {
			probs = append(probs, fmt.Sprintf("signed by %s while the chain is under %s", r.KeyID, chainKey))
		}
		if r.Kind == KindKeyRotation {
			if next := str(body, "newKey.id"); next != "" {
				chainKey = next
			} else {
				probs = append(probs, "a key rotation that names no new key")
			}
		}
		if Pends(r.Kind) {
			r.State = StatePending
		}
		if Closes(r.Kind) {
			j, ok := byID[r.RefersTo]
			switch {
			case !ok:
				probs = append(probs, fmt.Sprintf("closes %s, which is not an earlier record of this chain", r.RefersTo))
			case !Pends(recs[j].Kind):
				probs = append(probs, fmt.Sprintf("closes %s, a %s record", r.RefersTo, recs[j].Kind))
			case recs[j].ClosedBy != "":
				probs = append(probs, fmt.Sprintf("closes %s, already closed by %s", r.RefersTo, recs[j].ClosedBy))
			default:
				recs[j].ClosedBy = r.ID
				recs[j].State = StateConfirmed
				if r.Kind == KindWithdrawal {
					recs[j].State = StateWithdrawn
				}
			}
		}
		byID[r.ID] = i
		r.Problems, r.Verified = probs, len(probs) == 0
	}
	intact := true
	for _, r := range recs {
		if !r.Verified {
			intact = false
			chainProbs = append(chainProbs, fmt.Sprintf("record %d (%s, %s) does not verify", r.Seq, r.Kind, r.ID))
		}
	}
	return intact, chainProbs
}
