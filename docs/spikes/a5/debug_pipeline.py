"""A5 debug: step one clip through the pipeline and print what each step yields."""

import os
import sys

sys.path.insert(0, "/a5")
import soundfile as sf
import torch

from live_core import build_pipeline

profile = sys.argv[1] if len(sys.argv) > 1 else "160ms"
clip = sys.argv[2] if len(sys.argv) > 2 else "/work/clips/he/he01.wav"
lang = sys.argv[3] if len(sys.argv) > 3 else "he-IL"
pipe, _ = build_pipeline(os.environ["A5_MODEL"], profile)
print("prompt_enabled", pipe.prompt_enabled, "chunk", pipe.chunk_size_in_secs, "lang ids", len(pipe.language_token_ids))
from nemo.collections.asr.inference.streaming.framing.request import Frame
from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

x, _ = sf.read(clip, dtype="float32")
n = int(pipe.chunk_size_in_secs * 16000)
orig = pipe.asr_model.stream_step


def spy(*a, **k):
    hyp, ctx = orig(*a, **k)
    h = hyp[0]
    print("  hyp y_sequence", None if h.y_sequence is None else list(h.y_sequence)[-8:], "ts", None if h.timestamp is None else list(h.timestamp)[-4:])
    return hyp, ctx


pipe.asr_model.stream_step = spy
i = 0
while i < x.size:
    last = i + n >= x.size
    fr = torch.zeros(n)
    seg = torch.from_numpy(x[i : i + n])
    fr[: seg.numel()] = seg
    (o,) = pipe.transcribe_step(
        [
            Frame(
                samples=fr,
                stream_id=1,
                is_first=i == 0,
                is_last=last,
                length=seg.numel(),
                options=ASRRequestOptions(language_code=lang, asr_output_granularity="word") if i == 0 else None,
            )
        ]
    )
    print(i / 16000, repr(o.partial_transcript), repr(o.final_transcript), repr(o.current_step_transcript))
    i += n
