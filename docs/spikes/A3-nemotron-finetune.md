# A3 — Nemotron 3.5 fine-tune under a 24 GB cap, then ONNX parity

Status: todo
Box: 2 days

## Goal
Confirm the training and export path on the staging card while other services stay resident.

## Setup
NeMo Speech 26.07 container; nvidia/nemotron-3.5-asr-streaming-0.6b pinned; a 20-hour Hebrew subset as Lhotse Shar; staging card RTX PRO 5000 Blackwell 48 GB with the vLLM service resident (≈ 24 GB), so memory fraction 0.5 (24 GB).

## Steps
1. Run OOMptimizer under the cap; record bucket batch sizes. 2. Fine-tune 500 steps with init_from_nemo_model, bf16, explicit Noam scale (log the computed peak LR). 3. Evaluate in streaming at [56,0] on a held-out set. 4. Export to ONNX; run parity on 200 utterances. 5. Load into Triton with sequence batching; measure p50/p95 at 80 ms chunks.

## Acceptance
Training runs without OOM; resident services untouched; ONNX WER within 0.1 point of NeMo; Triton serves streaming.

## Record
Batch sizes; steps per second; WER before/after 500 steps; parity delta; Triton latencies.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
