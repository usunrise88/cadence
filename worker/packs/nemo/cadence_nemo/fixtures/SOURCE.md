# NeMo pack fixtures

Ten short Hebrew clips (2.9–3.7 s, 16 kHz mono 16-bit PCM, about 1.1 MB in all) from the **FLEURS** test split,
configuration `he_il` (`google/fleurs` at revision `70bb2e84b976b7e960aa89f1c648e09c59f894dd`), with their
`raw_transcription` (punctuated, cased — the base model's text style). Selected by `packs/nemo/scripts/make_fixtures.py`
from spike A3's FLEURS test manifest: eight marked `train`, two `validation`.

Licence: **CC-BY-4.0** — A. Conneau, M. Ma, S. Khanuja, Y. Zhang, V. Axelrod, S. Dalmia, J. Riesa, C. Rivera,
A. Bapna, "FLEURS: Few-shot Learning Evaluation of Universal Representations of Speech", 2022.
Changes: none to the audio (FLEURS ships 16 kHz WAV); files renamed.

| Fixture | FLEURS file |
| --- | --- |
| he01.wav | 4483874849492820167.wav |
| he02.wav | 10802447565377436171.wav |
| he03.wav | 13191301977081138910.wav |
| he04.wav | 17094305295227949694.wav |
| he05.wav | 16048281977742897081.wav |
| he06.wav | 12170875451503309371.wav |
| he07.wav | 3598140641113031108.wav |
| he08.wav | 12877459052574236982.wav |
| he09.wav | 9862303843404667133.wav |
| he10.wav | 13663261434495714627.wav |

`metadata.csv` makes the folder a `dataset_import` input (`format: folder-csv`, locale `he-IL`); the conformance suite
imports it into a `dataset` artifact first. These clips are FLEURS **test** utterances: never use this folder as
training data for a model you evaluate on FLEURS.
