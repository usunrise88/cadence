//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Long audio (phase 4 tail; R51, R52): the peaks of a dataset version's members are stored when the version is
// registered (media.peaks), so the first view reads them; long audio gets a multi-resolution peaks file whose overview
// is read from a coarse level; the spectrogram tile pyramid is built once, on the first spectrogram.get, by
// media.spectrogram, and a failed build is reported until media.tiles_retry_s has passed.

// startMedia starts a server whose job runner has the media job kinds (main registers them through RegisterJobs;
// tests build the server after the runner starts).
func startMedia(t *testing.T) *env {
	t.Helper()
	msvc := &media.Service{Builds: make(chan struct{}, 1)}
	return startWith(t, func(c *Config) { msvc.CAS = c.CAS }, func(pool *pgxpool.Pool, js *jobs.Service) {
		msvc.Pool = pool
		msvc.Register(js)
	})
}

// endJob waits for a job to end and returns it (whatever its state).
func (e *env) endJob(id string) jobs.Job {
	e.t.Helper()
	j, err := jobs.Wait(context.Background(), e.pool, id, 60*time.Second, 50*time.Millisecond)
	if err != nil {
		e.t.Fatalf("job %s: %v", id, err)
	}
	return j
}

type peaksView struct {
	Channels int     `json:"channels"`
	HopS     float64 `json:"hopS"`
	Frames   int     `json:"frames"`
	Start    float64 `json:"start"`
	Data     []byte  `json:"data"`
	Artifact string  `json:"artifact"`
	Computed bool    `json:"computed"`
}

// wavDataset writes a cadence.dataset/1 artifact of real WAVs (16-bit PCM at their rate) into store.
func wavDataset(t *testing.T, store *cas.Store, wavs map[string][]byte, durations map[string]float64) steps.ArtifactRef {
	t.Helper()
	h := fleursHeader("long-audio")
	h.Counts = map[string]int{}
	var (
		files []cas.File
		lines []string
	)
	for name, body := range wavs {
		hash, err := store.PutBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		path := "audio/" + name + ".wav"
		files = append(files, cas.File{Path: path, Hash: hash, Size: int64(len(body))})
		info, err := media.ReadInfo(strings.NewReader(string(body)), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		line := map[string]any{"audio": path, "duration": durations[name], "sampleRate": info.SampleRate, "language": "he-IL",
			"text": "שלום " + name, "origin": "human", "split": "train",
			"eou": map[string]any{"speechEnd": 1.5, "nextSpeech": 2.1, "gapS": 0.6}}
		b, _ := json.Marshal(line)
		lines = append(lines, string(b))
		h.Counts["train"]++
		h.Hours += durations[name] / 3600
	}
	for name, body := range map[string][]byte{data.ManifestFile: []byte(strings.Join(lines, "\n") + "\n"), data.HeaderFile: mustJSON(t, h)} {
		hash, err := store.PutBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, cas.File{Path: name, Hash: hash, Size: int64(len(body))})
	}
	hash, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return steps.ArtifactRef{Hash: hash, Type: data.ArtifactType, Size: 1234}
}

