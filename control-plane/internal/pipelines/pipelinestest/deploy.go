package pipelinestest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The deployment fixtures (phase 5 · stream D1): the fixture family's export and serve roles (fx_export writes a
// deployable directory with a model directory, a smoke client and deployable.json; fx_serve "serves" it by repeating
// each utterance's reference text, with serving timings whose chunk latency grows with the concurrency) and the
// neutral judges under their core names (parity_score compares texts, benchmark_score reads every level's p95). The
// parity reference is the family's transcribe fixture. A golden set for them is a directory dataset artifact
// (RegisterDirGoldenSet), as real golden sets are; the transcribe fixture reads both forms.
const (
	KindExport          = "fx_export"
	KindServe           = "fx_serve"
	KindParityScore     = "parity_score"
	KindBenchmarkScore  = "benchmark_score"
	FixtureFormat       = "fixture-dir"
	ServeLatencyPerConc = 2.0 // ms of chunk latency per concurrent stream
)

func strParam(name, def string) map[string]any {
	return map[string]any{"type": "string", "default": def, "x-cadence": xc(def, name, map[string]any{"maxLength": 100})}
}

func intParam(name string, def int) map[string]any {
	return map[string]any{"type": "integer", "default": def, "x-cadence": xc(def, name, map[string]any{"min": 0, "max": 100000})}
}

// DeployFixtures are the deployment fixture kinds as a worker publishes them.
var DeployFixtures = []map[string]any{
	{
		"name": KindExport, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "export",
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"profile": strParam("Latency profile", "offline"), "format": strParam("Format", FixtureFormat),
			"card_class": strParam("Card class", ""), "server_version": strParam("Server version", "1"),
		}},
		"consumes": map[string]string{"model": "checkpoint"}, "produces": map[string]string{"deployable": "deployable"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 4, "jobKind": "export"}, "help": "steps.fx-export",
	},
	{
		"name": KindServe, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "serve",
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"target": strParam("Target", ""), "profile": strParam("Latency profile", "offline"),
			"concurrency": intParam("Streams", 1), "pace": strParam("Pace", "fast"), "seconds": intParam("Seconds", 0),
			"warmup_seconds": intParam("Warm-up", 0), "target_lang": strParam("Language", ""),
		}},
		"consumes":  map[string]string{"deployable": "deployable", "data": "dataset"},
		"produces":  map[string]string{"hypotheses": "hypotheses", "timings": "serving_timings"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 4, "jobKind": "eval"}, "help": "steps.fx-serve",
	},
	neutral(KindParityScore, map[string]string{"reference": "hypotheses", "served": "hypotheses", "data": "dataset",
		"normalizer": "normalizer", "smoke": "smoke_inputs"}, map[string]string{"report": "parity_report"}, "smoke"),
	func() map[string]any {
		k := neutral(KindBenchmarkScore, map[string]string{"timings": "serving_timings"}, map[string]string{"report": "benchmark_report"})
		k["params"] = map[string]any{"type": "object", "properties": map[string]any{"target_streams": intParam("Target streams", 32)}}
		return k
	}(),
}

// RegisterDeploy publishes the deployment fixtures and a version of the fixture family that maps the export, parity
// and serve roles and declares its export format (call RegisterTraining and RegisterEvaluation first).
func RegisterDeploy(ctx context.Context, pool *pgxpool.Pool) error {
	if err := Register(ctx, pool, DeployFixtures...); err != nil {
		return err
	}
	fam := maps.Clone(Family)
	roles := maps.Clone(Family["roles"].(map[string]string))
	roles["export"], roles["parity"], roles["serve"] = KindExport, KindTranscribe, KindServe
	fam["roles"] = roles
	fam["exportFormats"] = []map[string]any{{"format": FixtureFormat, "server": "fixture", "default": true}}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(fam)
		_, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindModelFamily, Name: "model-family/" + FamilyName,
			Payload: b, Freeze: true, Actor: worker}, time.Now())
		return err
	})
}

