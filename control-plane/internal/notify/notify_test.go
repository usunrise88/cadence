package notify

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

func TestInQuietHours(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	night := QuietHours{Enabled: true, Start: "22:00", End: "08:00"}
	day := QuietHours{Enabled: true, Start: "12:00", End: "13:30"}
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", "2026-09-30 "+s, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	for _, tc := range []struct {
		name string
		q    QuietHours
		at   time.Time
		want bool
	}{
		{"spanning midnight, late evening", night, at("23:10"), true},
		{"spanning midnight, early morning", night, at("07:59"), true},
		{"spanning midnight, end is exclusive", night, at("08:00"), false},
		{"spanning midnight, start is inclusive", night, at("22:00"), true},
		{"spanning midnight, afternoon", night, at("15:00"), false},
		{"same day window", day, at("12:30"), true},
		{"same day window, after", day, at("13:30"), false},
		{"disabled", QuietHours{Start: "00:00", End: "23:59"}, at("12:00"), false},
		{"empty window", QuietHours{Enabled: true, Start: "09:00", End: "09:00"}, at("09:00"), false},
		// The clock is the instance timezone's, not the server's: 21:30 UTC is 23:30 in Berlin (CEST).
		{"timezone applies", night, time.Date(2026, 9, 30, 21, 30, 0, 0, time.UTC), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := InQuietHours(tc.q, tc.at, berlin); got != tc.want {
				t.Fatalf("InQuietHours = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]Clock{"00:00": 0, "09:00": 540, "23:59": 1439} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"9:00", "24:00", "12:60", "noon", ""} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) accepted", bad)
		}
	}
}

func TestNextAndWeekday(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, loc) // a Wednesday
	if got := Next(now, MustClock("09:00"), loc); !got.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, loc)) {
		t.Errorf("Next past today = %v", got)
	}
	if got := Next(now, MustClock("11:00"), loc); !got.Equal(time.Date(2026, 9, 30, 11, 0, 0, 0, loc)) {
		t.Errorf("Next later today = %v", got)
	}
	sun, ok := ParseWeekday("sunday")
	if !ok || sun != time.Sunday {
		t.Fatalf("ParseWeekday = %v %v", sun, ok)
	}
	if got := NextWeekday(now, sun, MustClock("04:00"), loc); !got.Equal(time.Date(2026, 10, 4, 4, 0, 0, 0, loc)) {
		t.Errorf("NextWeekday = %v", got)
	}
}

func TestDecide(t *testing.T) {
	quiet := Settings{QuietHours: QuietHours{Enabled: true, Start: "22:00", End: "08:00"}}
	night := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	noon := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	approval := Rule{EventClass: ClassApproval, Telegram: true, Timing: TimingImmediate}
	failure := Rule{EventClass: ClassFailure, Telegram: true, Timing: TimingImmediate, BypassQuietHours: true}
	held := Rule{EventClass: ClassOutcome, Telegram: true, Timing: TimingDigest}
	progress := Rule{EventClass: ClassProgress, InApp: true, Timing: TimingNone}
	for _, tc := range []struct {
		name  string
		rule  Rule
		ready bool
		at    time.Time
		want  string
	}{
		{"approval by day", approval, true, noon, StateQueued},
		{"approval in quiet hours is suppressed", approval, true, night, StateSuppressed},
		{"failures break through quiet hours", failure, true, night, StateQueued},
		{"held for the digest", held, true, night, StateDigest},
		{"progress never reaches Telegram", progress, true, noon, ""},
		{"no bot, nothing to send", approval, false, noon, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.rule, quiet, tc.ready, tc.at, time.UTC).State; got != tc.want {
				t.Fatalf("Decide = %q, want %q", got, tc.want)
			}
		})
	}
}

func record(topic, typ string, payload any) events.Record {
	b, _ := json.Marshal(payload)
	return events.Record{Seq: 1, Topic: topic, Type: typ, Payload: b}
}

