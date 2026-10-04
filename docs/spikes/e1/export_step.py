"""E1 step 1: export Nemotron 3.5 ASR Streaming as ONE cache-aware streaming step graph per latency profile.

  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/export_step.py <model.nemo> <out_dir> <left,right> [--fp16]

`step.onnx` = one chunk of one or more streams: the cache-aware ConformerEncoder (forward_for_export,
export_cache_support) + the language prompt kernel + greedy RNN-T (prediction network + joint, max_symbols per encoder
frame, unrolled) — every piece of per-stream state is an input and the matching `*_next` an output, batch-first, so
Triton's sequence batcher can keep it as implicit state on the card (R46, the k2-fsa streaming Zipformer pattern):

  inputs   audio_signal f32[B,128,T]   the pipeline decoder's feature buffer (pre-encode cache + chunk), right-padded
           length       i64[B,1]       valid frames in it
           start        i32[B,1]       1 on a stream's first chunk (Triton's CONTROL_SEQUENCE_START)
           prompt       i64[B,1]       prompt index (target language)
           cache_last_channel f32[B,L,C,D], cache_last_time f32[B,L,D,K], cache_last_channel_len i64[B,1],
           dec_h f32[B,R,H], dec_c f32[B,R,H], last_token i64[B,1]                       (state)
  outputs  tokens i32[B,T_out*max_symbols] (-1 = nothing), encoded_len i64[B,1], and every state as `<name>_next`

What had to change against NeMo's own export (`model.export()` / A3's `export_onnx.py`):
  1. the prompt kernel sits between encoder and joint (A3): the stock encoder graph feeds the joint un-prompted output;
  2. `drop_extra_pre_encoded` is a Python int that NeMo bakes into the graph, but Cadence's pipeline decoder drops
     nothing on a stream's first chunk (it has no pre-encode cache) and 2 frames on the others. Here the drop is per
     row, from `start`: the first chunk is right-padded to the full buffer and its first T_out pre-encoded frames are
     kept (the subsampling is causal, so the padding cannot reach them), the others drop the first 2. A batch can then
     mix first and later chunks of different streams — what a sequence batcher hands the model;
  3. caches, LSTM state and the last token are reset in-graph when `start` is 1, so a stream needs no zeroed state
     from the server (Triton's initial_state cannot say "blank" for the last token anyway);
  4. greedy RNN-T moves into the graph (no Python between encoder and joint): fixed max_symbols iterations per frame
     with an `active` mask, the semantics of NeMo's greedy_batch (a row stops at its first blank of a frame; after
     max_symbols the frame ends without one).
"""

from __future__ import annotations

import json
import os
import shutil
import sys
import time
from typing import Any

import torch
import torch.nn.functional as F

from nemo.collections.asr.models import ASRModel


class _Holder:
    drop: torch.Tensor


class _DropPreEncoded(torch.nn.Module):
    """ConvSubsampling, then the per-row drop of extra pre-encoded frames (row b keeps frames [d_b, d_b + T - D))."""

    def __init__(self, pre: torch.nn.Module, holder: _Holder, d: int) -> None:
        super().__init__()
        self.pre = pre
        self.holder = holder
        self.d = d

    def get_sampling_frames(self) -> Any:  # noqa: ANN401
        return self.pre.get_sampling_frames()

    def forward(self, x: torch.Tensor, lengths: torch.Tensor) -> tuple[torch.Tensor, torch.Tensor]:
        y, n = self.pre(x=x, lengths=lengths)
        drop = self.holder.drop
        t_out = y.size(1) - self.d
        idx = torch.arange(t_out, device=y.device).unsqueeze(0) + drop.unsqueeze(1)  # (B, T_out)
        y = y.gather(1, idx.unsqueeze(-1).expand(-1, -1, y.size(2)))
        n = (n.to(torch.int64) - drop).clamp(min=0)
        return y, n


