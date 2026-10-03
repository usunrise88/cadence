package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Transcript origins other than model:<id>.
const (
	OriginHuman       = "human"
	OriginPseudoLabel = "pseudo-label"
	originModelPrefix = "model:"
)

// ValidOrigin reports whether o is human, pseudo-label or model:<id>.
func ValidOrigin(o string) bool {
	return o == OriginHuman || o == OriginPseudoLabel || (strings.HasPrefix(o, originModelPrefix) && len(o) > len(originModelPrefix))
}

// Transcript is text for an utterance with its origin.
type Transcript struct {
	ID         string
	Text       string
	Origin     string
	Confidence *float64
	CreatedAt  time.Time
}

// Membership is an utterance's place in one dataset version.
type Membership struct {
	VersionID    string
	Split        string
	TranscriptID string
}

// Utterance is one audio segment in the content store.
type Utterance struct {
	ID           string
	ContentHash  string
	SourceID     string
	SourceName   string
	Duration     float64
	Language     string
	Speaker      string
	SampleRate   int
	Channels     int
	Bytes        int64
	CreatedAt    time.Time
	Split        string // set when listed within a dataset version
	Transcripts  []Transcript
	Fingerprints map[string]string // Get only
	Datasets     []Membership      // Get only
}

// UtteranceFilter narrows ListUtterances; zero fields do not filter.
type UtteranceFilter struct {
	Source   string // id or name
	Dataset  string // dataset version id
	Split    string // needs Dataset
	Language string
	After    string // cursor: the last id of the previous page
	Limit    int    // 1–500, default 100

	// utterances.search (phase 4): Text is a case-insensitive substring of a transcript (within a dataset version, of
	// the transcript the version uses); Origin a transcript origin; durations in seconds (0 = no bound).
	Text        string
	Speaker     string
	Origin      string
	MinDuration float64
	MaxDuration float64
}

// DefaultLimit and MaxLimit bound a page of utterances.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// ListUtterances returns a page of utterances, oldest first (ids are UUIDv7), with their transcripts, and the
// cursor of the next page ("" on the last one).
func ListUtterances(ctx context.Context, q storage.Querier, f UtteranceFilter) ([]Utterance, string, error) {
	if f.Split != "" && f.Dataset == "" {
		return nil, "", problems.Validation([]problems.FieldError{{Path: "/split", Message: "a split needs a dataset version (dataset=ver_…)"}})
	}
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	var (
		conds []string
		args  []any
	)
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	splitCol := "''"
	from := "utterances u JOIN sources s ON s.id = u.source_id"
	if f.Dataset != "" {
		from += " JOIN dataset_utterances d ON d.utterance_id = u.id AND d.version_id = " + arg(f.Dataset)
		splitCol = "d.split"
		if f.Split != "" {
			conds = append(conds, "d.split = "+arg(f.Split))
		}
	}
	if f.Source != "" {
		a := arg(f.Source)
		conds = append(conds, "(s.id = "+a+" OR s.name = "+a+")")
	}
	if f.Language != "" {
		conds = append(conds, languageMatch("u.language", arg(f.Language)))
	}
	if f.Speaker != "" {
		conds = append(conds, "u.speaker = "+arg(f.Speaker))
	}
	if f.MinDuration > 0 {
		conds = append(conds, "u.duration_s >= "+arg(f.MinDuration))
	}
	if f.MaxDuration > 0 {
		conds = append(conds, "u.duration_s <= "+arg(f.MaxDuration))
	}
	if f.Text != "" || f.Origin != "" {
		tcond := []string{"t.utterance_id = u.id"}
		if f.Dataset != "" {
			tcond = []string{"t.id = d.transcript_id"}
		}
		if f.Text != "" {
			tcond = append(tcond, "lower(t.text) LIKE "+arg("%"+escapeLike(strings.ToLower(f.Text))+"%"))
		}
		if f.Origin != "" {
			tcond = append(tcond, "t.origin = "+arg(f.Origin))
		}
		conds = append(conds, "EXISTS (SELECT 1 FROM transcripts t WHERE "+strings.Join(tcond, " AND ")+")")
	}
	if f.After != "" {
		conds = append(conds, "u.id > "+arg(f.After))
	}
	where := "true"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	sql := `SELECT u.id, u.content_hash, u.source_id, s.name, u.duration_s, u.language, u.speaker, u.sample_rate, u.channels,
		u.bytes, u.created_at, ` + splitCol + ` FROM ` + from + ` WHERE ` + where + ` ORDER BY u.id LIMIT ` + arg(limit+1)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list utterances: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanUtterance)
	if err != nil {
		return nil, "", fmt.Errorf("list utterances: %w", err)
	}
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = list[limit-1].ID
	}
	if err := withTranscripts(ctx, q, list); err != nil {
		return nil, "", err
	}
	return list, next, nil
}

