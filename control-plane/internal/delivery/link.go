package delivery

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Signer signs and checks bundle download links (delivery.get) with an HMAC-SHA256 key, as audio links are signed
// (R25): bound to the record, the viewer and an expiry.
type Signer struct {
	key []byte
	// Now is the clock (tests).
	Now func() time.Time
}

// NewSigner returns a signer with key; an empty key makes a random one (links then survive no restart).
func NewSigner(key []byte) *Signer {
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	return &Signer{key: key, Now: time.Now}
}

// Sign returns the signature of a link to record's bundle for viewer until exp (Unix seconds; base64url).
func (s *Signer) Sign(record, viewer string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	_, _ = fmt.Fprintf(m, "cadence-delivery-link/1\n%s\n%s\n%d", record, viewer, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks a link's signature and expiry.
func (s *Signer) Verify(record, viewer string, exp int64, sig string) error {
	if !hmac.Equal([]byte(sig), []byte(s.Sign(record, viewer, exp))) {
		return problems.DeliveryLinkInvalid.New("the link's signature does not match this bundle and viewer; open the promotion again for a new link")
	}
	if s.Now().Unix() > exp {
		return problems.DeliveryLinkInvalid.New("the link expired at %s; open the promotion again for a new link", time.Unix(exp, 0).UTC().Format(time.RFC3339))
	}
	return nil
}
