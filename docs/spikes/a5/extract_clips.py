"""Copy N clips of a dataset artifact out of the stand's content store (read-only) into /work/clips/<tag>/ with a
manifest (audio, text, duration, language).  Run as the store's owner uid with the store mounted read-only:
   docker run --rm --user 65532:65532 -v /cadence/stand/artifacts:/stand:ro -v ~/cadence-spikes/a5:/work \
     -v $PWD/docs/spikes/a5:/a5:ro --entrypoint python cadence/worker:635702e /a5/extract_clips.py b3:<hash> ru 12
"""

import json
import os
import shutil
import sys

ROOT = "/stand/cas/b3"


def blob(h: str) -> str:
    h = h.removeprefix("b3:")
    return os.path.join(ROOT, h[:2], h)


art, tag, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
with open(blob(art)) as f:
    files = {x["path"]: x["hash"] for x in json.load(f)["files"]}
with open(blob(files["dataset.json"])) as f:
    header = json.load(f)
print("header:", json.dumps({k: header[k] for k in list(header)[:15]}, ensure_ascii=False)[:800])
out = f"/work/clips/{tag}"
os.makedirs(out, exist_ok=True)
rows = []
with open(blob(files["manifest.jsonl"]), encoding="utf-8") as f:
    lines = f.read().splitlines()
print("first row:", lines[0][:400])
for line in lines:
    r = json.loads(line)
    dur = float(r.get("duration") or r.get("durationS") or 0)
    if not 4.0 <= dur <= 15.0:
        continue
    rel = r.get("audio") or r.get("audio_filepath") or r.get("path")
    dst = os.path.join(out, f"{tag}{len(rows) + 1:02d}.wav")
    shutil.copyfile(blob(files[rel]), dst)
    rows.append(
        {"audio": os.path.basename(dst), "text": r.get("text"), "duration": dur, "language": r.get("language")}
    )
    if len(rows) >= n:
        break
with open(os.path.join(out, "manifest.jsonl"), "w", encoding="utf-8") as f:
    for r in rows:
        f.write(json.dumps(r, ensure_ascii=False) + "\n")
print(f"{len(rows)} clips -> {out}")
