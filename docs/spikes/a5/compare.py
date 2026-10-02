"""A5: compare the decodes of fast_decode.py (a: pipeline.run, b: live code path fast, c: the pack's loop) and any
bench.py runs against each other and the references. Words are compared after a light normalisation (lower case,
Unicode punctuation removed, whitespace collapsed); "raw" compares the strings as emitted.
   python3 docs/spikes/a5/compare.py ~/cadence-spikes/a5/out 80ms 160ms 1120ms
"""

import json
import sys
import unicodedata
from pathlib import Path


def norm(t: str) -> list[str]:
    t = "".join(" " if unicodedata.category(ch).startswith("P") else ch for ch in t.lower())
    return t.split()


def edits(r: list[str], h: list[str]) -> int:
    d = list(range(len(h) + 1))
    for i in range(1, len(r) + 1):
        prev, d[0] = d[0], i
        for j in range(1, len(h) + 1):
            cur = min(d[j] + 1, d[j - 1] + 1, prev + (r[i - 1] != h[j - 1]))
            prev, d[j] = d[j], cur
    return d[len(h)]


def wer(refs: list[str], hyps: list[str]) -> float:
    e = sum(edits(norm(r), norm(h)) for r, h in zip(refs, hyps))
    return round(100 * e / max(1, sum(len(norm(r)) for r in refs)), 2)


def main() -> None:
    out = Path(sys.argv[1])
    for prof in sys.argv[2:]:
        ab = json.loads((out / f"fast-{prof}-ab.json").read_text())
        c = json.loads((out / f"fast-{prof}-c.json").read_text())
        refs = ab["refs"]
        he = [i for i, p in enumerate(ab["clips"]) if "/he/" in p]
        ru = [i for i, p in enumerate(ab["clips"]) if "/ru/" in p]
        row = {"profile": prof, "pipelineLoadS": ab["pipelineLoadS"], "packLoadS": c.get("packLoadS")}
        for k, src in (("a", ab), ("b", ab), ("c", c)):
            for lang, idx in (("he", he), ("ru", ru)):
                row[f"wer_{k}_{lang}"] = wer([refs[i] for i in idx], [src[k][i] for i in idx])
        for x, y in (("a", "b"), ("a", "c"), ("b", "c")):
            sx = ab[x] if x in ab else c[x]
            sy = ab[y] if y in ab else c[y]
            row[f"raw_{x}{y}"] = f"{sum(p == q for p, q in zip(sx, sy))}/{len(sx)}"
            row[f"words_{x}{y}"] = f"{sum(norm(p) == norm(q) for p, q in zip(sx, sy))}/{len(sx)}"
        row["empty_a"] = sum(1 for t in ab["a"] if not t)
        row["empty_c"] = sum(1 for t in c["c"] if not t)
        row["liveFastRTF_p50"] = ab.get("liveFastRTF_p50")
        row["pipelineRunRTF"] = {k: v for k, v in ab.items() if k.startswith("pipelineRunRTF")}
        row["peakGiB"] = ab.get("pipelinePeakReservedGiB")
        print(json.dumps(row, ensure_ascii=False))


if __name__ == "__main__":
    main()
