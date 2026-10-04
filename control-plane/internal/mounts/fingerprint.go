package mounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Mount content in step hashes (owner decision of 2026-10-04 on the phase-4 gate, docs/spec/02-domain-projects-
// registry.md "Storage and mounts"): a step whose parameters name mount:// URIs (sdp_ingest's path, dataset_import's
// path) reads files the input hash does not cover. The pipeline engine folds a fingerprint of what the step would
// read into its input hash, so a finished step is reused only while those files are unchanged. The fingerprint is a
// listing, never the bytes: for every file under each URI, its path, size and stamp (modification time, ETag or Hub
// blob id; StampedReader), less the files the step's exclude globs leave out.

// ErrTooManyFiles is a listing of more files than the cap: a URI lists more files than the cap (storage.mount_scan_max_files) — the step is not fingerprinted
// and so never reused.
var ErrTooManyFiles = errors.New("too many files to fingerprint")

// Fingerprinter computes step mount fingerprints (pipelines.MountFingerprinter).
type Fingerprinter struct {
	Secrets  Secrets
	HTTP     *http.Client
	MaxFiles func() int // files listed per step at most; 0 or nil: no cap
}

// Fingerprinter returns the service's fingerprinter: its secrets and HTTP client, capped at
// storage.mount_scan_max_files files a step.
func (s *Service) Fingerprinter() Fingerprinter {
	return Fingerprinter{Secrets: s.Secrets, HTTP: s.HTTP,
		MaxFiles: func() int { return s.defaults().Storage.MountScanMaxFiles.Value }}
}

// Fingerprint answers the fingerprint of what a step with params reads from mounts: "" when params name no mount
// URI. The pattern a step may take is not applied: a step reads sidecar files beside the files it matches
// (sdp_ingest's <stem>.txt and <stem>.cadence.json), so every file under the URI counts unless an exclude glob
// (params.exclude, fnmatch: * crosses /) leaves it out — a superset never reuses a step whose input changed.
func (f Fingerprinter) Fingerprint(ctx context.Context, q storage.Querier, params map[string]any) (string, error) {
	uris := mountURIs(params, nil)
	if len(uris) == 0 {
		return "", nil
	}
	exclude, err := excludeGlobs(params["exclude"])
	if err != nil {
		return "", err
	}
	limit := 0
	if f.MaxFiles != nil {
		limit = f.MaxFiles()
	}
	h := sha256.New()
	files := 0
	for _, u := range uris {
		name, rel := splitURI(u)
		m, err := mountByName(ctx, q, name)
		if err != nil {
			return "", err
		}
		r, err := Open(ctx, m, f.Secrets, f.HTTP)
		if err != nil {
			return "", err
		}
		sr, ok := r.(StampedReader)
		if !ok {
			return "", fmt.Errorf("mount %s: its reader answers no file stamps", m.Name)
		}
		var lines []string
		err = sr.WalkStamped(ctx, rel, func(path string, size int64, stamp string) error {
			under := strings.TrimPrefix(strings.TrimPrefix(path, rel), "/")
			if slices.ContainsFunc(exclude, func(g *regexp.Regexp) bool { return g.MatchString(under) }) {
				return nil
			}
			if files++; limit > 0 && files > limit {
				return ErrTooManyFiles
			}
			lines = append(lines, path+"\t"+strconv.FormatInt(size, 10)+"\t"+stamp)
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("list %s: %w", u, err)
		}
		sort.Strings(lines)
		_, _ = fmt.Fprintf(h, "mount\t%s\t%s\t%s\t%s\t%s\n", m.Name, m.Kind, m.Root, m.Revision, rel) // a hash never fails
		for _, l := range lines {
			_, _ = fmt.Fprintln(h, l)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// mountURIs collects the distinct mount:// URIs among the string values of v (nested maps and lists), sorted.
func mountURIs(v any, out []string) []string {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, Scheme) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			out = mountURIs(x[k], out)
		}
	case []any:
		for _, e := range x {
			out = mountURIs(e, out)
		}
	case []string:
		for _, e := range x {
			out = mountURIs(e, out)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// splitURI is mount://<name>/<path>[#…] → (name, path without surrounding slashes).
func splitURI(u string) (string, string) {
	rest := strings.TrimPrefix(u, Scheme)
	rest, _, _ = strings.Cut(rest, "#")
	name, p, _ := strings.Cut(rest, "/")
	return name, strings.Trim(p, "/")
}

func mountByName(ctx context.Context, q storage.Querier, name string) (Mount, error) {
	var m Mount
	err := q.QueryRow(ctx, `SELECT id, name, kind, root, endpoint, region, revision, credentials FROM mounts WHERE name = $1`,
		name).Scan(&m.ID, &m.Name, &m.Kind, &m.Root, &m.Endpoint, &m.Region, &m.Revision, &m.Credentials)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mount{}, fmt.Errorf("no mount %q", name)
	}
	if err != nil {
		return Mount{}, fmt.Errorf("read mount %s: %w", name, err)
	}
	return m, nil
}

// excludeGlobs compiles a step's exclude parameter (a list of fnmatch globs) into patterns.
func excludeGlobs(v any) ([]*regexp.Regexp, error) {
	var globs []string
	switch x := v.(type) {
	case nil:
	case string:
		globs = []string{x}
	case []string:
		globs = x
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				globs = append(globs, s)
			}
		}
	}
	out := make([]*regexp.Regexp, 0, len(globs))
	for _, g := range globs {
		re, err := regexp.Compile(fnmatchRegexp(g))
		if err != nil {
			return nil, fmt.Errorf("exclude glob %q: %w", g, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// fnmatchRegexp translates a Python fnmatch glob (what the worker's steps match with): * is any run of characters,
// / included; ? one character; [seq] and [!seq] a class.
func fnmatchRegexp(g string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := strings.IndexByte(g[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+j]
			i += j + 1
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}
