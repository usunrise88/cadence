# FLEURS sr_rs fixture

Three clips from the FLEURS Serbian (`sr_rs`) **test** split, trimmed to their first 1.2 s, for the `dataset_import`
tests. About 150 KB in all.

- Source: `google/fleurs` on Hugging Face, revision `70bb2e84b976b7e960aa89f1c648e09c59f894dd`,
  `data/sr_rs/audio/test.tar.gz` and `data/sr_rs/test.tsv` (files `8412944753138602386.wav`,
  `2455323429440555331.wav` — sentence 1817 — and `18030482151960581409.wav` — sentence 1724).
- Licence: **CC-BY-4.0** (FLEURS, A. Conneau et al., "FLEURS: Few-shot Learning Evaluation of Universal
  Representations of Speech", 2022; https://huggingface.co/datasets/google/fleurs).
- Changes: trimmed to 1.2 s; `clip1` and `clip2` converted from 32-bit float to 16-bit PCM WAV, `clip3` keeps FLEURS'
  own 32-bit float WAV so the float decoding path is tested. The transcripts are the full sentences (`raw_transcription`),
  so they do not match the trimmed audio — the tests check bookkeeping, not recognition.
- `metadata.csv` is the `folder-csv` form, `manifest.json` the `nemo-manifest` form. Its `speaker` values are synthetic
  labels for the split tests (FLEURS has no speaker ids).
