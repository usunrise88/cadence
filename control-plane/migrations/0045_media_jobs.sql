-- 0045 · Phase 4 tail (long audio): the control plane's media jobs, one per subject. media.peaks stores the waveform
-- peaks of a dataset version's members when the version is registered (subject: the version id and the artifact that
-- registered it, so a draft and then its frozen cut); media.spectrogram builds the spectrogram tile pyramid of one
-- audio on the audio view's first request (subject: the audio's key and the views.audio settings). A row names the
-- newest job of its subject, so concurrent requests share one job and a failed one is retried only after
-- media.tiles_retry_s. Derived bookkeeping: losing a row only means a job runs again.
CREATE TABLE media_jobs (
    kind        text NOT NULL,                    -- media.peaks | media.spectrogram
    subject     text NOT NULL,                    -- ver_…|<artifact> or <audio key>|<settings>
    job_id      text NOT NULL DEFAULT '',         -- the newest job (job_…); '' while the first is being enqueued
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, subject)
);
