package queue

import (
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
)

func ptr[T any](v T) *T { return &v }

func card(windows compute.Windows, held ...Held) Card {
	return Card{Config: compute.Card{Index: 0, MemoryGB: 48, MemoryCapGB: 24,
		AllowedJobKinds: []string{"training", "eval", "data"}, Windows: windows}, Held: held}
}

// liveCard also takes interactive sessions and benchmarks.
func liveCard(held ...Held) Card {
	c := card(nil, held...)
	c.Config.AllowedJobKinds = []string{"training", "eval", "data", "interactive", "benchmark"}
	return c
}

func TestFit(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) // a Wednesday
	nights := compute.Windows{"training": {{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "20:00", End: "08:00"}}}
	tests := []struct {
		name    string
		card    Card
		need    Need
		now     time.Time
		wantMB  int
		wantWhy string
	}{
		{"training takes the whole cap", card(nil), Need{JobKind: "training"}, now, 24 * 1024, ""},
		{"declared memory", card(nil), Need{JobKind: "eval", MemoryMB: 4096}, now, 4096, ""},
		{"second training refused", card(nil, Held{"training", 8192}), Need{JobKind: "training", MemoryMB: 1024}, now, 0, "already runs a training"},
		{"eval beside a sized training", card(nil, Held{"training", 16384}), Need{JobKind: "eval", MemoryMB: 8192}, now, 8192, ""},
		{"eval too big beside training", card(nil, Held{"training", 16384}), Need{JobKind: "eval", MemoryMB: 9000}, now, 0, "left under its cap"},
		{"nothing beside a whole-cap training", card(nil, Held{"training", 24576}), Need{JobKind: "data"}, now, 0, "left under its cap"},
		{"kind not allowed", card(nil), Need{JobKind: "export"}, now, 0, "does not accept export"},
		{"telemetry says busy", Card{Config: card(nil).Config, FreeMB: ptr(20000)}, Need{JobKind: "training"}, now, 0, "by its telemetry"},
		{"telemetry slack", Card{Config: card(nil).Config, FreeMB: ptr(24*1024 - 300)}, Need{JobKind: "training"}, now, 24 * 1024, ""},
		{"window closed at noon", card(nights), Need{JobKind: "training"}, now, 0, "no availability window"},
		{"window open at night", card(nights), Need{JobKind: "training", EstimateSeconds: ptr(3600.0)}, now.Add(10 * time.Hour), 24 * 1024, ""},
		{"estimate does not fit", card(nights), Need{JobKind: "training", EstimateSeconds: ptr(13 * 3600.0)}, now.Add(10 * time.Hour), 0, "does not fit"},
		{"resuming ignores the estimate", card(nights), Need{JobKind: "training", EstimateSeconds: ptr(13 * 3600.0), Resuming: true}, now.Add(10 * time.Hour), 24 * 1024, ""},
		{"other kinds are always open", card(nights), Need{JobKind: "eval", MemoryMB: 1024}, now, 1024, ""},
		{"interactive not accepted", card(nil), Need{JobKind: "interactive", MemoryMB: 6000}, now, 0, "does not accept interactive"},
		{"interactive beside a sized training", liveCard(Held{"training", 16384}), Need{JobKind: "interactive", MemoryMB: 6000}, now, 6000, ""},
		{"interactive beside training and an eval", liveCard(Held{"training", 12288}, Held{"eval", 4096}), Need{JobKind: "interactive", MemoryMB: 8600}, now, 0, "left under its cap"},
		{"interactive waits beside a whole-cap training", liveCard(Held{"training", 24576}), Need{JobKind: "interactive", MemoryMB: 6000}, now, 0, "left under its cap"},
		{"two interactive sessions share a card", liveCard(Held{"interactive", 6000}), Need{JobKind: "interactive", MemoryMB: 6000}, now, 6000, ""},
		{"interactive never beside a benchmark", liveCard(Held{"benchmark", 4096}), Need{JobKind: "interactive", MemoryMB: 6000}, now, 0, "runs a benchmark"},
		{"a benchmark never beside a session", liveCard(Held{"interactive", 6000}), Need{JobKind: "benchmark", MemoryMB: 4096}, now, 0, "holds a live session"},
		{"training beside a session takes the rest", liveCard(Held{"interactive", 6000}), Need{JobKind: "training"}, now, 24*1024 - 6000, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mb, why := Fit(tt.card, tt.need, tt.now)
			if mb != tt.wantMB || (tt.wantWhy == "" && why != "") || !strings.Contains(why, tt.wantWhy) {
				t.Fatalf("Fit = %d, %q; want %d, %q", mb, why, tt.wantMB, tt.wantWhy)
			}
		})
	}
}

