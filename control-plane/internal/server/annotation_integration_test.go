//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Phase 4 · stream A against the real control plane, signed in as people: a batch sampled from a segments artifact of
// stereo μ-law calls on a local mount, a reviewer invited to it (one batch, play without download, nothing else), blind
// double annotation and adjudication, the agreement, the freeze approval, the cut and the golden set with the
// guidelines commit and the inter-annotator WER in its card; the triage queue's resolutions.

// annFreezeKind is dataset_freeze@1 with the parameters a batch's draft carries.
var annFreezeKind = func() map[string]any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{"default": "", "description": desc, "source": "test", "range": "any"}}
	}
	return map[string]any{
		"name": "dataset_freeze", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"mode": map[string]any{"type": "string", "default": "draft", "x-cadence": map[string]any{"default": "draft",
				"description": "draft or cut", "source": "test", "range": map[string]any{"values": []string{"draft", "cut"}}}},
			"name": str("collection"), "description": str("description"), "draft_version": str("the draft"),
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []string{},
				"x-cadence": map[string]any{"default": []string{}, "description": "tags", "source": "test", "range": "any"}},
			"eval_only": map[string]any{"type": "boolean", "default": false, "x-cadence": map[string]any{"default": false,
				"description": "eval only", "source": "test", "range": map[string]any{"values": []bool{true, false}}}},
		}},
		"consumes": map[string]string{"segments": "segments"}, "produces": map[string]string{"dataset": "dataset"},
		"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.dataset-freeze",
	}
}()

// muEncode is G.711 μ-law encoding of a sample in [-1, 1].
func muEncode(x float64) byte {
	const bias, clip = 0x84, 32635
	s := int(math.Round(x * 32767))
	sign := 0
	if s < 0 {
		s, sign = -s, 0x80
	}
	s = min(s, clip) + bias
	exp := 7
	for mask := 0x4000; s&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mant := (s >> (exp + 3)) & 0x0F
	return ^byte(sign | exp<<4 | mant) //nolint:gosec // 8 bits by construction
}

// callWAV is a stereo 8 kHz μ-law call: tones where each channel speaks (start, end seconds), silence elsewhere.
func callWAV(seconds float64, caller, bot [][2]float64) []byte {
	frames := int(seconds * 8000)
	active := func(spans [][2]float64, t float64) bool {
		for _, s := range spans {
			if t >= s[0] && t < s[1] {
				return true
			}
		}
		return false
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+frames*2))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(7), uint16(2), uint32(8000), uint32(16000), uint16(2), uint16(8)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(frames*2))
	for i := range frames {
		t := float64(i) / 8000
		c, o := 0.0, 0.0
		if active(caller, t) {
			c = 0.3 * math.Sin(2*math.Pi*440*t)
		}
		if active(bot, t) {
			o = 0.3 * math.Sin(2*math.Pi*660*t)
		}
		b.WriteByte(muEncode(c))
		b.WriteByte(muEncode(o))
	}
	return b.Bytes()
}

type annBatch struct {
	ID         string
	Rev        int
	State      string
	Purpose    string
	Guidelines struct{ Name, Path, Commit string }
	Frame      struct {
		SegmentsHash string `json:"segmentsHash"`
		Source       string
		Segments     int
	}
	Strata   []struct{ Frame, Sampled int }
	Progress struct {
		Items, Pending, Agreed, Disputed, Adjudicated, Excluded, Annotations, DoubleItems, DoubleDone int
	}
	Agreement struct {
		Pairs, RefWords, Edits int
		IaaWer                 *float64 `json:"iaaWer"`
		Meets                  bool
	}
	Adjudication struct{ Queue int }
	EOU          struct {
		Items   int
		P50GapS *float64 `json:"p50GapS"`
	} `json:"eou"`
	Reviewers []struct{ ID, Name, Role string }
	CanFreeze struct {
		OK      bool
		Reasons []string
	} `json:"canFreeze"`
	Freeze *struct {
		PipelineRunID      string `json:"pipelineRunId"`
		DatasetVersionID   string `json:"datasetVersionId"`
		GoldenSetVersionID string `json:"goldenSetVersionId"`
		ApprovalID         string `json:"approvalId"`
		Error              string
	}
	Sample []annItem
}

