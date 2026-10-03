-- 0029 · Phase 3 stream A (media: audio serving, R25; docs/spec/06-platform.md "Media"). Derived media of an
-- utterance's audio are ordinary registry artifacts: `peaks` (10 ms min/max, cached by peaks.get on first use) and
-- `spectrogram_tiles` (the tile pyramid of spectrogram_tiles@1). Both name the audio they were computed from in
-- meta.audio (its b3: hash); this index finds the newest live one of a kind for an audio. 0026–0028 belong to other
-- phase-3 streams; migrations apply by number.
CREATE INDEX artifacts_media_audio_idx ON artifacts (type, (meta->>'audio'), created_at DESC)
    WHERE evicted_at IS NULL AND type IN ('peaks', 'spectrogram_tiles');
