// Package data holds the minimal data entities imports need (docs/spec/08-resolutions.md R18): sources with their
// licence and training clearance, utterances identified by the content hash of their audio, transcripts with their
// origin, the membership of utterances in dataset versions and per-utterance fingerprints. They are registry data:
// events carry no projectId. The "dataset" output hook (hook.go) turns an imported dataset artifact into these rows
// and a frozen dataset version; Trainable (trainable.go) is what mixes and runs ask before they train on a version.
package data

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds (the contract's EntityKind and topic segment).
const (
	SourceKind    = "source"
	UtteranceKind = "utterance"
)

// SourceKinds are the kinds of a source (the contract's SourceKind).
var SourceKinds = []string{"public", "production", "synthetic"}

// Splits are the split names of a dataset version (the contract's DatasetSplitName).
var Splits = []string{"train", "validation", "test"}

var sourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$`)

// Source is a corpus in the registry.
type Source struct {
	ID              string
	Name            string
	Description     string
	Licence         string
	Kind            string
	Languages       []string
	URL             string
	TrainingCleared bool
	ClearedBy       *auth.Actor
	ClearedAt       *time.Time
	Archived        bool
	Rev             int
	CreatedBy       auth.Actor
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Utterances      int
	Hours           float64
	Datasets        []string    // Get only: dataset version ids built from the source
	Clearances      []Clearance // Get only: licence and clearance history, oldest first
	Ingests         []Ingest    // Get only: dataset versions registered from the source, newest first
}

const sourceSelect = `SELECT s.id, s.name, s.description, s.licence, s.kind, s.languages, s.url, s.training_cleared,
	s.cleared_by, s.cleared_at, s.archived, s.rev, s.created_by, s.created_at, s.updated_at,
	(SELECT count(*) FROM utterances u WHERE u.source_id = s.id),
	coalesce((SELECT sum(u.duration_s) FROM utterances u WHERE u.source_id = s.id), 0) / 3600.0
	FROM sources s`

func scanSource(row pgx.CollectableRow) (Source, error) {
	var s Source
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Licence, &s.Kind, &s.Languages, &s.URL, &s.TrainingCleared,
		&s.ClearedBy, &s.ClearedAt, &s.Archived, &s.Rev, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.Utterances, &s.Hours)
	return s, err
}

// SourceFilter narrows ListSources; zero fields do not filter.
type SourceFilter struct {
	Archived bool   // include archived sources
	Kind     string // public | production | synthetic
	Language string // he matches he-IL and the other way round
}

// ListSources returns sources by name.
func ListSources(ctx context.Context, q storage.Querier, f SourceFilter) ([]Source, error) {
	rows, err := q.Query(ctx, sourceSelect+` WHERE ($1 OR NOT s.archived) AND ($2 = '' OR s.kind = $2)
		AND ($3 = '' OR EXISTS (SELECT 1 FROM unnest(s.languages) l WHERE `+languageMatch("l", "$3")+`))
		ORDER BY s.name`, f.Archived, f.Kind, f.Language)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanSource)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	return out, nil
}

// languageMatch is the SQL condition "column is lang, or one is a regional variant of the other" (he ~ he-IL).
func languageMatch(col, arg string) string {
	return fmt.Sprintf("(lower(%[1]s) = lower(%[2]s) OR lower(%[1]s) LIKE lower(%[2]s) || '-%%' OR lower(%[2]s) LIKE lower(%[1]s) || '-%%')", col, arg)
}

// GetSource returns the source with this id or name, with the dataset versions built from it.
func GetSource(ctx context.Context, q storage.Querier, idOrName string) (Source, error) {
	s, err := getSource(ctx, q, idOrName, "")
	if err != nil {
		return Source{}, err
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT d.version_id FROM dataset_utterances d JOIN utterances u ON u.id = d.utterance_id
		WHERE u.source_id = $1 ORDER BY d.version_id`, s.ID)
	if err != nil {
		return Source{}, fmt.Errorf("query source datasets: %w", err)
	}
	if s.Datasets, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return Source{}, fmt.Errorf("query source datasets: %w", err)
	}
	if err := withHistory(ctx, q, &s); err != nil {
		return Source{}, err
	}
	return s, nil
}

func getSource(ctx context.Context, q storage.Querier, idOrName, lock string) (Source, error) {
	rows, err := q.Query(ctx, sourceSelect+" WHERE s.id = $1 OR s.name = $1 "+lock, idOrName)
	if err != nil {
		return Source{}, fmt.Errorf("query source: %w", err)
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, problems.NotFound.New("no source %q in the registry", idOrName)
	}
	if err != nil {
		return Source{}, fmt.Errorf("read source: %w", err)
	}
	if s.Datasets == nil {
		s.Datasets = []string{}
	}
	return s, nil
}

