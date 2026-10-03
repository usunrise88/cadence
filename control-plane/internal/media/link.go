package media

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Span is the part of an utterance a request or link names; nil fields take the defaults (whole audio, every
// channel).
type Span struct {
	Channel *int
	Start   *float64
	End     *float64
}

// Link is what a signed audio link binds: the utterance, the span, the viewer and the expiry.
type Link struct {
	Utterance string
	Span      Span
	Viewer    string
	Expires   int64 // Unix seconds
}

// Signer signs and checks audio links with an HMAC-SHA256 key (derived from the master key in production, so links
// die with it; random per process in tests).
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

func num(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func message(l Link) string {
	ch := ""
	if l.Span.Channel != nil {
		ch = strconv.Itoa(*l.Span.Channel)
	}
	return fmt.Sprintf("cadence-audio-link/1\n%s\n%s\n%s\n%s\n%s\n%d", l.Utterance, ch, num(l.Span.Start), num(l.Span.End), l.Viewer, l.Expires)
}

// Sign returns the link's signature (base64url, no padding).
func (s *Signer) Sign(l Link) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(message(l)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Query returns the query string of audio.get for the link: the span, viewer, expiry and signature.
func (s *Signer) Query(l Link) url.Values {
	q := url.Values{}
	if l.Span.Channel != nil {
		q.Set("channel", strconv.Itoa(*l.Span.Channel))
	}
	if l.Span.Start != nil {
		q.Set("start", num(l.Span.Start))
	}
	if l.Span.End != nil {
		q.Set("end", num(l.Span.End))
	}
	q.Set("viewer", l.Viewer)
	q.Set("exp", strconv.FormatInt(l.Expires, 10))
	q.Set("sig", s.Sign(l))
	return q
}

// Verify checks sig against the link: the signature must match the utterance, span and viewer exactly, and the
// link must not have expired.
func (s *Signer) Verify(l Link, sig string) error {
	if !hmac.Equal([]byte(sig), []byte(s.Sign(l))) {
		return problems.MediaLinkInvalid.New("the link's signature does not match this utterance, span and viewer; ask for a new link")
	}
	if s.Now().Unix() > l.Expires {
		return problems.MediaLinkInvalid.New("the link expired at %s; ask for a new link", time.Unix(l.Expires, 0).UTC().Format(time.RFC3339))
	}
	return nil
}