// escapeLike escapes LIKE's wildcards and its escape character (the backslash) in s.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

func scanUtterance(row pgx.CollectableRow) (Utterance, error) {
	var u Utterance
	err := row.Scan(&u.ID, &u.ContentHash, &u.SourceID, &u.SourceName, &u.Duration, &u.Language, &u.Speaker, &u.SampleRate,
		&u.Channels, &u.Bytes, &u.CreatedAt, &u.Split)
	return u, err
}

func withTranscripts(ctx context.Context, q storage.Querier, list []Utterance) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	at := make(map[string]int, len(list))
	for i, u := range list {
		ids[i], at[u.ID] = u.ID, i
		list[i].Transcripts = []Transcript{}
	}
	rows, err := q.Query(ctx, `SELECT utterance_id, id, text, origin, confidence, created_at FROM transcripts
		WHERE utterance_id = ANY($1) ORDER BY utterance_id, created_at, id`, ids)
	if err != nil {
		return fmt.Errorf("query transcripts: %w", err)
	}
	var (
		uid string
		t   Transcript
	)
	_, err = pgx.ForEachRow(rows, []any{&uid, &t.ID, &t.Text, &t.Origin, &t.Confidence, &t.CreatedAt}, func() error {
		list[at[uid]].Transcripts = append(list[at[uid]].Transcripts, t)
		t = Transcript{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read transcripts: %w", err)
	}
	return nil
}

// GetUtterance returns the utterance with this id (utt_…) or content hash (b3:…), with its transcripts,
// fingerprints and dataset memberships.
func GetUtterance(ctx context.Context, q storage.Querier, idOrHash string) (Utterance, error) {
	rows, err := q.Query(ctx, `SELECT u.id, u.content_hash, u.source_id, s.name, u.duration_s, u.language, u.speaker,
		u.sample_rate, u.channels, u.bytes, u.created_at, '' FROM utterances u JOIN sources s ON s.id = u.source_id
		WHERE u.id = $1 OR u.content_hash = $1`, idOrHash)
	if err != nil {
		return Utterance{}, fmt.Errorf("query utterance: %w", err)
	}
	u, err := pgx.CollectExactlyOneRow(rows, scanUtterance)
	if errors.Is(err, pgx.ErrNoRows) {
		return Utterance{}, problems.NotFound.New("no utterance %q in the registry", idOrHash)
	}
	if err != nil {
		return Utterance{}, fmt.Errorf("read utterance: %w", err)
	}
	list := []Utterance{u}
	if err := withTranscripts(ctx, q, list); err != nil {
		return Utterance{}, err
	}
	u = list[0]
	u.Fingerprints = map[string]string{}
	rows, err = q.Query(ctx, "SELECT kind, value FROM utterance_fingerprints WHERE utterance_id = $1", u.ID)
	if err != nil {
		return Utterance{}, fmt.Errorf("query fingerprints: %w", err)
	}
	var k, v string
	if _, err := pgx.ForEachRow(rows, []any{&k, &v}, func() error { u.Fingerprints[k] = v; return nil }); err != nil {
		return Utterance{}, fmt.Errorf("read fingerprints: %w", err)
	}
	rows, err = q.Query(ctx, `SELECT version_id, split, transcript_id FROM dataset_utterances WHERE utterance_id = $1
		ORDER BY version_id`, u.ID)
	if err != nil {
		return Utterance{}, fmt.Errorf("query memberships: %w", err)
	}
	u.Datasets, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Membership, error) {
		var m Membership
		return m, row.Scan(&m.VersionID, &m.Split, &m.TranscriptID)
	})
	if err != nil {
		return Utterance{}, fmt.Errorf("read memberships: %w", err)
	}
	return u, nil
}