class StreamStep(torch.nn.Module):
    def __init__(self, model: ASRModel, max_symbols: int, dtype: torch.dtype = torch.float32,
                 float_state: bool = False) -> None:
        super().__init__()
        self.dtype = dtype
        self.float_state = float_state
        enc = model.encoder
        self.enc = enc
        self.kernel = model.prompt_kernel
        self.num_prompts = int(model.num_prompts)
        self.decoder = model.decoder
        self.joint = model.joint
        self.blank = int(model.decoder.blank_idx)
        self.max_symbols = max_symbols
        self.d = int(enc.streaming_cfg.drop_extra_pre_encoded)
        self.holder = _Holder()
        enc.pre_encode = _DropPreEncoded(enc.pre_encode, self.holder, self.d)
        enc.streaming_cfg.drop_extra_pre_encoded = 0  # dropped per row by _DropPreEncoded

    def forward(  # type: ignore[no-untyped-def]
        self, audio_signal, length, start, prompt, cache_last_channel, cache_last_time, cache_last_channel_len,
        dec_h, dec_c, last_token,
    ):
        # fp16: weights in half, every float input cast in the graph and every float output cast back, so the I/O
        # (and Triton's state buffers) stay fp32 whatever the precision inside
        audio_signal = audio_signal.to(self.dtype)
        cache_last_channel = cache_last_channel.to(self.dtype)
        cache_last_time = cache_last_time.to(self.dtype)
        dec_h, dec_c = dec_h.to(self.dtype), dec_c.to(self.dtype)
        if self.float_state:  # Triton 26.08's ORT backend hands an INT64 state back as FP32: keep every state FP32
            cache_last_channel_len = cache_last_channel_len.to(torch.int64)
            last_token = last_token.to(torch.int64)
        s = start.reshape(-1) > 0
        keep = (~s).to(audio_signal.dtype)
        cache_last_channel = cache_last_channel * keep[:, None, None, None]
        cache_last_time = cache_last_time * keep[:, None, None, None]
        cache_len = cache_last_channel_len.reshape(-1) * (~s).to(torch.int64)
        h = (dec_h * keep[:, None, None]).transpose(0, 1).contiguous()  # (R, B, H)
        c = (dec_c * keep[:, None, None]).transpose(0, 1).contiguous()
        last = torch.where(s.unsqueeze(1), torch.full_like(last_token, self.blank), last_token)
        self.holder.drop = (~s).to(torch.int64) * self.d

        enc, enc_len, ch2, t2, len2 = self.enc.forward_for_export(
            audio_signal, length.reshape(-1), cache_last_channel, cache_last_time, cache_len
        )
        x = enc.transpose(1, 2)  # (B, T, D)
        p = F.one_hot(prompt.reshape(-1), self.num_prompts).to(x.dtype).unsqueeze(1).expand(-1, x.shape[1], -1)
        x = self.kernel(torch.cat([x, p], dim=-1))
        fp = self.joint.project_encoder(x)  # (B, T, Hj)
        b = x.shape[0]
        toks = []
        for t in range(x.shape[1]):  # T_out is fixed per profile (valid_out_len): unrolled
            ft = fp[:, t : t + 1, :]
            active = enc_len > t
            for _ in range(self.max_symbols):
                g, (h2, c2) = self.decoder.predict(last, state=[h, c], add_sos=False, batch_size=b)
                logits = self.joint.joint_after_projection(ft, self.joint.project_prednet(g))  # (B,1,1,V+1)
                k = logits.reshape(b, -1).argmax(-1)
                emit = active & (k != self.blank)
                toks.append(torch.where(emit, k, torch.full_like(k, -1)))
                last = torch.where(emit.unsqueeze(1), k.unsqueeze(1), last)
                h = torch.where(emit[None, :, None], h2, h)
                c = torch.where(emit[None, :, None], c2, c)
                active = emit
        tokens = torch.stack(toks, dim=1).to(torch.int32)
        f32 = torch.float32
        st = torch.float32 if self.float_state else torch.int64
        return (tokens, enc_len.reshape(-1, 1), ch2.to(f32), t2.to(f32), len2.reshape(-1, 1).to(st),
                h.transpose(0, 1).to(f32), c.transpose(0, 1).to(f32), last.to(st))


IN = ["audio_signal", "length", "start", "prompt", "cache_last_channel", "cache_last_time", "cache_last_channel_len",
      "dec_h", "dec_c", "last_token"]
STATE = ["cache_last_channel", "cache_last_time", "cache_last_channel_len", "dec_h", "dec_c", "last_token"]
OUT = ["tokens", "encoded_len"] + [f"{n}_next" for n in STATE]
# --triton-names: Triton 26.08 mixes up implicit states whose names extend one another (`cache_last_channel` and
# `cache_last_channel_len`: the second request got the rank-4 channel cache as the length), so the served graph
# names its states apart.
ALIAS = {"cache_last_channel": "kv_cache", "cache_last_time": "conv_cache", "cache_last_channel_len": "cache_fill",
         "dec_h": "lstm_h", "dec_c": "lstm_c", "last_token": "prev_token"}


def state_io(triton_names: bool) -> dict[str, list[str]]:
    if triton_names:
        return {s: [f"{ALIAS[s]}_in", f"{ALIAS[s]}_out"] for s in STATE}
    return {s: [s, f"{s}_next"] for s in STATE}


def geometry(model: ASRModel, ctx: list[int], max_symbols: int) -> dict[str, Any]:
    sc = model.encoder.streaming_cfg
    geo = {k: (v.tolist() if hasattr(v, "tolist") else v) for k, v in vars(sc).items()}

    def later(v: Any) -> int:  # noqa: ANN401
        return int(v[1]) if isinstance(v, (list, tuple)) else int(v)

    geo.update(
        att_context_size=ctx,
        buffer_frames=later(sc.pre_encode_cache_size) + later(sc.chunk_size),
        chunk_ms=later(sc.chunk_size) * 10,
        subsampling_factor=model.encoder.subsampling_factor,
        window_stride_s=model.cfg.preprocessor.window_stride,
        n_mels=model.cfg.preprocessor.features,
        sample_rate=model.cfg.sample_rate,
        d_model=model.encoder.d_model,
        n_layers=len(model.encoder.layers),
        blank_id=int(model.decoder.blank_idx),
        vocab_size=int(model.decoder.vocab_size),
        pred_rnn_layers=int(model.cfg.decoder.prednet.pred_rnn_layers),
        pred_hidden=int(model.cfg.decoder.prednet.pred_hidden),
        prompt_dictionary=dict(model.cfg.model_defaults.prompt_dictionary),
        num_prompts=int(model.num_prompts),
        max_symbols=max_symbols,
    )
    return geo


