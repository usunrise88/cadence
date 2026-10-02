package langpacks

import (
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/templates"
)

var limits = Limits{BoostMaxTerms: 5000, BoostMinWeight: 0, BoostMaxWeight: 10}

func TestBundledPacksCheckOut(t *testing.T) {
	shipped, err := Bundled(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"he-IL", "sr"} {
		if !strings.Contains(strings.Join(shipped, " "), want) {
			t.Fatalf("Cadence ships %v, want %s among them", shipped, want)
		}
	}
	for _, name := range shipped {
		t.Run(name, func(t *testing.T) {
			p, err := FromTree(templates.FS, path.Join(Dir, name), name)
			if err != nil {
				t.Fatal(err)
			}
			if issues := p.Check(limits); len(issues) > 0 {
				t.Fatalf("starter pack %s does not check out: %+v", name, issues)
			}
			for _, f := range []string{FileNormalizer, FileITN, FileTranslit, FileLID, FileGoldenRecipe, FileReadme} {
				if _, ok := p.Files[f]; !ok {
					t.Errorf("starter pack %s lacks %s", name, f)
				}
			}
			n, err := p.Normalizer()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(n.Scoring.Normalizer, "normalizer/") {
				t.Errorf("scoring.normalizer = %q, want a normalizer collection", n.Scoring.Normalizer)
			}
			if len(p.BoostLists(0)) == 0 {
				t.Errorf("starter pack %s has no boost list", name)
			}
			for _, fp := range p.Paths() {
				if strings.HasSuffix(fp, ".yaml") && !strings.HasPrefix(string(p.Files[fp]), "# written by Cadence") {
					t.Errorf("%s does not say it is written by Cadence", fp)
				}
			}
		})
	}
}

// The sr pack's scheme is the worker's sr-Cyrl-Latn letter map (worker/cadence_worker/translit.py), letter for letter.
func TestSerbianSchemeMatchesTheWorker(t *testing.T) {
	src, err := os.ReadFile("../../../worker/cadence_worker/translit.py")
	if err != nil {
		t.Fatal(err)
	}
	cyrl := regexp.MustCompile(`_SR_CYRL = "([^"]+)"`).FindStringSubmatch(string(src))
	latn := regexp.MustCompile(`(?s)_SR_LATN = \[(.*?)\]`).FindStringSubmatch(string(src))
	if cyrl == nil || latn == nil {
		t.Fatal("translit.py no longer has _SR_CYRL and _SR_LATN")
	}
	var want [][2]string
	letters := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(latn[1], -1)
	for i, r := range []rune(cyrl[1]) {
		want = append(want, [2]string{string(r), letters[i][1]})
	}
	p, err := FromTree(templates.FS, "lang/sr", "sr")
	if err != nil {
		t.Fatal(err)
	}
	var tr Translit
	if err := strict(p.Files[FileTranslit], &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Schemes) != 1 || tr.Schemes[0].Name != "sr-Cyrl-Latn" {
		t.Fatalf("schemes = %+v, want sr-Cyrl-Latn", tr.Schemes)
	}
	got := tr.Schemes[0].Map
	if len(got) != len(want) {
		t.Fatalf("%d letters, the worker maps %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].From != w[0] || got[i].To != w[1] {
			t.Errorf("letter %d: %s → %s, the worker maps %s → %s", i, got[i].From, got[i].To, w[0], w[1])
		}
	}
}

func TestMatch(t *testing.T) {
	shipped := []string{"he-IL", "sr", "sr-Latn"}
	tests := []struct {
		locale, want string
		ok           bool
	}{
		{"he-IL", "he-IL", true},
		{"he-il", "he-IL", true},
		{"he", "he-IL", true},
		{"sr", "sr", true},
		{"sr-Latn-RS", "sr-Latn", true},
		{"sr-Cyrl", "sr", true},
		{"ru-RU", "", false},
	}
	for _, tt := range tests {
		got, ok := Match(shipped, tt.locale)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Match(%s) = %q, %v; want %q, %v", tt.locale, got, ok, tt.want, tt.ok)
		}
	}
}

