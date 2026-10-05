"""E1 step 3a: the benchmark's input — every chunk the pipeline decoder would send for the parity sample, precomputed.

  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/prep_feats.py <model.nemo> <onnx_dir> /work/bench/feats-80.npz [--n 200]

Features come from cadence_nemo.pipeline (PipelineStream: Features + _chunks), right-padded to the profile's buffer, so
the served model sees exactly the parity run's buffers; `real` is the audio each chunk accounts for, which paces the
client. The mel front-end is therefore outside the measured latency (see the brief: it belongs in front of the step
model in the repository the builder writes).
"""

from __future__ import annotations

import argparse
import json
import os

import numpy as np


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("model")
    ap.add_argument("onnx_dir")
    ap.add_argument("out")
    ap.add_argument("--n", type=int, default=200)
    a = ap.parse_args()
    import torch

    from cadence_nemo import pipeline
    from parity import sample

    torch.set_num_threads(8)
    geo = json.load(open(os.path.join(a.onnx_dir, "streaming_cfg.json")))
    att = [int(v) for v in geo["att_context_size"]]
    os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
    model = pipeline.load(a.model, {"p": att}, stop_history_eou_ms=800, batch_size=1, device="cpu")
    pipe = model.pipelines["p"]
    T = int(geo["buffer_frames"])
    feats, lens, first, real, clip = [], [], [], [], []
    ids = []
    for i, r in enumerate(sample(a.n)):
        s = pipeline.PipelineStream(target="A", pipeline=pipe, att=att, profile="p", language="hr-HR")
        s._open()
        assert s.feats is not None
        s.feats.push(r["audio"])
        s.feats.finish()
        for c in s._chunks(final=True):
            f = np.zeros((geo["n_mels"], T), np.float32)
            x = c.features.detach().float().cpu().numpy()
            f[:, : x.shape[1]] = x[:, :T]
            feats.append(f)
            lens.append(int(c.length))
            first.append(int(c.first))
            real.append(int(c.real))
            clip.append(i)
        ids.append(r["id"])
    np.savez(a.out, feats=np.stack(feats), length=np.array(lens, np.int64), first=np.array(first, np.int32),
             real=np.array(real, np.int64), clip=np.array(clip, np.int32), ids=np.array(ids),
             prompt=np.int64(geo["prompt_dictionary"]["hr-HR"]), chunk_ms=np.int64(geo["chunk_ms"]))
    print(f"{len(ids)} clips, {len(feats)} chunks, {sum(real) / 16000 / 3600:.3f} h -> {a.out}")


if __name__ == "__main__":
    main()
