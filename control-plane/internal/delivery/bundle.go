// Package delivery builds the delivery bundle of a promotion record (cadence.delivery/1, docs/spec/02-domain-
// projects-registry.md "Delivery bundle"): deliver.sh, the canonical record and its signature, the instance public
// key, the model directory under its versioned name, decoding configuration and the smoke set. A person copies the
// bundle to the production host and runs deliver.sh there; Cadence never reaches that host (non-negotiable 8). The
// script's server functions are data (templates/delivery/servers/<server kind>.sh), so no Go code names a server.
package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

// ArtifactType is the content-store type of a bundle; Format is the bundle format.
const (
	ArtifactType = "delivery"
	Format       = "cadence.delivery/1"
)

// MaxSmoke is the most smoke utterances a bundle carries (02 "Delivery bundle": ≤ 20 of the parity sample).
const MaxSmoke = 20

// Executables are the bundle paths written with mode 0755 (a content-store manifest keeps no modes).
var Executables = []string{"deliver.sh", "smoke/transcribe"}

// ManifestEntry is one file of a model directory: its path relative to the directory and its SHA-256 (hex).
type ManifestEntry struct {
	Path   string
	SHA256 string
}

// ManifestText is the text whose SHA-256 is a model directory's manifestSha256: one "<sha256>  <path>\n" line per
// file (sha256sum's format), sorted by path bytes. The delivery script computes it over the installed directory
// (find | LC_ALL=C sort | sha256sum), so the receipt's servedSha256 equals the record's manifestSha256 exactly when
// the installed files are the approved ones. The family's export (stream D1) writes deployable.json's
// manifestSha256 by this definition.
func ManifestText(files []ManifestEntry) string {
	sorted := append([]ManifestEntry(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var b strings.Builder
	for _, f := range sorted {
		b.WriteString(f.SHA256)
		b.WriteString("  ")
		b.WriteString(f.Path)
		b.WriteByte('\n')
	}
	return b.String()
}

// ManifestSHA256 is the hex SHA-256 of ManifestText.
func ManifestSHA256(files []ManifestEntry) string {
	sum := sha256.Sum256([]byte(ManifestText(files)))
	return hex.EncodeToString(sum[:])
}

var (
	// pathSegRe keeps model and smoke paths safe for the script's find | sort | sha256sum (no spaces, no leading
	// dash or dot, nothing sha256sum would escape).
	pathSegRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,199}$`)
	recordIDRe  = regexp.MustCompile(`^prm_[0-9a-f-]{36}$`)
	hex64Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyIDRe     = regexp.MustCompile(`^ed25519:[0-9a-f]{32}$`)
	targetRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	slotRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	stageRe     = regexp.MustCompile(`^[a-z][a-z-]{0,29}$`)
	shareRe     = regexp.MustCompile(`^[0-9][0-9.e+-]{0,23}$`)
	modelNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	repoPathRe  = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,498}$`)
	serverRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	decodingRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.json$`)
)

// SafePath reports whether p (slash-separated, relative) is a path the delivery script handles.
func SafePath(p string) bool {
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if !pathSegRe.MatchString(seg) || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// File is one bundle file: content from the content store (Hash) or inline (Data).
type File struct {
	Path string
	Hash string // b3:… of content already in the store
	Data []byte // small generated files
}

// SmokeItem is one smoke utterance: a 16 kHz WAV in the content store and the text the staging server wrote for it.
type SmokeItem struct {
	Name string // file name under smoke/, e.g. 01.wav
	Hash string // b3:… of the WAV
	Text string
}

// Smoke is the smoke set and the family's smoke client: an executable the script runs as
// `transcribe <server url> <model name> <wav>`, printing the model's text on stdout.
type Smoke struct {
	Items  []SmokeItem
	Client []byte
}

// DecodingFile is one boost list shipped as decoding configuration (decoding/<File>).
type DecodingFile struct {
	File   string
	Hash   string // b3:… of the JSON
	SHA256 string
}

// ModelFile is one file of the deployable's model directory.
type ModelFile struct {
	Path   string // relative to the model directory
	Hash   string // b3:…
	SHA256 string
	Size   int64
}

// Values are what deliver.sh bakes in; every one is checked shell-safe by Render.
type Values struct {
	RecordID, RecordHash, Kind, KeyID, Target, Slot, Stage, TrafficShare string
	ModelName, ManifestSHA256, RepositoryPath, ServerKind                string
	ShipModel                                                            bool
	SmokeTotal, SmokeRequired                                            int
	Decoding                                                             []DecodingFile
	ServerFunctions                                                      string
}

func (v Values) check() error {
	checks := []struct {
		name, val string
		re        *regexp.Regexp
	}{
		{"record id", v.RecordID, recordIDRe}, {"record hash", v.RecordHash, hex64Re}, {"key id", v.KeyID, keyIDRe},
		{"target name", v.Target, targetRe}, {"slot", v.Slot, slotRe}, {"stage", v.Stage, stageRe},
		{"traffic share", v.TrafficShare, shareRe}, {"model name", v.ModelName, modelNameRe},
		{"manifest sha256", v.ManifestSHA256, hex64Re}, {"repository path", v.RepositoryPath, repoPathRe},
		{"server kind", v.ServerKind, serverRe},
	}
	for _, c := range checks {
		if !c.re.MatchString(c.val) {
			return fmt.Errorf("delivery: %s %q is not safe to bake into deliver.sh", c.name, c.val)
		}
	}
	if v.Kind != "promotion" && v.Kind != "rollback" {
		return fmt.Errorf("delivery: a %s record has no delivery script", v.Kind)
	}
	if v.SmokeTotal < 0 || v.SmokeTotal > MaxSmoke || v.SmokeRequired < 0 || v.SmokeRequired > v.SmokeTotal {
		return fmt.Errorf("delivery: smoke %d of %d is out of range", v.SmokeRequired, v.SmokeTotal)
	}
	for _, d := range v.Decoding {
		if !decodingRe.MatchString(d.File) || !hex64Re.MatchString(d.SHA256) {
			return fmt.Errorf("delivery: decoding file %q (%s) is not safe to bake into deliver.sh", d.File, d.SHA256)
		}
	}
	return nil
}

// ServerKinds lists the server kinds the script has functions for (templates/delivery/servers/<kind>.sh).
func ServerKinds(tree fs.FS) []string {
	entries, err := fs.ReadDir(tree, "delivery/servers")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if k, ok := strings.CutSuffix(e.Name(), ".sh"); ok && serverRe.MatchString(k) {
			out = append(out, k)
		}
	}
	return out
}

// Render writes deliver.sh from templates/delivery/deliver.sh.tmpl with the server functions of v.ServerKind.
func Render(tree fs.FS, v Values) (string, error) {
	funcs, err := fs.ReadFile(tree, "delivery/servers/"+v.ServerKind+".sh")
	if err != nil {
		return "", fmt.Errorf("delivery: no server functions for server kind %q (templates/delivery/servers): %w", v.ServerKind, err)
	}
	v.ServerFunctions = strings.TrimRight(string(funcs), "\n")
	if err := v.check(); err != nil {
		return "", err
	}
	src, err := fs.ReadFile(tree, "delivery/deliver.sh.tmpl")
	if err != nil {
		return "", fmt.Errorf("delivery: read the script template: %w", err)
	}
	tmpl, err := template.New("deliver.sh").Option("missingkey=error").Parse(string(src))
	if err != nil {
		return "", fmt.Errorf("delivery: parse the script template: %w", err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, v); err != nil {
		return "", fmt.Errorf("delivery: render deliver.sh: %w", err)
	}
	return b.String(), nil
}

// Put stores the bundle's files in the content store and returns the directory artifact's hash and total size.
func Put(store *cas.Store, files []File) (string, int64, error) {
	m := cas.Manifest{Files: make([]cas.File, 0, len(files))}
	var total int64
	for _, f := range files {
		h, size := f.Hash, int64(len(f.Data))
		if h == "" {
			var err error
			if h, err = store.PutBytes(f.Data); err != nil {
				return "", 0, fmt.Errorf("delivery: store %s: %w", f.Path, err)
			}
		} else {
			ok, n, err := store.Has(h)
			if err != nil {
				return "", 0, err
			}
			if !ok {
				return "", 0, fmt.Errorf("delivery: %s (%s) is not in the content store", f.Path, h)
			}
			size = n
		}
		total += size
		m.Files = append(m.Files, cas.File{Path: f.Path, Hash: h, Size: size})
	}
	h, err := store.PutManifest(m)
	return h, total, err
}

func mode(p string) int64 {
	for _, e := range Executables {
		if p == e {
			return 0o755
		}
	}
	return 0o644
}

// WriteTar writes the bundle artifact as a gzipped tar under the directory prefix (deliver.sh and the smoke client
// executable).
func WriteTar(w io.Writer, store *cas.Store, hash, prefix string) error {
	m, err := store.ReadManifest(hash)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	dirs := map[string]bool{}
	files := append([]cas.File(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		for d := path.Dir(f.Path); ; d = path.Dir(d) {
			name := path.Join(prefix, d) + "/"
			if d == "." {
				name = prefix + "/"
			}
			if !dirs[name] {
				dirs[name] = true
				if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o755}); err != nil {
					return err
				}
			}
			if d == "." {
				break
			}
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: path.Join(prefix, f.Path), Mode: mode(f.Path), Size: f.Size}); err != nil {
			return err
		}
		r, err := store.Open(f.Hash)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, r)
		_ = r.Close()
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// WriteDir writes the bundle artifact into dir (tests and the shell test of deliver.sh).
func WriteDir(store *cas.Store, hash, dir string) error {
	m, err := store.ReadManifest(hash)
	if err != nil {
		return err
	}
	for _, f := range m.Files {
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil { //nolint:gosec // a manifest path ReadManifest checked relative
			return err
		}
		r, err := store.Open(f.Hash)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode(f.Path))) //nolint:gosec // dst is dir joined with a manifest path ReadManifest checked relative
		if err != nil {
			_ = r.Close()
			return err
		}
		_, err = io.Copy(out, r)
		_ = r.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// errNoSource is the answer of a source a later stream fills.
var errNoSource = errors.New("not available yet")
