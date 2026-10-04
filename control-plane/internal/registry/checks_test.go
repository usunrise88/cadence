package registry

import (
	"slices"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func problemType(err error) string {
	if err == nil {
		return ""
	}
	if pe, ok := problems.As(err); ok {
		return pe.Type.Slug
	}
	return err.Error()
}

func TestCheckLicence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		v       Version
		refused bool
	}{
		{"open dataset", Version{Kind: KindDataset, Licence: "CC-BY-4.0", Payload: []byte(`{}`)}, false},
		{"unknown licence", Version{Kind: KindDataset, Licence: "NOASSERTION", Payload: []byte(`{}`)}, true},
		{"empty licence, payload names one", Version{Kind: KindBaseModel, Payload: []byte(`{"licence":"openmdw-1.1"}`)}, false},
		{"empty everywhere", Version{Kind: KindBaseModel, Payload: []byte(`{}`)}, true},
		{"NC training data", Version{Kind: KindDataset, Licence: "CC-BY-NC-4.0", Payload: []byte(`{}`)}, true},
		{"NC eval-only data", Version{Kind: KindDataset, Licence: "CC-BY-NC-4.0", Payload: []byte(`{"evalOnly":true}`)}, false},
		{"NC eval-only by tag", Version{Kind: KindDataset, Licence: "CC-BY-NC-SA-4.0", Tags: []string{"eval-only"}, Payload: []byte(`{}`)}, false},
		{"NC golden set", Version{Kind: KindGoldenSet, Licence: "CC-BY-NC-4.0", Payload: []byte(`{}`)}, false},
		{"NC base model", Version{Kind: KindBaseModel, Licence: "CC-BY-NC-4.0", Payload: []byte(`{}`)}, true},
		{"ND base model", Version{Kind: KindBaseModel, Licence: "CC-BY-ND-4.0", Payload: []byte(`{}`)}, true},
		{"research only model", Version{Kind: KindModel, Licence: "research-only", Payload: []byte(`{}`)}, true},
		{"one of two unknown", Version{Kind: KindDataset, Licence: "CC-BY-4.0 AND unknown", Payload: []byte(`{}`)}, true},
		{"auxiliary forbids outputs", Version{Kind: kindAuxiliary, Licence: "Apache-2.0", Payload: []byte(`{"outputsCommercialUse":false}`)}, true},
		{"auxiliary allows outputs", Version{Kind: kindAuxiliary, Licence: "Apache-2.0", Payload: []byte(`{"outputsCommercialUse":true}`)}, false},
		{"template has no licence to check", Version{Kind: KindTemplate, Payload: []byte(`{}`)}, false},
		{"normalizer internal", Version{Kind: KindNormalizer, Licence: "internal", Payload: []byte(`{}`)}, false},
		{"apache is not NC", Version{Kind: KindBaseModel, Licence: "Apache-2.0", Payload: []byte(`{}`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := problemType(CheckLicence(tc.v))
			if want := map[bool]string{true: "licence-forbids-adoption", false: ""}[tc.refused]; got != want {
				t.Fatalf("CheckLicence = %q, want %q", got, want)
			}
		})
	}
}

func TestLocales(t *testing.T) {
	v := Version{Tags: []string{"locale:he-IL", "domain:x"}, Payload: []byte(`{"locales":["he-IL","en"],"locale":"ar","languages":[{"language":"ru"}]}`)}
	if got, want := Locales(v), []string{"he-IL", "en", "ar", "ru"}; !slices.Equal(got, want) {
		t.Fatalf("Locales = %v, want %v", got, want)
	}
	v = Version{Payload: []byte(`{"languages":["sr","hr"]}`)}
	if got, want := Locales(v), []string{"sr", "hr"}; !slices.Equal(got, want) {
		t.Fatalf("Locales = %v, want %v", got, want)
	}
}

func TestCheckLocale(t *testing.T) {
	ds := func(tags ...string) Version {
		return Version{Kind: KindDataset, Name: "dataset/x", Tags: tags, Payload: []byte(`{}`)}
	}
	for _, tc := range []struct {
		name    string
		v       Version
		project []string
		purpose string
		refused bool
	}{
		{"same language other region", ds("locale:he"), []string{"he-IL"}, "", false},
		{"other language", ds("locale:ru-RU"), []string{"he-IL"}, "", true},
		{"other language as replay", ds("locale:ru-RU"), []string{"he-IL"}, PurposeReplay, false},
		{"explicit target", ds("locale:ru-RU"), []string{"he-IL"}, PurposeTarget, true},
		{"declares none", ds(), []string{"he-IL"}, "", false},
		{"project has none", ds("locale:ru-RU"), nil, "", false},
		{"every locale normalizer", Version{Kind: KindNormalizer, Payload: []byte(`{"locale":"*"}`)}, []string{"sr-RS"}, "", false},
		{"base model is exempt", Version{Kind: KindBaseModel, Tags: []string{"locale:en"}, Payload: []byte(`{}`)}, []string{"sr-RS"}, "", false},
		{"script subtag", ds("locale:sr-Latn"), []string{"sr-RS"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := problemType(CheckLocale(tc.v, tc.project, tc.purpose))
			if want := map[bool]string{true: "locale-mismatch", false: ""}[tc.refused]; got != want {
				t.Fatalf("CheckLocale = %q, want %q", got, want)
			}
		})
	}
}

func TestCollectionName(t *testing.T) {
	for _, tc := range []struct{ kind, name, want string }{
		{KindDataset, "fleurs-he", "dataset/fleurs-he"},
		{KindDataset, "dataset/fleurs-he", "dataset/fleurs-he"},
		{KindGoldenSet, "calls", "golden-set/calls"},
		{"auxiliary-not-yet", "oasis", "oasis"},
	} {
		if got := CollectionName(tc.kind, tc.name); got != tc.want {
			t.Errorf("CollectionName(%s, %s) = %s, want %s", tc.kind, tc.name, got, tc.want)
		}
	}
}
