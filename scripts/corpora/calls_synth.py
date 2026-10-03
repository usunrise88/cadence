"""Build synthetic stereo "calls" for the telephone path while no real calls exist (phase-4 plan, decision 9).

Channel 0 (the caller) is Serbian read speech from FLEURS (dev split: never the golden test split, not the training
split); channel 1 (the bot) is the host's local TTS (OmniVoice, OpenAI-compatible /v1/audio/speech) speaking a fixed
script. Both are mixed to an 8 kHz G.711 mu-law stereo WAV, the way a call recorder stores one party per channel, with
turn-taking gaps, a little crosstalk and a low line-noise floor.

Per call the corpora mount gets:
  calls/<id>.wav            8 kHz, 2 channels, mu-law (WAV format 7)
  calls/<id>.cadence.json   the sdp_ingest sidecar: roles, speakers, language, and the bot's TTS script
  truth/<id>.json           the caller turns' reference text, outside the ingested tree (for measuring pseudo-labels
                            and annotation agreement, never read by Cadence)

  uv run --with numpy --with scipy scripts/corpora/calls_synth.py --fleurs /cadence/corpora/fleurs-sr/<rev> \
      --out /cadence/corpora/calls-synth-sr --calls 40 [--tts http://127.0.0.1:30020] [--seed 7]
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import io
import json
import random
import sys
import urllib.request
from pathlib import Path

import numpy as np
from scipy.io import wavfile
from scipy.signal import resample_poly

RATE = 8000

# What a bank's or a delivery service's voice bot says; Serbian, Latin script (the training data's script).
BOT_OPENINGS = [
    "Dobar dan, ovde je automatska služba korisničke podrške. Kako mogu da vam pomognem?",
    "Zdravo, dobili ste servis za dostavu. Recite ukratko razlog vašeg poziva.",
    "Dobar dan, vaš poziv je važan za nas. Šta vas zanima danas?",
]
BOT_TURNS = [
    "Razumem. Možete li da mi kažete vaše ime i prezime?",
    "Hvala. Molim vas, recite broj vašeg ugovora.",
    "U redu, proveravam podatke. Da li je adresa za dostavu i dalje ista?",
    "Možete li da ponovite, nisam vas dobro razumela?",
    "Hvala vam. Da li želite da vas povežem sa operaterom?",
    "Vaš zahtev je zabeležen. Da li vam mogu pomoći još nešto?",
    "Kada vam odgovara da vas kontaktiramo, pre ili posle podne?",
    "Proveravam stanje vašeg računa, sačekajte trenutak.",
    "Da li ste zadovoljni našom uslugom, od jedan do pet?",
]
BOT_CLOSINGS = [
    "Hvala na pozivu i prijatan dan.",
    "Zahvaljujemo se, doviđenja.",
]


def tts(url: str, text: str, cache: Path) -> np.ndarray:
    """The bot's turn at 8 kHz float, cached by text."""
    key = hashlib.sha256(text.encode()).hexdigest()[:16]
    f = cache / f"{key}.wav"
    if not f.exists():
        body = json.dumps({"model": "omnivoice", "input": text, "voice": "default", "response_format": "wav"}).encode()
        req = urllib.request.Request(f"{url}/v1/audio/speech", body, {"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=120) as r:
            f.write_bytes(r.read())
    return read_wav(f.read_bytes())


def read_wav(b: bytes) -> np.ndarray:
    """Any PCM or float WAV as mono float at 8 kHz."""
    rate, raw = wavfile.read(io.BytesIO(b))
    x = raw.astype(np.float64)
    if x.ndim == 2:
        x = x.mean(axis=1)
    scale = {np.dtype(np.int16): 32768.0, np.dtype(np.int32): 2.0**31, np.dtype(np.uint8): 128.0}.get(raw.dtype)
    if scale:
        x = (x - 128.0) / scale if raw.dtype == np.uint8 else x / scale
    g = np.gcd(rate, RATE)
    return resample_poly(x, RATE // g, rate // g)


def mulaw(x: np.ndarray) -> bytes:
    """ITU-T G.711 mu-law: sign, 3-bit segment, 4-bit mantissa, bits inverted (bias 0x84, clip 32635)."""
    s = np.round(np.clip(x, -1.0, 1.0) * 32767).astype(np.int32)
    sign = np.where(s < 0, 0x80, 0)
    m = np.minimum(np.abs(s), 32635) + 0x84
    exp = np.clip(np.floor(np.log2(m)).astype(np.int32) - 7, 0, 7)
    mant = (m >> (exp + 3)) & 0x0F
    return ((~(sign | (exp << 4) | mant)) & 0xFF).astype(np.uint8).tobytes()


def mulaw_decode(b: bytes) -> np.ndarray:
    u = ~np.frombuffer(b, dtype=np.uint8).astype(np.int32) & 0xFF
    exp, mant = (u >> 4) & 0x07, u & 0x0F
    mag = (((mant << 3) + 0x84) << exp) - 0x84
    return np.where(u & 0x80, -mag, mag) / 32768.0


def write_mulaw_stereo(path: Path, left: np.ndarray, right: np.ndarray) -> None:
    n = max(len(left), len(right))
    st = np.zeros((n, 2))
    st[: len(left), 0] = left
    st[: len(right), 1] = right
    data = mulaw(st.reshape(-1))
    fmt = (7).to_bytes(2, "little") + (2).to_bytes(2, "little") + RATE.to_bytes(4, "little")
    fmt += (RATE * 2).to_bytes(4, "little") + (2).to_bytes(2, "little") + (8).to_bytes(2, "little") + (0).to_bytes(2, "little")
    fact = (n).to_bytes(4, "little")
    chunks = b"fmt " + len(fmt).to_bytes(4, "little") + fmt + b"fact" + (4).to_bytes(4, "little") + fact
    chunks += b"data" + len(data).to_bytes(4, "little") + data + (b"\0" if len(data) % 2 else b"")
    path.write_bytes(b"RIFF" + (4 + len(chunks)).to_bytes(4, "little") + b"WAVE" + chunks)


def level(x: np.ndarray, dbfs: float) -> np.ndarray:
    rms = np.sqrt(np.mean(x**2)) or 1.0
    return x * (10 ** (dbfs / 20) / rms)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--fleurs", type=Path, required=True, help="the fetched FLEURS revision directory (dev.tsv, dev/)")
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--calls", type=int, default=40)
    ap.add_argument("--tts", default="http://127.0.0.1:30020")
    ap.add_argument("--seed", type=int, default=7)
    a = ap.parse_args()
    rng = random.Random(a.seed)

    rows = list(csv.reader((a.fleurs / "dev.tsv").open(encoding="utf-8"), delimiter="\t"))
    rows = [r for r in rows if len(r) >= 3 and (a.fleurs / "dev" / r[1]).exists()]
    if len(rows) < 10:
        print(f"too few FLEURS dev rows with audio under {a.fleurs}", file=sys.stderr)
        return 1
    rev = hashlib.sha256(f"{a.fleurs.name}:{a.seed}:{a.calls}".encode()).hexdigest()[:12]
    root = a.out / rev
    calls, truth, cache = root / "calls", root / "truth", a.out / ".tts-cache"
    for d in (calls, truth, cache):
        d.mkdir(parents=True, exist_ok=True)

    for i in range(a.calls):
        cid = f"call-{i:04d}"
        if (calls / f"{cid}.cadence.json").exists():
            continue
        caller, bot = np.zeros(0), np.zeros(0)
        script, refs = [], []
        t = rng.uniform(0.3, 0.8)

        def place(track: np.ndarray, x: np.ndarray, at: float) -> np.ndarray:
            s = int(at * RATE)
            if len(track) < s + len(x):
                track = np.concatenate([track, np.zeros(s + len(x) - len(track))])
            track[s : s + len(x)] += x
            return track

        bot_lines = [rng.choice(BOT_OPENINGS), *rng.sample(BOT_TURNS, rng.randint(3, 6)), rng.choice(BOT_CLOSINGS)]
        for k, line in enumerate(bot_lines):
            x = level(tts(a.tts, line, cache), -20)
            bot = place(bot, x, t)
            script.append({"channel": 1, "start": round(t, 3), "end": round(t + len(x) / RATE, 3), "text": line})
            t += len(x) / RATE
            if k == len(bot_lines) - 1:
                break
            r = rng.choice(rows)
            y = level(read_wav((a.fleurs / "dev" / r[1]).read_bytes()), rng.uniform(-26, -16))
            # Mostly a pause before the caller answers; sometimes the caller barges in (crosstalk).
            start = t - rng.uniform(0.2, 0.6) if rng.random() < 0.12 else t + rng.uniform(0.3, 1.2)
            caller = place(caller, y, start)
            refs.append({"channel": 0, "start": round(start, 3), "end": round(start + len(y) / RATE, 3), "text": r[2], "normalized": r[3] if len(r) > 3 else "", "fleurs": r[1]})
            t = start + len(y) / RATE + rng.uniform(0.3, 0.9)
        n = max(len(caller), len(bot)) + int(0.5 * RATE)
        noise = np.random.default_rng(a.seed + i).normal(0, 10 ** (-55 / 20), (2, n))
        left = np.pad(caller, (0, n - len(caller))) + noise[0]
        right = np.pad(bot, (0, n - len(bot))) + noise[1]
        write_mulaw_stereo(calls / f"{cid}.wav", left, right)
        side = {"roles": ["caller", "bot"], "speakers": {"0": f"{cid}-caller", "1": "bot"}, "language": "sr-RS", "script": script}
        (calls / f"{cid}.cadence.json").write_text(json.dumps(side, ensure_ascii=False, indent=1))
        (truth / f"{cid}.json").write_text(json.dumps({"call": cid, "turns": refs}, ensure_ascii=False, indent=1))
        print(f"{cid}: {n / RATE:.1f} s, {len(script)} bot turns, {len(refs)} caller turns")

    (root / "SOURCE.yaml").write_text(
        "name: calls-synth-sr\nlicence: CC-BY-4.0\nkind: synthetic\nlanguages: [sr-RS]\n"
        f"revision: {rev}\nurl: scripts/corpora/calls_synth.py\n"
        "note: >\n  Synthetic stereo calls: channel 0 FLEURS sr_rs dev read speech (CC BY 4.0), channel 1 the host's\n"
        "  OmniVoice TTS speaking a fixed bot script; 8 kHz G.711 mu-law. Ingest calls/ only; truth/ holds the caller\n"
        "  references for measuring pseudo-labels and annotation. Not real calls: never the telephone golden set.\n"
    )
    print(f"done: {root}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
