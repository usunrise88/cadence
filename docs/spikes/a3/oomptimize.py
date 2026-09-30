"""A3 step 1: OOMptimizer under the cap.

  docs/spikes/a3/nemo.sh --gpu python /spike/oomptimize.py -n /work/model/nemotron-3.5-asr-streaming-0.6b.nemo \
      --no-ddp -b "[4,6,8,10,12,14,16,18,20]" -r 14 -s 16 -f 0.4337   # fraction = cap.py allocator cap

Wraps NeMo's scripts/speech_recognition/oomptimizer.py (v3.0.0) with two fixes the prompt model needs:
  * EncDecRNNTBPEModelWithPrompt inherits ASRModel.oomptimizer_schema (4 tensors) but its training_step unpacks 5
    (audio, audio_len, tokens, token_len, prompt_indices). We append a placeholder and fill it with the he-IL prompt
    index (64) for the whole batch, so the profiled step is the real one (prompt kernel included).
  * the batch search stops at 0 instead of looping forever when even batch 1 does not fit (see _patch_generator).
  * cuFFT errors raised from the STFT under the cap are reported as OOM (see _step).
  * `-f/--memory-fraction` is the fraction of the WHOLE card; we pass the one cap.py computes for the allocator cap.
Pack note: the step kind `oomptimizer_calibrate` should own this shim until upstream adds the prompt to the schema.
"""

from __future__ import annotations

import runpy
import sys

import torch

from nemo.collections.asr.models.rnnt_bpe_models_prompt import EncDecRNNTBPEModelWithPrompt
from nemo.core.neural_types import AudioSignal, LabelsType, LengthsType, NeuralType

HE_IL = 64


def _schema(self: EncDecRNNTBPEModelWithPrompt) -> dict:
    return {
        "cls": tuple,
        "inputs": [
            {"type": NeuralType(("B", "T"), AudioSignal()), "seq_length": "input"},
            {"type": NeuralType(("B",), LengthsType()), "seq_length": "input"},
            {"type": NeuralType(("B", "T"), LabelsType()), "seq_length": "output",
             "vocab_size": self.tokenizer.vocab_size},
            {"type": NeuralType(("B",), LengthsType()), "seq_length": "output"},
            {"type": "dummy", "seq_length": "input"},  # prompt_indices, filled below
        ],
    }


_orig_step = EncDecRNNTBPEModelWithPrompt.training_step


def _step(self, batch, batch_nb):  # type: ignore[no-untyped-def]
    if len(batch) == 5 and batch[4].numel() == 0:
        b = batch[0].shape[0]
        batch = (*batch[:4], torch.full((b,), HE_IL, dtype=torch.long, device=batch[0].device))
    try:
        return _orig_step(self, batch, batch_nb)
    except RuntimeError as e:
        # Under a per-process memory fraction, cuFFT fails its workspace allocation with CUFFT_INVALID_SIZE (seen
        # on sm_120 / CUDA 13.2); upstream only maps CUFFT_INTERNAL_ERROR to OOM. Treat both as OOM.
        if "cuFFT error" in str(e):
            raise torch.cuda.OutOfMemoryError(str(e)) from e
        raise


def _patch_generator(globs: dict) -> None:
    """Upstream halves the batch on OOM with round(); from 1 it reaches 0 and loops forever (an empty STFT raises
    CUFFT_INVALID_SIZE, counted as OOM). Stop at 0 and report the bucket as infeasible under the cap."""
    gen = globs["ProfilingBatchGenerator"]
    orig = gen.advance

    def advance(self, oom: bool) -> bool:  # type: ignore[no-untyped-def]
        done = orig(self, oom)
        if not done and self._current < 1:
            self._max_ok, self._min_err = 0, 1
            return True
        return done

    gen.advance = advance


EncDecRNNTBPEModelWithPrompt.oomptimizer_schema = property(_schema)  # type: ignore[assignment]
EncDecRNNTBPEModelWithPrompt.training_step = _step  # type: ignore[method-assign]

sys.argv = ["oomptimizer.py", *sys.argv[1:]]
mod = runpy.run_path("/work/NeMo/scripts/speech_recognition/oomptimizer.py", run_name="oomptimizer_lib")
_patch_generator(mod)
mod["oomptimizer"]()
