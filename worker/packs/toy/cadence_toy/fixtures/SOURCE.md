# Toy fixtures

Eight clips synthesised by `packs/toy/scripts/make_fixtures.py` (deterministic): each letter is an 80 ms tone at its
own pitch (300 Hz + 280 Hz per letter), a space a short burst of broadband noise, plus faint Gaussian noise; 16 kHz mono 16-bit PCM, about 250 KB in all.
Generated for Cadence, no third-party audio: CC0 1.0. `metadata.csv` lists each clip with its text and split: the folder is a `dataset_import`
input (`format: folder-csv`), and the conformance suite and the pack's tests import it into a `dataset` artifact first.
