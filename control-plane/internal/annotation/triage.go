package annotation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/triage"
)

// Resolving the triage queue (triage.accept|correct|reject): a disputed pseudo-label is a segment several models
// disagreed on; a person's decision makes its text a human transcript of the segment's utterance (created by its
// canonical hash when the segment was never part of a dataset version), or drops it.

// TriageKind is the entity kind of a triage item.
const TriageKind = "triage_item"

// Triage resolutions (the contract's TriageState).
const (
	TriageAccepted  = "accepted"
	TriageCorrected = "corrected"
	TriageRejected  = "rejected"
)

// Resolution is how a person resolved a triage item (the contract's TriageResolution).
type Resolution struct {
	Text         string     `json:"text,omitempty"`
	Tags         []string   `json:"tags,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	UtteranceID  string     `json:"utteranceId,omitempty"`
	TranscriptID string     `json:"transcriptId,omitempty"`
	By           auth.Actor `json:"by"`
	At           time.Time  `json:"at"`
}

// ResolveInput is a triage.accept, triage.correct or triage.reject request.
type ResolveInput struct {
	ID     string
	Rev    int
	State  string // accepted | corrected | rejected
	Text   string // corrected
	Tags   []string
	Reason string // rejected
	Actor  auth.Actor
}

// TriageProject returns the project of triage item id.
func TriageProject(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (string, error) {
	var p string
	err := q.QueryRow(ctx, "SELECT project_id FROM triage_items WHERE id = $1", id).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problems.NotFound.New("no triage item %q", id)
	}
	if err != nil {
		return "", fmt.Errorf("read triage item %s: %w", id, err)
	}
	return p, nil
}

// Resolve resolves an open triage item: accept writes its best candidate as the human transcript, correct the
// person's text, reject drops it; agents and API keys are refused (a human transcript is a person's).
func (s *Service) Resolve(ctx context.Context, tx pgx.Tx, in ResolveInput) (triage.Item, []events.Draft, error) {
	if in.Actor.Kind != auth.KindUser {
		return triage.Item{}, nil, problems.Forbidden.New("resolving a disputed pseudo-label writes a human transcript: a person does it in the Triage panel")
	}
	var (
		projectID, state, segHash, segsHash, best string
		rev                                       int
		seg                                       triage.Segment
	)
	err := tx.QueryRow(ctx, `SELECT project_id, state, rev, segment_hash, segments_hash, segment, best FROM triage_items
		WHERE id = $1 FOR UPDATE`, in.ID).Scan(&projectID, &state, &rev, &segHash, &segsHash, &seg, &best)
	if errors.Is(err, pgx.ErrNoRows) {
		return triage.Item{}, nil, problems.NotFound.New("no triage item %q", in.ID)
	}
	if err != nil {
		return triage.Item{}, nil, fmt.Errorf("read triage item %s: %w", in.ID, err)
	}
	if err := commands.CheckRev(TriageKind, in.Rev, rev); err != nil {
		return triage.Item{}, nil, err
	}
	if state != triage.StateOpen {
		return triage.Item{}, nil, problems.Conflict.New("triage item %s is %s already", in.ID, state)
	}
	tags, err := checkTags(in.Tags)
	if err != nil {
		return triage.Item{}, nil, err
	}
	res := Resolution{Tags: tags, By: in.Actor, At: s.now(), Reason: strings.TrimSpace(in.Reason)}
	switch in.State {
	case TriageAccepted:
		res.Text = strings.TrimSpace(best)
		if res.Text == "" {
			return triage.Item{}, nil, problems.Conflict.New("triage item %s has no candidate text to accept; correct it with your own text or reject it", in.ID)
		}
	case TriageCorrected:
		res.Text = strings.TrimSpace(in.Text)
		if res.Text == "" || utf8.RuneCountInString(res.Text) > 5000 {
			return triage.Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/text", Message: "1–5000 characters"}})
		}
	case TriageRejected:
	default:
		return triage.Item{}, nil, fmt.Errorf("triage resolution %q", in.State)
	}
	if res.Text != "" {
		if excluding(tags) {
			return triage.Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/tags",
				Message: "a segment in a foreign language or unintelligible has no transcript to write: reject it"}})
		}
		if res.UtteranceID, res.TranscriptID, err = s.humanTranscript(ctx, tx, segsHash, seg, res.Text); err != nil {
			return triage.Item{}, nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE triage_items SET state = $2, resolution = $3, rev = rev + 1, updated_at = now() WHERE id = $1`,
		in.ID, in.State, marshal(res)); err != nil {
		return triage.Item{}, nil, fmt.Errorf("resolve triage item %s: %w", in.ID, err)
	}
	it, err := triage.Get(ctx, tx, in.ID)
	if err != nil {
		return triage.Item{}, nil, err
	}
	return it, []events.Draft{{
		Topic: events.EntityTopic(TriageKind, in.ID), Type: "triage.item_" + in.State, ProjectID: projectID,
		Entity:  &events.EntityRef{Kind: TriageKind, ID: in.ID, Rev: it.Rev},
		Payload: map[string]any{"triageItemId": in.ID, "state": in.State, "utteranceId": res.UtteranceID},
	}}, nil
}