type annItem struct {
	ID       string
	Position int
	State    string
	Rev      int
	Double   bool
	Required int
	Segment  struct {
		Hash    string
		Channel int
		Role    string
	}
	Window struct {
		Start, End float64
		Channels   int
	}
	Prefill     struct{ Text, Origin string }
	Context     struct{ Turns []struct{ Text, Role string } }
	EOU         *struct{ GapS *float64 } `json:"eou"`
	Annotations []struct {
		ID        string
		Status    string
		Text      string
		Annotator struct{ ID string }
	}
	WER   *float64 `json:"wer"`
	Final *struct{ Text string }
}

func TestAnnotationBatchToGoldenSet(t *testing.T) {
	clk := &clock{now: time.Now()}
	var store *cas.Store
	e := startWith(t, func(c *Config) {
		c.Actor = auth.Actor{}
		c.Credentials = credentials.NewStore(c.Pool, clk.Now, auth.PasswordParams{Time: 1, Memory: 1024, Threads: 1, SaltLen: 16, KeyLen: 32})
		c.LoginLimiter = auth.NewLimiter(time.Now, auth.DefaultLoginLimits()...)
		store = c.CAS
	})
	ctx := context.Background()
	admin := e.setup()
	adm := func(hdr ...string) []string { return web(admin, hdr...) }
	p := e.createProject(`{"slug":"calls","name":"calls"}`, "calls", adm()...)
	if err := pipelinestest.Register(ctx, e.pool, annFreezeKind); err != nil {
		t.Fatal(err)
	}

	// A local mount with one stereo μ-law call: the caller (channel 0) speaks four turns, the bot (1) answers.
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
		adm("Idempotency-Key", e.key())...), 201, nil)

	// The frame: a segments artifact as sdp_ingest and the ensemble write it.
	texts := []string{"dobar dan zovem se ana petrović i imam jedno pitanje o mom poslednjem računu",
		"broj mog ugovora je dvadeset tri četrdeset pet i sklopljen je prošle godine u novom sadu",
		"posle podne mi više odgovara jer sam ujutru na poslu do dva sata hvala vam",
		"ne treba mi operater sve sam razumeo hvala vam puno i prijatan dan doviđenja"}
	var rows []string
	hashes := map[string]string{}
	for i, s := range callerTurns {
		h, size := blob(t, store, fmt.Sprintf("caller-%d", i))
		hashes[h] = fmt.Sprintf("caller-%d", i)
		r := map[string]any{"uri": fmt.Sprintf("mount://corpora/calls/c1.wav#t=%g,%g&ch=0", s[0], s[1]), "file": "mount://corpora/calls/c1.wav",
			"hash": h, "bytes": size, "start": s[0], "end": s[1], "duration": s[1] - s[0], "channel": 0, "role": "caller", "language": "sr-RS",
			"speaker": "c1-caller", "text": texts[i] + " možda", "origin": "pseudo-label", "confidence": 0.7,
			"vad": map[string]any{"speech": [][]float64{{0, s[1] - s[0]}}, "ratio": 1}, "sourceRate": 8000, "codec": "pcm_mulaw"}
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	}
	for i, s := range botTurns {
		h, _ := blob(t, store, fmt.Sprintf("bot-%d", i))
		r := map[string]any{"uri": fmt.Sprintf("mount://corpora/calls/c1.wav#t=%g,%g&ch=1", s[0], s[1]), "hash": h, "start": s[0], "end": s[1],
			"duration": s[1] - s[0], "channel": 1, "role": "bot", "language": "sr-RS", "text": fmt.Sprintf("Bot rečenica %d.", i), "origin": "model:tts-script"}
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	}
	files, _ := json.Marshal(map[string]any{"uri": "mount://corpora/calls/c1.wav", "duration": 17, "sampleRate": 8000, "channels": 2,
		"roles": []string{"caller", "bot"}, "speech": [][][]float64{{{1, 3}, {5, 7}, {9, 11}, {13, 15}}, {{3.5, 4.5}, {7.4, 8.5}, {11.2, 12.5}, {15.3, 16}}}})
	segHash := putFiles(t, store, map[string][]byte{"segments.json": []byte(`{"format":"cadence.segments/1","source":{"name":"calls-test"},"language":"sr-RS","tags":["synthetic"]}`),
		"segments.jsonl": []byte(strings.Join(rows, "\n") + "\n"), "files.jsonl": append(files, '\n')})
	m, err := store.ReadManifest(segHash)
	if err != nil {
		t.Fatal(err)
	}
	segSize := int64(0)
	for _, f := range m.Files {
		segSize += f.Size
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(ctx, tx, store, steps.ArtifactRef{Hash: segHash, Type: "segments", Size: segSize}, p.ID, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// batches.new: a dry run shows the sample; the batch pins the bootstrapped guidelines.
	body := `{"name":"calls-golden","segments":"` + segHash + `","size":4,"doubleShare":0.5,"seed":3}`
	var dry annBatch
	e.ok(e.do("POST", "/api/projects/calls/batches?dryRun=true", body, adm("Idempotency-Key", e.key())...), 200, &dry)
	if len(dry.Sample) != 4 || dry.Frame.Segments != 4 || dry.Guidelines.Path != "annotation/guidelines/default.md" || dry.Guidelines.Commit == "" {
		t.Fatalf("dry run: %+v", dry)
	}
	if n := e.count("SELECT count(*) FROM annotation_batches"); n != 0 {
		t.Fatalf("a dry run wrote %d batches", n)
	}
	var b annBatch
	e.ok(e.do("POST", "/api/projects/calls/batches", body, adm("Idempotency-Key", e.key())...), 201, &b)
	if b.State != "open" || b.Purpose != "golden-set" || b.Progress.Items != 4 || b.Progress.DoubleItems != 2 || b.CanFreeze.OK {
		t.Fatalf("batch: %+v", b)
	}
	expectProblem(t, e.do("POST", "/api/projects/calls/batches", body, adm("Idempotency-Key", e.key())...), 409, "conflict")

	// The admin's queue: every item, the next one named; items carry their window, context and gap.
	var q struct {
		Items []annItem
		Next  string
	}
	e.ok(e.do("GET", "/api/batches/"+b.ID+"/batch-items?queue=all", "", adm()...), 200, &q)
	if len(q.Items) != 4 {
		t.Fatalf("items %d", len(q.Items))
	}
	var doubles, singles []annItem
	for _, it := range q.Items {
		if it.Segment.Role != "caller" || it.Window.Channels != 2 || len(it.Context.Turns) == 0 || it.EOU == nil || it.EOU.GapS == nil ||
			!strings.HasSuffix(it.Prefill.Text, "možda") || it.Prefill.Origin != "pseudo-label" {
			t.Fatalf("item %+v", it)
		}
		if it.Double {
			doubles = append(doubles, it)
		} else {
			singles = append(singles, it)
		}
	}
	textOf := func(it annItem) string { return texts[mustIndex(t, hashes[it.Segment.Hash])] }

	// Agents never annotate (policy), and never hear audio.
	expectProblem(t, e.agent("POST", "/api/batches/"+b.ID+"/batch-items/"+q.Items[0].ID+"/annotations", `{"status":"done","text":"x"}`,
		"Idempotency-Key", e.key()), 403, "policy-denied")

	// A reviewer is invited; the link opens this batch only.
	var inv struct {
		ID, Token, URL string
		Reviewer       struct{ ID, Name string }
		ExpiresAt      time.Time
	}
	e.ok(e.do("POST", "/api/batches/"+b.ID+"/invitations", `{"name":"ana"}`, adm("Idempotency-Key", e.key())...), 201, &inv)
	if !strings.HasPrefix(inv.Token, "cri_") || inv.URL != "/#invitation="+inv.Token || inv.Reviewer.Name != "ana" || inv.ExpiresAt.After(time.Now().Add(15*24*time.Hour)) {
		t.Fatalf("invitation %+v", inv)
	}
	expectProblem(t, e.do("POST", "/api/auth:accept", `{"token":"cri_nothing-like-it"}`, auth.ClientHeader, auth.ClientWeb), 401, "invitation-invalid")
	resp := e.ok(e.do("POST", "/api/auth:accept", `{"token":"`+inv.Token+`"}`, auth.ClientHeader, auth.ClientWeb), 200, nil)
	rv := sessionCookie(t, resp).Value
	rev := func(hdr ...string) []string { return web(rv, hdr...) }
	var st struct {
		Actor    struct{ ID string }
		Reviewer struct{ BatchID, ProjectID, Role string } `json:"reviewer"`
	}
	e.ok(e.do("GET", "/api/auth", "", rev()...), 200, &st)
	if st.Actor.ID != inv.Reviewer.ID || st.Reviewer.BatchID != b.ID || st.Reviewer.ProjectID != p.ID || st.Reviewer.Role != "annotator" {
		t.Fatalf("reviewer status %+v", st)
	}
	expectProblem(t, e.do("GET", "/api/projects", "", rev()...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/projects/calls/batches", "", rev()...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/registry/datasets", "", rev()...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/batches/"+b.ID+"/batch-items?queue=all", "", rev()...), 403, "forbidden")
	e.ok(e.do("GET", "/api/batches/"+b.ID, "", rev()...), 200, nil)
	e.ok(e.do("GET", "/api/defaults", "", rev()...), 200, nil) // the audio view's settings

	// The reviewer plays an item's window through a signed link (every channel; the call's caller and bot), with its
	// peaks and tracks — narrowband, both parties' speech, the end-of-utterance gap.
	it0 := doubles[0]
	var link struct {
		URL, Audio string
		Channels   int
	}
	e.ok(e.do("POST", "/api/registry/utterances/"+it0.ID+"/audio:sign", `{}`, rev("Idempotency-Key", e.key())...), 200, &link)
	if link.Channels != 2 {
		t.Fatalf("link %+v", link)
	}
	wav := e.do("GET", link.URL, "")
	audio, _ := io.ReadAll(wav.Body)
	_ = wav.Body.Close()
	frames := int(math.Round((it0.Window.End - it0.Window.Start) * 16000))
	if wav.StatusCode != 200 || wav.Header.Get("Content-Type") != "audio/wav" || len(audio) != 44+frames*4 {
		t.Fatalf("audio: %d %s %d bytes, want %d", wav.StatusCode, wav.Header.Get("Content-Type"), len(audio), 44+frames*4)
	}
	var peaks struct{ Channels, Frames int }
	e.ok(e.do("GET", "/api/registry/utterances/"+it0.ID+"/peaks?hopMs=100", "", rev()...), 200, &peaks)
	if peaks.Channels != 2 || peaks.Frames < 10 {
		t.Errorf("peaks %+v", peaks)
	}
	var tracks struct {
		Narrowband bool
		Channels   []struct {
			Channel int
			Role    string
			Speech  [][]float64
		}
		EOU *struct{ GapS *float64 } `json:"eou"`
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+it0.ID+"/tracks", "", rev()...), 200, &tracks)
	if !tracks.Narrowband || len(tracks.Channels) != 2 || tracks.Channels[0].Role != "caller" || len(tracks.Channels[0].Speech) == 0 ||
		len(tracks.Channels[1].Speech) == 0 || tracks.EOU == nil || tracks.EOU.GapS == nil {
		t.Errorf("tracks %+v", tracks)
	}
	// Unknown items and the registry's utterances stay closed to a reviewer.
	expectProblem(t, e.do("GET", "/api/registry/utterances/bit_unknown/peaks", "", rev()...), 404, "not-found")
	expectProblem(t, e.do("GET", "/api/registry/utterances/utt_any/peaks", "", rev()...), 403, "forbidden")

	// Blind double annotation: the admin and the reviewer transcribe the double items; one pair differs by a word.
	annotate := func(hdr []string, it annItem, status, text string) annItem {
		t.Helper()
		var out annItem
		e.ok(e.do("POST", "/api/batches/"+b.ID+"/batch-items/"+it.ID+"/annotations", fmt.Sprintf(`{"status":%q,"text":%q}`, status, text),
			append(hdr, "Idempotency-Key", e.key())...), 201, &out)
		return out
	}
	annotate(adm(), doubles[0], "done", textOf(doubles[0]))
	t0 := textOf(doubles[0])
	got := annotate(rev(), doubles[0], "done", strings.ToUpper(t0[:1])+t0[1:]+".")
	if len(got.Annotations) != 1 || got.Final != nil || got.WER != nil {
		t.Errorf("a reviewer saw another's transcript: %+v", got)
	}
	annotate(adm(), doubles[1], "done", textOf(doubles[1]))
	words := strings.Fields(textOf(doubles[1]))
	words[1] = "nešto"
	annotate(rev(), doubles[1], "done", strings.Join(words, " "))
	// The reviewer cannot change a transcript once the other is in.
	expectProblem(t, e.do("POST", "/api/batches/"+b.ID+"/batch-items/"+doubles[1].ID+"/annotations", `{"status":"done","text":"x"}`,
		rev("Idempotency-Key", e.key())...), 409, "conflict")
	for _, it := range singles {
		annotate(adm(), it, "done", textOf(it))
	}
	e.ok(e.do("GET", "/api/batches/"+b.ID, "", adm()...), 200, &b)
	if b.Progress.Agreed != 3 || b.Progress.Disputed != 1 || b.Adjudication.Queue != 1 || b.Agreement.Pairs != 2 || b.CanFreeze.OK ||
		b.Agreement.IaaWer == nil || !b.Agreement.Meets || len(b.Reviewers) != 2 {
		t.Fatalf("batch after annotation: %+v", b)
	}
	expectProblem(t, e.do("POST", "/api/batches/"+b.ID+":freeze?dryRun=true", "", adm("Idempotency-Key", e.key(), "If-Match", fmt.Sprint(`"`, b.Rev, `"`))...), 409, "batch-incomplete")

	// Adjudication: the admin takes the first transcript; an annotator may not adjudicate.
	var disputed struct{ Items []annItem }
	e.ok(e.do("GET", "/api/batches/"+b.ID+"/batch-items?queue=adjudication", "", adm()...), 200, &disputed)
	if len(disputed.Items) != 1 || len(disputed.Items[0].Annotations) != 2 || disputed.Items[0].WER == nil {
		t.Fatalf("adjudication queue %+v", disputed)
	}
	d := disputed.Items[0]
	etag := fmt.Sprint(`"`, d.Rev, `"`)
	expectProblem(t, e.do("POST", "/api/batches/"+b.ID+"/batch-items/"+d.ID+":accept", `{"from":"`+d.Annotations[0].ID+`"}`,
		rev("Idempotency-Key", e.key(), "If-Match", etag)...), 403, "forbidden")
	e.ok(e.do("POST", "/api/batches/"+b.ID+"/batch-items/"+d.ID+":accept",
		`{"from":"`+d.Annotations[0].ID+`","entities":[{"start":0,"end":4,"class":"number"}]}`, adm("Idempotency-Key", e.key(), "If-Match", etag)...), 200, nil)
	e.ok(e.do("GET", "/api/batches/"+b.ID, "", adm()...), 200, &b)
	if !b.CanFreeze.OK || b.Progress.Adjudicated != 1 {
		t.Fatalf("batch ready to freeze: %+v", b)
	}

	// Freeze: dry run, then an approval the admin decides; the replay writes the draft and starts the cut.
	var fz struct {
		Items, Excluded  int
		IaaWer           *float64 `json:"iaaWer"`
		PipelineRunID    string   `json:"pipelineRunId"`
		DatasetVersionID string   `json:"datasetVersionId"`
		Batch            annBatch
	}
	ifm := fmt.Sprint(`"`, b.Rev, `"`)
	e.ok(e.do("POST", "/api/batches/"+b.ID+":freeze?dryRun=true", "", adm("Idempotency-Key", e.key(), "If-Match", ifm)...), 200, &fz)
	if fz.Items != 4 || fz.PipelineRunID != "" {
		t.Fatalf("freeze dry run %+v", fz)
	}
	e.useWorkers() // no worker: the cut stays queued until the test lands it
	var a accepted
	e.ok(e.do("POST", "/api/batches/"+b.ID+":freeze", "", adm("Idempotency-Key", e.key(), "If-Match", ifm)...), 202, &a)
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+a.ApprovalID+":approve", "{}", adm("Idempotency-Key", e.key(), "If-Match", `"1"`)...), 200, &decided)
	if decided.State != "approved" || decided.Result == nil || decided.Result.Status != 200 {
		t.Fatalf("decided %+v: %d %s", decided, decided.Result.Status, decided.Result.Body)
	}
	if err := json.Unmarshal(decided.Result.Body, &fz); err != nil {
		t.Fatal(err)
	}
	if fz.PipelineRunID == "" || fz.DatasetVersionID == "" || fz.Batch.State != "freezing" {
		t.Fatalf("freeze %+v", fz)
	}
	// The reviewer's session closed with the batch, and so did the links it signed (audit L1).
	expectProblem(t, e.do("GET", "/api/batches/"+b.ID, "", rev()...), 401, "unauthenticated")
	expectProblem(t, e.do("GET", link.URL, "", adm()...), 403, "media-link-invalid")
	expectProblem(t, e.do("POST", "/api/batches/"+b.ID+"/batch-items/"+singles[0].ID+"/annotations", `{"status":"skipped"}`,
		adm("Idempotency-Key", e.key())...), 409, "batch-closed")

	// The draft holds the human transcripts as eval-only test data.
	var draft struct {
		State   string
		Tags    []string
		Dataset struct {
			Utterances int
			Frozen     *bool
		}
	}
	e.ok(e.do("GET", "/api/registry/datasets/"+fz.DatasetVersionID, "", adm()...), 200, &draft)
	if draft.State != "draft" || draft.Dataset.Utterances != 4 || !strings.Contains(strings.Join(draft.Tags, ","), "eval-only") {
		t.Fatalf("draft %+v", draft)
	}

	// The cut lands: the batch's dataset version freezes and becomes golden-set/calls-golden with its card.
	var finals []map[string]any
	var rowsQ pgx.Rows
	rowsQ, err = e.pool.Query(ctx, "SELECT hash, final->>'text' FROM annotation_items WHERE batch_id = $1 ORDER BY position", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rowsQ.Next() {
		var h, txt string
		if err := rowsQ.Scan(&h, &txt); err != nil {
			t.Fatal(err)
		}
		finals = append(finals, map[string]any{"hash": h, "text": txt})
	}
	rowsQ.Close()
	var lines []string
	var audioFiles []cas.File
	for _, f := range finals {
		h := f["hash"].(string)
		path := "audio/" + h[3:5] + "/" + h[3:] + ".wav"
		_, size := blob(t, store, hashes[h])
		audioFiles = append(audioFiles, cas.File{Path: path, Hash: h, Size: size})
		l, _ := json.Marshal(map[string]any{"audio": path, "duration": 2.0, "sampleRate": 16000, "channels": 1, "language": "sr-RS",
			"text": f["text"], "origin": "human", "split": "test", "role": "caller", "speaker": "c1-caller"})
		lines = append(lines, string(l))
	}
	hdr, _ := json.Marshal(map[string]any{"format": data.FormatV1, "source": map[string]any{"name": "calls-test"}, "splitRule": "all-test",
		"counts": map[string]int{"test": 4}, "hours": 8.0 / 3600, "card": "card.md", "draftVersionId": fz.DatasetVersionID,
		"shards": []any{map[string]any{"index": 0, "cuts": "shards/cuts.000000.jsonl.gz", "utterances": 4, "bytes": 100, "seconds": 8}}})
	cutHash := putFiles(t, store, map[string][]byte{data.HeaderFile: hdr, data.ManifestFile: []byte(strings.Join(lines, "\n") + "\n"),
		"card.md": []byte("# cut\n"), "shards/cuts.000000.jsonl.gz": []byte("gz")}, audioFiles...)
	if err := e.runOutput(steps.Output{ProjectID: p.ID, PipelineRunID: fz.PipelineRunID, Name: "dataset",
		Artifact: steps.ArtifactRef{Hash: cutHash, Type: data.ArtifactType, Size: 1},
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1",
			Params: json.RawMessage(`{"mode":"cut","draft_version":"` + fz.DatasetVersionID + `"}`)}}); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("GET", "/api/batches/"+b.ID, "", adm()...), 200, &b)
	if b.State != "frozen" || b.Freeze == nil || b.Freeze.GoldenSetVersionID == "" || b.Freeze.ApprovalID != a.ApprovalID {
		t.Fatalf("frozen batch %+v (%+v)", b, b.Freeze)
	}
	var gs struct {
		Name      string
		Tags      []string
		GoldenSet struct {
			DatasetVersionID string `json:"datasetVersionId"`
			ApprovalID       string `json:"approvalId"`
			Annotation       struct {
				BatchID    string `json:"batchId"`
				Guidelines struct{ Path, Commit string }
				IaaWer     *float64 `json:"iaaWer"`
				Items      int
			}
		} `json:"goldenSet"`
	}
	e.ok(e.do("GET", "/api/registry/golden-sets/"+b.Freeze.GoldenSetVersionID, "", adm()...), 200, &gs)
	if gs.Name != "golden-set/calls-golden" || gs.GoldenSet.DatasetVersionID != fz.DatasetVersionID || gs.GoldenSet.ApprovalID != a.ApprovalID ||
		gs.GoldenSet.Annotation.BatchID != b.ID || gs.GoldenSet.Annotation.Guidelines.Commit != b.Guidelines.Commit ||
		gs.GoldenSet.Annotation.IaaWer == nil || gs.GoldenSet.Annotation.Items != 4 || !strings.Contains(strings.Join(gs.Tags, ","), "annotated") {
		t.Fatalf("golden set %+v", gs)
	}
	var types []string
	if err := e.pool.QueryRow(ctx, "SELECT array_agg(type ORDER BY seq) FROM events WHERE topic = $1", "entity.annotation_batch."+b.ID).Scan(&types); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(types, ","), "annotation_batch.freezing") || types[len(types)-1] != "annotation_batch.frozen" {
		t.Errorf("batch events %v", types)
	}
	expectProblem(t, e.do("POST", "/api/batches/"+b.ID+":freeze", "", adm("Idempotency-Key", e.key(), "If-Match", fmt.Sprint(`"`, b.Rev, `"`))...), 409, "batch-closed")
}

