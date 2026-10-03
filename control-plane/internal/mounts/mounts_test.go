package mounts

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// failQuerier stands in for a database a test must not reach.
type failQuerier struct{}

var errNoDB = errors.New("no database in this test")

func (failQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errNoDB
}
func (failQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, errNoDB }
func (failQuerier) QueryRow(context.Context, string, ...any) pgx.Row        { return failRow{} }

type failRow struct{}

func (failRow) Scan(...any) error { return errNoDB }

func TestParseURI(t *testing.T) {
	ch := func(n int) *int { return &n }
	f := func(x float64) *float64 { return &x }
	tests := []struct {
		in   string
		want URI
	}{
		{"mount://corpora/fleurs-sr/2024/a.wav", URI{Mount: "corpora", Path: "fleurs-sr/2024/a.wav"}},
		{"mount://corpora/calls/x.wav#t=1.5,3.25&ch=1", URI{Mount: "corpora", Path: "calls/x.wav", Start: f(1.5), End: f(3.25), Channel: ch(1)}},
		{"mount://c/x.wav#ch=0", URI{Mount: "c", Path: "x.wav", Channel: ch(0)}},
		{"mount://c/x.wav#t=0,2", URI{Mount: "c", Path: "x.wav", Start: f(0), End: f(2)}},
		{"mount://c/дир/файл 1.wav", URI{Mount: "c", Path: "дир/файл 1.wav"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseURI(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.in {
				t.Errorf("String() = %q, want the canonical %q", got.String(), tt.in)
			}
			if got.Mount != tt.want.Mount || got.Path != tt.want.Path || fmt.Sprint(deref(got.Start), deref(got.End)) !=
				fmt.Sprint(deref(tt.want.Start), deref(tt.want.End)) || fmt.Sprint(got.Channel == nil) != fmt.Sprint(tt.want.Channel == nil) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, bad := range []string{"file:///x.wav", "mount://Corpora/x.wav", "mount://corpora", "mount://corpora/",
		"mount://corpora//x.wav", "mount://corpora/../x.wav", "mount://corpora/a/./x.wav", "mount://corpora/x.wav#",
		"mount://corpora/x.wav#t=2,1", "mount://corpora/x.wav#t=-1,1", "mount://corpora/x.wav#t=1",
		"mount://corpora/x.wav#t=NaN,1", "mount://corpora/x.wav#ch=64", "mount://corpora/x.wav#ch=-1",
		"mount://corpora/x.wav#t=0,1&t=0,2", "mount://corpora/x.wav#q=1"} {
		if _, err := ParseURI(bad); err == nil {
			t.Errorf("ParseURI(%q) accepted", bad)
		}
	}
}

func deref(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func TestNamedAndCredentialsEnv(t *testing.T) {
	spec := steps.Spec{Params: json.RawMessage(`{"path":"mount://corpora/src/rev","x":"mount://exports/out/"}`),
		Inputs: map[string]steps.ArtifactRef{"segments": {Meta: json.RawMessage(`{"root":"mount://calls-nas/2026/"}`)}}}
	if got := Named(spec); !slices.Equal(got, []string{"calls-nas", "corpora", "exports"}) {
		t.Errorf("Named = %v", got)
	}
	if got := CredentialsEnv("calls-nas"); got != "CADENCE_MOUNT_CALLS_NAS_CREDENTIALS" {
		t.Errorf("CredentialsEnv = %q", got)
	}
	if !readsMounts(steps.JobData) || readsMounts(steps.JobTraining) {
		t.Error("data steps read mounts, training steps do not")
	}
}

func TestScanCountsEntriesAndFindsBlobCopies(t *testing.T) {
	root := t.TempDir()
	h := strings.Repeat("ab", 32)
	files := map[string]int{
		"fleurs-sr/2024/a.wav":     10,
		"fleurs-sr/2024/sub/b.wav": 20,
		"calls/x.wav":              5,
		"README":                   1,
		"cas/b3/ab/" + h:           7,
		"cas/b3/cd/" + h:           7, // the prefix does not match the hash: not a blob
	}
	for p, n := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inv, blobs, err := Scan(t.Context(), pathReader{root: root}, "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Files != 6 || inv.Bytes != 50 || inv.Blobs != 1 || inv.BlobBytes != 7 {
		t.Errorf("inventory = %+v", inv)
	}
	if c, ok := blobs["b3:"+h]; !ok || c.Path != "cas/b3/ab/"+h {
		t.Errorf("blobs = %+v", blobs)
	}
	if inv.Entries[0].Path != "fleurs-sr/2024" || inv.Entries[0].Bytes != 30 {
		t.Errorf("largest entry = %+v", inv.Entries[0])
	}
	inv, _, err = Scan(t.Context(), pathReader{root: root}, "fleurs-sr", 1, nil)
	if err != nil || !inv.Truncated || inv.Files != 1 {
		t.Errorf("limited scan = %+v, %v", inv, err)
	}
	if _, _, err := Scan(t.Context(), pathReader{root: root}, "../etc", 0, nil); err == nil {
		t.Error("a path outside the root was scanned")
	}
}

func TestValidateRefusesBadShapes(t *testing.T) {
	no := false
	for _, in := range []NewInput{
		{Name: "Bad", Kind: KindLocal, Root: "/mnt/x"},
		{Name: "c", Kind: KindLocal, Root: "relative"},
		{Name: "c", Kind: KindLocal, Root: "/"},
		{Name: "c", Kind: KindNFS, Root: "/mnt/x", Endpoint: "http://x"},
		{Name: "c", Kind: KindS3, Root: "Bucket", Endpoint: "http://x", Credentials: "s3"},
		{Name: "c", Kind: KindS3, Root: "bucket", Credentials: "s3"},
		{Name: "c", Kind: KindHF, Root: "datasets/google/fleurs", Revision: "main"},
		{Name: "c", Kind: KindHF, Root: "datasets/google/fleurs", Revision: strings.Repeat("a", 40), ReadOnly: &no},
		{Name: "c", Kind: "ftp", Root: "/x"},
	} {
		// Shape errors are found before any lookup, so no database is needed.
		if _, err := Validate(t.Context(), failQuerier{}, in); err == nil || strings.Contains(err.Error(), "no database") {
			t.Errorf("Validate(%+v) = %v, want a validation problem", in, err)
		}
	}
}

func TestS3ReaderListsAndReadsSigned(t *testing.T) {
	var sawAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/bucket" && r.URL.Query().Get("list-type") == "2":
			if r.URL.Query().Get("continuation-token") == "" {
				_ = xml.NewEncoder(w).Encode(struct {
					XMLName  xml.Name `xml:"ListBucketResult"`
					Contents []struct {
						Key  string
						Size int64
					}
					IsTruncated           bool
					NextContinuationToken string
				}{Contents: []struct {
					Key  string
					Size int64
				}{{"pre/a.wav", 3}, {"pre/dir/", 0}}, IsTruncated: true, NextContinuationToken: "t2"})
				return
			}
			_, _ = io.WriteString(w, `<ListBucketResult><Contents><Key>pre/b c.wav</Key><Size>4</Size></Contents></ListBucketResult>`)
		case r.URL.EscapedPath() == "/bucket/pre/b%20c.wav":
			_, _ = io.WriteString(w, "abcd")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &s3Reader{endpoint: srv.URL, region: "us-east-1", bucket: "bucket", prefix: "pre", key: "AK", secret: "SK",
		client: srv.Client(), now: func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }}
	var got []string
	if err := r.Walk(t.Context(), "", func(rel string, size int64) error {
		got = append(got, fmt.Sprintf("%s:%d", rel, size))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a.wav:3", "b c.wav:4"}) {
		t.Errorf("walk = %v", got)
	}
	f, err := r.Open(t.Context(), "b c.wav")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "abcd" {
		t.Errorf("read %q", b)
	}
	for _, a := range sawAuth {
		if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AK/20261003/us-east-1/s3/aws4_request") {
			t.Errorf("authorization %q", a)
		}
	}
}

// TestSignV4Headers checks the signed headers and the credential scope (the signature itself is exercised against a
// server in TestS3ReaderListsAndReadsSigned).
func TestSignV4Headers(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	SignV4(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", "s3",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	a := req.Header.Get("Authorization")
	if !strings.Contains(a, "Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request") ||
		!strings.Contains(a, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("authorization %q", a)
	}
	if req.Header.Get("X-Amz-Date") != "20130524T000000Z" {
		t.Errorf("date %q", req.Header.Get("X-Amz-Date"))
	}
}

func TestHubReaderPagesAndResolves(t *testing.T) {
	rev := strings.Repeat("a", 40)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/datasets/org/set/tree/"+rev && r.URL.Query().Get("cursor") == "":
			w.Header().Set("Link", `<`+srv.URL+`/api/datasets/org/set/tree/`+rev+`?recursive=true&cursor=2>; rel="next"`)
			_, _ = io.WriteString(w, `[{"type":"directory","path":"data"},{"type":"file","path":"data/a.tar","size":5}]`)
		case r.URL.Path == "/api/datasets/org/set/tree/"+rev:
			_, _ = io.WriteString(w, `[{"type":"file","path":"README.md","size":2}]`)
		case r.URL.Path == "/datasets/org/set/resolve/"+rev+"/data/a.tar":
			_, _ = io.WriteString(w, "hello")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := &hfReader{base: srv.URL, repo: "datasets/org/set", revision: rev, token: "tok", client: srv.Client()}
	var got []string
	if err := r.Walk(t.Context(), "", func(rel string, size int64) error {
		got = append(got, fmt.Sprintf("%s:%d", rel, size))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"data/a.tar:5", "README.md:2"}) {
		t.Errorf("walk = %v", got)
	}
	f, err := r.Open(t.Context(), "data/a.tar")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	_ = f.Close()
	if string(b) != "hello" {
		t.Errorf("read %q", b)
	}
}
