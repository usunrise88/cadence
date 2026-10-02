"""A5 debug 2: where does the pipeline path lose the words? Compare its feature chunks and encoder output with the
pack's loop on the same model instance."""

import os
import sys

sys.path.insert(0, "/a5")
import soundfile as sf
import torch

from live_core import build_pipeline

clip = "/work/clips/ru/ru01.wav"
pipe, _ = build_pipeline(os.environ["A5_MODEL"], "320ms")
w = pipe.asr_model
m = w.asr_model
print("model dtype", m.dtype, "cast", w.cast_dtype, "concat", getattr(m, "concat", None), "prompt idx", getattr(m, "_inference_prompt_index", None))
print("streaming_cfg", m.encoder.streaming_cfg)
print("pipe use_cache", pipe.use_cache, "buffer s", pipe.buffer_size_in_secs, "pre_encode", pipe.pre_encode_cache_size, "drop", w.drop_extra_pre_encoded)
print("decoding cfg strategy", m.cfg.decoding.strategy if "decoding" in m.cfg else None)

from nemo.collections.asr.inference.streaming.framing.request import Frame
from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

x, _ = sf.read(clip, dtype="float32")
with torch.inference_mode():
    f, fl = m.preprocessor(input_signal=torch.from_numpy(x)[None].cuda(), length=torch.tensor([x.size]).cuda())
print("whole-file features mean %.3f std %.3f" % (f.mean(), f.std()), "preproc cfg", dict(m.cfg.preprocessor))
print("pipeline preproc cfg", dict(pipe.preprocessor_config))
n = int(pipe.chunk_size_in_secs * 16000)
orig_enc = m.encoder.cache_aware_stream_step


def spy_enc(**k):
    out = orig_enc(**k)
    ps = k["processed_signal"]
    print(
        "  feat", tuple(ps.shape), "len", k["processed_signal_length"].tolist(), "mean %.3f std %.3f" % (ps.float().mean(), ps.float().std()),
        "enc", tuple(out[0].shape), "enc_len", out[1].tolist(), "drop", k["drop_extra_pre_encoded"],
    )
    return out


m.encoder.cache_aware_stream_step = spy_enc
orig_dec = m.decoding.rnnt_decoder_predictions_tensor


def spy_dec(encoded, encoded_len, **k):
    r = orig_dec(encoded, encoded_len, **k)
    h = r[0]
    print("  dec in", tuple(encoded.shape), "y", None if h.y_sequence is None else len(h.y_sequence), "prompted std %.3f" % encoded.float().std())
    return r


m.decoding.rnnt_decoder_predictions_tensor = spy_dec
i = 0
k = 0
while i < x.size:
    fr = torch.from_numpy(x[i : i + n])
    (o,) = pipe.transcribe_step(
        [Frame(samples=fr, stream_id=1, is_first=i == 0, is_last=False, options=ASRRequestOptions(language_code="ru-RU") if i == 0 else None)]
    )
    print(i / 16000, repr(o.partial_transcript))
    i += n
    k += 1
