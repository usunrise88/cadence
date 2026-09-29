package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// MaxBodyBytes caps a command's request body.
const MaxBodyBytes = 4 << 20

// HashRequest fingerprints a command request for idempotency: method, path, query (sorted, dryRun excluded
// because dry runs bypass idempotency), If-Match and the body. A JSON body is canonicalised (keys sorted,
// insignificant whitespace dropped) so that re-serialising the same value gives the same hash.
func HashRequest(method, path string, query url.Values, ifMatch string, body []byte) string {
	q := url.Values{}
	for k, v := range query {
		if k != "dryRun" {
			q[k] = v
		}
	}
	h := sha256.New()
	for _, part := range []string{method, path, q.Encode(), ifMatch} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write(canonicalJSON(body))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalJSON(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

type hashKey struct{}

// RequestHash returns the fingerprint HashMiddleware stored for this request.
func RequestHash(ctx context.Context) string {
	h, _ := ctx.Value(hashKey{}).(string)
	return h
}

// HashMiddleware buffers the body of mutating requests (POST, PUT, PATCH), caps it at MaxBodyBytes and stores
// the request fingerprint (and the Cadence-Tool-Call-Id, if any) in the context.
func HashMiddleware(onErr func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
			default:
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
			if err != nil {
				onErr(w, r, problems.BadRequest.New("cannot read the request body (limit %d bytes): %v", MaxBodyBytes, err))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			hash := HashRequest(r.Method, r.URL.EscapedPath(), r.URL.Query(), r.Header.Get("If-Match"), body)
			ctx := context.WithValue(r.Context(), hashKey{}, hash)
			if id := r.Header.Get(HeaderToolCallID); id != "" && len(id) <= maxToolCallID {
				ctx = WithToolCallID(ctx, id)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ETag renders a revision as a strong entity tag: "3".
func ETag(rev int) string { return `"` + strconv.Itoa(rev) + `"` }

// ParseIfMatch reads the revision in an If-Match value; it accepts "3", 3 and W/"3".
func ParseIfMatch(v string) (int, error) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "W/")
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	rev, err := strconv.Atoi(s)
	if err != nil || rev < 1 {
		return 0, problems.BadRequest.New("If-Match %q is not a revision; send the ETag of your last read, e.g. \"3\"", v)
	}
	return rev, nil
}

// CheckRev fails with precondition-failed (carrying currentRev) when the client's revision is not current.
func CheckRev(kind string, want, current int) error {
	if want != current {
		return problems.Stale(current, "the %s is at revision %d, not %d; re-read it, reapply your change and retry",
			kind, current, want)
	}
	return nil
}

// Precondition returns the error for a write that needs If-Match but has none.
func Precondition(kind string) error {
	return problems.PreconditionRequired.New("the %s exists; send If-Match with its current revision", kind)
}