func TestClassify(t *testing.T) {
	approval := map[string]any{"approval": map[string]any{
		"id": "apr_1", "operation": "runs.new", "reason": "the job spends GPU time", "projectId": "prj_1",
		"actor":    map[string]any{"kind": "agent", "id": "ses_1", "name": "claude-code"},
		"estimate": map[string]any{"gpuHours": 3.5, "remainingGpuHours": 1.0},
	}}
	for _, tc := range []struct {
		name      string
		r         events.Record
		class     string
		title     string
		bodyHas   string
		approvalI string
	}{
		{"approval request with estimate", record("approvals", "approval.requested", approval), ClassApproval,
			"Approval requested: runs.new", "Estimate: 3.5 GPU-hours (1.0 left", "apr_1"},
		{"approval on its entity topic is a repeat", record("entity.approval.apr_1", "approval.requested", approval), "", "", "", ""},
		{"job failed", record("job.job_1", "job.state_changed", map[string]any{"job": map[string]any{"id": "job_1", "kind": "backup", "state": "failed", "error": "disk full"}}),
			ClassFailure, "Job failed: backup", "disk full", ""},
		{"job done is progress", record("job.job_1", "job.state_changed", map[string]any{"job": map[string]any{"id": "job_1", "kind": "noop", "state": "done"}}),
			ClassProgress, "Job done: noop", "", ""},
		{"job running is nothing", record("job.job_1", "job.state_changed", map[string]any{"job": map[string]any{"state": "running"}}), "", "", "", ""},
		{"backup failed", record("backups", "backup.failed", map[string]any{"backup": map[string]any{"id": "bkp_1", "error": "pg_dump: boom"}}),
			ClassFailure, "Backup failed", "pg_dump: boom", ""},
		{"backup failed on the entity topic is a repeat", record("entity.backup.bkp_1", "backup.failed", map[string]any{}), "", "", "", ""},
		{"an unknown event", record("mixes", "mix.edited", map[string]any{}), "", "", "", ""},
		{"a step job's state is told by its pipeline step", record("job.job_2", "job.state_changed", map[string]any{"job": map[string]any{"id": "job_2", "kind": "step", "state": "failed"}}),
			"", "", "", ""},
		{"step done is progress", record("pipeline_run.plr_1", "pipeline_run.step_changed", map[string]any{"pipelineRunId": "plr_1", "runState": "running",
			"step": map[string]any{"id": "pls_1", "step": "train", "kind": "toy_train", "state": "done"}}), ClassProgress, "Step done: train (toy_train)", "plr_1", ""},
		{"step failed is a failure", record("pipeline_run.plr_1", "pipeline_run.step_changed", map[string]any{"pipelineRunId": "plr_1", "runState": "failed",
			"step": map[string]any{"id": "pls_1", "step": "train", "kind": "toy_train", "state": "failed", "error": map[string]any{"type": "oom", "message": "CUDA out of memory"}}}),
			ClassFailure, "Step failed: train (toy_train)", "oom: CUDA out of memory", ""},
		{"step running is nothing", record("pipeline_run.plr_1", "pipeline_run.step_changed", map[string]any{"step": map[string]any{"state": "running"}}), "", "", "", ""},
		{"step event on its entity topic is a repeat", record("entity.pipeline_step.pls_1", "pipeline_run.step_changed", map[string]any{"step": map[string]any{"state": "done"}}), "", "", "", ""},
		{"host unreachable is a failure", record("compute.cmp_1", "compute.health", map[string]any{"hostId": "cmp_1", "health": map[string]any{"state": "unreachable", "detail": "no worker on this host has reported for a minute"}}),
			ClassFailure, "Compute host unreachable", "no worker on this host", ""},
		{"host healthy again is nothing", record("compute.cmp_1", "compute.health", map[string]any{"hostId": "cmp_1", "health": map[string]any{"state": "healthy"}}), "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := Classify(tc.r)
			if tc.class == "" {
				if ok {
					t.Fatalf("classified %+v", n)
				}
				return
			}
			if !ok || n.Class != tc.class || n.Title != tc.title || !strings.Contains(n.Body, tc.bodyHas) || n.ApprovalID != tc.approvalI {
				t.Fatalf("Classify = %+v, %v", n, ok)
			}
		})
	}
}

