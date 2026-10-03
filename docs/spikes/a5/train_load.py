"""A5 step 5: a real Nemotron fine-tuning load under the pack's memory cap, beside a live session.
Optimiser steps on the spike's clips (FLEURS he + ru, padded batches of 4, real token ids, the prompt index per clip)
in bf16 autocast with AdamW, through the model's own ``training_step`` (the pack's calibrate/OOMptimizer path), capped
with ``cadence_nemo.gpu.apply_cap`` (allocator fraction with the CUDA context reserve). Logs a JSON line every 10 steps.
   docs/spikes/a5/run.sh --gpu --name a5-train python /a5/train_load.py --cap-mb 14000 --seconds 600
"""

import argparse
import json
import os
import random
import time
from pathlib import Path

import lightning.pytorch as pl
import soundfile as sf
import torch

from cadence_nemo import gpu, training

ap = argparse.ArgumentParser()
ap.add_argument("--cap-mb", type=int, default=14000)
ap.add_argument("--reserve-mb", type=int, default=1000)
ap.add_argument("--seconds", type=float, default=600)
ap.add_argument("--batch", type=int, default=2)
ap.add_argument("--max-dur", type=float, default=9.0)
a = ap.parse_args()

applied = gpu.apply_cap(a.cap_mb, a.reserve_mb)
print(json.dumps({"cap": applied}), flush=True)
pl.seed_everything(1)
trainer = pl.Trainer(barebones=True, accelerator="gpu", devices=1, logger=False)
trainer.log_every_n_steps = 10**6
t0 = time.time()
model = training.load_model(Path(os.environ["A5_MODEL"]), "cuda", trainer=trainer)
facts = training.model_facts(model)
print(json.dumps({"loadS": round(time.time() - t0, 1)}), flush=True)
clips = []
for tag in ("he", "ru"):
    for line in Path(f"/work/clips/{tag}/manifest.jsonl").read_text(encoding="utf-8").splitlines():
        r = json.loads(line)
        x, _ = sf.read(f"/work/clips/{tag}/{r['audio']}", dtype="float32")
        if x.size > a.max_dur * 16000:
            continue
        ids = model.tokenizer.text_to_ids(r["text"] + f" <{r['language']}>")
        clips.append((torch.from_numpy(x), ids, facts.prompt_dictionary[r["language"]]))
optimizer, _ = model.setup_optimization({"name": "adamw", "lr": 1e-6, "weight_decay": 1e-3})
model.train()
rng = random.Random(1)
step = 0
start = time.time()
last = start
while time.time() - start < a.seconds:
    batch = rng.sample(clips, a.batch)
    n = max(c[0].numel() for c in batch)
    u = max(len(c[1]) for c in batch)
    audio = torch.zeros(a.batch, n)
    toks = torch.zeros(a.batch, u, dtype=torch.long)
    for i, (x, ids, _) in enumerate(batch):
        audio[i, : x.numel()] = x
        toks[i, : len(ids)] = torch.tensor(ids)
    data = (
        audio.cuda(),
        torch.tensor([c[0].numel() for c in batch], device="cuda"),
        toks.cuda(),
        torch.tensor([len(c[1]) for c in batch], device="cuda"),
        torch.tensor([c[2] for c in batch], device="cuda"),
    )
    optimizer.zero_grad(set_to_none=True)
    with torch.autocast("cuda", dtype=torch.bfloat16):
        out = model.training_step(data, step)
    out["loss"].sum().backward()
    optimizer.step()
    step += 1
    if step % 10 == 0:
        torch.cuda.synchronize()
        now = time.time()
        print(
            json.dumps(
                {
                    "step": step,
                    "loss": round(float(out["loss"].detach().float().mean()), 3),
                    "stepsPerS": round(10 / (now - last), 2),
                    **gpu.peak_mb(),
                    "t": round(now - start, 1),
                }
            ),
            flush=True,
        )
        last = now
print(json.dumps({"done": step, "seconds": round(time.time() - start, 1), **gpu.peak_mb()}), flush=True)