func mustIndex(t *testing.T, name string) int {
	t.Helper()
	var i int
	if _, err := fmt.Sscanf(name, "caller-%d", &i); err != nil {
		t.Fatalf("fixture %q: %v", name, err)
	}
	return i
}

func TestTriageResolutions(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	p := e.newProject("triage")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "c2.wav"), callWAV(6, [][2]float64{{1, 3}}, [][2]float64{{3.5, 5}}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, created_by) VALUES ('mnt_corpora', 'corpora', 'local', $1,
		'{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"calls-test","licence":"CC-BY-4.0","kind":"synthetic","languages":["sr-RS"]}`, "Idempotency-Key", e.key()), 201, nil)
	var ids []string
	var segsHash string
	for i := range 3 {
		h, size := fmt.Sprintf("b3:%064x", 7000+i), 64044 // indexed in place: no blob in the content store
		row, _ := json.Marshal(map[string]any{"uri": "mount://corpora/c2.wav#t=1,3&ch=0", "hash": h, "bytes": size, "start": 1, "end": 3,
			"duration": 2, "channel": 0, "role": "caller", "language": "sr-RS"})
		segsHash = putFiles(t, store, map[string][]byte{"segments.json": []byte(`{"format":"cadence.segments/1","source":{"name":"calls-test"}}`),
			"segments.jsonl": append(row, '\n')})
		id := fmt.Sprintf("tri_%d", i)
		if _, err := e.pool.Exec(ctx, `INSERT INTO triage_items (id, project_id, reason, pipeline_run_id, step_id, segments_hash, segment_hash,
			segment, candidates, best) VALUES ($1, $2, 'disagreement', 'plr_x', 'pls_x', $3, $4, $5, '[{"member":"a","text":"dobar dan"}]', 'dobar dan')`,
			id, p.ID, segsHash, h, string(row)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	type triView struct {
		State, Best string
		Rev         int
		Resolution  *struct {
			Text, Reason string
			UtteranceID  string `json:"utteranceId"`
			TranscriptID string `json:"transcriptId"`
		}
	}
	var tv triView
	// Agents never resolve (a human transcript is a person's).
	expectProblem(t, e.agent("POST", "/api/triage/"+ids[0]+":accept", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 403, "policy-denied")
	e.ok(e.do("POST", "/api/triage/"+ids[0]+":accept?dryRun=true", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &tv)
	if n := e.count("SELECT count(*) FROM transcripts WHERE origin = 'human'"); n != 0 {
		t.Fatalf("a dry run wrote %d transcripts", n)
	}
	e.ok(e.do("POST", "/api/triage/"+ids[0]+":accept", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &tv)
	if tv.State != "accepted" || tv.Resolution == nil || tv.Resolution.Text != "dobar dan" || tv.Resolution.UtteranceID == "" {
		t.Fatalf("accepted %+v", tv)
	}
	var txt, origin, uri string
	if err := e.pool.QueryRow(ctx, `SELECT t.text, t.origin, x.uri FROM transcripts t JOIN utterance_uris x ON x.utterance_id = t.utterance_id
		WHERE t.id = $1`, tv.Resolution.TranscriptID).Scan(&txt, &origin, &uri); err != nil {
		t.Fatal(err)
	}
	if txt != "dobar dan" || origin != "human" || uri != "mount://corpora/c2.wav#t=1,3&ch=0" {
		t.Errorf("transcript %q %q %q", txt, origin, uri)
	}
	expectProblem(t, e.do("POST", "/api/triage/"+ids[0]+":reject", "{}", "Idempotency-Key", e.key(), "If-Match", `"2"`), 409, "conflict")
	e.ok(e.do("POST", "/api/triage/"+ids[1]+":correct", `{"text":"Dobar dan, izvolite.","tags":["noise"]}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &tv)
	if tv.State != "corrected" || tv.Resolution.Text != "Dobar dan, izvolite." {
		t.Errorf("corrected %+v", tv)
	}
	expectProblem(t, e.do("POST", "/api/triage/"+ids[2]+":correct", `{"text":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"7"`), 412, "precondition-failed")
	tv = triView{}
	e.ok(e.do("POST", "/api/triage/"+ids[2]+":reject", `{"reason":"music only"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &tv)
	if tv.State != "rejected" || tv.Resolution.Reason != "music only" || tv.Resolution.UtteranceID != "" {
		t.Errorf("rejected %+v", tv)
	}
	var list struct{ Items []triView }
	e.ok(e.do("GET", "/api/projects/triage/triage?state=open", ""), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("open items %+v", list.Items)
	}

	// A triage item's window plays from the mount (the person's project), and the utterance a resolution created plays
	// in place too (its blob is not in the content store).
	resp := e.do("GET", "/api/registry/utterances/"+ids[1]+"/audio", "")
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(b) != 44+int(math.Round((5-0)*16000))*4 {
		t.Errorf("triage window: %d, %d bytes", resp.StatusCode, len(b))
	}
	var utt string
	if err := e.pool.QueryRow(ctx, "SELECT utterance_id FROM transcripts WHERE text = 'Dobar dan, izvolite.'").Scan(&utt); err != nil {
		t.Fatal(err)
	}
	resp = e.do("GET", "/api/registry/utterances/"+utt+"/audio", "")
	b, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(b) != 44+2*16000*2 {
		t.Errorf("an in-place utterance: %d, %d bytes", resp.StatusCode, len(b))
	}
}