// EditInput is the body of sources.edit; nil fields stay as they are.
type EditInput struct {
	Description     *string
	Licence         *string
	TrainingCleared *bool
}

// Edit changes the source at revision rev. Clearing it for training records who cleared it and when; an archived
// source cannot be edited. Who may clear is the policy engine's call (agents need an approval, docs/help/steps/
// dataset-import.md); Edit only records it.
func Edit(ctx context.Context, tx pgx.Tx, idOrName string, rev int, in EditInput, actor auth.Actor, now time.Time) (Source, []events.Draft, error) {
	s, err := getSource(ctx, tx, idOrName, "FOR UPDATE OF s")
	if err != nil {
		return Source{}, nil, err
	}
	if err := commands.CheckRev(SourceKind, rev, s.Rev); err != nil {
		return Source{}, nil, err
	}
	if s.Archived {
		return Source{}, nil, problems.Conflict.New("source %s is archived; an archived source cannot be edited", s.Name)
	}
	var changes []string
	if in.Licence != nil {
		l := strings.TrimSpace(*in.Licence)
		if l == "" {
			return Source{}, nil, problems.Validation([]problems.FieldError{{Path: "/licence", Message: "must not be empty"}})
		}
		if l != s.Licence {
			changes = append(changes, ChangeLicence)
		}
		s.Licence = l
	}
	if in.Description != nil {
		s.Description = *in.Description
	}
	// No clearance under a licence that forbids training (R26): neither clearing such a source nor moving a cleared
	// source to such a licence. Unclearing is always allowed.
	clearing := in.TrainingCleared != nil && *in.TrainingCleared && !s.TrainingCleared
	if (clearing || (s.TrainingCleared && len(changes) > 0 && (in.TrainingCleared == nil || *in.TrainingCleared))) &&
		registry.TrainingForbidden(s.Licence) != "" {
		return Source{}, nil, problems.Validation([]problems.FieldError{{Path: "/trainingCleared",
			Message: fmt.Sprintf("source %s is licensed %s, which %s: its audio may be evaluated on but never cleared for training (R26)",
				s.Name, s.Licence, registry.TrainingForbidden(s.Licence))}})
	}
	if in.TrainingCleared != nil && *in.TrainingCleared != s.TrainingCleared {
		s.TrainingCleared = *in.TrainingCleared
		if s.TrainingCleared {
			s.ClearedBy, s.ClearedAt = &actor, &now
			changes = append(changes, ChangeCleared)
		} else {
			s.ClearedBy, s.ClearedAt = nil, nil
			changes = append(changes, ChangeUncleared)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sources SET description = $2, licence = $3, training_cleared = $4, cleared_by = $5,
		cleared_at = $6, rev = rev + 1, updated_at = $7 WHERE id = $1`,
		s.ID, s.Description, s.Licence, s.TrainingCleared, s.ClearedBy, s.ClearedAt, now); err != nil {
		return Source{}, nil, fmt.Errorf("update source: %w", err)
	}
	for _, c := range changes {
		if err := recordClearance(ctx, tx, s.ID, c, s.Licence, s.TrainingCleared, actor, now); err != nil {
			return Source{}, nil, err
		}
	}
	if s, err = GetSource(ctx, tx, s.ID); err != nil {
		return Source{}, nil, err
	}
	return s, []events.Draft{SourceEvent(s, "source.edited")}, nil
}

// Archive archives the source at revision rev (soft): it takes no new imports; its utterances and the dataset
// versions built from it stay.
func Archive(ctx context.Context, tx pgx.Tx, idOrName string, rev int, now time.Time) (Source, []events.Draft, error) {
	s, err := getSource(ctx, tx, idOrName, "FOR UPDATE OF s")
	if err != nil {
		return Source{}, nil, err
	}
	if err := commands.CheckRev(SourceKind, rev, s.Rev); err != nil {
		return Source{}, nil, err
	}
	if s.Archived {
		return Source{}, nil, problems.Conflict.New("source %s is already archived", s.Name)
	}
	if _, err := tx.Exec(ctx, "UPDATE sources SET archived = true, rev = rev + 1, updated_at = $2 WHERE id = $1", s.ID, now); err != nil {
		return Source{}, nil, fmt.Errorf("archive source: %w", err)
	}
	if s, err = GetSource(ctx, tx, s.ID); err != nil {
		return Source{}, nil, err
	}
	return s, []events.Draft{SourceEvent(s, "source.archived")}, nil
}

// SourceInput describes a source an import names.
type SourceInput struct {
	Name      string
	Licence   string
	Kind      string
	Languages []string
	URL       string
}

func (in SourceInput) validate() error {
	switch {
	case !sourceName.MatchString(in.Name):
		return fmt.Errorf("source name %q must be 2–100 lowercase letters, digits, dots, dashes or underscores", in.Name)
	case Unlicensed(in.Licence):
		return fmt.Errorf("source %s has no usable licence (%q); an import must carry the licence of its corpus — no licence, no ingest", in.Name, in.Licence)
	}
	for _, k := range SourceKinds {
		if in.Kind == k {
			return nil
		}
	}
	return fmt.Errorf("source %s: kind %q is not one of %s", in.Name, in.Kind, strings.Join(SourceKinds, ", "))
}

// Ensure returns the source an import names, creating it (eval-only: not cleared for training) when it is new.
// An existing source must not be archived and must carry the same licence; languages it did not list yet are
// added. created tells whether the source is new.
func Ensure(ctx context.Context, tx pgx.Tx, in SourceInput, actor auth.Actor, now time.Time) (Source, bool, []events.Draft, error) {
	if err := in.validate(); err != nil {
		return Source{}, false, nil, err
	}
	langs := in.Languages
	if langs == nil {
		langs = []string{}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sources (id, name, licence, kind, languages, url, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) ON CONFLICT (name) DO NOTHING`,
		"src_"+uuid.Must(uuid.NewV7()).String(), in.Name, strings.TrimSpace(in.Licence), in.Kind, langs, in.URL, actor, now)
	if err != nil {
		return Source{}, false, nil, fmt.Errorf("insert source: %w", err)
	}
	s, err := getSource(ctx, tx, in.Name, "FOR UPDATE OF s")
	if err != nil {
		return Source{}, false, nil, err
	}
	if tag.RowsAffected() == 1 {
		if err := recordClearance(ctx, tx, s.ID, ChangeCreated, s.Licence, false, actor, now); err != nil {
			return Source{}, false, nil, err
		}
		return s, true, []events.Draft{SourceEvent(s, "source.created")}, nil
	}
	switch {
	case s.Archived:
		return Source{}, false, nil, fmt.Errorf("source %s is archived and takes no new imports", s.Name)
	case s.Licence != strings.TrimSpace(in.Licence):
		return Source{}, false, nil, fmt.Errorf("source %s has licence %q in the registry but the import says %q; fix the import or edit the source (sources.edit)",
			s.Name, s.Licence, in.Licence)
	case s.Kind != in.Kind:
		return Source{}, false, nil, fmt.Errorf("source %s is %s in the registry but the import says %s", s.Name, s.Kind, in.Kind)
	}
	var added []string
	for _, l := range langs {
		if !containsFold(s.Languages, l) {
			added = append(added, l)
		}
	}
	if len(added) == 0 {
		return s, false, nil, nil
	}
	if _, err := tx.Exec(ctx, "UPDATE sources SET languages = languages || $2, rev = rev + 1, updated_at = $3 WHERE id = $1",
		s.ID, added, now); err != nil {
		return Source{}, false, nil, fmt.Errorf("update source languages: %w", err)
	}
	if s, err = getSource(ctx, tx, s.ID, ""); err != nil {
		return Source{}, false, nil, err
	}
	return s, false, []events.Draft{SourceEvent(s, "source.edited")}, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// SourceView is a source's JSON form in events (the contract's Source without the dataset list).
type SourceView struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Licence         string      `json:"licence"`
	Kind            string      `json:"kind"`
	Languages       []string    `json:"languages"`
	TrainingCleared bool        `json:"trainingCleared"`
	ClearedBy       *auth.Actor `json:"clearedBy,omitempty"`
	Archived        bool        `json:"archived"`
	Rev             int         `json:"rev"`
}

// SourceEvent is the registry event of a source: topic entity.source.<id>, no projectId.
func SourceEvent(s Source, typ string) events.Draft {
	return events.Draft{
		Topic:  events.EntityTopic(SourceKind, s.ID),
		Type:   typ,
		Entity: &events.EntityRef{Kind: SourceKind, ID: s.ID, Rev: s.Rev},
		Payload: map[string]any{"source": SourceView{ID: s.ID, Name: s.Name, Licence: s.Licence, Kind: s.Kind,
			Languages: s.Languages, TrainingCleared: s.TrainingCleared, ClearedBy: s.ClearedBy, Archived: s.Archived, Rev: s.Rev}},
	}
}
