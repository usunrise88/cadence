// Command seedannotation puts what an annotation batch frames into the e2e stack's throwaway database, for the
// Playwright spec of the annotation workflow (phase 4 · stream A): a local mount with one stereo 8 kHz μ-law call (the
// caller on channel 0 speaks four turns, the bot on channel 1 answers), the call's source, and a segments artifact as
// sdp_ingest and the pseudo-label ensemble write it, recorded in the project — what batches.new samples from. It is
// test tooling: the e2e stack script builds it next to the control plane; it never ships in the image.
//
//	DATABASE_URL=… CADENCE_CAS_DIR=… CADENCE_SEED_DIR=… seedannotation --project e2e-…
//
// It prints {"segments": "b3:…", "texts": [the caller's four turns, in order]} on stdout. Each project gets its own
// call file and segment hashes, so seeding several projects of one stack never collides.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// mountName is the mount the seeded calls live on; source names their source.
const (
	mountName = "calls-e2e"
	source    = "calls-e2e"
)

// texts are the caller's turns (the prefill adds a wrong last word, as a pseudo-label would).
var texts = []string{
	"dobar dan zovem se ana petrović i imam jedno pitanje o mom poslednjem računu",
	"broj mog ugovora je dvadeset tri četrdeset pet i sklopljen je prošle godine u novom sadu",
	"posle podne mi više odgovara jer sam ujutru na poslu do dva sata hvala vam",
	"ne treba mi operater sve sam razumeo hvala vam puno i prijatan dan doviđenja",
}

var (
	callerTurns = [][2]float64{{1, 3}, {5, 7}, {9, 11}, {13, 15}}
	botTurns    = [][2]float64{{3.5, 4.5}, {7.4, 8.5}, {11.2, 12.5}, {15.3, 16}}
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seedannotation:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seedannotation", flag.ContinueOnError)
	slug := fs.String("project", "", "the project the segments artifact is recorded in")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dsn, dir, seed := os.Getenv("DATABASE_URL"), os.Getenv("CADENCE_CAS_DIR"), os.Getenv("CADENCE_SEED_DIR")
	if dsn == "" || dir == "" || seed == "" || *slug == "" {
		return errors.New("DATABASE_URL, CADENCE_CAS_DIR, CADENCE_SEED_DIR and --project are required")
	}
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := cas.New(dir)
	if err != nil {
		return err
	}
	p, err := projects.Get(ctx, pool, *slug)
	if err != nil {
		return err
	}

	// The call on a local mount, its source.
	root := filepath.Join(seed, mountName)
	rel := "calls/" + *slug + ".wav"
	if err := os.MkdirAll(filepath.Join(root, "calls"), 0o750); err != nil { //nolint:gosec // test tooling: the stack names the directory
		return err
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), callWAV(17, callerTurns, botTurns), 0o600); err != nil { //nolint:gosec // test tooling: the stack names the directory
		return err
	}
	admin := auth.Actor{Kind: auth.KindUser, ID: "usr_admin", Name: "admin"}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, created_by) VALUES ('mnt_calls_e2e', $1, 'local', $2, $3)
			ON CONFLICT (name) DO NOTHING`, mountName, root, admin); err != nil {
			return fmt.Errorf("mount: %w", err)
		}
		_, _, _, err := data.Ensure(ctx, tx, data.SourceInput{Name: source, Licence: "CC-BY-4.0", Kind: "synthetic", Languages: []string{"sr-RS"}},
			admin, time.Now().UTC())
		return err
	}); err != nil {
		return err
	}

	// The frame: the caller's turns with a pseudo-label, the bot's with its script.
	uri := "mount://" + mountName + "/" + rel
	var rows []string
	for i, s := range callerTurns {
		h, size, err := blob(store, fmt.Sprintf("%s-caller-%d", *slug, i))
		if err != nil {
			return err
		}
		r := map[string]any{"uri": fmt.Sprintf("%s#t=%g,%g&ch=0", uri, s[0], s[1]), "file": uri, "hash": h, "bytes": size,
			"start": s[0], "end": s[1], "duration": s[1] - s[0], "channel": 0, "role": "caller", "language": "sr-RS",
			"speaker": "c1-caller", "text": texts[i] + " možda", "origin": "pseudo-label", "confidence": 0.7,
			"vad": map[string]any{"speech": [][]float64{{0, s[1] - s[0]}}, "ratio": 1}, "sourceRate": 8000, "codec": "pcm_mulaw"}
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	}
	for i, s := range botTurns {
		h, _, err := blob(store, fmt.Sprintf("%s-bot-%d", *slug, i))
		if err != nil {
			return err
		}
		r := map[string]any{"uri": fmt.Sprintf("%s#t=%g,%g&ch=1", uri, s[0], s[1]), "hash": h, "start": s[0], "end": s[1],
			"duration": s[1] - s[0], "channel": 1, "role": "bot", "language": "sr-RS", "text": fmt.Sprintf("Bot rečenica %d.", i),
			"origin": "model:tts-script"}
		b, _ := json.Marshal(r)
		rows = append(rows, string(b))
	}
	files, _ := json.Marshal(map[string]any{"uri": uri, "duration": 17, "sampleRate": 8000, "channels": 2,
		"roles": []string{"caller", "bot"}, "speech": [][][2]float64{callerTurns, botTurns}})
	header := `{"format":"cadence.segments/1","source":{"name":"` + source + `"},"language":"sr-RS","tags":["synthetic"]}`
	segHash, segSize, err := putFiles(store, map[string][]byte{"segments.json": []byte(header),
		"segments.jsonl": []byte(strings.Join(rows, "\n") + "\n"), "files.jsonl": append(files, '\n')})
	if err != nil {
		return err
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(ctx, tx, store, steps.ArtifactRef{Hash: segHash, Type: "segments", Size: segSize}, p.ID, nil)
		return err
	}); err != nil {
		return fmt.Errorf("record the segments: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"segments": segHash, "texts": texts})
}

// blob stands in for a segment's canonical audio in the content store (a batch item plays its window from the mount).
func blob(store *cas.Store, name string) (string, int64, error) {
	body := []byte("RIFF-fake-audio:" + name)
	h, err := store.PutBytes(body)
	return h, int64(len(body)), err
}

func putFiles(store *cas.Store, files map[string][]byte) (string, int64, error) {
	var (
		list []cas.File
		size int64
	)
	for name, body := range files {
		h, err := store.PutBytes(body)
		if err != nil {
			return "", 0, err
		}
		list = append(list, cas.File{Path: name, Hash: h, Size: int64(len(body))})
		size += int64(len(body))
	}
	h, err := store.PutManifest(cas.Manifest{Files: list})
	return h, size, err
}

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
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+frames*2)) //nolint:gosec // a 17-second fixture
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(7), uint16(2), uint32(8000), uint32(16000), uint16(2), uint16(8)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(frames*2)) //nolint:gosec // a 17-second fixture
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
