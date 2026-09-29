# F1 — A second training framework through the seams (k2/icefall)

Status: deferred (owner, 2026-09-29: packs beyond NeMo wait without a phase; the seams are built in phase 2)
Box: 3 days

## Goal
Test that R40–R46 hold for a framework that is not NeMo, before any framework pack beyond NeMo is planned: nothing in
the control plane, the web client or the scorers should need a change.

## Setup
- A k2/icefall runtime image of its own, so it upgrades independently of NeMo:
  - base: CUDA 13.2 with the PyTorch the k2 dev wheels are built for (`1.24.4.dev20260710`, torch 2.12/2.13);
  - Lhotse;
  - icefall at a pinned commit (it is not pip-installable: clone plus `PYTHONPATH`).
- The image runs on the staging host beside the NeMo runtime.
- A 2-hour FLEURS he subset as Shar (audio and text only, as frozen versions are).
- The CPU `toy` pack's conformance suite.

## Steps
1. Add a Shar data module to the pack. icefall reads per-recipe manifests with precomputed fbank and has no Shar
   reader, so the module uses `CutSet.from_shar`, a dynamic bucketing sampler and on-the-fly fbank.
2. Train a BPE tokenizer (`tokenizers.new`, SentencePiece). Then train a small causal Zipformer transducer from
   scratch for one epoch:
   - one card, bf16, several chunk sizes;
   - metrics tailed from its TensorBoard files and posted through the worker;
   - `train_args.json` stored with every checkpoint, because export needs every architecture argument again.
3. Average with `--use-averaged-model`. Decode the validation set in streaming at two chunk sizes into the `hypotheses`
   artifact, and score it with the shared scorer.
4. Export with `export-onnx-streaming.py`, decode the same set with sherpa-onnx, and run parity.
5. Run the whole conformance suite and list every change the control plane, web client or scorers needed. Note also:
   - the image size;
   - whether the k2 wheel would also install into the NeMo image (torch 2.12 + CUDA 13.2), as a fallback.

## Acceptance
- The conformance suite passes.
- The list of changes outside the pack is empty, or each item on it becomes a seam fix in the spec.
- The latency profiles map icefall's chunk arguments to milliseconds.

## Record
- Image size and build time.
- Step durations and throughput per card.
- WER after one epoch (a sanity check only).
- Latency profile mapping.
- The change list.

## Result
_(fill in)_
