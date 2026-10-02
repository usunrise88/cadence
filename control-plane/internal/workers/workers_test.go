package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func TestMissingXCadence(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   []string
	}{
		{"complete", `{"type":"object","properties":{"lr":{"x-cadence":{"default":1,"description":"d","source":"s","range":{"min":0}}}}}`, nil},
		{"range may be null", `{"properties":{"lang":{"x-cadence":{"default":"he","description":"d","source":"s","range":null}}}}`, nil},
		{"missing source", `{"properties":{"lr":{"x-cadence":{"default":1,"description":"d","range":{}}}}}`, []string{"lr"}},
		{"no metadata", `{"properties":{"b":{"type":"integer"},"a":{"type":"integer"}}}`, []string{"a", "b"}},
		{"no parameters", `{"type":"object"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MissingXCadence(json.RawMessage(tt.schema))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MissingXCadence = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	if _, err := MissingXCadence(json.RawMessage(`[]`)); err == nil {
		t.Fatal("an array is not a parameter schema")
	}
}

func TestSchemaHashIgnoresKeyOrder(t *testing.T) {
	// Wording and defaults from defaults.yaml are not identity; a literal default or a type is.
	w1, _ := SchemaHash(json.RawMessage(`{"properties":{"n":{"type":"integer","default":3,"description":"a","x-cadence":{"defaultRef":"training.steps","default":3,"description":"a","source":"s","range":{"min":1}}}}}`))
	w2, _ := SchemaHash(json.RawMessage(`{"properties":{"n":{"type":"integer","default":5,"description":"b","x-cadence":{"defaultRef":"training.steps","default":5,"description":"b","source":"t","range":{"min":2}}}}}`))
	l1, _ := SchemaHash(json.RawMessage(`{"properties":{"n":{"type":"integer","default":3,"x-cadence":{"default":3,"description":"a","source":"s","range":{"min":1}}}}}`))
	l2, _ := SchemaHash(json.RawMessage(`{"properties":{"n":{"type":"integer","default":4,"x-cadence":{"default":4,"description":"a","source":"s","range":{"min":1}}}}}`))
	if w1 != w2 || l1 == l2 || w1 == l1 {
		t.Fatalf("identity hashes: refs %s %s, literals %s %s", w1, w2, l1, l2)
	}
	a, err1 := SchemaHash(json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`))
	b, err2 := SchemaHash(json.RawMessage(`{"properties":{"x":{"type":"integer"}},"type":"object"}`))
	if err1 != nil || err2 != nil || a != b || len(a) != 64 {
		t.Fatalf("SchemaHash = %q, %q (%v %v)", a, b, err1, err2)
	}
}

