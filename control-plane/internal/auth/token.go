package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// Token prefixes. A fixed prefix lets the agent host refuse to commit a token (R2) and lets secret scanners find
// them; it also tells the server which kind of credential to look up.
const (
	PrefixAPIKey  = "cdk_" // personal API key, Bearer
	PrefixAgent   = "cst_" // agent session token, Bearer (the MCP credential)
	PrefixWorker  = "cwk_" // worker lease token (phase 2)
	PrefixHost    = "cah_" // agent host token, Bearer: claims sessions and reports transcripts, nothing else
	PrefixSession = "cws_" // browser session, in the cadence_session cookie
)

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns prefix followed by 256 random bits, base32 in lower case (cookie- and header-safe).
func NewToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return prefix + strings.ToLower(tokenEncoding.EncodeToString(b)), nil
}

// HashToken is the stored form of a token: hex SHA-256. Tokens carry 256 random bits, so a fast hash is enough;
// no salt or stretching is needed (unlike passwords).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
