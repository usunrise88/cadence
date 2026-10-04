//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The windows annotators and triage show get their peaks before the first view (phase 4 tail): batches.new queues
// media.peaks for its items' windows, and the triage items a segments artifact indexes get theirs from a hook after
// the triage hook; the first peaks.get of an item then reads stored peaks. The first-view computation stays the
// fallback (TestMediaPeaksStoredAtRegistration).
// dirSize is the bytes of a directory artifact's files.
func dirSize(t *testing.T, store *cas.Store, hash string) int64 {
	t.Helper()
	m, err := store.ReadManifest(hash)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

func TestMediaPeaksOfWindows(t *testing.T) {
	e := startMedia(t)
	ctx := context.Background()
	store := e.admin.CAS
	p := e.newProject("windows")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "calls"), 0o750); err != nil {
		t.Fatal(err)
	}
	callerTurns := [][2]float64{{1, 3}, {5, 7}, {9, 11}, {13, 15}}
	botTurns := [][2]float64{{3.5, 4.5}, {7.4, 8.5}, {11.2, 12.5}, {15.3, 16}}
	if err := os.WriteFile(filepath.Join(root, "calls", "c1.wav"), callWAV(17, callerTurns, botTurns), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, created_by) VALUES ('mnt_corpora', 'corpora', 'local', $1,
		'{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"calls-test","licence":"CC-BY-4.0","kind":"synthetic","languages":["sr-RS"]}`,
		"Idempotency-Key", e.key()), 201, nil)

	// A segments artifact of the call's caller turns, the frame of a batch.
	var rows []string
	for i, s := range callerTurns {
		h, size := blob(t, store, fmt.Sprintf("peaks-caller-%d", i))
		r := map[string]any{"uri": fmt.Sprintf("mount://corpora/calls/c1.wav#t=%g,%g&ch=0", s[0], s[1]), "file": "mount://corpora/calls/c1.wav",
			"hash": h, "bytes": size, "start": s[0], "end": s[1], "duration": s[1] - s[0], "channel": 0, "role": "caller", "language": "sr-RS",
			"speaker": "c1-caller", "text": fmt.Sprintf("rečenica broj %d", i), "origin": "pseudo-label", "confidence": 0.7,
			"vad": map[string]any{"speech": [][]float64{{0, s[1] - s[0]}}, "ratio": 1}, "sourceRate": 8000, "codec": "pcm_mulaw"}
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	}
	files, _ := json.Marshal(map[string]any{"uri": "mount://corpora/calls/c1.wav", "duration": 17, "sampleRate": 8000, "channels": 2,
		"roles": []string{"caller", "bot"}, "speech": [][][]float64{{{1, 3}, {5, 7}, {9, 11}, {13, 15}}, {{3.5, 4.5}, {7.4, 8.5}, {11.2, 12.5}, {15.3, 16}}}})
	segHash := putFiles(t, store, map[string][]byte{"segments.json": []byte(`{"format":"cadence.segments/1","source":{"name":"calls-test"},"language":"sr-RS"}`),
		"segments.jsonl": []byte(strings.Join(rows, "\n") + "\n"), "files.jsonl": append(files, '\n')})
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(ctx, tx, store, steps.ArtifactRef{Hash: segHash, Type: "segments", Size: dirSize(t, store, segHash)}, p.ID, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// batches.new queues media.peaks for the batch (a dry run queues nothing).
	body := `{"name":"calls-peaks","segments":"` + segHash + `","size":4,"doubleShare":0,"seed":3}`
	e.ok(e.do("POST", "/api/projects/windows/batches?dryRun=true", body, "Idempotency-Key", e.key()), 200, nil)
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media.peaks'`); n != 0 {
		t.Fatalf("a dry run queued %d media.peaks jobs", n)
	}
	var b struct{ ID string }
	e.ok(e.do("POST", "/api/projects/windows/batches", body, "Idempotency-Key", e.key()), 201, &b)
	var jobID string
	if err := e.pool.QueryRow(ctx, `SELECT job_id FROM media_jobs WHERE kind = 'media.peaks' AND subject = $1`, b.ID).Scan(&jobID); err != nil {
		t.Fatalf("no media.peaks job for the batch: %v", err)
	}
	j := e.endJob(jobID)
	var res media.PeaksResult
	if err := json.Unmarshal(j.Result, &res); j.State != "done" || err != nil || res.BatchID != b.ID || res.Utterances != 4 ||
		res.Stored+res.Present != 4 || res.Skipped != 0 {
		t.Fatalf("batch peaks job %s %s: %+v", j.State, j.Error, res)
	}
	var q struct{ Items []struct{ ID string } }
	e.ok(e.do("GET", "/api/batches/"+b.ID+"/batch-items?queue=all", ""), 200, &q)
	if len(q.Items) != 4 {
		t.Fatalf("items %+v", q.Items)
	}
	var pv peaksView
	e.ok(e.do("GET", "/api/registry/utterances/"+q.Items[0].ID+"/peaks", ""), 200, &pv)
	if pv.Computed || pv.Artifact == "" || pv.Channels != 2 {
		t.Fatalf("an item's first view computed its peaks: %+v", peaksSummary(pv))
	}

	// A disputed segment indexed by the triage hook gets the peaks of its window from the hook after it.
	h, size := blob(t, store, "peaks-disputed")
	row, _ := json.Marshal(map[string]any{"uri": "mount://corpora/calls/c1.wav#t=5,7&ch=0", "hash": h, "bytes": size, "start": 5, "end": 7,
		"duration": 2, "channel": 0, "role": "caller", "language": "sr-RS", "text": "dobar dan", "origin": "pseudo-label:disputed",
		"dispute": map[string]any{"reason": "disagreement", "candidates": []map[string]any{{"member": "a", "text": "dobar dan"}, {"member": "b", "text": "dobro dan"}}}})
	disputed := putFiles(t, store, map[string][]byte{"segments.json": []byte(`{"format":"cadence.segments/1","source":{"name":"calls-test"}}`),
		"segments.jsonl": append(row, '\n')})
	out := steps.Output{ProjectID: p.ID, PipelineRunID: "plr_peaks", StepID: "pls_ensemble", Name: "segments",
		Artifact: steps.ArtifactRef{Hash: disputed, Type: "segments", Size: dirSize(t, store, disputed), Meta: json.RawMessage(`{"disputed":1}`)}}
	for range 2 { // a reused output queues nothing again
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			drafts, err := e.admin.StepHooks.Run(ctx, tx, out)
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, auth.Actor{Kind: auth.KindAutomation, ID: "plr_peaks"}, nil, drafts)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media.peaks'`); n != 2 {
		t.Fatalf("%d media.peaks jobs, want the batch's and the triage items'", n)
	}
	if err := e.pool.QueryRow(ctx, `SELECT job_id FROM media_jobs WHERE kind = 'media.peaks' AND subject = $1`,
		"triage|"+p.ID+"|"+disputed).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	j = e.endJob(jobID)
	res = media.PeaksResult{}
	if err := json.Unmarshal(j.Result, &res); j.State != "done" || err != nil || res.Segments != disputed || res.Utterances != 1 ||
		res.Stored+res.Present != 1 || res.Skipped != 0 { // its window may be a batch item's too (present)
		t.Fatalf("triage peaks job %s %s: %+v", j.State, j.Error, res)
	}
	var tri string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM triage_items WHERE segments_hash = $1`, disputed).Scan(&tri); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+tri+"/peaks", ""), 200, &pv)
	if pv.Computed || pv.Artifact == "" {
		t.Fatalf("a triage item's first view computed its peaks: %+v", peaksSummary(pv))
	}
}
