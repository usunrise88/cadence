//go:build integration

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/data"
)

// TestNoiseImportRegistersANoiseBank: a dataset artifact with purpose noise registers a frozen noise-bank version
// (noise-bank/<name>, spec 02 entity Noise bank) with its source and licence, and no utterances or transcripts; the
// same clips again are the same version.
func TestNoiseImportRegistersANoiseBank(t *testing.T) {
	e, store := startData(t)
	h := data.Header{Format: data.FormatV1, SplitRule: "all-train", Purpose: data.PurposeNoise, Tags: []string{"noise-bank"},
		Source: data.HeaderSource{Name: "musan", Licence: "CC-BY-4.0", Kind: "public", Languages: []string{"und"},
			URL: "https://www.openslr.org/17/"}}
	clips := []fixtureUtt{{"n1", "", "train", "und", ""}, {"n2", "", "train", "und", ""}}
	ref := artifact(t, store, h, clips)
	if err := e.runHook(ref, "plr_noise", `{"name":"musan-noise","purpose":"noise"}`); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Kind, Payload, Tags, Licence, State string
	}
	if err := e.pool.QueryRow(t.Context(), `SELECT c.kind, v.payload::text, array_to_string(c.tags, ','), c.licence, v.state
		FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id WHERE c.name = 'noise-bank/musan-noise'`).
		Scan(&v.Kind, &v.Payload, &v.Tags, &v.Licence, &v.State); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Clips    int
		Licence  string
		Source   string
		Artifact struct{ Hash string }
		Lineage  struct{ PipelineRunID string }
	}
	if err := json.Unmarshal([]byte(v.Payload), &p); err != nil {
		t.Fatal(err)
	}
	if v.Kind != "noise_bank" || v.State != "frozen" || v.Licence != "CC-BY-4.0" || v.Tags != "noise-bank,source:musan" ||
		p.Clips != 2 || p.Source != "https://www.openslr.org/17/" || p.Artifact.Hash != ref.Hash || p.Lineage.PipelineRunID != "plr_noise" {
		t.Fatalf("noise bank %+v payload %+v", v, p)
	}
	if n := e.count("SELECT count(*) FROM utterances") + e.count("SELECT count(*) FROM transcripts"); n != 0 {
		t.Errorf("a noise import wrote %d utterance and transcript rows", n)
	}
	if n := e.count("SELECT count(*) FROM sources WHERE name = 'musan'"); n != 1 {
		t.Errorf("musan source rows = %d", n)
	}
	if err := e.runHook(artifact(t, store, h, []fixtureUtt{clips[1], clips[0]}), "plr_noise2", `{"name":"musan-noise"}`); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT count(*) FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id WHERE c.kind = 'noise_bank'"); n != 1 {
		t.Errorf("re-importing the same clips made %d noise-bank versions", n)
	}
	var search struct{ Items []struct{ Kind, Name string } }
	e.ok(e.do("GET", "/api/registry?kind=noise_bank", ""), 200, &search)
	if len(search.Items) != 1 || !strings.HasPrefix(search.Items[0].Name, "noise-bank/") {
		t.Errorf("registry.search kind=noise_bank: %+v", search.Items)
	}
}
