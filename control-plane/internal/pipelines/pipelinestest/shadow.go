package pipelinestest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The shadow replay fixtures (phase 5 · stream D4): the neutral kinds a night's replay runs, under their core names.
// sdp_ingest "indexes" each listed call as one caller segment of ShadowSegmentSeconds of a ShadowCallSeconds call;
// segments_cut writes a directory dataset of those segments (text "words of <file>", so fx_serve repeats it);
// shadow_score reports a fixed divergence and lists the first segment as the most divergent.
const (
	KindIngest           = "sdp_ingest"
	KindCut              = "segments_cut"
	KindShadowScore      = "shadow_score"
	ShadowCallSeconds    = 7 * 3600.0 // three calls are 21 h: past deploy.shadow_min_hours
	ShadowSegmentSeconds = 30.0
	ShadowDivergence     = 0.125
)

func listParam(name string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "default": []any{},
		"x-cadence": xc([]any{}, name, map[string]any{"maxLength": 20000})}
}

func numParam(name string, def float64) map[string]any {
	return map[string]any{"type": "number", "default": def, "x-cadence": xc(def, name, map[string]any{"min": 0, "max": 100000})}
}

// ShadowFixtures are the shadow replay fixture kinds as a worker publishes them.
var ShadowFixtures = []map[string]any{
	func() map[string]any {
		k := neutral(KindIngest, map[string]string{}, map[string]string{"segments": "segments"})
		k["version"] = "3"
		k["resources"] = map[string]any{"gpu": false, "jobKind": "data"}
		k["params"] = map[string]any{"type": "object", "properties": map[string]any{
			"source": strParam("Source", ""), "path": strParam("Path", ""), "files": listParam("Files"),
			"exclude": listParam("Exclude"), "channels": strParam("Channels", "auto"), "channel_roles": listParam("Roles"),
			"language": strParam("Language", ""), "max_hours": numParam("Hours", 0),
		}}
		return k
	}(),
	func() map[string]any {
		k := neutral(KindCut, map[string]string{"segments": "segments"}, map[string]string{"data": "dataset"})
		k["resources"] = map[string]any{"gpu": false, "jobKind": "data"}
		k["params"] = map[string]any{"type": "object", "properties": map[string]any{"which": strParam("Which", "unlabelled")}}
		return k
	}(),
	func() map[string]any {
		k := neutral(KindShadowScore, map[string]string{"segments": "segments", "candidate": "hypotheses", "current": "hypotheses"},
			map[string]string{"report": "shadow_report"})
		k["resources"] = map[string]any{"gpu": false, "jobKind": "shadow"}
		return k
	}(),
}

// RegisterShadow publishes the shadow replay fixtures (with RegisterTraining, RegisterEvaluation and RegisterDeploy).
func RegisterShadow(ctx context.Context, pool *pgxpool.Pool) error {
	return Register(ctx, pool, ShadowFixtures...)
}

