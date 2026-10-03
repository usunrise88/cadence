package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Freezing a draft (datasets.freeze) and previewing a version (datasets.preview).

// FreezePlan is what datasets.freeze would do with a dataset version.
type FreezePlan struct {
	Version    registry.Version
	Frozen     bool   // the version is frozen already: nothing to do
	Running    string // a freeze pipeline run that is still running
	ProjectID  string // the project the draft was ingested in, where the freeze runs
	Kind       string // the draft step's kind@version, run again in mode cut
	Params     map[string]any
	Segments   steps.ArtifactRef
	GoldenSets int // golden sets the leakage check covered
}

// PlanFreeze checks that version id is a draft that can be frozen: it came from an ingest (it knows its segments,
// recipe step and project) and shares nothing with a golden set (golden-set-leakage). A frozen version comes back
// with Frozen set; a freeze still running with Running set.
func PlanFreeze(ctx context.Context, q storage.Querier, id string) (FreezePlan, error) {
	v, err := registry.GetVersion(ctx, q, registry.KindDataset, id)
	if err != nil {
		return FreezePlan{}, err
	}
	fp := FreezePlan{Version: v}
	if err := q.QueryRow(ctx, "SELECT count(*) FROM golden_sets").Scan(&fp.GoldenSets); err != nil {
		return FreezePlan{}, fmt.Errorf("count golden sets: %w", err)
	}
	if v.State != registry.StateDraft {
		fp.Frozen = true
		return fp, nil
	}
	var p payload
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return FreezePlan{}, fmt.Errorf("decode dataset %s: %w", id, err)
	}
	if p.Recipe == nil || p.Recipe.StepKind == "" || p.Segments == nil || p.Recipe.ProjectID == "" {
		return FreezePlan{}, problems.BadRequest.New("%s %s is a draft without an ingest recipe (segments, step kind, project); only drafts from pipelines/data-ingest are frozen with datasets.freeze",
			v.Name, v.Version)
	}
	fp.ProjectID, fp.Kind, fp.Segments = p.Recipe.ProjectID, p.Recipe.StepKind, *p.Segments
	fp.Params = map[string]any{}
	if len(p.Recipe.Params) > 0 {
		if err := json.Unmarshal(p.Recipe.Params, &fp.Params); err != nil {
			return FreezePlan{}, fmt.Errorf("decode the draft step's parameters: %w", err)
		}
	}
	fp.Params["mode"], fp.Params["draft_version"] = "cut", v.ID
	if p.Freeze != nil && p.Freeze.PipelineRunID != "" {
		var state string
		err := q.QueryRow(ctx, "SELECT state FROM pipeline_runs WHERE id = $1", p.Freeze.PipelineRunID).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return FreezePlan{}, fmt.Errorf("read the freeze pipeline run: %w", err)
		}
		if state == "running" {
			fp.Running = p.Freeze.PipelineRunID
			return fp, nil
		}
	}
	if err := NotGolden(ctx, q, v); err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.GoldenSetLeakage {
			pe.Detail = strings.Replace(pe.Detail, "can never be trained on; re-freeze it without those utterances",
				"cannot be frozen; filter those utterances out of the ingest (or re-split) and freeze the new draft", 1)
		}
		return FreezePlan{}, err
	}
	return fp, nil
}

// MarkFreezing records on the draft that pipeline run runID is freezing it.
func MarkFreezing(ctx context.Context, tx pgx.Tx, id, runID string, actor auth.Actor, now time.Time) error {
	v, err := registry.GetVersion(ctx, tx, registry.KindDataset, id)
	if err != nil {
		return err
	}
	if v.State != registry.StateDraft {
		return nil // frozen inside the start (a cut reused by its input hash)
	}
	var p payload
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return fmt.Errorf("decode dataset %s: %w", id, err)
	}
	p.Freeze = &freezeState{PipelineRunID: runID, StartedAt: &now, Actor: &actor}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode dataset payload: %w", err)
	}
	canonical, err := registry.Canonical(body)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE registry_versions SET payload = $2 WHERE id = $1 AND state = 'draft'", id, canonical); err != nil {
		return fmt.Errorf("mark %s freezing: %w", id, err)
	}
	return nil
}

// PreviewFilter narrows datasets.preview; zero fields do not filter.
type PreviewFilter struct {
	MinDuration, MaxDuration             float64
	MinCharsPerSecond, MaxCharsPerSecond float64
	Languages                            []string
	Origins                              []string
	Splits                               []string
}

// PreviewCell is the kept utterances and hours of one language and split.
type PreviewCell struct {
	Language   string  `json:"language"`
	Split      string  `json:"split"`
	Utterances int     `json:"utterances"`
	Hours      float64 `json:"hours"`
	Speakers   int     `json:"speakers"`
}

