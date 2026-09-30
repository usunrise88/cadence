package mixes

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestComputePreview(t *testing.T) {
	meta := map[string]DatasetMeta{
		"ver_he":  {ID: "ver_he", Name: "dataset/fleurs-he-smoke", Locales: []string{"he-IL"}, Hours: 1.8},
		"ver_he2": {ID: "ver_he2", Name: "dataset/he-calls", Locales: []string{"he-IL"}, Hours: 0.2},
		"ver_ru":  {ID: "ver_ru", Name: "dataset/fleurs-ru-smoke", Locales: []string{"ru-RU"}, Hours: 0.9},
		"ver_bi":  {ID: "ver_bi", Name: "dataset/bilingual", Locales: []string{"he-IL", "en-US"}, Hours: 2},
	}
	type lang struct {
		locale       string
		hours, share float64
	}
	tests := []struct {
		name     string
		c        Content
		total    float64
		langs    []lang
		shares   []float64 // per group
		warnings int
	}{
		{
			name:   "one group",
			c:      Content{Temperature: 1, Groups: []Group{{Name: "target", Weight: 1, Datasets: []string{"ver_he", "ver_he2"}}}},
			total:  2,
			langs:  []lang{{"he-IL", 2, 1}},
			shares: []float64{1},
		},
		{
			name: "replay share",
			c: Content{Temperature: 1, ReplayShare: 0.15, Groups: []Group{
				{Name: "target", Weight: 1, Datasets: []string{"ver_he"}},
				{Name: "replay", Weight: 1, Replay: true, Datasets: []string{"ver_ru"}},
			}},
			total:  2.7,
			langs:  []lang{{"he-IL", 1.8, 0.85}, {"ru-RU", 0.9, 0.15}},
			shares: []float64{0.85, 0.15},
		},
		{
			name: "weights with temperature",
			// weights 4 and 1 at T=2: 4^0.5 = 2 and 1 → 2/3 and 1/3.
			c: Content{Temperature: 2, Groups: []Group{
				{Name: "a", Weight: 4, Datasets: []string{"ver_he"}},
				{Name: "b", Weight: 1, Datasets: []string{"ver_ru"}},
			}},
			total:  2.7,
			langs:  []lang{{"he-IL", 1.8, 0.6667}, {"ru-RU", 0.9, 0.3333}},
			shares: []float64{0.6667, 0.3333},
		},
		{
			name:   "a bilingual dataset splits its hours",
			c:      Content{Temperature: 1, Groups: []Group{{Name: "bi", Weight: 1, Datasets: []string{"ver_bi"}}}},
			total:  2,
			langs:  []lang{{"en-US", 1, 0.5}, {"he-IL", 1, 0.5}},
			shares: []float64{1},
		},
		{
			name: "replay group with share 0 and unknown dataset warn",
			c: Content{Temperature: 1, Groups: []Group{
				{Name: "target", Weight: 1, Datasets: []string{"ver_he", "ver_missing"}},
				{Name: "replay", Weight: 1, Replay: true, Datasets: []string{"ver_ru"}},
			}},
			total:    2.7,
			langs:    []lang{{"he-IL", 1.8, 1}, {"ru-RU", 0.9, 0}},
			shares:   []float64{1, 0},
			warnings: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ComputePreview(tt.c, meta)
			if !near(p.TotalHours, tt.total) || p.Basis != "metadata" {
				t.Errorf("total %v basis %s, want %v", p.TotalHours, p.Basis, tt.total)
			}
			if len(p.Languages) != len(tt.langs) {
				t.Fatalf("languages %+v", p.Languages)
			}
			for i, l := range tt.langs {
				got := p.Languages[i]
				if got.Locale != l.locale || !near(got.Hours, l.hours) || !near(got.Share, l.share) {
					t.Errorf("language %d = %+v, want %+v", i, got, l)
				}
			}
			for i, s := range tt.shares {
				if !near(p.Groups[i].Share, s) {
					t.Errorf("group %s share %v, want %v", p.Groups[i].Name, p.Groups[i].Share, s)
				}
			}
			if len(p.Warnings) != tt.warnings {
				t.Errorf("warnings %v, want %d", p.Warnings, tt.warnings)
			}
		})
	}
}

func TestEditInput(t *testing.T) {
	c := Content{Name: "he", Temperature: 1, ReplayShare: 0.15, Groups: []Group{{Name: "t", Weight: 2, Datasets: []string{"ver_a"}}}}
	w := 5.0
	in := c.Input().Apply(EditInput{Temperature: &w})
	if *in.Temperature != 5 || in.Name != "he" || *in.ReplayShare != 0.15 || len(in.Groups) != 1 || *in.Groups[0].Weight != 2 {
		t.Errorf("edit of temperature only: %+v", in)
	}
	groups := []GroupInput{{Name: "x", Datasets: []string{"ver_b"}}}
	in = c.Input().Apply(EditInput{Groups: &groups})
	if len(in.Groups) != 1 || in.Groups[0].Name != "x" || in.Groups[0].Weight != nil {
		t.Errorf("groups replace the whole list: %+v", in.Groups)
	}
}