// RegisterDirGoldenSet registers a golden set over a directory dataset artifact (dataset.json, manifest.jsonl and
// one audio blob per utterance, its content "audio <i> of <name>"), like goldenSets.freeze writes; it returns the
// golden set's version id and the audio hashes in manifest order.
func RegisterDirGoldenSet(ctx context.Context, pool *pgxpool.Pool, store *cas.Store, name, locale string, refs []string) (string, []string, error) {
	var files []cas.File
	var man bytes.Buffer
	var hashes []string
	for i, ref := range refs {
		b := []byte(fmt.Sprintf("audio %d of %s", i, name))
		h, err := store.PutBytes(b)
		if err != nil {
			return "", nil, err
		}
		path := fmt.Sprintf("audio/%03d.wav", i)
		files = append(files, cas.File{Path: path, Hash: h, Size: int64(len(b))})
		hashes = append(hashes, h)
		line, _ := json.Marshal(map[string]any{"audio": path, "text": ref, "duration": 2.0, "language": locale, "speaker": "spk", "split": "test"})
		man.Write(append(line, '\n'))
	}
	hdr, _ := json.Marshal(map[string]any{"format": "cadence.dataset/1", "name": name, "splitRule": "all-test",
		"counts": map[string]int{"test": len(refs)}, "hours": float64(len(refs)) * 2 / 3600, "evalOnly": true,
		"source": map[string]any{"name": name, "licence": "cc-by-4.0", "kind": "public", "languages": []string{locale}}})
	for _, f := range []struct {
		p string
		b []byte
	}{{"dataset.json", hdr}, {"manifest.jsonl", man.Bytes()}} {
		h, err := store.PutBytes(f.b)
		if err != nil {
			return "", nil, err
		}
		files = append(files, cas.File{Path: f.p, Hash: h, Size: int64(len(f.b))})
	}
	slices.SortFunc(files, func(a, b cas.File) int { return strings.Compare(a.Path, b.Path) })
	hash, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return "", nil, err
	}
	var id string
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		reg := func(kind, coll string, payload any) (registry.Version, error) {
			p, _ := json.Marshal(payload)
			v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: kind, Name: coll, Payload: p, Freeze: true, Actor: worker}, time.Now())
			return v, err
		}
		norm, err := reg(registry.KindNormalizer, "normalizer/fixture", map[string]any{"locale": "*", "unicode": "NFC", "casefold": true,
			"punctuation": "strip", "removeMarks": false, "mappings": []any{}, "numbers": "keep"})
		if err != nil {
			return err
		}
		art := steps.ArtifactRef{Hash: hash, Type: "dataset"}
		ds, err := reg(registry.KindDataset, "dataset/"+name, map[string]any{"hours": 0.01, "locales": []string{locale}, "artifact": art,
			"evalOnly": true, "utterances": len(refs)})
		if err != nil {
			return err
		}
		gs, err := reg(registry.KindGoldenSet, "golden-set/"+name, map[string]any{"datasetVersionId": ds.ID, "datasetHash": hash,
			"normalizerVersionId": norm.ID, "locale": locale, "domain": "read", "utterances": len(refs), "hours": 0.01,
			"fingerprint": ds.Fingerprint, "groups": "speaker"})
		id = gs.ID
		return err
	})
	return id, hashes, err
}

// datasetUtterances reads a dataset artifact as the fixtures know it: JSON lines of GoldenUtterance, or a directory
// (manifest.jsonl, each line's audio named by its blob hash).
func datasetUtterances(store *cas.Store, hash string) ([]GoldenUtterance, error) {
	m, err := store.ReadManifest(hash)
	if err != nil {
		return readLines[GoldenUtterance](store, hash)
	}
	byPath := map[string]string{}
	man := ""
	for _, f := range m.Files {
		byPath[f.Path] = f.Hash
		if f.Path == "manifest.jsonl" {
			man = f.Hash
		}
	}
	rows, err := readLines[struct {
		Audio, Text, Speaker string
		Duration             float64
	}](store, man)
	if err != nil {
		return nil, err
	}
	var out []GoldenUtterance
	for _, r := range rows {
		out = append(out, GoldenUtterance{Audio: byPath[r.Audio], Ref: r.Text, Speaker: r.Speaker, DurationS: r.Duration})
	}
	return out, nil
}