// Preview is datasets.preview's answer.
type Preview struct {
	VersionID  string         `json:"versionId"`
	Frozen     bool           `json:"frozen"`
	Utterances int            `json:"utterances"`
	Hours      float64        `json:"hours"`
	Cells      []PreviewCell  `json:"cells"`
	Dropped    map[string]int `json:"dropped"`
}

// Reasons a preview filter drops an utterance (the first that applies counts).
const (
	DropDuration       = "duration"
	DropCharsPerSecond = "charsPerSecond"
	DropLanguage       = "language"
	DropOrigin         = "origin"
	DropSplit          = "split"
)

// PreviewVersion reports the utterances and hours per language and split of version id after f, and how many each
// filter drops. It reads the memberships and transcripts only.
func PreviewVersion(ctx context.Context, q storage.Querier, id string, f PreviewFilter) (Preview, error) {
	v, err := registry.GetVersion(ctx, q, registry.KindDataset, id)
	if err != nil {
		return Preview{}, err
	}
	if f.MaxDuration > 0 && f.MinDuration > f.MaxDuration {
		return Preview{}, problems.Validation([]problems.FieldError{{Path: "/minDuration", Message: "must not exceed maxDuration"}})
	}
	if f.MaxCharsPerSecond > 0 && f.MinCharsPerSecond > f.MaxCharsPerSecond {
		return Preview{}, problems.Validation([]problems.FieldError{{Path: "/minCharsPerSecond", Message: "must not exceed maxCharsPerSecond"}})
	}
	rows, err := q.Query(ctx, `SELECT u.language, d.split, u.duration_s, u.speaker, t.text, t.origin
		FROM dataset_utterances d JOIN utterances u ON u.id = d.utterance_id JOIN transcripts t ON t.id = d.transcript_id
		WHERE d.version_id = $1`, id)
	if err != nil {
		return Preview{}, fmt.Errorf("query dataset members: %w", err)
	}
	pv := Preview{VersionID: v.ID, Frozen: v.State != registry.StateDraft, Cells: []PreviewCell{}, Dropped: map[string]int{}}
	type key struct{ lang, split string }
	cells := map[key]*PreviewCell{}
	speakers := map[key]map[string]bool{}
	var (
		lang, split, speaker, text, origin string
		dur                                float64
	)
	_, err = pgx.ForEachRow(rows, []any{&lang, &split, &dur, &speaker, &text, &origin}, func() error {
		if reason := f.drop(lang, split, dur, text, origin); reason != "" {
			pv.Dropped[reason]++
			return nil
		}
		k := key{lang, split}
		c := cells[k]
		if c == nil {
			c = &PreviewCell{Language: lang, Split: split}
			cells[k], speakers[k] = c, map[string]bool{}
		}
		c.Utterances++
		c.Hours += dur / 3600
		if speaker != "" {
			speakers[k][speaker] = true
		}
		pv.Utterances++
		pv.Hours += dur / 3600
		return nil
	})
	if err != nil {
		return Preview{}, fmt.Errorf("read dataset members: %w", err)
	}
	for k, c := range cells {
		c.Hours, c.Speakers = round(c.Hours, 4), len(speakers[k])
		pv.Cells = append(pv.Cells, *c)
	}
	sort.Slice(pv.Cells, func(i, j int) bool {
		a, b := pv.Cells[i], pv.Cells[j]
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		return slices.Index(Splits, a.Split) < slices.Index(Splits, b.Split)
	})
	pv.Hours = round(pv.Hours, 4)
	return pv, nil
}

func (f PreviewFilter) drop(lang, split string, dur float64, text, origin string) string {
	cps := 0.0
	if dur > 0 {
		cps = float64(utf8.RuneCountInString(strings.TrimSpace(text))) / dur
	}
	switch {
	case (f.MinDuration > 0 && dur < f.MinDuration) || (f.MaxDuration > 0 && dur > f.MaxDuration):
		return DropDuration
	case (f.MinCharsPerSecond > 0 && cps < f.MinCharsPerSecond) || (f.MaxCharsPerSecond > 0 && cps > f.MaxCharsPerSecond) || math.IsNaN(cps):
		return DropCharsPerSecond
	case len(f.Languages) > 0 && !slices.ContainsFunc(f.Languages, func(l string) bool { return sameLanguage(l, lang) }):
		return DropLanguage
	case len(f.Origins) > 0 && !slices.Contains(f.Origins, origin):
		return DropOrigin
	case len(f.Splits) > 0 && !slices.Contains(f.Splits, split):
		return DropSplit
	}
	return ""
}

// sameLanguage is languageMatch in Go: equal, or one a regional variant of the other (he ~ he-IL).
func sameLanguage(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || strings.HasPrefix(a, b+"-") || strings.HasPrefix(b, a+"-")
}
