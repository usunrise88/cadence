package promotions

import (
	"strings"
	"testing"
)

func TestParseReceipt(t *testing.T) {
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	line := "CADENCE-RECEIPT 1 " + h1 + " " + h2 + " 20/20 era-asr-01 2026-11-03T10:00:00Z"
	r, err := ParseReceipt("deliver.sh: smoke check: 20/20 match the staging server\n" + line + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordHash != h1 || r.ServedSHA256 != h2 || r.SmokeOK != 20 || r.SmokeTotal != 20 || r.Host != "era-asr-01" ||
		r.RanAt.Format("2006-01-02T15:04:05Z") != "2026-11-03T10:00:00Z" || r.Line != line {
		t.Errorf("parsed %+v", r)
	}
	if _, err := ParseReceipt(line + "\r\n  " + line + "  "); err != nil {
		t.Errorf("the same line pasted twice: %v", err)
	}
	for name, text := range map[string]string{
		"none":         "deliver.sh: STOPPED: the smoke check passed 3 of 20",
		"two":          line + "\n" + strings.Replace(line, "20/20", "19/20", 1),
		"version":      strings.Replace(line, " 1 ", " 2 ", 1),
		"short hash":   strings.Replace(line, h1, "abc", 1),
		"upper hex":    strings.Replace(line, h2, strings.ToUpper(h2), 1),
		"smoke":        strings.Replace(line, "20/20", "21/20", 1),
		"smoke format": strings.Replace(line, "20/20", "all", 1),
		"time":         strings.Replace(line, "2026-11-03T10:00:00Z", "2026-11-03 10:00", 1),
		"fields":       line + " extra",
		"host":         strings.Replace(line, "era-asr-01", "era;rm", 1),
	} {
		if _, err := ParseReceipt(text); err == nil {
			t.Errorf("%s: accepted %q", name, text)
		}
	}
}
