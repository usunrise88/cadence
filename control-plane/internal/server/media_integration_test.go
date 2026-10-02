//go:build integration

package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Media (phase 3 · stream A, R25): audio spans as 16 kHz WAV with byte ranges, signed links bound to span and
// viewer, the audit of every play, peaks cached as an artifact, words marked against scores, the tile pyramid's
// manifest and tiles, and agents refused everywhere.

// mediaUtterance stores a WAV in the content store and registers an utterance for it.
func mediaUtterance(t *testing.T, e *env, name string, wav []byte, rate, channels int, dur float64) (id, hash string) {
	t.Helper()
	hash, err := e.admin.CAS.PutBytes(wav)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO sources (id, name, licence, kind, created_by)
		VALUES ('src_media', 'media-fixtures', 'CC-BY-4.0', 'public', '{"kind":"user","id":"usr_admin"}')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	id = "utt_" + name
	if _, err := e.pool.Exec(ctx, `INSERT INTO utterances (id, content_hash, source_id, duration_s, language, sample_rate,
		channels, bytes) VALUES ($1, $2, 'src_media', $3, 'he-IL', $4, $5, $6)`, id, hash, dur, rate, channels, len(wav)); err != nil {
		t.Fatal(err)
	}
	return id, hash
}

func tone(n, rate int, hz float64) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return x
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wavInfo(t *testing.T, b []byte) media.Info {
	t.Helper()
	info, err := media.ReadInfo(strings.NewReader(string(b)), int64(len(b)))
	if err != nil {
		t.Fatalf("not a WAV: %v", err)
	}
	return info
}

type mediaAudit struct {
	Operation string
	ActorID   string
	Detail    map[string]any
}

