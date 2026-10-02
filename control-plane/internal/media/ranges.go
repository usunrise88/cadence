package media

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// ByteRange is a satisfiable range [Start, Start+Length).
type ByteRange struct{ Start, Length int64 }

// ParseRange reads a Range header for a body of size bytes (RFC 9110 §14). It returns nil for no range, a header
// it ignores (another unit, malformed, or several ranges: the whole body is then served with 200, which the RFC
// allows), and a range-not-satisfiable problem when the one range lies outside the body.
func ParseRange(header string, size int64) (*ByteRange, error) {
	h := strings.TrimSpace(header)
	unit, spec, ok := strings.Cut(h, "=")
	if h == "" || !ok || strings.TrimSpace(unit) != "bytes" || strings.Contains(spec, ",") {
		return nil, nil
	}
	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return nil, nil
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	unsatisfiable := problems.RangeNotSatisfiable.New("the range %q lies outside the %d bytes of the audio", spec, size)
	if first == "" { // suffix: the last n bytes
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return nil, nil
		}
		if n == 0 || size == 0 {
			return nil, unsatisfiable
		}
		n = min(n, size)
		return &ByteRange{Start: size - n, Length: n}, nil
	}
	a, err := strconv.ParseInt(first, 10, 64)
	if err != nil || a < 0 {
		return nil, nil
	}
	if a >= size {
		return nil, unsatisfiable
	}
	end := size - 1
	if last != "" {
		b, err := strconv.ParseInt(last, 10, 64)
		if err != nil || b < a {
			return nil, nil
		}
		end = min(b, size-1)
	}
	return &ByteRange{Start: a, Length: end - a + 1}, nil
}

// Serve writes body with Accept-Ranges and, for a Range request, 206 and Content-Range. The caller writes the
// problem of a returned error (nothing has been written then).
func Serve(w http.ResponseWriter, r *http.Request, body io.ReadSeeker, size int64, contentType string) (status int, err error) {
	rg, err := ParseRange(r.Header.Get("Range"), size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		return http.StatusRequestedRangeNotSatisfiable, err
	}
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	start, length := int64(0), size
	status = http.StatusOK
	if rg != nil {
		start, length, status = rg.Start, rg.Length, http.StatusPartialContent
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rg.Start, rg.Start+rg.Length-1, size))
	}
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("seek audio: %w", err)
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return status, nil
	}
	if _, err := io.CopyN(w, body, length); err != nil && !errors.Is(err, io.EOF) {
		// The headers are out: the client sees a short body. Nothing else can be said to it.
		return status, nil
	}
	return status, nil
}

// FirstPlay reports whether a request starts a play (no Range, or one from byte 0): the audit log records those,
// not every range a media element fetches while it plays and seeks.
func FirstPlay(r *http.Request) bool {
	h := strings.TrimSpace(r.Header.Get("Range"))
	if h == "" {
		return true
	}
	_, spec, _ := strings.Cut(h, "=")
	first, _, _ := strings.Cut(strings.TrimSpace(spec), "-")
	return strings.TrimSpace(first) == "0"
}
