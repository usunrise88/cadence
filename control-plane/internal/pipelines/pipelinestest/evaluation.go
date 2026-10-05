package pipelinestest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The evaluation fixtures: the fixture family's materialize and transcribe roles and a scorer named like the core
// kind (wer_score@1), so evals run end to end in-process. The transcribe fixture drops words: a materialised base
// model drops the first BaseDropWords words of every reference, a trained checkpoint none, and a boost list adds its
// first term to every hypothesis (an insertion); an augmented utterance (robustness.go) loses one more word.
const (
	KindMaterialize = "fx_materialize"
	KindTranscribe  = "fx_transcribe"
	KindScorer      = "wer_score"
	BaseDropWords   = 2
)

// EvaluationFixtures are the evaluation step kinds as a worker publishes them.
var EvaluationFixtures = []map[string]any{
	{
		"name": KindMaterialize, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "materialize",
		"params":   map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes": map[string]string{"base": "base_model"}, "produces": map[string]string{"checkpoint": "checkpoint"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 4, "jobKind": "eval"}, "help": "steps.fx-materialize",
		"estimateSeconds": 30,
	},
	{
		"name": KindTranscribe, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "transcribe",
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"profile":     map[string]any{"type": "string", "default": "offline", "x-cadence": xc("offline", "Latency profile", map[string]any{})},
			"target_lang": map[string]any{"type": "string", "default": "", "x-cadence": xc("", "Decoding language", map[string]any{})},
		}},
		"consumes": map[string]string{"model": "checkpoint", "data": "dataset", "boost": "boost_list"}, "optionalInputs": []string{"boost"},
		"produces":  map[string]string{"hypotheses": "hypotheses"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 8, "jobKind": "eval"}, "help": "steps.fx-transcribe",
	},
	{
		"name": KindScorer, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
		"params":    map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes":  map[string]string{"hypotheses": "hypotheses", "data": "dataset", "normalizer": "normalizer"},
		"produces":  map[string]string{"scores": "scores"},
		"resources": map[string]any{"gpu": false, "jobKind": "eval"}, "help": "steps.wer-score", "estimateSeconds": 5,
	},
}

// RegisterEvaluation publishes the evaluation fixtures (call RegisterTraining for the family and base model).
func RegisterEvaluation(ctx context.Context, pool *pgxpool.Pool) error {
	return Register(ctx, pool, EvaluationFixtures...)
}

// GoldenUtterance is one utterance of a fixture golden set.
type GoldenUtterance struct {
	Audio     string  `json:"audio"`
	Ref       string  `json:"ref"`
	Group     string  `json:"group,omitempty"`
	Speaker   string  `json:"speaker,omitempty"`
	DurationS float64 `json:"durationS"`
	Augment   string  `json:"augment,omitempty"` // set by the augment fixture: the transcribe fixture drops one more word
}

// RegisterGoldenSet registers a frozen normalizer (normalizer/fixture), an eval-only dataset version whose artifact
// is the utterances as JSON lines, and the golden set over them (registry kind golden_set, the payload goldenSets.freeze
// writes). It returns the golden set's version id.
func RegisterGoldenSet(ctx context.Context, pool *pgxpool.Pool, store *cas.Store, name, locale string, hours float64, utts []GoldenUtterance) (string, error) {
	var b strings.Builder
	for _, u := range utts {
		line, _ := json.Marshal(u)
		b.Write(line)
		b.WriteByte('\n')
	}
	hash, err := store.PutBytes([]byte(b.String()))
	if err != nil {
		return "", err
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
		art := steps.ArtifactRef{Hash: hash, Type: "dataset", Size: int64(b.Len())}
		ds, err := reg(registry.KindDataset, "dataset/"+name, map[string]any{"hours": hours, "locales": []string{locale}, "artifact": art,
			"evalOnly": true, "utterances": len(utts)})
		if err != nil {
			return err
		}
		gs, err := reg(registry.KindGoldenSet, "golden-set/"+name, map[string]any{"datasetVersionId": ds.ID, "datasetHash": hash,
			"normalizerVersionId": norm.ID, "locale": locale, "domain": "read", "utterances": len(utts), "hours": hours,
			"fingerprint": ds.Fingerprint, "groups": "speaker"})
		id = gs.ID
		return err
	})
	return id, err
}