func mediaAudits(t *testing.T, e *env) []mediaAudit {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT operation, actor_id, coalesce(detail, '{}') FROM audit_log
		WHERE operation LIKE 'audio.%' ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (mediaAudit, error) {
		var a mediaAudit
		err := r.Scan(&a.Operation, &a.ActorID, &a.Detail)
		return a, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMediaAudio(t *testing.T) {
	e := start(t)
	// A 2 s stereo call at 8 kHz: left a 440 Hz tone, right a 1 kHz tone.
	call := media.EncodeWAV([][]float32{tone(16000, 8000, 440), tone(16000, 8000, 1000)}, 8000)
	callID, callHash := mediaUtterance(t, e, "call", call, 8000, 2, 2)
	// A canonical clip: 1 s, 16 kHz mono.
	clip := media.EncodeWAV([][]float32{tone(16000, 16000, 300)}, 16000)
	clipID, _ := mediaUtterance(t, e, "clip", clip, 16000, 1, 1)

	// The whole call: converted to 16 kHz, both channels.
	resp := e.do("GET", "/api/registry/utterances/"+callID+"/audio", "")
	body := readAll(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("whole call: %d %v", resp.StatusCode, resp.Header)
	}
	if info := wavInfo(t, body); info.SampleRate != 16000 || info.Channels != 2 || info.Frames() != 32000 {
		t.Fatalf("converted call %+v", info)
	}

	// A span of one channel, addressed by the audio's hash.
	resp = e.do("GET", "/api/registry/utterances/"+callHash+"/audio?channel=1&start=0.5&end=1.5", "")
	body = readAll(t, resp)
	if info := wavInfo(t, body); resp.StatusCode != 200 || info.Channels != 1 || info.Frames() != 16000 {
		t.Fatalf("span: %d %+v", resp.StatusCode, info)
	}

	// Byte ranges.
	resp = e.do("GET", "/api/registry/utterances/"+callID+"/audio?channel=0", "", "Range", "bytes=44-143")
	body = readAll(t, resp)
	if resp.StatusCode != 206 || len(body) != 100 || resp.Header.Get("Content-Range") != "bytes 44-143/64044" {
		t.Fatalf("range: %d %d %q", resp.StatusCode, len(body), resp.Header.Get("Content-Range"))
	}
	p := expectProblem(t, e.do("GET", "/api/registry/utterances/"+callID+"/audio?channel=0", "", "Range", "bytes=64044-"), 416, "range-not-satisfiable")
	if !strings.Contains(p.Detail, "64044") {
		t.Fatalf("416 detail %q", p.Detail)
	}

	// The canonical clip asked whole is the stored file itself.
	resp = e.do("GET", "/api/registry/utterances/"+clipID+"/audio", "")
	if body = readAll(t, resp); string(body) != string(clip) {
		t.Fatalf("canonical clip not served as stored (%d bytes vs %d)", len(body), len(clip))
	}

	// Bad spans and channels.
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+callID+"/audio?channel=2", ""), 400, "bad-request")
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+callID+"/audio?start=1.5&end=1", ""), 400, "bad-request")
	expectProblem(t, e.do("GET", "/api/registry/utterances/utt_missing/audio", ""), 404, "not-found")

	// Plays are audited: requests from byte 0, not other ranges (3 plays above: whole call, span, clip).
	plays := mediaAudits(t, e)
	if len(plays) != 3 || plays[0].ActorID != "usr_admin" || plays[1].Detail["channel"] != float64(1) ||
		plays[1].Detail["start"] != 0.5 || plays[1].Detail["end"] != 1.5 || plays[1].Detail["utteranceId"] != callID ||
		plays[1].Detail["via"] != "session" {
		t.Fatalf("audit %+v", plays)
	}

	// A signed link: bound to its span; altered or expired, refused.
	var link struct {
		URL         string  `json:"url"`
		ExpiresAt   string  `json:"expiresAt"`
		UtteranceID string  `json:"utteranceId"`
		DurationS   float64 `json:"durationS"`
		SampleRate  int     `json:"sampleRate"`
		Channels    int     `json:"channels"`
	}
	e.ok(e.do("POST", "/api/registry/utterances/"+callID+"/audio:sign", `{"channel":0,"start":0.25,"end":0.75}`,
		"Cadence-Client", "web"), 200, &link)
	if !strings.HasPrefix(link.URL, "/api/registry/utterances/"+callID+"/audio?") || link.Channels != 1 || link.SampleRate != 16000 ||
		link.DurationS != 2 {
		t.Fatalf("link %+v", link)
	}
	exp, _ := time.Parse(time.RFC3339, link.ExpiresAt)
	if d := time.Until(exp); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("link expires in %v (media.signed_link_ttl_s is 300 s)", d)
	}
	resp = e.do("GET", link.URL, "")
	if body = readAll(t, resp); resp.StatusCode != 200 || wavInfo(t, body).Frames() != 8000 {
		t.Fatalf("signed link: %d", resp.StatusCode)
	}
	u, _ := url.Parse(link.URL)
	q := u.Query()
	q.Set("end", "2")
	expectProblem(t, e.do("GET", u.Path+"?"+q.Encode(), ""), 403, "media-link-invalid")
	q = u.Query()
	q.Set("exp", fmt.Sprint(time.Now().Add(-time.Minute).Unix()))
	expectProblem(t, e.do("GET", u.Path+"?"+q.Encode(), ""), 403, "media-link-invalid")
	plays = mediaAudits(t, e)
	if len(plays) != 5 || plays[3].Operation != "audio.sign" || plays[3].Detail["start"] != 0.25 || plays[4].Detail["via"] != "link" {
		t.Fatalf("audit after link %+v", plays)
	}

	// Agents reach no media endpoint, with or without a link.
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/registry/utterances/" + callID + "/audio", ""},
		{"GET", link.URL, ""},
		{"POST", "/api/registry/utterances/" + callID + "/audio:sign", "{}"},
		{"GET", "/api/registry/utterances/" + callID + "/peaks", ""},
		{"GET", "/api/registry/utterances/" + callID + "/spectrogram", ""},
	} {
		expectProblem(t, e.agent(r.method, r.path, r.body), 403, "forbidden")
	}
	if n := len(mediaAudits(t, e)); n != 5 {
		t.Fatalf("an agent's refused request was audited as a play (%d rows)", n)
	}
}

