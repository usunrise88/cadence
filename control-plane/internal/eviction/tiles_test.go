package eviction

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClassifyTiles(t *testing.T) {
	const retention = 14 * 24 * time.Hour
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	art := func(hash string, ageDays int, ref string) tilesArtifact {
		return tilesArtifact{Candidate: Candidate{Hash: hash, Type: TypeTiles}, lastView: now.Add(-time.Duration(ageDays) * 24 * time.Hour), referenced: ref}
	}
	tests := []struct {
		name   string
		arts   []tilesArtifact
		evict  []string
		reason string // a substring of the first kept reason, or of the first evicted one
	}{
		{"a pyramid not viewed for longer than the retention goes", []tilesArtifact{art("b3:old", 15, "")}, []string{"b3:old"}, "the next view builds it again"},
		{"one viewed within the retention stays", []tilesArtifact{art("b3:new", 13, "")}, nil, "less than 14 days ago"},
		{"a reference keeps an old one", []tilesArtifact{art("b3:ref", 90, "a registry version references it")}, nil, "registry version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evict, kept := classifyTiles(tt.arts, retention, now)
			var got []string
			for _, c := range evict {
				got = append(got, c.Hash)
			}
			if !slices.Equal(got, tt.evict) {
				t.Fatalf("evict %v, want %v", got, tt.evict)
			}
			why := ""
			if len(kept) > 0 {
				why = kept[0].Reason
			} else if len(evict) > 0 {
				why = evict[0].Reason
			}
			if !strings.Contains(why, tt.reason) {
				t.Fatalf("reason %q, want %q", why, tt.reason)
			}
		})
	}
}
