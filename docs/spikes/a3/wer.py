"""WER with a stated Hebrew normalisation, over the JSON lines NeMo's streaming script writes (pred_text, text).

  python wer.py <streaming_out.json> [more.json ...]      (jiwer from /work/pylib)

Normalisation (applied to reference and hypothesis alike), chosen for FLEURS he_il raw transcriptions:
  1. Unicode NFKC; strip any leftover `<xx-XX>` language tags.
  2. Remove Hebrew niqqud/cantillation (U+0591-U+05C7 except maqaf U+05BE) and bidi control marks.
  3. Maqaf (U+05BE) and hyphens/dashes -> space.
  4. Remove geresh/gershayim and ASCII quote variants (׳ ״ ' " ` ’ ‘ “ ”) - they mark abbreviations inconsistently.
  5. Remove all remaining punctuation (Unicode category P*) and symbols (S*) except %; lowercase Latin.
  6. Collapse whitespace. Digits are kept as written (no number verbalisation on either side).
"""

from __future__ import annotations

import json
import re
import sys
import unicodedata

sys.path.insert(0, "/work/pylib")
import jiwer  # noqa: E402

TAG = re.compile(r"<[a-z]{2,3}-[A-Z]{2}>|<auto>")
NIQQUD = re.compile(r"[֑-ֽֿ-ׇ]")
BIDI = re.compile(r"[‎‏‪-‮⁦-⁩]")
DASH = re.compile(r"[־\-‐-―]")
QUOTES = re.compile(r"[׳״'\"`‘’“”]")


def norm(s: str) -> str:
    s = unicodedata.normalize("NFKC", s)
    s = TAG.sub(" ", s)
    s = BIDI.sub("", NIQQUD.sub("", s))
    s = QUOTES.sub("", DASH.sub(" ", s))
    s = "".join(" " if (unicodedata.category(c)[0] in "PS" and c != "%") else c for c in s)
    return re.sub(r"\s+", " ", s.lower()).strip()


def score(rows: list[dict]) -> dict:
    refs = [norm(r["text"]) for r in rows]
    hyps = [norm(r["pred_text"]) for r in rows]
    keep = [i for i, r in enumerate(refs) if r]
    refs, hyps = [refs[i] for i in keep], [hyps[i] for i in keep]
    o = jiwer.process_words(refs, hyps)
    c = jiwer.process_characters(refs, hyps)
    return {"utts": len(refs), "wer": round(o.wer * 100, 2), "cer": round(c.cer * 100, 2),
            "sub": o.substitutions, "del": o.deletions, "ins": o.insertions,
            "ref_words": sum(len(r.split()) for r in refs)}


if __name__ == "__main__":
    for p in sys.argv[1:]:
        rows = [json.loads(x) for x in open(p, encoding="utf-8")]
        print(json.dumps({"file": p, **score(rows)}, ensure_ascii=False))