func TestMediaPeaksStoredAtRegistration(t *testing.T) {
	e := startMedia(t)
	short := media.EncodeWAV([][]float32{tone(2*16000, 16000, 300)}, 16000)
	long := media.EncodeWAV([][]float32{tone(12*60*8000, 8000, 440)}, 8000) // a 12-minute call channel at 8 kHz
	ref := wavDataset(t, e.admin.CAS, map[string][]byte{"short": short, "long": long}, map[string]float64{"short": 2, "long": 720})
	if err := e.runHook(ref, "plr_media", `{}`); err != nil {
		t.Fatal(err)
	}
	// Both test servers ran their hooks: one job for the version.
	var jobID string
	if err := e.pool.QueryRow(context.Background(), `SELECT job_id FROM media_jobs WHERE kind = 'media.peaks'`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media.peaks'`); n != 1 {
		t.Fatalf("%d media.peaks jobs", n)
	}
	j := e.endJob(jobID)
	var res media.PeaksResult
	if err := json.Unmarshal(j.Result, &res); j.State != "done" || err != nil || res.Utterances != 2 || res.Stored != 2 || res.Skipped != 0 {
		t.Fatalf("job %s %s: %+v", j.State, j.Error, res)
	}
	// Registering the same artifact again queues nothing more.
	if err := e.runHook(ref, "plr_media", `{}`); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media.peaks'`); n != 1 {
		t.Fatalf("%d media.peaks jobs after a second registration", n)
	}

	longHash := cas.Hash(long)
	var p peaksView
	e.ok(e.do("GET", "/api/registry/utterances/"+longHash+"/peaks", ""), 200, &p)
	if p.Computed || p.Artifact == "" || p.Frames != 72000 || p.HopS != 0.01 || len(p.Data) != 144000 {
		t.Fatalf("the first view computed the long peaks again: %+v", peaksSummary(p))
	}
	var meta struct {
		Format string
		Source string
		Levels []media.PeaksLevel
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT meta FROM artifacts WHERE hash = $1`, p.Artifact).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Format != "cadence.peaks/2" || meta.Source != "media.peaks" || len(meta.Levels) != 3 || meta.Levels[2].Factor != 256 {
		t.Fatalf("peaks meta %+v", meta)
	}
	// The overview of the whole call at 2.56 s per pair comes from the coarsest level.
	e.ok(e.do("GET", "/api/registry/utterances/"+longHash+"/peaks?hopMs=2560", ""), 200, &p)
	if p.Computed || p.Frames != 282 || p.HopS != 2.56 || len(p.Data) != 564 {
		t.Fatalf("overview %+v", peaksSummary(p))
	}
	// A span at 160 ms starts on the ×16 level's grid.
	e.ok(e.do("GET", "/api/registry/utterances/"+longHash+"/peaks?hopMs=160&start=100.05&end=110", ""), 200, &p)
	if p.Frames != 63 || p.Start != 100 {
		t.Fatalf("span %+v", peaksSummary(p))
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+cas.Hash(short)+"/peaks", ""), 200, &p)
	if p.Computed || p.Frames != 200 {
		t.Fatalf("short %+v", peaksSummary(p))
	}
	if n := e.count(`SELECT count(*) FROM artifacts WHERE type = 'peaks'`); n != 2 {
		t.Fatalf("%d peaks artifacts", n)
	}

	// The end-of-utterance record of each member is in the manifest the version points at; an utterance with no stored
	// peaks is computed on its first view (the fallback) and stored then.
	other, _ := mediaUtterance(t, e, "fallback", media.EncodeWAV([][]float32{tone(8000, 8000, 200)}, 8000), 8000, 1, 1)
	e.ok(e.do("GET", "/api/registry/utterances/"+other+"/peaks", ""), 200, &p)
	if !p.Computed || p.Artifact == "" {
		t.Fatalf("fallback %+v", peaksSummary(p))
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+other+"/peaks", ""), 200, &p)
	if p.Computed {
		t.Fatal("the fallback's peaks were not stored")
	}
}

func peaksSummary(p peaksView) string {
	return fmt.Sprintf("frames %d hop %v start %v bytes %d artifact %q computed %v", p.Frames, p.HopS, p.Start, len(p.Data), p.Artifact, p.Computed)
}

func TestMediaTilesBuiltOnDemand(t *testing.T) {
	e := startMedia(t)
	ctx := context.Background()
	// A 30 s stereo call at 8 kHz.
	call := media.EncodeWAV([][]float32{tone(30*8000, 8000, 440), tone(30*8000, 8000, 1000)}, 8000)
	id, hash := mediaUtterance(t, e, "longcall", call, 8000, 2, 30)

	// A tile with no pyramid is not-found; the manifest request starts the build and answers the job.
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/spectrogram?tile=c0/l0/0", ""), 404, "not-found")
	var acc, again struct{ JobID string }
	resp := e.ok(e.do("GET", "/api/registry/utterances/"+id+"/spectrogram", ""), 202, &acc)
	if acc.JobID == "" || resp.Header.Get("Retry-After") != "2" {
		t.Fatalf("accepted %+v %v", acc, resp.Header)
	}
	// A second request while it builds answers the same job (or, once it is done, the manifest).
	if resp := e.do("GET", "/api/registry/utterances/"+hash+"/spectrogram", ""); resp.StatusCode == 202 {
		if err := json.Unmarshal(readAll(t, resp), &again); err != nil || again.JobID != acc.JobID {
			t.Fatalf("a second request started another build: %s, %s", again.JobID, acc.JobID)
		}
	} else if b := readAll(t, resp); resp.StatusCode != 200 {
		t.Fatalf("second request: %d %s", resp.StatusCode, b)
	}
	if j := e.endJob(acc.JobID); j.State != "done" {
		t.Fatalf("build %s: %s", j.State, j.Error)
	}
	var m struct {
		Schema, Artifact, Audio, Settings string
		Channels, Bins, OriginSampleRate  int
		Narrowband                        bool
		Levels                            []struct{ Level, Frames, Tiles int }
		PeakDb                            []float64
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/spectrogram", ""), 200, &m)
	if m.Schema != media.TilesSchema || m.Audio != hash || m.Channels != 2 || m.Bins != 129 || m.OriginSampleRate != 8000 ||
		!m.Narrowband || m.Settings != media.TileSettingsOf(defaults.Get()).Key() || len(m.Levels) != 4 || m.Levels[0].Frames != 3001 ||
		len(m.PeakDb) != 2 {
		t.Fatalf("manifest %+v", m)
	}
	resp = e.do("GET", "/api/registry/utterances/"+id+"/spectrogram?tile=c1/l3/0", "")
	if b := readAll(t, resp); resp.StatusCode != 200 || len(b) != 129*512 {
		t.Fatalf("tile: %d, %d bytes", resp.StatusCode, len(b))
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media.spectrogram'`); n != 1 {
		t.Fatalf("%d builds", n)
	}
	if n := e.count(`SELECT count(*) FROM artifacts WHERE type = 'spectrogram_tiles' AND meta->>'source' = 'media.spectrogram'`); n != 1 {
		t.Fatalf("%d pyramids", n)
	}

	// A failed build is reported until media.tiles_retry_s has passed; then a request builds again.
	other, otherHash := mediaUtterance(t, e, "failed", media.EncodeWAV([][]float32{tone(8000, 8000, 300)}, 8000), 8000, 1, 1)
	var failed jobs.Job
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		failed, _, err = e.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: jobs.KindNoop, Args: map[string]any{"fail": "the mount went away"}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO media_jobs (kind, subject, job_id) VALUES ('media.spectrogram', $1, $2)`,
			otherHash+"|"+media.TileSettingsOf(defaults.Get()).Key(), failed.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if j := e.endJob(failed.ID); j.State != "failed" {
		t.Fatalf("the failing job %s", j.State)
	}
	if p := expectProblem(t, e.do("GET", "/api/registry/utterances/"+other+"/spectrogram", ""), 422, "media-tiles-failed"); !strings.Contains(p.Detail, failed.ID) {
		t.Fatalf("failure detail %q", p.Detail)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET finished_at = now() - interval '1 day' WHERE id = $1`, failed.ID); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+other+"/spectrogram", ""), 202, &acc)
	if acc.JobID == failed.ID {
		t.Fatal("the failed build was answered after the retry window")
	}
	if j := e.endJob(acc.JobID); j.State != "done" {
		t.Fatalf("rebuild %s: %s", j.State, j.Error)
	}
}
