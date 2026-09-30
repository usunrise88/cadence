"""A3 step 0: build a ~20 h Hebrew training subset + held-out test set as NeMo manifests and Lhotse Shar.

Run inside the NeMo image:  docs/spikes/a3/nemo.sh python /spike/prep_data.py
Sources (all pinned to a dataset revision; licences recorded in the spike Result):
  * google/fleurs he_il        CC-BY-4.0   train+dev -> train, test -> held-out test
  * fsicoli/common_voice_17_0  CC0-1.0     he train+dev (validated clips only)
  * ivrit-ai/crowd-recital     ivrit.ai licence v2 (CC-BY-4.0 + "AI training or academic research only", no
      deep-fakes). Gated per repo: the host account had accepted this one (its -whisper-training derivative was
      NOT accepted -> 403, so we cut the source sessions ourselves). Read Wikipedia sentences, Stable-Whisper
      aligned; consecutive aligned sentences are merged into <= 20 s clips. Audio is browser Opus/AAC in .mka,
      which libsndfile cannot read and the image's torchcodec cannot load (no FFmpeg libs in the image), so PyAV
      (pip --target /work/pylib) decodes it.
Every clip gets `lang` and `target_lang` = he-IL: the Nemotron 3.5 prompt model reads the language prompt from
`target_lang` (train_ds.lang_field) for NeMo manifests and from `supervision.language` for Lhotse cuts.
Output: /work/data/{wav,manifests,shar}. Audio is written once as 16 kHz mono 16-bit WAV.
"""

from __future__ import annotations

import csv
import io
import json
import re
import sys
import tarfile
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

import numpy as np
import soundfile as sf
from huggingface_hub import hf_hub_download

OUT = Path("/work/data")
LANG = "he-IL"
SR = 16000
FLEURS_REV = "70bb2e84b976b7e960aa89f1c648e09c59f894dd"
CV_REV = "8262c16bf297c87a9cd88c51997c4758ed7a8ba2"
IVRIT_REV = "34d93a876ae075a1a7904e7ec2c54d2af1c9d4d7"  # ivrit-ai/crowd-recital
TARGET_TRAIN_HOURS = 20.0
MAX_DUR = 20.0  # the base model was trained with max_duration 20 (model_config.yaml train_ds)


def to_mono16k(data: np.ndarray, sr: int) -> np.ndarray:
    if data.ndim > 1:
        data = data.mean(axis=1)
    if sr != SR:
        import librosa

        data = librosa.resample(data.astype(np.float32), orig_sr=sr, target_sr=SR)
    return data.astype(np.float32)


def write_wav(path: Path, data: np.ndarray) -> float:
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(path, data, SR, subtype="PCM_16")
    return len(data) / SR


def row(path: Path, dur: float, text: str, source: str) -> dict:
    return {"audio_filepath": str(path), "duration": round(dur, 3), "text": text, "lang": LANG,
            "target_lang": LANG, "source": source}


def fleurs(split_tsv: str, split_tar: str, name: str) -> list[dict]:
    tsv = hf_hub_download("google/fleurs", f"data/he_il/{split_tsv}.tsv", repo_type="dataset", revision=FLEURS_REV)
    tgz = hf_hub_download("google/fleurs", f"data/he_il/audio/{split_tar}.tar.gz", repo_type="dataset",
                          revision=FLEURS_REV)
    texts = {}
    with open(tsv, encoding="utf-8") as f:
        for r in csv.reader(f, delimiter="\t", quoting=csv.QUOTE_NONE):
            texts[r[1]] = r[2]  # file_name -> raw_transcription (punctuated)
    out = []
    with tarfile.open(tgz) as tf:
        for m in tf:
            if not m.isfile() or not m.name.endswith(".wav"):
                continue
            fn = Path(m.name).name
            if fn not in texts:
                continue
            data, sr = sf.read(io.BytesIO(tf.extractfile(m).read()))
            p = OUT / "wav" / f"fleurs_{name}" / fn
            out.append(row(p, write_wav(p, to_mono16k(data, sr)), texts[fn].strip(), f"fleurs_{name}"))
    return out


def _cv_one(args: tuple[bytes, str, str]) -> dict | None:
    blob, fn, text = args
    data, sr = sf.read(io.BytesIO(blob))
    p = OUT / "wav" / "cv17" / fn.replace(".mp3", ".wav")
    dur = write_wav(p, to_mono16k(data, sr))
    return row(p, dur, text, "cv17") if 0.3 < dur <= MAX_DUR else None


def common_voice(split: str) -> list[dict]:
    tsv = hf_hub_download("fsicoli/common_voice_17_0", f"transcript/he/{split}.tsv", repo_type="dataset",
                          revision=CV_REV)
    tar = hf_hub_download("fsicoli/common_voice_17_0", f"audio/he/{split}/he_{split}_0.tar", repo_type="dataset",
                          revision=CV_REV)
    with open(tsv, encoding="utf-8") as f:
        texts = {r["path"]: r["sentence"] for r in csv.DictReader(f, delimiter="\t", quoting=csv.QUOTE_NONE)}
    jobs = []
    with tarfile.open(tar) as tf:
        for m in tf:
            fn = Path(m.name).name
            if m.isfile() and fn in texts:
                jobs.append((tf.extractfile(m).read(), fn, texts[fn].strip()))
    with ProcessPoolExecutor(16) as ex:
        return [r for r in ex.map(_cv_one, jobs, chunksize=32) if r]