// TestMediaSignedLinkWithoutSession: with authentication on, a signed link plays without a session and the play is
// the viewer's; the same path without a signature is 401.
func TestMediaSignedLinkWithoutSession(t *testing.T) {
	e := startWith(t, func(c *Config) { c.Actor = auth.Actor{} })
	id, _ := mediaUtterance(t, e, "clip", media.EncodeWAV([][]float32{tone(8000, 16000, 300)}, 16000), 16000, 1, 0.5)
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/audio", ""), 401, "unauthenticated")
	l := media.Link{Utterance: id, Viewer: "usr_reviewer", Expires: time.Now().Add(time.Minute).Unix()}
	resp := e.do("GET", "/api/registry/utterances/"+id+"/audio?"+e.admin.mediaLinks.Query(l).Encode(), "", "Range", "bytes=0-")
	if body := readAll(t, resp); resp.StatusCode != 206 || len(body) != 16044 {
		t.Fatalf("signed link without a session: %d %d", resp.StatusCode, len(body))
	}
	if plays := mediaAudits(t, e); len(plays) != 1 || plays[0].ActorID != "usr_reviewer" || plays[0].Detail["via"] != "link" {
		t.Fatalf("audit %+v", plays)
	}
	// A signature does not open any other path.
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/peaks?sig=x", ""), 401, "unauthenticated")
}

