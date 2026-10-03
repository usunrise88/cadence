package mounts

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
)

// Scheme is the URI scheme of audio on a mount.
const Scheme = "mount://"

// URI names a file on a mount, or a segment of it: mount://<mount>/<path>[#t=<start>,<end>][&ch=<n>]
// (docs/review/2026-10-03-phase-4-plan.md "Interfaces between streams", M → D). The path is relative to the mount's
// root, UTF-8, never percent-encoded, without "." or ".." segments and without '#'. Times are seconds; the channel
// is 0-based. The worker resolves a URI with cadence_worker.mounts; the control plane only parses and records it.
type URI struct {
	Mount   string
	Path    string
	Start   *float64
	End     *float64
	Channel *int
}

// ParseURI parses s strictly; String gives the canonical form back.
func ParseURI(s string) (URI, error) {
	if !strings.HasPrefix(s, Scheme) {
		return URI{}, fmt.Errorf("%q is not a mount URI (mount://<mount>/<path>)", s)
	}
	rest := s[len(Scheme):]
	frag := ""
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest, frag = rest[:i], rest[i+1:]
	}
	name, p, ok := strings.Cut(rest, "/")
	if !ok || !nameRe.MatchString(name) {
		return URI{}, fmt.Errorf("%q: the mount name must match %s and be followed by /<path>", s, nameRe)
	}
	if err := checkPath(p); err != nil {
		return URI{}, fmt.Errorf("%q: %w", s, err)
	}
	u := URI{Mount: name, Path: p}
	if frag == "" {
		if strings.Contains(s, "#") {
			return URI{}, fmt.Errorf("%q: empty fragment", s)
		}
		return u, nil
	}
	for _, part := range strings.Split(frag, "&") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			if u.Start != nil {
				return URI{}, fmt.Errorf("%q: t given twice", s)
			}
			a, b, ok := strings.Cut(v, ",")
			start, err1 := strconv.ParseFloat(a, 64)
			end, err2 := strconv.ParseFloat(b, 64)
			if !ok || err1 != nil || err2 != nil || !finite(start) || !finite(end) || start < 0 || end <= start {
				return URI{}, fmt.Errorf("%q: t must be <start>,<end> in seconds with 0 <= start < end", s)
			}
			u.Start, u.End = &start, &end
		case "ch":
			if u.Channel != nil {
				return URI{}, fmt.Errorf("%q: ch given twice", s)
			}
			ch, err := strconv.Atoi(v)
			if err != nil || ch < 0 || ch > 63 {
				return URI{}, fmt.Errorf("%q: ch must be a channel index 0–63", s)
			}
			u.Channel = &ch
		default:
			return URI{}, fmt.Errorf("%q: unknown fragment key %q (t, ch)", s, k)
		}
	}
	return u, nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// checkPath refuses an empty path, an absolute one, "." or ".." segments, NUL and '#'.
func checkPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("the path is empty")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("the path is relative to the mount's root (no leading /)")
	case strings.ContainsAny(p, "\x00#\\"):
		return fmt.Errorf("the path may not contain NUL, # or \\")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || seg == "" {
			return fmt.Errorf("the path may not contain empty, . or .. segments")
		}
	}
	if path.Clean(p) != p {
		return fmt.Errorf("the path is not clean")
	}
	return nil
}

// String is the canonical form: mount://<mount>/<path>[#t=<start>,<end>][&ch=<n>] (#ch=<n> without times).
func (u URI) String() string {
	var b strings.Builder
	b.WriteString(Scheme + u.Mount + "/" + u.Path)
	sep := "#"
	if u.Start != nil && u.End != nil {
		b.WriteString(sep + "t=" + strconv.FormatFloat(*u.Start, 'f', -1, 64) + "," + strconv.FormatFloat(*u.End, 'f', -1, 64))
		sep = "&"
	}
	if u.Channel != nil {
		b.WriteString(sep + "ch=" + strconv.Itoa(*u.Channel))
	}
	return b.String()
}
