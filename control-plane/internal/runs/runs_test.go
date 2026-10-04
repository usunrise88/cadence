package runs

import (
	"encoding/json"
	"errors"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
)

func points(n int) ([]float64, []telemetry.Point) {
	t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	xs := make([]float64, n)
	pts := make([]telemetry.Point, n)
	for i := range n {
		step := int64(i + 1)
		xs[i] = float64(step)
		pts[i] = telemetry.Point{Name: "loss", Step: &step, Value: float64(i % 10), WallTime: t0.Add(time.Duration(i) * time.Second)}
	}
	return xs, pts
}

// The lease overhead is added after the spread: the ± covers the steps, not the fixed load and saves.
func TestSpreadAfter(t *testing.T) {
	tests := []struct {
		name                     string
		fixed, v, pm             float64
		digits                   int
		wantValue, wantLo, wantH float64
	}{
		{"no overhead", 0, 100, 0.1, 0, 100, 90, 110},
		{"overhead", 120, 2100, 0.2, 0, 2220, 1800, 2640},
		{"gpu hours", 120.0 / 3600, 100.0 / 3600, 0.1, 3, 0.061, 0.058, 0.064},
		{"spread above one", 10, 50, 1.5, 0, 60, 10, 135},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spreadAfter(tt.fixed, tt.v, tt.pm, tt.digits)
			if got != (Range{Value: tt.wantValue, Low: tt.wantLo, High: tt.wantH}) {
				t.Fatalf("spreadAfter = %+v", got)
			}
		})
	}
}

func TestBin(t *testing.T) {
	tests := []struct {
		name       string
		n, limit   int
		wantBinned bool
		wantLen    int
	}{
		{"short series stays raw", 5, 10, false, 5},
		{"exactly the limit stays raw", 10, 10, false, 10},
		{"long series binned", 1000, 10, true, 10},
		{"uneven buckets", 1001, 7, true, 7},
		{"empty", 0, 10, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xs, pts := points(tt.n)
			bins, binned := bin(xs, pts, tt.limit)
			if binned != tt.wantBinned || len(bins) != tt.wantLen {
				t.Fatalf("binned %v, %d bins; want %v, %d", binned, len(bins), tt.wantBinned, tt.wantLen)
			}
			total := 0
			for _, b := range bins {
				total += b.Count
				if b.Min > b.Value || b.Value > b.Max {
					t.Errorf("bin %+v: value outside [min, max]", b)
				}
			}
			if total != tt.n {
				t.Errorf("bins hold %d points, want %d", total, tt.n)
			}
		})
	}
	xs, pts := points(1000)
	bins, _ := bin(xs, pts, 10)
	if bins[0].Min != 0 || bins[0].Max != 9 || bins[0].Value != 4.5 || *bins[9].Step != 1000 {
		t.Errorf("first bin %+v, last step %d", bins[0], *bins[9].Step)
	}
}

func TestGPUClock(t *testing.T) {
	t0 := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	g := gpuClock{{t0, t0.Add(time.Hour)}, {t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}}
	tests := []struct {
		at   time.Time
		want float64
	}{
		{t0, 0},
		{t0.Add(30 * time.Minute), 0.5},
		{t0.Add(90 * time.Minute), 1}, // paused between leases: no GPU time
		{t0.Add(150 * time.Minute), 1.5},
		{t0.Add(5 * time.Hour), 2},
	}
	for _, tt := range tests {
		if got := g.at(tt.at, t0); got != tt.want {
			t.Errorf("at %s: %v, want %v", tt.at.Sub(t0), got, tt.want)
		}
	}
	if got := (gpuClock{}).at(t0.Add(2*time.Hour), t0); got != 2 {
		t.Errorf("without leases the wall hours stand in: %v", got)
	}
}