// TestClassTableMatchesWeb keeps the web shell's table (web/src/shell/notifications/classes.ts) equal to classTable,
// apart from the two payload-free types only the web table lists (approval.requested, notification.digest).
func TestClassTableMatchesWeb(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "shell", "notifications", "classes.ts"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "const TABLE")
	end := strings.Index(body[start:], "};")
	if start < 0 || end < 0 {
		t.Fatal("classes.ts has no TABLE")
	}
	web := map[string]string{}
	for _, m := range regexp.MustCompile(`"([a-z_.]+)":\s*"([a-z_]+)"`).FindAllStringSubmatch(body[start:start+end], -1) {
		web[m[1]] = m[2]
	}
	want := maps.Clone(classTable)
	want["approval.requested"], want["notification.digest"] = ClassApproval, ClassDigest
	if !maps.Equal(web, want) {
		t.Fatalf("classes.ts TABLE = %v\nclassify.go classTable (+ approval, digest) = %v", web, want)
	}
	for _, typ := range []string{"job.state_changed", "pipeline_run.step_changed", "compute.health"} {
		if !strings.Contains(body, `"`+typ+`"`) {
			t.Errorf("classes.ts classOf does not handle %s", typ)
		}
	}
}

func TestSignerParse(t *testing.T) {
	s := NewSigner([]byte("key-one"))
	other := NewSigner([]byte("key-two"))
	id := "abcdefghijklmnop"
	data := actionCode(ActionApprove) + "." + id + "." + s.mac(ActionApprove, id)
	if len(data) > 64 {
		t.Fatalf("callback data is %d bytes; Telegram allows 64", len(data))
	}
	if gotID, action, err := s.Parse(data); err != nil || gotID != id || action != ActionApprove {
		t.Fatalf("Parse = %q %q %v", gotID, action, err)
	}
	flipped := "d" + data[1:] // approve's signature on a deny
	for name, bad := range map[string]string{
		"another key":           actionCode(ActionApprove) + "." + id + "." + other.mac(ActionApprove, id),
		"action swapped":        flipped,
		"id changed":            "a.abcdefghijklmnoq." + s.mac(ActionApprove, id),
		"truncated":             data[:len(data)-2],
		"garbage":               "hello",
		"unknown action letter": "x" + data[1:],
	} {
		if _, _, err := s.Parse(bad); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("%s: Parse err = %v, want ErrTokenInvalid", name, err)
		}
	}
}

func TestDigestText(t *testing.T) {
	used := 2.5
	d := Digest{
		Until:         time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC),
		Jobs:          []JobCount{{Kind: "backup", Done: 1}, {Kind: "step", Done: 3, Failed: 1, Running: 2}},
		Spend:         []ProjectSpend{{Project: "hebrew", BudgetGPUHours: 8, UsedGPUHours: &used}, {Project: "demo", BudgetGPUHours: 4}},
		OpenApprovals: 2, Approvals: []string{"runs.new", "aliases.set"}, Held: []string{"Gate verdict"},
		LastBackup: "succeeded (2026-09-30 03:00 UTC)",
	}
	text := d.Text()
	for _, want := range []string{
		"• step: 3 done, 1 failed, 2 queued or running", "• hebrew: 2.5 of 8.0 GPU-h",
		"• demo: budget 4.0 GPU-h/day (use not metered yet)", "Open approvals: 2\n• runs.new\n• aliases.set",
		"Held for the digest (1):\n• Gate verdict", "Last backup: succeeded",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("digest lacks %q:\n%s", want, text)
		}
	}
	if d.Title() != "Cadence daily digest · Wed 30 Sep" {
		t.Errorf("title %q", d.Title())
	}
}

func TestRuleDepartures(t *testing.T) {
	r := Rule{EventClass: ClassOutcome, InApp: true, Telegram: false, Timing: TimingDigest}
	v := r.JSON()
	if strings.Join(v.Departures, ",") != "channels.telegram,timing" || v.Channels.Telegram {
		t.Fatalf("departures %v channels %+v", v.Departures, v.Channels)
	}
	if len(v.Events) == 0 || v.Events[0] != "batch.closed" {
		t.Fatalf("events %v", v.Events)
	}
}