func TestMediaPeaksWordsTiles(t *testing.T) {
	e := start(t)
	// 1 s at 8 kHz, clipped from 0.5 s for 20 ms.
	x := tone(8000, 8000, 100)
	for i := 4000; i < 4160; i++ {
		x[i] = 1
	}
	id, hash := mediaUtterance(t, e, "peaks", media.EncodeWAV([][]float32{x}, 8000), 8000, 1, 1)

	type peaks struct {
		Channels         int     `json:"channels"`
		HopS             float64 `json:"hopS"`
		Frames           int     `json:"frames"`
		Start            float64 `json:"start"`
		Data             []byte  `json:"data"`
		Clipped          []int   `json:"clipped"`
		Artifact         string  `json:"artifact"`
		OriginSampleRate int     `json:"originSampleRate"`
	}
	var p1, p2, p3 peaks
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/peaks", ""), 200, &p1)
	if p1.Frames != 100 || p1.HopS != 0.01 || len(p1.Data) != 200 || p1.Artifact == "" || p1.OriginSampleRate != 8000 ||
		len(p1.Clipped) != 2 || p1.Clipped[0] != 50 {
		t.Fatalf("peaks %+v", p1)
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/peaks?hopMs=40&start=0.4&end=0.8", ""), 200, &p2)
	if p2.Frames != 10 || p2.HopS != 0.04 || p2.Start != 0.4 || p2.Artifact != p1.Artifact || len(p2.Clipped) != 1 || p2.Clipped[0] != 2 {
		t.Fatalf("pooled peaks %+v", p2)
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+hash+"/peaks", ""), 200, &p3)
	if p3.Artifact != p1.Artifact || e.count(`SELECT count(*) FROM artifacts WHERE type = 'peaks'`) != 1 {
		t.Fatalf("peaks were computed again: %s vs %s", p3.Artifact, p1.Artifact)
	}
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/peaks?hopMs=15", ""), 400, "bad-request")

	// Words: a hypotheses file artifact and a scores directory artifact of the same cell.
	record := func(ref steps.ArtifactRef) {
		t.Helper()
		if err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
			_, err := artifacts.Record(context.Background(), tx, e.admin.CAS, ref, "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	put := func(b []byte) string {
		t.Helper()
		h, err := e.admin.CAS.PutBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	hypLines := `{"audio":"b3:` + strings.Repeat("0", 64) + `","text":"other","words":[]}` + "\n" +
		`{"audio":"` + hash + `","text":"Shalom, olam uh","words":[{"word":"Shalom,","start":0.0,"end":0.3,"confidence":0.97},` +
		`{"word":"olam","start":0.35,"end":0.6,"confidence":0.6},{"word":"uh","start":0.7,"end":0.8,"confidence":0.2}],` +
		`"partials":[{"audioOffsetMs":160,"emitMs":20.5,"text":"sha"}]}` + "\n"
	hypHash := put([]byte(hypLines))
	record(steps.ArtifactRef{Hash: hypHash, Type: "hypotheses", Size: int64(len(hypLines))})
	scoreLine := `{"audio":"` + hash + `","ref":"shalom lekulam olamot","hyp":"shalom olam uh","refWords":3,"sub":1,"del":1,"ins":1,` +
		`"ops":[["=","shalom","shalom"],["D","lekulam",""],["S","olamot","olam"],["I","","uh"]]}` + "\n"
	summary := `{"schema":"cadence.scores/1"}`
	m := cas.Manifest{Files: []cas.File{
		{Path: "summary.json", Hash: put([]byte(summary)), Size: int64(len(summary))},
		{Path: "utterances.jsonl", Hash: put([]byte(scoreLine)), Size: int64(len(scoreLine))},
	}}
	scoresHash, err := e.admin.CAS.PutManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	record(steps.ArtifactRef{Hash: scoresHash, Type: "scores", Size: int64(len(summary) + len(scoreLine))})
	var words struct {
		Text    string `json:"text"`
		Ref     string `json:"ref"`
		Aligned bool   `json:"aligned"`
		Sub     int    `json:"sub"`
		Words   []struct {
			Word, Op, Ref string
			Start, End    float64
		} `json:"words"`
		Deletions []struct {
			Before int    `json:"before"`
			Ref    string `json:"ref"`
		} `json:"deletions"`
		Partials []struct {
			Text          string  `json:"text"`
			AudioOffsetMs float64 `json:"audioOffsetMs"`
		} `json:"partials"`
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/words?hypotheses="+hypHash+"&scores="+scoresHash, ""), 200, &words)
	if !words.Aligned || words.Sub != 1 || len(words.Words) != 3 || words.Words[1].Op != "S" || words.Words[1].Ref != "olamot" ||
		words.Words[2].Op != "I" || len(words.Deletions) != 1 || words.Deletions[0].Before != 1 || len(words.Partials) != 1 ||
		words.Partials[0].AudioOffsetMs != 160 {
		t.Fatalf("words %+v", words)
	}
	var plain struct {
		Aligned bool             `json:"aligned"`
		Words   []map[string]any `json:"words"`
		Ref     *string          `json:"ref"`
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/words?hypotheses="+hypHash, ""), 200, &plain)
	if plain.Aligned || plain.Words[1]["op"] != nil || plain.Ref != nil || len(plain.Words) != 3 {
		t.Fatalf("words without scores %+v", plain)
	}
	other, _ := mediaUtterance(t, e, "other", media.EncodeWAV([][]float32{tone(800, 8000, 100)}, 8000), 8000, 1, 0.1)
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+other+"/words?hypotheses="+hypHash, ""), 404, "not-found")

	// Tiles: none computed yet, then a spectrogram_tiles artifact of the audio.
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/spectrogram", ""), 404, "not-found")
	manifest := `{"schema":"cadence.spectrogram-tiles/1","sampleRate":16000,"originSampleRate":8000,"channels":1,"bins":129,` +
		`"binHz":31.25,"tileFrames":512,"encoding":{"floorDb":-120,"stepDb":0.5},"levels":[{"level":0,"hopS":0.01,"frames":101,"tiles":1}],` +
		`"peakDb":[-6.0],"durationS":1.0}`
	tile := make([]byte, 129*512)
	binary.LittleEndian.PutUint16(tile, 0xBEEF)
	tm := cas.Manifest{Files: []cas.File{
		{Path: "manifest.json", Hash: put([]byte(manifest)), Size: int64(len(manifest))},
		{Path: "c0/l0/0.u8", Hash: put(tile), Size: int64(len(tile))},
	}}
	tilesHash, err := e.admin.CAS.PutManifest(tm)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"audio": hash, "levels": 1})
	record(steps.ArtifactRef{Hash: tilesHash, Type: "spectrogram_tiles", Size: int64(len(manifest) + len(tile)), Meta: meta})
	var sm struct {
		Schema   string `json:"schema"`
		Artifact string `json:"artifact"`
		Audio    string `json:"audio"`
		Bins     int    `json:"bins"`
	}
	e.ok(e.do("GET", "/api/registry/utterances/"+id+"/spectrogram", ""), 200, &sm)
	if sm.Schema != "cadence.spectrogram-tiles/1" || sm.Artifact != tilesHash || sm.Audio != hash || sm.Bins != 129 {
		t.Fatalf("manifest %+v", sm)
	}
	resp := e.do("GET", "/api/registry/utterances/"+id+"/spectrogram?tile=c0/l0/0", "")
	if b := readAll(t, resp); resp.StatusCode != 200 || len(b) != len(tile) || b[0] != 0xEF {
		t.Fatalf("tile: %d %d", resp.StatusCode, len(b))
	}
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/spectrogram?tile=c0/l3/0", ""), 404, "not-found")
	expectProblem(t, e.do("GET", "/api/registry/utterances/"+id+"/spectrogram?tile=../x", ""), 422, "validation-failed")
}
