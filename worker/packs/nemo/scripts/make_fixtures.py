"""Regenerate the NeMo pack's conformance fixtures: ten short FLEURS Hebrew (he_il) test clips as a folder-csv import
folder (metadata.csv + WAV), eight marked train and two validation.

    uv run python packs/nemo/scripts/make_fixtures.py <fleurs he_il test manifest (NeMo JSON lines)> [--out DIR]

The manifest lines need ``audio_filepath`` (16 kHz mono 16-bit WAV), ``duration`` and ``text`` (FLEURS
``raw_transcription``) — spike A3's ``data/manifests/test_fleurs.json`` (google/fleurs at revision
70bb2e84b976b7e960aa89f1c648e09c59f894dd) is one. Picks the clips between 2.8 and 3.9 s whose text has at most 12
words, in manifest order after sorting by duration, so the selection is deterministic. FLEURS is CC-BY-4.0.
"""

from __future__ import annotations

import argparse
import csv
import json
import shutil
from pathlib import Path

OUT = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("manifest", type=Path)
    ap.add_argument("--out", type=Path, default=OUT)
    args = ap.parse_args()
    rows = [json.loads(x) for x in args.manifest.read_text(encoding="utf-8").splitlines() if x.strip()]
    rows = [r for r in rows if 2.8 <= float(r["duration"]) <= 3.9 and len(str(r["text"]).split()) <= 12]
    rows.sort(key=lambda r: (float(r["duration"]), Path(r["audio_filepath"]).name))
    picked = rows[:10]
    if len(picked) < 10:
        raise SystemExit(f"only {len(picked)} clips qualify")
    args.out.mkdir(parents=True, exist_ok=True)
    with (args.out / "metadata.csv").open("w", encoding="utf-8", newline="") as f:
        w = csv.writer(f)
        w.writerow(["file", "text", "split"])
        for i, r in enumerate(picked, 1):
            name = f"he{i:02d}.wav"
            shutil.copyfile(r["audio_filepath"], args.out / name)
            w.writerow([name, r["text"], "validation" if i > 8 else "train"])
    print(f"wrote {len(picked)} clips to {args.out}")


if __name__ == "__main__":
    main()
