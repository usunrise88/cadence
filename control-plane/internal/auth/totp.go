package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP uses HMAC-SHA1, the only algorithm every authenticator app supports
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults, the ones authenticator apps assume): HMAC-SHA1, 6 digits, 30-second steps.
const (
	TOTPDigits = 6
	TOTPPeriod = 30 * time.Second
	// TOTPSkew is how many steps before and after now a code is accepted, for clock drift.
	TOTPSkew = 1
	// TOTPIssuer names Cadence in authenticator apps.
	TOTPIssuer = "Cadence"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh 160-bit secret (RFC 4226 §4 recommends 160 bits), base32 without padding.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read TOTP secret: %w", err)
	}
	return b32.EncodeToString(b), nil
}

// TOTPURI is the otpauth URI an authenticator app reads from a QR code.
func TOTPURI(secret, account string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", TOTPIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(int(TOTPPeriod/time.Second)))
	label := url.PathEscape(TOTPIssuer + ":" + account)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// TOTPStep is the RFC 6238 time step of t.
func TOTPStep(t time.Time) int64 { return t.Unix() / int64(TOTPPeriod/time.Second) }

// TOTPCode is the code of secret (base32) at time step step (RFC 4226 §5.3 dynamic truncation).
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("decode TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // steps are positive
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, bin%1_000_000), nil
}

// VerifyTOTP checks code against secret at now, within TOTPSkew steps. A code is accepted only for a step after
// lastStep, so a code that was used once cannot be replayed (RFC 6238 §5.2). It returns the matched step, which
// the caller stores as the new lastStep.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (step int64, ok bool, err error) {
	if len(code) != TOTPDigits {
		return 0, false, nil
	}
	cur := TOTPStep(now)
	for s := cur - TOTPSkew; s <= cur+TOTPSkew; s++ {
		want, err := TOTPCode(secret, s)
		if err != nil {
			return 0, false, err
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && s > lastStep {
			return s, true, nil
		}
	}
	return 0, false, nil
}