func TestEnvName(t *testing.T) {
	for in, want := range map[string]string{"hf-token": "HF_TOKEN", "ngc_key": "NGC_KEY", "s3.backup": "S3_BACKUP"} {
		if got := envName(in); got != want {
			t.Errorf("envName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTraceparent(t *testing.T) {
	a, b := traceparent("job_1", "lse_1"), traceparent("job_1", "lse_2")
	if len(a) != 55 || !strings.HasPrefix(a, "00-") || !strings.HasSuffix(a, "-01") || a[3:35] != b[3:35] || a == b {
		t.Fatalf("traceparent = %q, %q: one trace per job, one span per lease", a, b)
	}
}

func TestLogsRoundTrip(t *testing.T) {
	s := New(Options{LogDir: t.TempDir()})
	const job = "job_0192f0a0-0000-7000-8000-000000000001"
	lines, err := parseLines([]byte(`{"t":"2026-09-30T12:00:00Z","msg":"one"}`+"\n\n"+
		`{"t":"2026-09-30T12:00:01Z","level":"error","msg":"Two","fields":{"k":1}}`+"\n"), time.Now())
	if err != nil || len(lines) != 2 || lines[0].Level != "info" {
		t.Fatalf("parseLines = %+v, %v", lines, err)
	}
	path, _ := s.logPath(job)
	if err := s.appendFile(path, job, lines); err != nil {
		t.Fatal(err)
	}
	if lines[1].Seq != 2 {
		t.Fatalf("seq = %d", lines[1].Seq)
	}
	s.logSeq = map[string]int{} // a new process counts the file
	more, _ := parseLines([]byte(`{"t":"2026-09-30T12:00:02Z","level":"warn","msg":"three two"}`), time.Now())
	if err := s.appendFile(path, job, more); err != nil || more[0].Seq != 3 {
		t.Fatalf("append after restart: seq %d, %v", more[0].Seq, err)
	}
	tests := []struct {
		f        LogFilter
		wantSeqs []int
		wantNext int
	}{
		{LogFilter{}, []int{1, 2, 3}, 3},
		{LogFilter{MinLevel: "warn"}, []int{2, 3}, 3},
		{LogFilter{Text: "TWO"}, []int{2, 3}, 3},
		{LogFilter{After: 1, Limit: 1}, []int{2}, 2},
		{LogFilter{Tail: true, Limit: 2}, []int{2, 3}, 3},
	}
	for _, tt := range tests {
		got, next, err := s.ReadLogs(job, tt.f)
		if err != nil {
			t.Fatal(err)
		}
		seqs := []int{}
		for _, l := range got {
			seqs = append(seqs, l.Seq)
		}
		if !reflect.DeepEqual(seqs, tt.wantSeqs) || next != tt.wantNext {
			t.Errorf("ReadLogs(%+v) = %v next %d; want %v next %d", tt.f, seqs, next, tt.wantSeqs, tt.wantNext)
		}
	}
	if _, err := parseLines([]byte(`{"msg":"no time"}`), time.Now()); err == nil {
		t.Fatal("a line without t was accepted")
	} else if p, ok := problems.As(err); !ok || p.Type != problems.ValidationFailed {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.logPath("../etc/passwd"); err == nil {
		t.Fatal("a path outside the log directory")
	}
	// Retention removes logs untouched for 14 days.
	old := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneLogs(t.Context()); err != nil || n != 1 {
		t.Fatalf("PruneLogs = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(s.LogDir(), job+".ndjson")); !os.IsNotExist(err) {
		t.Fatal("old log kept")
	}
}

func TestByCardIndex(t *testing.T) {
	used := func(mb int) *int { return &mb }
	tests := []struct {
		name string
		in   []CardTelemetry
		want []int // indexes, in lock order
		used []int // MemoryUsedMB of each, to tell duplicates apart
	}{
		{"none", nil, []int{}, []int{}},
		{"already ordered", []CardTelemetry{{Index: 0, MemoryUsedMB: used(1)}, {Index: 1, MemoryUsedMB: used(2)}}, []int{0, 1}, []int{1, 2}},
		{"reversed", []CardTelemetry{{Index: 3, MemoryUsedMB: used(3)}, {Index: 1, MemoryUsedMB: used(1)}, {Index: 2, MemoryUsedMB: used(2)}},
			[]int{1, 2, 3}, []int{1, 2, 3}},
		{"duplicate index: the last report wins", []CardTelemetry{{Index: 1, MemoryUsedMB: used(10)}, {Index: 0, MemoryUsedMB: used(5)},
			{Index: 1, MemoryUsedMB: used(11)}}, []int{0, 1}, []int{5, 11}},
	}
	for _, tt := range tests {
		got := byCardIndex(tt.in)
		idx, mb := []int{}, []int{}
		for _, c := range got {
			idx, mb = append(idx, c.Index), append(mb, *c.MemoryUsedMB)
		}
		if !reflect.DeepEqual(idx, tt.want) || !reflect.DeepEqual(mb, tt.used) {
			t.Errorf("%s: byCardIndex = %v (used %v); want %v (used %v)", tt.name, idx, mb, tt.want, tt.used)
		}
	}
}

func TestOversizeLogLinesAreTruncated(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	then := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	huge := strings.Repeat("x", 2<<20)
	bigFields := `{"blob":"` + strings.Repeat("y", MaxLogLineBytes) + `"}`
	after := `{"t":"2026-09-30T12:00:05Z","msg":"after"}`
	tests := []struct {
		name      string
		line      string
		wantLevel string
		wantT     time.Time
		wantStart string
		noFields  bool
	}{
		{"valid line with a huge msg", `{"t":"2026-09-30T12:00:00Z","level":"error","msg":"` + huge + `"}`, "error", then, "xxx", false},
		{"valid line with huge fields", `{"t":"2026-09-30T12:00:00Z","msg":"m","fields":` + bigFields + `}`, "info", then, "m [truncated", true},
		{"not JSON", huge, "warn", now, "xxx", true},
		{"cut by the request limit", `{"t":"2026-09-30T12:00:00Z","msg":"` + huge[:MaxLogLineBytes], "warn", now, `{"t":`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := parseLines([]byte(tt.line+"\n"+after+"\n"), now)
			if err != nil {
				t.Fatalf("parseLines refused the batch: %v", err)
			}
			if len(lines) != 2 || lines[1].Msg != "after" {
				t.Fatalf("lines = %d; the line after the oversize one must land", len(lines))
			}
			l := lines[0]
			if l.Level != tt.wantLevel || !l.T.Equal(tt.wantT) || !strings.HasPrefix(l.Msg, tt.wantStart) ||
				!strings.Contains(l.Msg, "[truncated: the line had") || (tt.noFields && l.Fields != nil) {
				t.Fatalf("line = level %q t %v msg %.40q… fields %d bytes", l.Level, l.T, l.Msg, len(l.Fields))
			}
			b, err := json.Marshal(l)
			if err != nil || len(b) > MaxLogLineBytes {
				t.Fatalf("stored line has %d bytes (max %d), %v", len(b), MaxLogLineBytes, err)
			}
		})
	}
}
