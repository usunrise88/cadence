package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// The control plane's media jobs (phase 4 tail; R51, R52). Both read audio exactly as the media endpoints serve it —
// the content store, a mount copy of a segment, a window of a file on a mount — so what they store is what a first
// view would have computed:
//   - media.peaks stores the waveform peaks of a dataset version's members when the version is registered (a frozen
//     version, an import, and a draft's segments whose files the control plane reads without a decoder);
//   - media.spectrogram builds the tile pyramid of one audio on the audio view's first request for it.
//
// media_jobs (migration 0045) names the newest job of each subject, so concurrent requests share one job.

// Job kinds.
const (
	JobPeaks       = "media.peaks"
	JobSpectrogram = "media.spectrogram"
)

// jobTimeout bounds one media job (a large version's peaks, a four-hour recording's pyramid).
const jobTimeout = 6 * time.Hour

// Register adds the media job kinds; call it before the job runner starts.
func (s *Service) Register(js *jobs.Service) {
	s.Jobs = js
	js.Register(JobPeaks, s.runPeaks, jobs.KindOptions{Timeout: jobTimeout})
	js.Register(JobSpectrogram, s.runSpectrogram, jobs.KindOptions{Timeout: jobTimeout})
}

// heavy takes one of the slots media jobs share (Builds; nil: no bound), waiting for one.
func (s *Service) heavy(ctx context.Context) (func(), error) {
	if s.Builds == nil {
		return func() {}, nil
	}
	select {
	case s.Builds <- struct{}{}:
		return func() { <-s.Builds }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ensureJob returns the subject's queued or running job, or enqueues one (also after a failed or cancelled job once
// retry has passed since it ended; before that the failed job is answered). A finished job is answered unless
// rerunDone (its result may be gone: a pyramid evicted). It runs in tx, which it locks the subject's row in.
func (s *Service) ensureJob(ctx context.Context, tx pgx.Tx, kind, subject string, args any, retry time.Duration, rerunDone bool) (jobs.Job, bool, []events.Draft, error) {
	if s.Jobs == nil || !s.Jobs.Registered(kind) {
		return jobs.Job{}, false, nil, problems.NotImplemented.New("this control plane runs no %s jobs", kind)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_jobs (kind, subject) VALUES ($1, $2) ON CONFLICT DO NOTHING`, kind, subject); err != nil {
		return jobs.Job{}, false, nil, fmt.Errorf("media job of %s: %w", subject, err)
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT job_id FROM media_jobs WHERE kind = $1 AND subject = $2 FOR UPDATE`, kind, subject).Scan(&id); err != nil {
		return jobs.Job{}, false, nil, fmt.Errorf("media job of %s: %w", subject, err)
	}
	if id != "" {
		j, err := jobs.Get(ctx, tx, id)
		pe, isProblem := problems.As(err)
		switch {
		case err == nil && !jobs.Terminal(j.State), err == nil && j.State == jobs.StateDone && !rerunDone:
			return j, false, nil, nil
		case err == nil && j.State != jobs.StateDone && j.FinishedAt != nil && time.Since(*j.FinishedAt) < retry:
			return j, false, nil, nil
		case err != nil && (!isProblem || pe.Type != problems.NotFound):
			return jobs.Job{}, false, nil, err
		}
	}
	j, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: kind, Args: args})
	if err != nil {
		return jobs.Job{}, false, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE media_jobs SET job_id = $3, updated_at = now() WHERE kind = $1 AND subject = $2`, kind, subject, j.ID); err != nil {
		return jobs.Job{}, false, nil, fmt.Errorf("media job of %s: %w", subject, err)
	}
	return j, true, drafts, nil
}

// ---------------------------------------------------------------- peaks of a dataset version

type peaksArgs struct {
	VersionID string `json:"versionId"`
}

// PeaksResult is what a media.peaks job reports.
type PeaksResult struct {
	VersionID  string `json:"versionId"`
	Utterances int    `json:"utterances"`
	Stored     int    `json:"stored"`  // computed and stored by this job
	Present    int    `json:"present"` // stored before (a first view, another version with the same audio)
	Skipped    int    `json:"skipped"` // not readable here (a codec only a worker decodes): computed on first view
	FirstError string `json:"firstError,omitempty"`
}

// DatasetHook is a second output hook of dataset artifacts, run after the importer registered the version: it
// queues media.peaks for every dataset version registered for the artifact (draft or frozen). Derived datasets (an
// eval's augmentation, the pseudo-label members' cut) are never versions and get none.
func (s *Service) DatasetHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	if s.Jobs == nil || !s.Jobs.Registered(JobPeaks) || data.Derived(out.Artifact.Meta) {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $1 AND v.payload->'artifact'->>'hash' = $2`, registry.KindDataset, out.Artifact.Hash)
	if err != nil {
		return nil, fmt.Errorf("versions of %s: %w", out.Artifact.Hash, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("versions of %s: %w", out.Artifact.Hash, err)
	}
	var all []events.Draft
	for _, id := range ids {
		_, _, drafts, err := s.EnqueuePeaks(ctx, tx, id, out.Artifact.Hash)
		if err != nil {
			return nil, err
		}
		all = append(all, drafts...)
	}
	return all, nil
}

// EnqueuePeaks queues media.peaks for a dataset version and the artifact that registered it (a draft, then its frozen
// cut: the cut reads members a draft could not) unless one was queued for them already.
func (s *Service) EnqueuePeaks(ctx context.Context, tx pgx.Tx, versionID, artifact string) (jobs.Job, bool, []events.Draft, error) {
	return s.ensureJob(ctx, tx, JobPeaks, versionID+"|"+artifact, peaksArgs{VersionID: versionID}, 0, false)
}

// members lists a dataset version's utterance ids in order.
func members(ctx context.Context, q storage.Querier, versionID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT utterance_id FROM dataset_utterances WHERE version_id = $1 ORDER BY utterance_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("members of %s: %w", versionID, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("members of %s: %w", versionID, err)
	}
	return ids, nil
}

// runPeaks stores the peaks of every member the store has none of. A member that cannot be read here (a draft's
// segment of a compressed file, audio no longer anywhere) is skipped: its first view computes or reports it.
func (s *Service) runPeaks(ctx context.Context, run *jobs.Run) (any, error) {
	var a peaksArgs
	if err := json.Unmarshal(run.Args, &a); err != nil || a.VersionID == "" {
		return nil, fmt.Errorf("media.peaks: bad args %s", run.Args)
	}
	ids, err := members(ctx, s.Pool, a.VersionID)
	if err != nil {
		return nil, err
	}
	release, err := s.heavy(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	res := PeaksResult{VersionID: a.VersionID, Utterances: len(ids)}
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		u, err := Lookup(ctx, s.Pool, id)
		if err == nil {
			var has bool
			if has, err = s.HasPeaks(ctx, u.Hash); err == nil && has {
				res.Present++
			} else if err == nil {
				var sp StoredPeaks
				if sp, err = s.PeaksOf(ctx, u, PeaksSourceJob); err == nil && sp.Artifact == "" {
					err = errors.New("the peaks were computed but not recorded")
				}
				if err == nil {
					res.Stored++
				}
			}
		}
		if err != nil {
			res.Skipped++
			if res.FirstError == "" {
				res.FirstError = fmt.Sprintf("%s: %v", id, err)
			}
		}
		if i%50 == 49 || i == len(ids)-1 {
			_ = run.Progress(ctx, float64(i+1)/float64(len(ids)), fmt.Sprintf("%d/%d utterances", i+1, len(ids)))
		}
	}
	return res, nil
}

// HasPeaks reports whether the store holds peaks of the audio (key: an utterance's hash or a window's key).
func (s *Service) HasPeaks(ctx context.Context, key string) (bool, error) {
	_, ok, err := cached(ctx, s.Pool, TypePeaks, key)
	return ok, err
}