def main() -> None:
    src, out = sys.argv[1], sys.argv[2]
    ctx = [int(v) for v in sys.argv[3].split(",")]
    fp16 = "--fp16" in sys.argv
    os.makedirs(out, exist_ok=True)
    t0 = time.time()
    os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
    model = ASRModel.restore_from(src, map_location="cpu").eval()
    model.encoder.set_default_att_context_size(ctx)
    model.set_export_config({"cache_support": "True"})
    model.encoder.setup_streaming_params()
    max_symbols = int(model.cfg.decoding.greedy.get("max_symbols", 10) or 10)
    geo = geometry(model, ctx, max_symbols)
    print(json.dumps(geo, default=str))

    if fp16:
        model = model.half()
    float_state = "--float-state" in sys.argv
    step = StreamStep(model, max_symbols, torch.float16 if fp16 else torch.float32, float_state).eval()
    B, T = 2, geo["buffer_frames"]
    sdt = torch.float32 if float_state else torch.int64
    geo["float_state"] = float_state
    L, C, D = geo["n_layers"], geo["last_channel_cache_size"], geo["d_model"]
    ch0, tt0, _ = model.encoder.get_initial_cache_state(batch_size=B)
    K = tt0.shape[-1]
    R, H = geo["pred_rnn_layers"], geo["pred_hidden"]
    ex = (
        torch.randn(B, geo["n_mels"], T), torch.full((B, 1), T, dtype=torch.int64),
        torch.tensor([[1], [0]], dtype=torch.int32), torch.tensor([[0], [0]], dtype=torch.int64),
        torch.zeros(B, L, C, D), torch.zeros(B, L, D, K), torch.zeros(B, 1, dtype=sdt),
        torch.zeros(B, R, H), torch.zeros(B, R, H), torch.full((B, 1), geo["blank_id"], dtype=sdt),
    )
    geo.update(cache_last_time_k=int(K), state_shapes={
        "cache_last_channel": [L, C, D], "cache_last_time": [L, D, int(K)], "cache_last_channel_len": [1],
        "dec_h": [R, H], "dec_c": [R, H], "last_token": [1]})
    sio = state_io("--triton-names" in sys.argv)
    geo["state_io"] = sio
    in_names = IN[:4] + [sio[s][0] for s in STATE]
    out_names = OUT[:2] + [sio[s][1] for s in STATE]
    dyn = {n: {0: "B"} for n in in_names + out_names}
    tmp = os.path.join(out, "_tmp")
    os.makedirs(tmp, exist_ok=True)
    with torch.no_grad():
        ref = step(*ex)
        torch.onnx.export(step, ex, os.path.join(tmp, "step.onnx"), input_names=in_names, output_names=out_names,
                          dynamic_axes=dyn, opset_version=17, dynamo=False, do_constant_folding=True)
    t_export = time.time() - t0
    import onnx

    m = onnx.load(os.path.join(tmp, "step.onnx"))
    geo["onnx_nodes"] = len(m.graph.node)
    for f in ("step.onnx", "step.onnx.data"):  # onnx appends external data to an existing file
        if os.path.exists(os.path.join(out, f)):
            os.remove(os.path.join(out, f))
    onnx.save_model(m, os.path.join(out, "step.onnx"), save_as_external_data=True, all_tensors_to_one_file=True,
                    location="step.onnx.data")
    shutil.rmtree(tmp)
    geo["export_seconds"] = round(t_export, 1)
    geo["precision"] = "fp16" if fp16 else "fp32"
    with open(os.path.join(out, "streaming_cfg.json"), "w") as f:
        json.dump(geo, f, indent=1, default=str)

    # quick check: ORT CPU against the traced torch module on the example
    import numpy as np
    import onnxruntime as ort

    try:
        sess = ort.InferenceSession(os.path.join(out, "step.onnx"), providers=["CPUExecutionProvider"])
        got = sess.run(None, {n: v.numpy() for n, v in zip(in_names, ex, strict=True)})
        diffs = {n: float(np.abs(g.astype(np.float64) - r.float().numpy().astype(np.float64)).max())
                 for n, g, r in zip(out_names, got, ref, strict=True)}
        print("ort-vs-torch max abs diff", json.dumps(diffs))
    except Exception as e:  # fp16 graphs: ORT's CPU provider lacks some half kernels; the GPU parity run checks them
        print("ort-vs-torch check skipped:", str(e)[:300])
    for f in sorted(os.listdir(out)):
        print(f, os.path.getsize(os.path.join(out, f)))


if __name__ == "__main__":
    main()