func TestInputsFor(t *testing.T) {
	base := steps.ArtifactRef{Hash: "b3:base", Type: TypeBaseModel}
	ckp := steps.ArtifactRef{Hash: "b3:ckp", Type: TypeCheckpoint}
	mix := RenderedMix{Artifact: steps.ArtifactRef{Hash: "b3:mix", Type: TypeMix},
		Datasets: []DatasetArtifact{{VersionID: "ver_1", Artifact: steps.ArtifactRef{Hash: "b3:ds", Type: TypeDataset}}}}
	two := mix
	two.Datasets = append(two.Datasets, DatasetArtifact{VersionID: "ver_2"})
	tests := []struct {
		name     string
		declared map[string]string
		s        start
		want     map[string]string // input → hash
		mismatch bool
	}{
		{"base and mix", map[string]string{"base": TypeBaseModel, "mix": TypeMix}, start{Base: base, Mix: mix},
			map[string]string{"base": "b3:base", "mix": "b3:mix"}, false},
		{"a checkpoint start feeds the base_model input", map[string]string{"base": TypeBaseModel}, start{Base: base, Checkpoint: &ckp, Mix: mix},
			map[string]string{"base": "b3:ckp"}, false},
		{"a single-dataset mix feeds a dataset input", map[string]string{"data": TypeDataset}, start{Mix: mix},
			map[string]string{"data": "b3:ds"}, false},
		{"two datasets cannot feed a dataset input", map[string]string{"data": TypeDataset}, start{Mix: two}, nil, true},
		{"a checkpoint input needs init checkpoint", map[string]string{"init": TypeCheckpoint}, start{Base: base, Mix: mix}, nil, true},
		{"an unknown type", map[string]string{"x": "hypotheses"}, start{Mix: mix}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inputsFor(tt.declared, tt.s)
			if tt.mismatch {
				if pe, ok := problems.As(err); !ok || pe.Type != problems.RecipeMismatch {
					t.Fatalf("err %v, want recipe-mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, hash := range tt.want {
				if got[name].Hash != hash {
					t.Errorf("%s = %s, want %s", name, got[name].Hash, hash)
				}
			}
		})
	}
}

func TestFamilyRoles(t *testing.T) {
	f := Family{Name: "some-family", Roles: map[string]string{RoleTrain: "k_train"}}
	if k, err := f.Role(RoleTrain); err != nil || k != "k_train" {
		t.Errorf("train role %q %v", k, err)
	}
	if _, err := f.Role(RoleAverage); err == nil {
		t.Error("a missing role must be family-unavailable")
	} else if pe, _ := problems.As(err); pe.Type != problems.FamilyUnavailable {
		t.Errorf("err %v", err)
	}
	if f.RoleOf("k_train") != RoleTrain || f.RoleOf("other") != "" {
		t.Error("RoleOf")
	}
}

// The dataset hook registers the artifact as a reference; older payloads carry the bare hash.
func TestDatasetArtifactDecode(t *testing.T) {
	const h = "b3:fb81404ff61b8bc411ce430604b36cd647e0e18c088a8fb0bf12b28e3926e275"
	for _, tc := range []struct{ name, payload string }{
		{"reference", `{"artifact":{"hash":"` + h + `","type":"dataset","size":346072156},"hours":3}`},
		{"hash", `{"artifact":"` + h + `","hours":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p struct {
				Artifact datasetArtifact `json:"artifact"`
			}
			if err := json.Unmarshal([]byte(tc.payload), &p); err != nil {
				t.Fatal(err)
			}
			if string(p.Artifact) != h {
				t.Fatalf("artifact %q", p.Artifact)
			}
		})
	}
	var p struct {
		Artifact datasetArtifact `json:"artifact"`
	}
	if err := json.Unmarshal([]byte(`{"artifact":42}`), &p); err == nil {
		t.Fatal("a number is not an artifact")
	}
}

func TestSharedParam(t *testing.T) {
	k := pipelines.Kind{Params: []byte(`{"properties":{
		"target_lang":{"type":"string","x-cadence":{"shared":true}},
		"warmup_steps":{"type":"integer","x-cadence":{"default":10}}}}`)}
	tests := []struct {
		name string
		want bool
	}{{"target_lang", true}, {"warmup_steps", false}, {"absent", false}}
	for _, tt := range tests {
		if got := sharedParam(k, tt.name); got != tt.want {
			t.Errorf("sharedParam(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckLanguages(t *testing.T) {
	tags := []string{"family:x", "locale:en", "locale:hr", "locale:ru"}
	tests := []struct {
		name   string
		tags   []string
		params map[string]any
		mix    []string
		want   string // a substring of the problem; "" = allowed
	}{
		{"the data's language is known", tags, map[string]any{"target_lang": ""}, []string{"ru-RU"}, ""},
		{"Serbian data, no override", tags, map[string]any{"target_lang": ""}, []string{"sr-RS"}, "sr-RS, a language base does not know"},
		{"Serbian data trains as Croatian", tags, map[string]any{"target_lang": "hr-HR"}, []string{"sr-RS"}, ""},
		{"an override the model does not know", tags, map[string]any{"target_lang": "sr-RS"}, []string{"sr-RS"}, "sr-RS is not a language base knows"},
		{"a kind without a language parameter", tags, map[string]any{}, []string{"sr-RS"}, "set the train step's language parameter"},
		{"a base model without locale tags", []string{"family:x"}, map[string]any{}, []string{"sr-RS"}, ""},
		{"Bokmål data on a Norwegian model", []string{"locale:no"}, map[string]any{"target_lang": ""}, []string{"nb-NO"}, ""},
		{"a Bokmål override on a Norwegian model", []string{"locale:no"}, map[string]any{"target_lang": "nb-NO"}, []string{"nb-NO"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckLanguages("base", tt.tags, tt.params, tt.mix)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(problemText(err), tt.want)):
				t.Fatalf("got %v, want a problem with %q", err, tt.want)
			}
		})
	}
}

func problemText(err error) string {
	var p *problems.Error
	if errors.As(err, &p) {
		b, _ := json.Marshal(p.Errors)
		return string(b)
	}
	return err.Error()
}
