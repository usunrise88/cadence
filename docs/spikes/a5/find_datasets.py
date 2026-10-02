"""List the dataset artifacts in the stand's content store (read-only): manifest hash, name, locale, clips.
   docker run --rm --user 65532:65532 -v /cadence/stand/artifacts:/stand:ro -v $PWD/docs/spikes/a5:/a5:ro \
     --entrypoint python cadence/worker:635702e /a5/find_datasets.py"""

import json
import os

root = "/stand/cas/b3"
for d in sorted(os.listdir(root)):
    for f in os.listdir(os.path.join(root, d)):
        p = os.path.join(root, d, f)
        try:
            if os.path.getsize(p) > 64 << 20:
                continue
            with open(p, "rb") as fh:
                head = fh.read(10)
                if head != b'{"files":[':
                    continue
                m = json.loads(head + fh.read())
        except Exception:
            continue
        files = {x["path"]: x["hash"] for x in m.get("files", [])}
        if "dataset.json" not in files:
            continue
        h = files["dataset.json"].removeprefix("b3:")
        try:
            with open(os.path.join(root, h[:2], h)) as fh:
                hdr = json.load(fh)
        except Exception as e:
            hdr = {"err": str(e)}
        print(
            json.dumps(
                {
                    "artifact": "b3:" + f,
                    "name": hdr.get("name"),
                    "locale": hdr.get("locale") or hdr.get("language"),
                    "clips": len(files) - 2,
                    "hours": hdr.get("hours"),
                },
                ensure_ascii=False,
            )
        )