func TestBoost(t *testing.T) {
	src := "# weight: 1.5\n# Names.\n\nMoshe Cohen\n  Dana Levi  \r\n# a comment between\nWhatsApp\n"
	b, err := ParseBoost("names", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	if b.Weight != 1.5 || strings.Join(b.Terms, "|") != "Moshe Cohen|Dana Levi|WhatsApp" {
		t.Fatalf("parsed %+v", b)
	}
	if got := string(b.Artifact()); got != `{"terms":["Moshe Cohen","Dana Levi","WhatsApp"],"weight":1.5}` {
		t.Fatalf("artifact %s", got)
	}
	if len(b.SHA256()) != 64 {
		t.Fatalf("sha %q", b.SHA256())
	}
	b.Terms, b.Weight = []string{"Avi"}, 2
	out := string(b.Render([]byte(src)))
	if out != "# weight: 2\n# Names.\n# a comment between\nAvi\n" {
		t.Fatalf("render:\n%s", out)
	}
	again, err := ParseBoost("names", []byte(out), 0)
	if err != nil || again.Weight != 2 || len(again.Terms) != 1 {
		t.Fatalf("round trip %+v %v", again, err)
	}

	bad := []struct{ name, src, want string }{
		{"no weight", "Moshe\n", "weight"},
		{"two weights", "# weight: 1\n# weight: 2\n", "second weight"},
		{"not a number", "# weight: lots\n", "not a number"},
		{"duplicate", "# weight: 1\nA\nA\n", "already on line 2"},
		{"too long", "# weight: 1\nA\nB\nC\n", "at most 2"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseBoost("x", []byte(tt.src), 2); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := NormalizeTerms([]string{" a ", "b", "a"}); err == nil {
		t.Fatal("duplicate terms accepted")
	}
	if got, err := NormalizeTerms([]string{" a ", "b"}); err != nil || strings.Join(got, ",") != "a,b" {
		t.Fatalf("NormalizeTerms = %v, %v", got, err)
	}
}

func TestCheckFindsBrokenFiles(t *testing.T) {
	base, err := FromTree(templates.FS, "lang/he-IL", "he-IL")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, file, content, want string
	}{
		{"unknown key", FileNormalizer, "version: 1\nlocale: he-IL\nscoring: {normalizer: normalizer/he-il}\ntraining: {unicode: NFC, removeMarks: true, casefold: false, punctuation: keep, numbers: keep, mappings: []}\nextra: 1\n", "field extra not found"},
		{"bad scoring reference", FileNormalizer, "version: 1\nlocale: he-IL\nscoring: {normalizer: basic}\ntraining: {unicode: NFC, removeMarks: true, casefold: false, punctuation: keep, numbers: keep, mappings: []}\n", "must name a registry normalizer"},
		{"bad training form", FileNormalizer, "version: 1\nlocale: he-IL\nscoring: {normalizer: normalizer/he-il}\ntraining: {unicode: NFD, removeMarks: true, casefold: false, punctuation: keep, numbers: keep, mappings: []}\n", "training: unicode"},
		{"unknown scheme", FileNormalizer, "version: 1\nlocale: he-IL\nscoring: {normalizer: normalizer/he-il}\ntraining: {unicode: NFC, removeMarks: true, casefold: false, punctuation: keep, numbers: keep, transliterate: he-Latn, mappings: []}\n", "not a scheme"},
		{"other language", FileLID, "version: 1\nlocale: sr\naccept: [sr]\ncodeSwitch: keep\nminConfidence: 0.5\n", "not the pack's language"},
		{"code switch", FileLID, "version: 1\nlocale: he-IL\naccept: [he-IL]\ncodeSwitch: maybe\nminConfidence: 0.5\n", "codeSwitch"},
		{"itn example", FileITN, "version: 1\nlocale: he-IL\nclasses:\n  - {name: number, description: n, pattern: '\\d+', examples: [{spoken: אחת, written: one}]}\n", "does not match"},
		{"itn pattern", FileITN, "version: 1\nlocale: he-IL\nclasses:\n  - {name: number, description: n, pattern: '(', examples: []}\n", "not a regular expression"},
		{"golden groups", FileGoldenRecipe, "version: 1\nlocale: he-IL\nnormalizer: normalizer/he-il\ntargetHours: 2\nminUtterances: 300\nutteranceSeconds: {min: 1, max: 30}\ngroups: team\nsplitRule: speaker-disjoint\nmaxSpeakerShare: 0.05\nstratify: []\ndomains: []\n", "groups"},
		{"boost weight range", "boost/names.txt", "# weight: 50\nA\n", "outside"},
		{"boost domain", "boost/Names.txt", "# weight: 1\n", "domain"},
		{"stray file", "notes.txt", "hello", "not this file"},
		{"not UTF-8", "boost/x.txt", "\xff\xfe", "UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Pack{Locale: base.Locale, Files: map[string][]byte{}}
			for k, v := range base.Files {
				p.Files[k] = v
			}
			p.Files[tt.file] = []byte(tt.content)
			issues := p.Check(limits)
			found := false
			for _, i := range issues {
				found = found || (strings.Contains(i.Message, tt.want) && strings.HasPrefix(i.Path, "lang/he-IL/"))
			}
			if !found {
				t.Fatalf("issues %+v, want one saying %q", issues, tt.want)
			}
		})
	}
	p := Pack{Locale: "he-IL", Files: map[string][]byte{FileReadme: []byte("# x")}}
	if issues := p.Check(limits); len(issues) == 0 || !strings.Contains(issues[0].Message, FileNormalizer) {
		t.Fatalf("a pack without normalizer.yaml checked out: %+v", issues)
	}
}
