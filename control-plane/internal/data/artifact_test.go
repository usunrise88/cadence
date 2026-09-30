package data

import (
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

func TestContentFingerprint(t *testing.T) {
	a := []Line{{Hash: "b3:aa", Split: "train", Text: "one"}, {Hash: "b3:bb", Split: "test", Text: "two"}}
	b := []Line{a[1], a[0]}
	if ContentFingerprint(a) != ContentFingerprint(b) {
		t.Error("order changed the fingerprint")
	}
	for _, tc := range []struct {
		name string
		mut  func(l *Line)
	}{
		{"split", func(l *Line) { l.Split = "validation" }},
		{"text", func(l *Line) { l.Text = "one." }},
		{"audio", func(l *Line) { l.Hash = "b3:cc" }},
	} {
		c := append([]Line(nil), a...)
		tc.mut(&c[0])
		if ContentFingerprint(c) == ContentFingerprint(a) {
			t.Errorf("a changed %s kept the fingerprint", tc.name)
		}
	}
	// Tab and newline in a transcript cannot make two different contents collide.
	x := []Line{{Hash: "b3:aa", Split: "train", Text: "a\tb"}}
	y := []Line{{Hash: "b3:aa", Split: "train\ta", Text: "b"}}
	if ContentFingerprint(x) == ContentFingerprint(y) {
		t.Error("tuple boundaries collided")
	}
	if len(ContentFingerprint(a)) != 64 {
		t.Error("fingerprint is not 64 hex digits")
	}
}

func TestParseLines(t *testing.T) {
	files := map[string]cas.File{"audio/a.wav": {Path: "audio/a.wav", Hash: "b3:" + strings.Repeat("a", 64), Size: 10}}
	ok := `{"audio":"audio/a.wav","duration":1.5,"sampleRate":16000,"language":"he-IL","text":"x","origin":"human","split":"train"}`
	lines, err := parseLines([]byte(ok+"\n\n"), files)
	if err != nil || len(lines) != 1 || lines[0].Channels != 1 || lines[0].Size != 10 {
		t.Fatalf("lines %+v err %v", lines, err)
	}
	for _, tc := range []struct{ line, want string }{
		{strings.Replace(ok, `"origin":"human"`, `"origin":"robot"`, 1), "origin"},
		{strings.Replace(ok, `"origin":"human"`, `"origin":"model:nemotron"`, 1), ""},
		{strings.Replace(ok, `"duration":1.5`, `"duration":0`, 1), "duration"},
		{strings.Replace(ok, `"split":"train"`, `"split":"dev"`, 1), "split"},
		{strings.Replace(ok, `audio/a.wav`, `audio/b.wav`, 1), "not a file"},
		{strings.Replace(ok, `"text":"x"`, `"text":"x","extra":1`, 1), "unknown field"},
		{strings.Replace(ok, `"text":"x"`, `"text":"x","fingerprints":{"audio-b3":"x"}`, 1), "fingerprint kind"},
		{strings.Replace(ok, `"text":"x"`, `"text":"x","confidence":1.5`, 1), "confidence"},
		{ok + "\n" + ok, "appears once"},
	} {
		_, err := parseLines([]byte(tc.line), files)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.line, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", tc.line, err, tc.want)
		}
	}
}
