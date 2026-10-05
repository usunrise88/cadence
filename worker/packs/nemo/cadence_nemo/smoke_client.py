#!/usr/bin/env python3
"""The smoke client of a Nemotron deployable (``client/transcribe``; phase 5 · stream D1).

    transcribe <server url> <model name> <input file>

The delivery script runs it on the production host for each smoke utterance (02 "Delivery bundle"): it streams the
utterance's feature buffers (``cadence.nemo-chunks/1``, written by ``nemotron_parity`` from the parity sample) through
the newly loaded model as one sequence, and prints the token ids the server returned for the whole stream,
space-separated, on one line. The delivery script compares that line with the tokens the staging server returned for
the same buffers. Exit status 0 when every chunk was answered, 1 otherwise (the reason on stderr).

Python 3 standard library only (a production host has no Cadence): Triton's HTTP/REST (KServe v2) JSON protocol,
``sequence_id`` random per run, ``sequence_start`` on the first chunk and ``sequence_end`` on the last. float32 values
travel as JSON numbers: the shortest repr of a float32 widened to a double reads back to the same float32.
"""

from __future__ import annotations

import base64
import json
import random
import struct
import sys
import urllib.error
import urllib.request
from typing import Any

FORMAT = "cadence.nemo-chunks/1"


class SmokeError(Exception):
    pass


def read_chunks(path: str) -> dict[str, Any]:
    """The chunk file, checked; each chunk's ``values`` is its buffer as a list of floats (mels by frames row-major)."""
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    if not isinstance(doc, dict) or doc.get("schema") != FORMAT:
        raise SmokeError(f"{path} is not {FORMAT}")
    mels, frames = int(doc["mels"]), int(doc["frames"])
    n = mels * frames
    for k, c in enumerate(doc["chunks"]):
        raw = base64.b64decode(c["data"])
        if len(raw) != 4 * n:
            raise SmokeError(f"chunk {k}: {len(raw)} bytes, expected {4 * n}")
        c["values"] = list(struct.unpack(f"<{n}f", raw))
    if not doc["chunks"]:
        raise SmokeError(f"{path} has no chunks")
    return doc


def request_body(doc: dict[str, Any], k: int, sequence_id: int) -> dict[str, Any]:
    c = doc["chunks"][k]
    last = k == len(doc["chunks"]) - 1
    return {
        "parameters": {"sequence_id": sequence_id, "sequence_start": k == 0, "sequence_end": last},
        "inputs": [
            {"name": "audio_signal", "shape": [1, doc["mels"], doc["frames"]], "datatype": "FP32", "data": c["values"]},
            {"name": "length", "shape": [1, 1], "datatype": "INT64", "data": [int(c["length"])]},
            {"name": "prompt", "shape": [1, 1], "datatype": "INT64", "data": [int(doc["prompt"])]},
        ],
        "outputs": [{"name": "tokens"}],
    }


def tokens_of(answer: dict[str, Any]) -> list[int]:
    for o in answer.get("outputs") or []:
        if o.get("name") == "tokens":
            return [int(t) for t in o.get("data") or [] if int(t) >= 0]
    raise SmokeError("the server's answer has no tokens output")


def transcribe(url: str, model: str, path: str, timeout: float = 30.0) -> list[int]:
    doc = read_chunks(path)
    seq = random.SystemRandom().randrange(1, 2**63 - 1)
    endpoint = f"{url.rstrip('/')}/v2/models/{model}/infer"
    out: list[int] = []
    for k in range(len(doc["chunks"])):
        body = json.dumps(request_body(doc, k, seq)).encode()
        req = urllib.request.Request(endpoint, data=body, headers={"Content-Type": "application/json"}, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                answer = json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            raise SmokeError(f"chunk {k}: HTTP {e.code}: {e.read().decode(errors='replace')[:300]}") from e
        except (urllib.error.URLError, OSError) as e:
            raise SmokeError(f"chunk {k}: {e}") from e
        out += tokens_of(answer)
    return out


def main(argv: list[str]) -> int:
    if len(argv) != 4:
        print("usage: transcribe <server url> <model name> <input file>", file=sys.stderr)
        return 2
    try:
        toks = transcribe(argv[1], argv[2], argv[3])
    except (SmokeError, OSError, ValueError, KeyError) as e:
        print(f"transcribe: {e}", file=sys.stderr)
        return 1
    print(" ".join(str(t) for t in toks))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