def _decode_any(path: str) -> np.ndarray:
    sys.path.insert(0, "/work/pylib")
    import av

    with av.open(path) as c:
        rs = av.AudioResampler(format="flt", layout="mono", rate=SR)
        chunks = []
        for frame in c.decode(audio=0):
            for f in rs.resample(frame):
                chunks.append(f.to_ndarray().reshape(-1))
        for f in rs.resample(None):
            chunks.append(f.to_ndarray().reshape(-1))
    return np.concatenate(chunks) if chunks else np.zeros(0, np.float32)


def _recital_session(sid: str) -> list[dict]:
    try:
        aj = hf_hub_download("ivrit-ai/crowd-recital", f"{sid}/transcript.aligned.json", repo_type="dataset",
                             revision=IVRIT_REV)
        am = hf_hub_download("ivrit-ai/crowd-recital", f"{sid}/audio.mka", repo_type="dataset", revision=IVRIT_REV)
        segs = [x for x in json.load(open(aj, encoding="utf-8"))["segments"] if x["text"].strip()]
        audio = _decode_any(am)
    except Exception as e:  # a broken session must not stop the build
        print("skip", sid, type(e).__name__, e)
        return []
    out, cur = [], []

    def flush() -> None:
        if not cur:
            return
        st, en = max(0.0, cur[0]["start"] - 0.15), cur[-1]["end"] + 0.15
        clip = audio[int(st * SR): int(en * SR)]
        text = re.sub(r"\s+", " ", " ".join(x["text"].strip() for x in cur)).strip()
        if 0.5 < len(clip) / SR <= MAX_DUR and text:
            p = OUT / "wav" / "ivrit_recital" / sid / f"{int(st * 100):07d}.wav"
            out.append(row(p, write_wav(p, clip), text, "ivrit_recital"))
        cur.clear()

    for x in segs:
        if cur and (x["end"] - cur[0]["start"] > MAX_DUR - 0.5 or x["start"] - cur[-1]["end"] > 1.0):
            flush()
        cur.append(x)
    flush()
    return out


def ivrit(hours_needed: float) -> list[dict]:
    from huggingface_hub import HfApi

    files = HfApi().list_repo_files("ivrit-ai/crowd-recital", repo_type="dataset", revision=IVRIT_REV)
    sessions = sorted({f.split("/")[0] for f in files if f.endswith("/audio.mka")})
    out: list[dict] = []
    total = 0.0
    with ProcessPoolExecutor(12) as ex:
        for i in range(0, len(sessions), 24):
            for rows in ex.map(_recital_session, sessions[i:i + 24]):
                out += rows
                total += sum(r["duration"] for r in rows)
            print(f"recital sessions {i + 24}: {total / 3600:.2f} h", flush=True)
            if total / 3600 >= hours_needed:
                break
    return out


def cached(name: str, fn):  # type: ignore[no-untyped-def]
    p = OUT / "manifests" / f"_part_{name}.json"
    if p.exists():
        return [json.loads(x) for x in open(p, encoding="utf-8")]
    rows = fn()
    dump(rows, f"_part_{name}")
    return rows


def dump(rows: list[dict], name: str) -> None:
    (OUT / "manifests").mkdir(parents=True, exist_ok=True)
    with open(OUT / "manifests" / f"{name}.json", "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
    by = {}
    for r in rows:
        by[r["source"]] = by.get(r["source"], 0) + r["duration"]
    print(name, len(rows), "utts", f"{sum(by.values()) / 3600:.2f} h", {k: round(v / 3600, 2) for k, v in by.items()})


def to_shar(name: str, shard_size: int = 1000) -> None:
    """NeMo manifest -> Lhotse Shar (flac audio). supervision.language carries the prompt key.

    The training text ends with the language tag token (` <he-IL>`): the base model emits `<xx-XX>` after the
    terminal punctuation (every locale tag is a single piece in the tokenizer, see the spike Result), so targets
    keep the pretraining format and decoding strips it (decoding.strip_lang_tags=true).
    """
    from lhotse import CutSet, Recording, SupervisionSegment

    def cuts():
        with open(OUT / "manifests" / f"{name}.json", encoding="utf-8") as f:
            for i, line in enumerate(f):
                r = json.loads(line)
                rec = Recording.from_file(r["audio_filepath"], recording_id=f"{name}-{i:07d}")
                c = rec.to_cut()
                c.supervisions = [SupervisionSegment(id=rec.id, recording_id=rec.id, start=0,
                                                     duration=rec.duration, text=f"{r['text']} <{LANG}>",
                                                     language=LANG,
                                                     custom={"source": r["source"]})]
                yield c

    out = OUT / "shar" / name
    out.mkdir(parents=True, exist_ok=True)
    CutSet(cuts()).to_shar(out, fields={"recording": "flac"}, shard_size=shard_size, num_jobs=8)
    print("shar", name, "->", out)


def main() -> None:
    what = sys.argv[1:] or ["manifests", "shar"]
    if "manifests" in what:
        test = cached("fleurs_test", lambda: fleurs("test", "test", "test"))
        dump(test, "test_fleurs")
        # the first 200 FLEURS dev clips are the trainer's validation set (a liveness signal only; the real
        # held-out evaluation is test_fleurs with the streaming decoder); the rest of dev goes into train
        dev = cached("fleurs_dev", lambda: fleurs("dev", "dev", "dev"))
        dump(dev[:200], "val")
        train = cached("fleurs_train", lambda: fleurs("train", "train", "train")) + dev[200:]
        train += cached("cv17", lambda: common_voice("train") + common_voice("dev"))
        have = sum(r["duration"] for r in train) / 3600
        print(f"fleurs+cv: {have:.2f} h; topping up from ivrit.ai crowd-recital")
        train += cached("ivrit_recital", lambda: ivrit(TARGET_TRAIN_HOURS - have))
        dump(train, "train")
    if "shar" in what:
        to_shar("train")


if __name__ == "__main__":
    main()
