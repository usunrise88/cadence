package mixes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Preview is hours per language and the sampling shares of a mix, from dataset-version metadata only (the
// contract's MixPreview).
type Preview struct {
	TotalHours float64           `json:"totalHours"`
	Languages  []LanguagePreview `json:"languages"`
	Groups     []GroupPreview    `json:"groups"`
	Datasets   []DatasetMeta     `json:"datasets"`
	Warnings   []string          `json:"warnings"`
	Basis      string            `json:"basis"`
}

// LanguagePreview is one locale of a preview.
type LanguagePreview struct {
	Locale string  `json:"locale"`
	Hours  float64 `json:"hours"`
	Share  float64 `json:"share"`
}

// GroupPreview is one group of a preview.
type GroupPreview struct {
	Name    string   `json:"name"`
	Replay  bool     `json:"replay"`
	Weight  float64  `json:"weight"`
	Hours   float64  `json:"hours"`
	Share   float64  `json:"share"`
	Locales []string `json:"locales"`
}

// DatasetMeta is what a preview knows about one dataset version.
type DatasetMeta struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Locales []string `json:"locales"`
	Hours   float64  `json:"hours"` // train split, or the whole version when it has no train split
	Adopted bool     `json:"adopted"`
}

// datasetPayload is the part of a dataset version's payload a preview reads.
type datasetPayload struct {
	Locales []string `json:"locales"`
	Hours   float64  `json:"hours"`
	Splits  []struct {
		Name  string  `json:"name"`
		Hours float64 `json:"hours"`
	} `json:"splits"`
}

// LoadDatasets reads the metadata of the dataset versions c references, marking the ones the project adopted.
func LoadDatasets(ctx context.Context, q storage.Querier, projectID string, c Content) (map[string]DatasetMeta, error) {
	var ids []string
	for _, g := range c.Groups {
		ids = append(ids, g.Datasets...)
	}
	out := make(map[string]DatasetMeta, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	versions, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindDataset, IDs: ids})
	if err != nil {
		return nil, err
	}
	adoptions, err := registry.ListAdoptions(ctx, q, projectID, registry.KindDataset)
	if err != nil {
		return nil, err
	}
	adopted := map[string]bool{}
	for _, a := range adoptions {
		adopted[a.Version.ID] = true
	}
	for _, v := range versions {
		var p datasetPayload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode dataset version %s: %w", v.ID, err)
		}
		hours := p.Hours
		for _, s := range p.Splits {
			if s.Name == "train" {
				hours = s.Hours
			}
		}
		out[v.ID] = DatasetMeta{ID: v.ID, Name: v.Name, Version: v.Version, Locales: p.Locales, Hours: hours, Adopted: adopted[v.ID]}
	}
	return out, nil
}

// ComputePreview derives the preview of c from dataset metadata. A group is drawn with probability proportional
// to weight^(1/temperature) among the groups of its kind; replay groups together get replayShare of the samples
// (when the mix has any), the other groups the rest. Inside a group, samples follow the hours of its datasets; a
// dataset with several locales splits its hours evenly between them.
func ComputePreview(c Content, meta map[string]DatasetMeta) Preview {
	pv := Preview{Languages: []LanguagePreview{}, Groups: []GroupPreview{}, Datasets: []DatasetMeta{}, Warnings: []string{}, Basis: "metadata"}
	hasReplay := false
	for _, g := range c.Groups {
		hasReplay = hasReplay || g.Replay
	}
	t := c.Temperature
	if t <= 0 {
		t = 1
	}
	mass := map[bool]float64{} // replay → Σ weight^(1/T)
	for _, g := range c.Groups {
		mass[g.Replay] += math.Pow(g.Weight, 1/t)
	}
	part := map[bool]float64{false: 1}
	if hasReplay {
		part = map[bool]float64{false: 1 - c.ReplayShare, true: c.ReplayShare}
	}
	langHours, langShare := map[string]float64{}, map[string]float64{}
	for _, g := range c.Groups {
		gp := GroupPreview{Name: g.Name, Replay: g.Replay, Weight: g.Weight, Locales: []string{}}
		if mass[g.Replay] > 0 {
			gp.Share = part[g.Replay] * math.Pow(g.Weight, 1/t) / mass[g.Replay]
		}
		locHours := map[string]float64{}
		for _, id := range g.Datasets {
			m, ok := meta[id]
			if !ok {
				pv.Warnings = append(pv.Warnings, fmt.Sprintf("group %s: dataset version %s has no metadata", g.Name, id))
				continue
			}
			pv.Datasets = append(pv.Datasets, m)
			gp.Hours += m.Hours
			locales := m.Locales
			if len(locales) == 0 {
				locales = []string{"und"}
			}
			for _, l := range locales {
				locHours[l] += m.Hours / float64(len(locales))
			}
		}
		for l, h := range locHours {
			gp.Locales = append(gp.Locales, l)
			langHours[l] += h
			if gp.Hours > 0 {
				langShare[l] += gp.Share * h / gp.Hours
			}
		}
		sort.Strings(gp.Locales)
		switch {
		case gp.Hours == 0:
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("group %s has no training hours", g.Name))
		case gp.Share == 0:
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("group %s gets no samples (replay share is 0)", g.Name))
		}
		pv.TotalHours += gp.Hours
		pv.Groups = append(pv.Groups, gp)
	}
	for l, h := range langHours {
		pv.Languages = append(pv.Languages, LanguagePreview{Locale: l, Hours: round(h), Share: round(langShare[l])})
	}
	sort.Slice(pv.Languages, func(i, j int) bool {
		if pv.Languages[i].Share != pv.Languages[j].Share {
			return pv.Languages[i].Share > pv.Languages[j].Share
		}
		return pv.Languages[i].Locale < pv.Languages[j].Locale
	})
	for i := range pv.Groups {
		pv.Groups[i].Hours, pv.Groups[i].Share = round(pv.Groups[i].Hours), round(pv.Groups[i].Share)
	}
	pv.TotalHours = round(pv.TotalHours)
	return pv
}

// round keeps four decimals, so shares and hours read the same in every client.
func round(x float64) float64 { return math.Round(x*1e4) / 1e4 }