func TestAvailability(t *testing.T) {
	berlin := compute.Windows{"training": {
		{Days: []string{"fri"}, Start: "22:00", End: "24:00", Timezone: "Europe/Berlin"},
		{Days: []string{"sat"}, Start: "00:00", End: "06:00", Timezone: "Europe/Berlin"},
	}}
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		at         time.Time
		open       bool
		wantCloses time.Time
	}{
		{"friday night, windows chain past midnight", time.Date(2026, 10, 2, 23, 0, 0, 0, loc), true, time.Date(2026, 10, 3, 6, 0, 0, 0, loc)},
		{"saturday early", time.Date(2026, 10, 3, 5, 59, 0, 0, loc), true, time.Date(2026, 10, 3, 6, 0, 0, 0, loc)},
		{"saturday at close", time.Date(2026, 10, 3, 6, 0, 0, 0, loc), false, time.Time{}},
		{"friday afternoon", time.Date(2026, 10, 2, 15, 0, 0, 0, loc), false, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, always, closes := berlin.Availability("training", tt.at)
			if open != tt.open || always || !closes.Equal(tt.wantCloses) {
				t.Fatalf("Availability = %v %v %v; want %v %v", open, always, closes, tt.open, tt.wantCloses)
			}
		})
	}
	if open, always, _ := berlin.Availability("eval", time.Now()); !open || !always {
		t.Fatal("a kind without windows must always be open")
	}
	if !WindowClosed(berlin, "training", time.Date(2026, 10, 2, 15, 0, 0, 0, loc)) || WindowClosed(berlin, "eval", time.Now()) {
		t.Fatal("WindowClosed")
	}
}

// A window naming no time zone follows the instance time zone (policies.timezone) once resolved through InZone; an
// explicit zone is kept.
func TestWindowsInInstanceZone(t *testing.T) {
	ws := compute.Windows{"training": {
		{Days: []string{"wed"}, Start: "22:00", End: "08:00"},
		{Days: []string{"sun"}, Start: "10:00", End: "11:00", Timezone: "UTC"},
	}}
	berlin := ws.InZone("Europe/Berlin")
	if berlin["training"][0].Timezone != "Europe/Berlin" || berlin["training"][1].Timezone != "UTC" {
		t.Fatalf("InZone = %+v", berlin)
	}
	if ws["training"][0].Timezone != "" {
		t.Fatal("InZone changed its receiver")
	}
	// Wednesday 21:00 UTC is 23:00 in Berlin (UTC+2 in September): the window resolved to Berlin is open (until
	// 08:00 Berlin, 06:00 UTC); read as UTC it would not open before 22:00.
	at := time.Date(2026, 9, 30, 21, 0, 0, 0, time.UTC)
	if open, _, _ := ws.Availability("training", at); open {
		t.Fatal("an unresolved window reads as UTC: 21:00 is before 22:00")
	}
	open, _, closes := berlin.Availability("training", at)
	if !open || !closes.Equal(time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("window in the instance zone: open %v closes %v", open, closes)
	}
	if compute.Windows(nil).InZone("Europe/Berlin") != nil {
		t.Fatal("no windows stay no windows")
	}
}

func TestValidateWindows(t *testing.T) {
	bad := compute.ValidateWindows(compute.Windows{
		"training": {{Days: []string{"mon", "funday"}, Start: "25:00", End: "24:00", Timezone: "Mars/Olympus"}},
		"nap":      {{Days: []string{"mon"}, Start: "01:00", End: "02:00"}},
	})
	for _, at := range []string{"/training/0/days/1", "/training/0/start", "/training/0/timezone", "/nap"} {
		if bad[at] == "" {
			t.Errorf("no problem at %s: %v", at, bad)
		}
	}
	if bad["/training/0/end"] != "" {
		t.Errorf("24:00 is a valid end: %v", bad)
	}
}
