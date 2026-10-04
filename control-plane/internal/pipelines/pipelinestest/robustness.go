package pipelinestest

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The robustness and streaming-metric fixtures (phase 3 stream R): the core kinds augment_dataset, entity_score and
// latency_score under their own names, and a VAD under a fixture name (the control plane finds a VAD by what it
// produces). The augment fixture marks every utterance with the profile's name (the transcribe fixture then drops
// one more word); the entity fixture counts the references' numbers a hypothesis repeats; the latency fixture reports
// fixed percentiles.
const (
	KindAugment = "augment_dataset"
	KindEntity  = "entity_score"
	KindLatency = "latency_score"
	KindVAD     = "fx_vad"

	LatencyP50 = 120.0
	LatencyP95 = 200.0
)

func neutral(name string, consumes, produces map[string]string, optional ...string) map[string]any {
	k := map[string]any{
		"name": name, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
		"params": map[string]any{"type": "object", "properties": map[string]any{}}, "consumes": consumes, "produces": produces,
		"resources": map[string]any{"gpu": false, "jobKind": "eval"}, "help": "steps." + strings.ReplaceAll(name, "_", "-"),
		"estimateSeconds": 5,
	}
	if len(optional) > 0 {
		k["optionalInputs"] = optional
	}
	return k
}

// RobustnessFixtures are the fixture kinds as a worker publishes them.
var RobustnessFixtures = []map[string]any{
	neutral(KindAugment, map[string]string{"data": "dataset", "profile": "augment_profile", "noise": "dataset"},
		map[string]string{"data": "dataset"}, "noise"),
	neutral(KindEntity, map[string]string{"hypotheses": "hypotheses", "data": "dataset", "itn": "itn"},
		map[string]string{"scores": "metric_scores"}),
	neutral(KindLatency, map[string]string{"hypotheses": "hypotheses", "data": "dataset", "vad": "vad"},
		map[string]string{"scores": "metric_scores"}),
	{
		"name": KindVAD, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
		"params":   map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes": map[string]string{"data": "dataset"}, "produces": map[string]string{"vad": "vad"},
		"resources": map[string]any{"gpu": false, "jobKind": "eval"}, "help": "steps.fx-vad", "estimateSeconds": 5,
	},
}

// RegisterRobustness publishes the robustness fixtures (with RegisterTraining and RegisterEvaluation).
func RegisterRobustness(ctx context.Context, pool *pgxpool.Pool) error {
	return Register(ctx, pool, RobustnessFixtures...)
}

func (l *Leases) put(b []byte, typ string, meta map[string]any) (steps.ArtifactRef, error) {
	h, err := l.CAS.PutBytes(b)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	m, _ := json.Marshal(meta)
	return steps.ArtifactRef{Hash: h, Type: typ, Size: int64(len(b)), Meta: m}, nil
}

// putMetric writes a metric_scores directory artifact.
func (l *Leases) putMetric(summary map[string]any) (steps.ArtifactRef, error) {
	sb, _ := json.Marshal(summary)
	var files []cas.File
	for _, f := range []struct {
		path string
		b    []byte
	}{{"summary.json", sb}, {"utterances.jsonl", []byte{}}} {
		h, err := l.CAS.PutBytes(f.b)
		if err != nil {
			return steps.ArtifactRef{}, err
		}
		files = append(files, cas.File{Path: f.path, Hash: h, Size: int64(len(f.b))})
	}
	h, err := l.CAS.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	return steps.ArtifactRef{Hash: h, Type: "metric_scores", Size: int64(len(sb))}, nil
}

func digits(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		if strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			out = append(out, w)
		}
	}
	return out
}

// runRobustness runs the robustness fixtures.
func (l *Leases) runRobustness(spec steps.Spec) (steps.Outcome, bool, error) {
	byType := map[string]steps.ArtifactRef{}
	for name, in := range spec.Inputs {
		if name == "noise" {
			continue
		}
		byType[in.Type] = in
	}
	fail := func(err error) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: err.Error()}}, true, nil
	}
	done := func(name string, ref steps.ArtifactRef) (steps.Outcome, bool, error) {
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{name: ref}}, true, nil
	}
	switch spec.Kind {
	case KindAugment:
		utts, err := readLines[GoldenUtterance](l.CAS, byType["dataset"].Hash)
		if err != nil {
			return fail(err)
		}
		var prof struct {
			Name string `json:"name"`
			Hash string `json:"hash"`
		}
		if f, err := l.CAS.Open(byType["augment_profile"].Hash); err == nil {
			_ = json.NewDecoder(f).Decode(&prof)
			_ = f.Close()
		}
		var b strings.Builder
		for _, u := range utts {
			u.Augment = prof.Name
			line, _ := json.Marshal(u)
			b.Write(line)
			b.WriteByte('\n')
		}
		ref, err := l.put([]byte(b.String()), "dataset", map[string]any{"purpose": "augmented", "profile": prof.Name, "profileHash": prof.Hash})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done("data", ref)
	case KindVAD:
		ref, err := l.put([]byte(`{"vad":{"kind":"fx_vad@1"}}`+"\n"), "vad", map[string]any{"kind": "fx_vad@1"})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done("vad", ref)
	case KindEntity:
		utts, err := readLines[GoldenUtterance](l.CAS, byType["dataset"].Hash)
		if err != nil {
			return fail(err)
		}
		hyps, err := readLines[struct {
			Text string `json:"text"`
		}](l.CAS, byType["hypotheses"].Hash)
		if err != nil {
			return fail(err)
		}
		ref, correct := 0, 0
		for i, u := range utts {
			want := digits(u.Ref)
			ref += len(want)
			if i < len(hyps) {
				got := digits(hyps[i].Text)
				for _, w := range want {
					for j, g := range got {
						if g == w {
							correct++
							got = append(got[:j], got[j+1:]...)
							break
						}
					}
				}
			}
		}
		summary := map[string]any{"schema": "cadence.metric-scores/1", "scorer": KindEntity + "@1", "metric": "entities", "available": ref > 0,
			"refEntities": ref, "hypEntities": correct, "correct": correct,
			"classes": []map[string]any{{"class": "number", "refEntities": ref, "hypEntities": correct, "correct": correct}}}
		if ref > 0 {
			summary["accuracy"] = float64(correct) / float64(ref)
		}
		out, err := l.putMetric(summary)
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done("scores", out)
	case KindLatency:
		out, err := l.putMetric(map[string]any{"schema": "cadence.metric-scores/1", "scorer": spec.KindRef(), "metric": "latency",
			"available": true, "pace": "simulated", "utteranceEnd": "vad", "measured": 6, "p50Ms": LatencyP50, "p95Ms": LatencyP95,
			"emission": emission(spec)})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return done("scores", out)
	}
	return steps.Outcome{}, false, nil
}