// runEvaluation runs the evaluation fixtures.
func (l *Leases) runEvaluation(spec steps.Spec) (steps.Outcome, bool, error) {
	byType := map[string]steps.ArtifactRef{}
	for _, in := range spec.Inputs {
		byType[in.Type] = in
	}
	switch spec.Kind {
	case KindMaterialize:
		base := byType["base_model"]
		b, _ := json.Marshal(map[string]any{"family": FamilyName, "materialized": base.Hash})
		h, err := l.CAS.PutBytes(b)
		if err != nil {
			return steps.Outcome{}, true, err
		}
		meta, _ := json.Marshal(map[string]any{"family": FamilyName, "dropWords": BaseDropWords, "weightsHash": cas.Hash(b)})
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{
			"checkpoint": {Hash: h, Type: "checkpoint", Size: int64(len(b)), Meta: meta}}}, true, nil
	case KindTranscribe:
		var params map[string]any
		_ = json.Unmarshal(spec.Params, &params)
		var m struct {
			DropWords int `json:"dropWords"`
		}
		_ = json.Unmarshal(byType["checkpoint"].Meta, &m)
		utts, err := datasetUtterances(l.CAS, byType["dataset"].Hash)
		if err != nil {
			return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: err.Error()}}, true, nil
		}
		var boost struct {
			Terms []string `json:"terms"`
		}
		if ref, ok := byType["boost_list"]; ok {
			if f, err := l.CAS.Open(ref.Hash); err == nil {
				_ = json.NewDecoder(f).Decode(&boost)
				_ = f.Close()
			}
		}
		var b strings.Builder
		for _, u := range utts {
			words := strings.Fields(u.Ref)
			drop := m.DropWords
			if u.Augment != "" {
				drop++
			}
			words = words[min(drop, len(words)):]
			if len(boost.Terms) > 0 {
				words = append(words, boost.Terms[0])
			}
			line, _ := json.Marshal(map[string]any{"audio": u.Audio, "text": strings.Join(words, " "), "profile": params["profile"]})
			b.Write(line)
			b.WriteByte('\n')
		}
		h, err := l.CAS.PutBytes([]byte(b.String()))
		if err != nil {
			return steps.Outcome{}, true, err
		}
		meta, _ := json.Marshal(map[string]any{"profile": params["profile"], "utterances": len(utts)})
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{
			"hypotheses": {Hash: h, Type: "hypotheses", Size: int64(b.Len()), Meta: meta}}}, true, nil
	case KindScorer:
		ref, err := l.score(byType["dataset"].Hash, byType["hypotheses"].Hash)
		if err != nil {
			return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: err.Error()}}, true, nil
		}
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"scores": ref}}, true, nil
	}
	return steps.Outcome{}, false, nil
}

func readLines[T any](store *cas.Store, hash string) ([]T, error) {
	f, err := store.Open(hash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []T
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", hash, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// score writes a scores directory artifact (summary.json, utterances.jsonl) the way wer_score@1 does.
func (l *Leases) score(dataHash, hypHash string) (steps.ArtifactRef, error) {
	utts, err := readLines[GoldenUtterance](l.CAS, dataHash)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	hyps, err := readLines[struct {
		Audio string `json:"audio"`
		Text  string `json:"text"`
	}](l.CAS, hypHash)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	if len(hyps) != len(utts) {
		return steps.ArtifactRef{}, fmt.Errorf("%d hypotheses for %d utterances", len(hyps), len(utts))
	}
	var rows strings.Builder
	var words, sub, del, ins int
	for i, u := range utts {
		ref, hyp := strings.Fields(strings.ToLower(u.Ref)), strings.Fields(strings.ToLower(hyps[i].Text))
		ops, s, d, n := align(ref, hyp)
		group := u.Group
		if group == "" {
			group = u.Speaker
		}
		if group == "" {
			group = u.Audio
		}
		line, _ := json.Marshal(map[string]any{"audio": u.Audio, "speaker": u.Speaker, "group": group, "durationS": u.DurationS,
			"ref": strings.Join(ref, " "), "hyp": strings.Join(hyp, " "), "refWords": len(ref), "sub": s, "del": d, "ins": n, "ops": ops})
		rows.Write(line)
		rows.WriteByte('\n')
		words, sub, del, ins = words+len(ref), sub+s, del+d, ins+n
	}
	wer := 0.0
	if words > 0 {
		wer = float64(sub+del+ins) / float64(words)
	}
	summary, _ := json.Marshal(map[string]any{"schema": "cadence.scores/1", "scorer": KindScorer + "@1", "utterances": len(utts),
		"refWords": words, "wer": wer, "cer": wer, "werNoPunct": wer, "sub": sub, "del": del, "ins": ins,
		"buckets": []map[string]any{{"lo": 0, "utterances": len(utts), "refWords": words, "wer": wer}}})
	var files []cas.File
	var total int64
	for _, f := range []struct {
		path string
		b    []byte
	}{{"summary.json", summary}, {"utterances.jsonl", []byte(rows.String())}} {
		h, err := l.CAS.PutBytes(f.b)
		if err != nil {
			return steps.ArtifactRef{}, err
		}
		files = append(files, cas.File{Path: f.path, Hash: h, Size: int64(len(f.b))})
		total += int64(len(f.b))
	}
	h, err := l.CAS.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	meta, _ := json.Marshal(map[string]any{"wer": wer, "utterances": len(utts)})
	return steps.ArtifactRef{Hash: h, Type: "scores", Size: total, Meta: meta}, nil
}

// align is a word-level Levenshtein alignment: ops [op, ref, hyp] with op =, S, D or I, and the S, D, I counts.
func align(ref, hyp []string) ([][]any, int, int, int) {
	n, m := len(ref), len(hyp)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			c := 1
			if ref[i-1] == hyp[j-1] {
				c = 0
			}
			d[i][j] = min(d[i-1][j-1]+c, d[i-1][j]+1, d[i][j-1]+1)
		}
	}
	var ops [][]any
	s, del, ins := 0, 0, 0
	for i, j := n, m; i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && d[i][j] == d[i-1][j-1] && ref[i-1] == hyp[j-1]:
			ops = append(ops, []any{"=", ref[i-1], hyp[j-1]})
			i, j = i-1, j-1
		case i > 0 && j > 0 && d[i][j] == d[i-1][j-1]+1:
			ops = append(ops, []any{"S", ref[i-1], hyp[j-1]})
			s++
			i, j = i-1, j-1
		case i > 0 && d[i][j] == d[i-1][j]+1:
			ops = append(ops, []any{"D", ref[i-1], nil})
			del++
			i--
		default:
			ops = append(ops, []any{"I", nil, hyp[j-1]})
			ins++
			j--
		}
	}
	for a, b := 0, len(ops)-1; a < b; a, b = a+1, b-1 {
		ops[a], ops[b] = ops[b], ops[a]
	}
	if ops == nil {
		ops = [][]any{}
	}
	return ops, s, del, ins
}