func (l *Leases) putDir(typ string, files map[string][]byte, meta map[string]any) (steps.ArtifactRef, error) {
	var list []cas.File
	size := int64(0)
	for _, p := range slices.Sorted(maps.Keys(files)) {
		h, err := l.CAS.PutBytes(files[p])
		if err != nil {
			return steps.ArtifactRef{}, err
		}
		list = append(list, cas.File{Path: p, Hash: h, Size: int64(len(files[p]))})
		size += int64(len(files[p]))
	}
	h, err := l.CAS.PutManifest(cas.Manifest{Files: list})
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	m, _ := json.Marshal(meta)
	return steps.ArtifactRef{Hash: h, Type: typ, Size: size, Meta: m}, nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// runDeploy runs the deployment fixtures.
func (l *Leases) runDeploy(spec steps.Spec) (steps.Outcome, bool, error) {
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	fail := func(err error) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: err.Error()}}, true, nil
	}
	done := func(outs map[string]steps.ArtifactRef) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateDone, Outputs: outs}, true, nil
	}
	switch spec.Kind {
	case KindExport:
		config := []byte("platform: \"fixture\"\n")
		plan := []byte("engine of " + spec.Inputs["model"].Hash)
		files := []map[string]any{{"path": "1/model.plan", "sha256": sha(plan), "bytes": len(plan)},
			{"path": "config.pbtxt", "sha256": sha(config), "bytes": len(config)}}
		lines := fmt.Sprintf("%s  1/model.plan\n%s  config.pbtxt\n", sha(plan), sha(config))
		doc, _ := json.Marshal(map[string]any{"schema": "cadence.deployable/1", "format": params["format"], "family": FamilyName,
			"profile": params["profile"], "weightsHash": "b3:w", "precision": "fp32",
			"serving": map[string]any{"server": map[string]any{"kind": "fixture", "version": params["server_version"]}, "modelDir": "model",
				"memoryMb": 1000, "maxStreams": 64, "chunkMs": 80, "engine": map[string]any{"cardClass": params["card_class"]}},
			"smoke": map[string]any{"client": "client/transcribe"}, "files": files, "manifestSha256": sha([]byte(lines))})
		ref, err := l.putDir("deployable", map[string][]byte{"deployable.json": doc, "model/config.pbtxt": config,
			"model/1/model.plan": plan, "client/transcribe": []byte("#!/bin/sh\necho 1 2 3\n")}, map[string]any{"format": params["format"]})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"deployable": ref})
	case KindServe:
		utts, err := datasetUtterances(l.CAS, spec.Inputs["data"].Hash)
		if err != nil {
			return fail(err)
		}
		conc := 1.0
		if c, ok := params["concurrency"].(float64); ok {
			conc = c
		}
		var hyp, tim bytes.Buffer
		header, _ := json.Marshal(map[string]any{"schema": "cadence.serving-timings/1", "concurrency": conc, "chunkMs": 80,
			"pace": params["pace"], "target": params["target"], "server": map[string]any{"kind": "fixture", "version": "1"}, "cardClass": "fx"})
		tim.Write(append(header, '\n'))
		for i, u := range utts {
			line, _ := json.Marshal(map[string]any{"audio": u.Audio, "text": u.Ref})
			hyp.Write(append(line, '\n'))
			row, _ := json.Marshal(map[string]any{"type": "chunk", "stream": i, "audio": u.Audio, "index": 0, "availableMs": 0,
				"doneMs": conc * ServeLatencyPerConc, "last": true})
			tim.Write(append(row, '\n'))
		}
		hRef, err := l.put(hyp.Bytes(), "hypotheses", map[string]any{"served": true})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		tRef, err := l.put(tim.Bytes(), "serving_timings", map[string]any{"concurrency": conc})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"hypotheses": hRef, "timings": tRef})
	case KindParityScore:
		read := func(name string) (map[string]string, error) {
			rows, err := readLines[struct{ Audio, Text string }](l.CAS, spec.Inputs[name].Hash)
			out := map[string]string{}
			for _, r := range rows {
				out[r.Audio] = r.Text
			}
			return out, err
		}
		ref, err := read("reference")
		if err != nil {
			return fail(err)
		}
		srv, err := read("served")
		if err != nil {
			return fail(err)
		}
		same := 0
		var smoke []map[string]any
		files := map[string][]byte{}
		for _, a := range slices.Sorted(maps.Keys(ref)) {
			if srv[a] == ref[a] {
				same++
			}
			if len(smoke) < 2 {
				name := fmt.Sprintf("%02d.json", len(smoke)+1)
				files["smoke/"+name] = []byte(`{"chunks":[]}`)
				smoke = append(smoke, map[string]any{"name": name, "audio": a, "file": "smoke/" + name, "expected": srv[a]})
			}
		}
		share := float64(same) / float64(max(1, len(ref)))
		verdict := "failed"
		if share == 1 {
			verdict = "passed"
		}
		report, _ := json.Marshal(map[string]any{"schema": "cadence.parity/1", "verdict": verdict, "identicalShare": share,
			"werDelta": 0.0, "disagreement": 1 - share, "compared": "text", "utterances": len(ref),
			"smoke": map[string]any{"format": "fixture", "items": smoke}})
		files["report.json"] = report
		out, err := l.putDir("parity_report", files, map[string]any{"verdict": verdict})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"report": out})
	case KindBenchmarkScore:
		target := 32
		if t, ok := params["target_streams"].(float64); ok {
			target = int(t)
		}
		var levels []map[string]any
		var streams []int
		for _, name := range slices.Sorted(maps.Keys(spec.Inputs)) {
			f, err := l.CAS.Open(spec.Inputs[name].Hash)
			if err != nil {
				return fail(err)
			}
			b, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				return fail(err)
			}
			first, _, _ := bytes.Cut(b, []byte("\n"))
			var h struct{ Concurrency float64 }
			_ = json.Unmarshal(first, &h)
			n := int(h.Concurrency)
			streams = append(streams, n)
			p95 := h.Concurrency * ServeLatencyPerConc
			levels = append(levels, map[string]any{"streams": n, "chunkLatencyMs": map[string]any{"p95": p95},
				"timeToFinalMs": map[string]any{"p95": p95}, "withinBudget": p95 <= 100})
		}
		slices.SortFunc(levels, func(a, b map[string]any) int { return a["streams"].(int) - b["streams"].(int) })
		verdict := "inconclusive"
		if slices.Contains(streams, target) {
			verdict = "passed"
			if float64(target)*ServeLatencyPerConc > 100 {
				verdict = "failed"
			}
		}
		report, _ := json.Marshal(map[string]any{"schema": "cadence.benchmark/1", "targetStreams": target, "levels": levels,
			"maxStreamsWithinBudget": slices.Max(streams), "contended": false, "cardClass": "fx",
			"server": map[string]any{"kind": "fixture", "version": "1"}, "verdict": verdict})
		out, err := l.put(report, "benchmark_report", map[string]any{"verdict": verdict})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"report": out})
	}
	return steps.Outcome{}, false, nil
}