type fxSegment struct {
	URI      string  `json:"uri"`
	File     string  `json:"file"`
	Hash     string  `json:"hash"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Duration float64 `json:"duration"`
	Channel  int     `json:"channel"`
	Role     string  `json:"role"`
}

func (l *Leases) dirFile(hash, path string) ([]byte, error) {
	m, err := l.CAS.ReadManifest(hash)
	if err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		if f.Path == path {
			r, err := l.CAS.Open(f.Hash)
			if err != nil {
				return nil, err
			}
			defer func() { _ = r.Close() }()
			return io.ReadAll(r)
		}
	}
	return nil, fmt.Errorf("%s has no %s", hash, path)
}

func (l *Leases) segments(hash string) ([]fxSegment, error) {
	b, err := l.dirFile(hash, "segments.jsonl")
	if err != nil {
		return nil, err
	}
	var out []fxSegment
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var s fxSegment
		if err := json.Unmarshal(line, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// runShadow runs the shadow replay fixtures.
func (l *Leases) runShadow(spec steps.Spec) (steps.Outcome, bool, error) {
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	fail := func(err error) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: err.Error()}}, true, nil
	}
	done := func(outs map[string]steps.ArtifactRef) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateDone, Outputs: outs}, true, nil
	}
	switch spec.Kind {
	case KindIngest:
		path, _ := params["path"].(string)
		files, _ := params["files"].([]any)
		if len(files) == 0 {
			return fail(fmt.Errorf("no files under %s", path))
		}
		var segs, calls bytes.Buffer
		for _, f := range files {
			rel, _ := f.(string)
			file := strings.TrimRight(path, "/") + "/" + rel
			h, err := l.CAS.PutBytes([]byte("segment of " + rel))
			if err != nil {
				return steps.Outcome{}, true, err
			}
			row, _ := json.Marshal(fxSegment{URI: fmt.Sprintf("%s#t=0,%g&ch=0", file, ShadowSegmentSeconds), File: file, Hash: h,
				End: ShadowSegmentSeconds, Duration: ShadowSegmentSeconds, Role: "caller"})
			segs.Write(append(row, '\n'))
			line, _ := json.Marshal(map[string]any{"uri": file, "duration": ShadowCallSeconds, "channels": 2})
			calls.Write(append(line, '\n'))
		}
		hdr, _ := json.Marshal(map[string]any{"format": "cadence.segments/1", "source": map[string]any{"name": params["source"]},
			"files": len(files), "counts": map[string]int{"segments": len(files)}})
		ref, err := l.putDir("segments", map[string][]byte{"segments.json": hdr, "segments.jsonl": segs.Bytes(), "files.jsonl": calls.Bytes()},
			map[string]any{"segments": len(files)})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"segments": ref})
	case KindCut:
		segs, err := l.segments(spec.Inputs["segments"].Hash)
		if err != nil {
			return fail(err)
		}
		files := map[string][]byte{}
		var man bytes.Buffer
		for i, s := range segs {
			r, err := l.CAS.Open(s.Hash)
			if err != nil {
				return fail(err)
			}
			b, _ := io.ReadAll(r)
			_ = r.Close()
			p := fmt.Sprintf("audio/%03d.wav", i)
			files[p] = b
			line, _ := json.Marshal(map[string]any{"audio": p, "text": "words of " + s.File, "duration": s.Duration})
			man.Write(append(line, '\n'))
		}
		files["manifest.jsonl"] = man.Bytes()
		files["dataset.json"] = []byte(`{"format":"cadence.dataset/1","purpose":"pseudo-label"}`)
		ref, err := l.putDir("dataset", files, map[string]any{"purpose": "pseudo-label"})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"data": ref})
	case KindShadowScore:
		segs, err := l.segments(spec.Inputs["segments"].Hash)
		if err != nil {
			return fail(err)
		}
		var callList, worst, rows []map[string]any
		for i, s := range segs {
			callList = append(callList, map[string]any{"call": s.File, "duration": ShadowCallSeconds, "segments": 1, "wer": 0.0})
			row := map[string]any{"audio": s.Hash, "uri": s.URI, "call": s.File, "start": s.Start, "end": s.End, "duration": s.Duration,
				"wer": 0.0, "errors": 0, "words": 3, "candidate": "words of " + s.File, "current": "words of " + s.File}
			if i == 0 {
				row["wer"], row["errors"], row["current"] = 0.5, 1, "other words"
				worst = append(worst, row)
			}
			rows = append(rows, row)
		}
		report, _ := json.Marshal(map[string]any{"schema": "cadence.shadow/1", "scorer": "shadow_score@1", "calls": len(segs),
			"hours": float64(len(segs)) * ShadowCallSeconds / 3600, "utterances": len(segs),
			"divergence": map[string]any{"wer": ShadowDivergence, "ci": []float64{0.1, 0.15}, "level": 0.95, "samples": 100},
			"confidence": map[string]any{"candidate": 0.9, "current": 0.8}, "callList": callList, "worst": worst})
		var lines bytes.Buffer
		for _, r := range rows {
			b, _ := json.Marshal(r)
			lines.Write(append(b, '\n'))
		}
		ref, err := l.putDir("shadow_report", map[string][]byte{"report.json": report, "segments.jsonl": lines.Bytes()},
			map[string]any{"schema": "cadence.shadow/1"})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done(map[string]steps.ArtifactRef{"report": ref})
	}
	return steps.Outcome{}, false, nil
}