// humanTranscript writes text as the human transcript of the segment's utterance, creating the utterance (by its
// canonical hash, with its mount URI) when no dataset version holds it yet.
func (s *Service) humanTranscript(ctx context.Context, tx pgx.Tx, segsHash string, seg triage.Segment, text string) (string, string, error) {
	var uttID string
	err := tx.QueryRow(ctx, "SELECT id FROM utterances WHERE content_hash = $1", seg.Hash).Scan(&uttID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("find the utterance of %s: %w", seg.Hash, err)
	}
	if uttID == "" {
		fr, err := ReadFrame(s.CAS, segsHash)
		if err != nil {
			return "", "", problems.BadRequest.New("the segments artifact of the item cannot be read: %v", err)
		}
		src, err := data.IngestAllowed(ctx, tx, fr.Source())
		if err != nil {
			return "", "", err
		}
		var row Row
		for _, r := range fr.Rows {
			if r.str("hash") == seg.Hash {
				row = r
				break
			}
		}
		dur := 0.0
		if seg.Start != nil && seg.End != nil {
			dur = *seg.End - *seg.Start
		}
		if d, ok := row.num("duration"); ok {
			dur = d
		}
		if dur <= 0 {
			return "", "", problems.BadRequest.New("the segment %s has no duration", seg.Hash)
		}
		lang := seg.Language
		if lang == "" {
			lang, _ = fr.Header["language"].(string)
		}
		if lang == "" {
			return "", "", problems.BadRequest.New("the segment %s names no language", seg.Hash)
		}
		size, _ := row.num("bytes")
		err = tx.QueryRow(ctx, `WITH ins AS (INSERT INTO utterances (id, content_hash, source_id, duration_s, language, speaker,
				sample_rate, channels, bytes) VALUES ($1, $2, $3, $4, $5, $6, 16000, 1, $7) ON CONFLICT (content_hash) DO NOTHING RETURNING id)
			SELECT id FROM ins UNION ALL SELECT id FROM utterances WHERE content_hash = $2 LIMIT 1`,
			"utt_"+uuid.Must(uuid.NewV7()).String(), seg.Hash, src.ID, dur, lang, seg.Speaker, int64(size)).Scan(&uttID)
		if err != nil {
			return "", "", fmt.Errorf("insert the utterance of %s: %w", seg.Hash, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO utterance_fingerprints (utterance_id, kind, value) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, uttID, data.AudioFingerprint, seg.Hash); err != nil {
			return "", "", fmt.Errorf("fingerprint %s: %w", uttID, err)
		}
		if strings.HasPrefix(seg.URI, mounts.Scheme) {
			if err := mounts.RecordURIs(ctx, tx, uttID, []string{seg.URI}); err != nil {
				return "", "", err
			}
		}
	}
	var trnID string
	err = tx.QueryRow(ctx, `WITH ins AS (INSERT INTO transcripts (id, utterance_id, text, origin) VALUES ($1, $2, $3, 'human')
			ON CONFLICT (utterance_id, origin, text) DO NOTHING RETURNING id)
		SELECT id FROM ins UNION ALL SELECT id FROM transcripts WHERE utterance_id = $2 AND origin = 'human' AND text = $3 LIMIT 1`,
		"trn_"+uuid.Must(uuid.NewV7()).String(), uttID, text).Scan(&trnID)
	if err != nil {
		return "", "", fmt.Errorf("insert the transcript of %s: %w", uttID, err)
	}
	return uttID, trnID, nil
}
