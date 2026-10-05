"""The Nemotron streaming *step graph* (spike E1 step 1, docs/spikes/e1/export_step.py): one ONNX graph per latency
profile in which one call is one chunk of B streams — the cache-aware encoder (``forward_for_export`` with cache
support), the language prompt kernel and greedy RNN-T, with every piece of per-stream state an input and its ``_out``
an output, batch-first, so Triton's sequence batcher can keep it as implicit state on the card (R46).

    inputs   audio_signal f32[B,128,T]  the pipeline decoder's feature buffer (pre-encode cache + chunk), right-padded
             length       i64[B,1]      valid frames in it
             start        i32[B,1]      1 on a stream's first chunk (Triton's CONTROL_SEQUENCE_START)
             prompt       i64[B,1]      prompt index (target language)
             kv_cache_in f32[B,L,C,D], conv_cache_in f32[B,L,D,K], cache_fill_in f32[B,1], lstm_h_in f32[B,R,H],
             lstm_c_in f32[B,R,H], prev_token_in f32[B,1]                                   (state)
    outputs  tokens i32[B,T_out*max_symbols] (-1 = nothing), encoded_len i64[B,1], every state as ``*_out``

What changes against NeMo's own export (``model.export()``):
  1. the prompt kernel sits between encoder and joint (spike A3): the stock encoder graph feeds the joint un-prompted;
  2. ``drop_extra_pre_encoded`` is per row, from ``start``: Cadence's pipeline decoder drops nothing on a stream's first
     chunk (it has no pre-encode cache) and 2 frames on the others (``pipeline.patch_prompt``), so a batch can mix first
     and later chunks of different streams — what a sequence batcher hands the model;
  3. caches, LSTM state and the last token are reset in the graph on ``start`` (Triton's initial state can only be
     zeros, and the last token must start as blank);
  4. greedy RNN-T runs in the graph: ``max_symbols`` prediction-network and joint steps per encoder frame, unrolled,
     with an ``active`` mask (NeMo's greedy_batch: a row stops at its first blank of a frame);
  5. for Triton 26.08: the states are named apart (``cache_last_channel`` and ``cache_last_channel_len`` were confused)
     and all FP32 (an INT64 state came back as FP32).

NeMo and torch are imported inside the functions: the module imports anywhere.
"""

from __future__ import annotations

import os
import shutil
import time
from pathlib import Path
from typing import Any

from cadence_nemo.deploy import STATE_ORDER, STREAMING_CFG, write_streaming_cfg

# Triton 26.08 confuses implicit states whose names extend one another: the served graph names them apart.
STATE_NAMES = {
    "cache_last_channel": ("kv_cache_in", "kv_cache_out"),
    "cache_last_time": ("conv_cache_in", "conv_cache_out"),
    "cache_last_channel_len": ("cache_fill_in", "cache_fill_out"),
    "dec_h": ("lstm_h_in", "lstm_h_out"),
    "dec_c": ("lstm_c_in", "lstm_c_out"),
    "last_token": ("prev_token_in", "prev_token_out"),
}
OPSET = 17


def _modules() -> tuple[Any, Any]:
    import torch
    import torch.nn.functional as F  # noqa: N812

    class Holder:
        drop: Any = None

    class DropPreEncoded(torch.nn.Module):
        """ConvSubsampling, then the per-row drop of extra pre-encoded frames (row b keeps [d_b, d_b + T - D))."""

        def __init__(self, pre: Any, holder: Holder, d: int) -> None:
            super().__init__()
            self.pre = pre
            self.holder = holder
            self.d = d

        def get_sampling_frames(self) -> Any:
            return self.pre.get_sampling_frames()

        def forward(self, x: Any, lengths: Any) -> tuple[Any, Any]:
            y, n = self.pre(x=x, lengths=lengths)
            drop = self.holder.drop
            t_out = y.size(1) - self.d
            idx = torch.arange(t_out, device=y.device).unsqueeze(0) + drop.unsqueeze(1)  # (B, T_out)
            y = y.gather(1, idx.unsqueeze(-1).expand(-1, -1, y.size(2)))
            n = (n.to(torch.int64) - drop).clamp(min=0)
            return y, n

    class StreamStep(torch.nn.Module):
        def __init__(self, model: Any, max_symbols: int) -> None:
            super().__init__()
            enc = model.encoder
            self.enc = enc
            self.kernel = model.prompt_kernel
            self.num_prompts = int(model.num_prompts)
            self.decoder = model.decoder
            self.joint = model.joint
            self.blank = int(model.decoder.blank_idx)
            self.max_symbols = max_symbols
            self.d = int(enc.streaming_cfg.drop_extra_pre_encoded)
            self.holder = Holder()
            enc.pre_encode = DropPreEncoded(enc.pre_encode, self.holder, self.d)
            enc.streaming_cfg.drop_extra_pre_encoded = 0  # dropped per row by DropPreEncoded

        def forward(
            self,
            audio_signal: Any,
            length: Any,
            start: Any,
            prompt: Any,
            cache_last_channel: Any,
            cache_last_time: Any,
            cache_last_channel_len: Any,
            dec_h: Any,
            dec_c: Any,
            last_token: Any,
        ) -> tuple[Any, ...]:
            cache_last_channel_len = cache_last_channel_len.to(torch.int64)  # FP32 states (Triton 26.08)
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
            fp = self.joint.project_encoder(x)
            b = x.shape[0]
            toks = []
            for t in range(x.shape[1]):  # T_out is fixed per profile: unrolled
                ft = fp[:, t : t + 1, :]
                active = enc_len > t
                for _ in range(self.max_symbols):
                    g, (h2, c2) = self.decoder.predict(last, state=[h, c], add_sos=False, batch_size=b)
                    logits = self.joint.joint_after_projection(ft, self.joint.project_prednet(g))
                    k = logits.reshape(b, -1).argmax(-1)
                    emit = active & (k != self.blank)
                    toks.append(torch.where(emit, k, torch.full_like(k, -1)))
                    last = torch.where(emit.unsqueeze(1), k.unsqueeze(1), last)
                    h = torch.where(emit[None, :, None], h2, h)
                    c = torch.where(emit[None, :, None], c2, c)
                    active = emit
            tokens = torch.stack(toks, dim=1).to(torch.int32)
            f32 = torch.float32
            return (
                tokens,
                enc_len.reshape(-1, 1),
                ch2.to(f32),
                t2.to(f32),
                len2.reshape(-1, 1).to(f32),
                h.transpose(0, 1).to(f32),
                c.transpose(0, 1).to(f32),
                last.to(f32),
            )

    return StreamStep, torch


def restore(nemo: Path) -> Any:
    """The ``.nemo`` on the CPU, in eval mode (2.5 GB of fp32 weights, about 5 GB of RAM)."""
    from nemo.collections.asr.models import ASRModel

    prev = os.environ.get("TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD")
    os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"  # the content store's checkpoint (pipeline.patch_load)
    try:
        return ASRModel.restore_from(str(nemo), map_location="cpu").eval()
    finally:
        if prev is None:
            os.environ.pop("TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD", None)
        else:
            os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = prev


def geometry(model: Any, att: list[int], max_symbols: int) -> dict[str, Any]:
    """The profile's streaming geometry, the state shapes and names, the prompt dictionary, and for the serve client the
    vocabulary and the front end: ``streaming_cfg.json``."""
    from cadence_nemo.training import model_facts

    sc = model.encoder.streaming_cfg
    geo: dict[str, Any] = {k: (v.tolist() if hasattr(v, "tolist") else v) for k, v in vars(sc).items()}

    def later(v: Any) -> int:
        return int(v[1]) if isinstance(v, list | tuple) else int(v)

    geo.update(
        att_context_size=list(att),
        buffer_frames=later(sc.pre_encode_cache_size) + later(sc.chunk_size),
        chunk_ms=later(sc.chunk_size) * 10,
        subsampling_factor=int(model.encoder.subsampling_factor),
        window_stride_s=float(model.cfg.preprocessor.window_stride),
        n_mels=int(model.cfg.preprocessor.features),
        sample_rate=int(model.cfg.sample_rate),
        d_model=int(model.encoder.d_model),
        n_layers=len(model.encoder.layers),
        blank_id=int(model.decoder.blank_idx),
        vocab_size=int(model.decoder.vocab_size),
        pred_rnn_layers=int(model.cfg.decoder.prednet.pred_rnn_layers),
        pred_hidden=int(model.cfg.decoder.prednet.pred_hidden),
        prompt_dictionary=dict(model_facts(model).prompt_dictionary),
        num_prompts=int(model.num_prompts),
        max_symbols=max_symbols,
        float_state=True,
        precision="fp32",
        state_io={s: list(STATE_NAMES[s]) for s in STATE_ORDER},
        vocabulary=vocabulary(model),
        frontend=frontend(model),
    )
    return geo


def vocabulary(model: Any) -> list[str]:
    """The tokenizer's pieces by id (the blank, ``vocab_size``, is not one): the serve client detokenises with them."""
    n = int(model.decoder.vocab_size)
    pieces = [str(p) for p in model.tokenizer.ids_to_tokens(list(range(n)))]
    if len(pieces) != n:
        raise RuntimeError(f"the tokenizer named {len(pieces)} pieces for a vocabulary of {n}")
    return pieces


def frontend(model: Any) -> dict[str, Any]:
    """The mel front end the serve client computes (the caller sends features, D1 2026-10-05): the ``.nemo``'s
    preprocessor config, resolved (``serving.nemo_preprocessor`` builds NeMo's module from it)."""
    from omegaconf import OmegaConf

    pre = OmegaConf.to_container(model.cfg.preprocessor, resolve=True)
    if not isinstance(pre, dict):
        raise RuntimeError("the model's preprocessor config is not a mapping")
    return {"kind": "nemo", "preprocessor": {str(k): v for k, v in pre.items()}}


def max_symbols_of(model: Any) -> int:
    greedy = model.cfg.decoding.get("greedy") or {}
    return int(greedy.get("max_symbols", 10) or 10)


def export(nemo: Path, att: list[int], out: Path, scratch: Path) -> dict[str, Any]:
    """Write ``step.onnx`` (+ ``step.onnx.data``) and ``streaming_cfg.json`` for one profile into ``out``; returns the
    geometry. The graph is checked with ONNX's checker and against the expected inputs and outputs (no ONNX Runtime in
    the image: parity is the run that compares numbers)."""
    import onnx

    step_cls, torch = _modules()
    t0 = time.perf_counter()
    model = restore(nemo)
    model.encoder.set_default_att_context_size(att)
    model.set_export_config({"cache_support": "True"})
    model.encoder.setup_streaming_params()
    max_symbols = max_symbols_of(model)
    geo = geometry(model, att, max_symbols)
    step = step_cls(model, max_symbols).eval()
    b, t = 2, int(geo["buffer_frames"])
    _, tt0, _ = model.encoder.get_initial_cache_state(batch_size=b)
    big_l, c_size, d = geo["n_layers"], geo["last_channel_cache_size"], geo["d_model"]
    k = int(tt0.shape[-1])
    r, hid = geo["pred_rnn_layers"], geo["pred_hidden"]
    example = (
        torch.randn(b, geo["n_mels"], t),
        torch.full((b, 1), t, dtype=torch.int64),
        torch.tensor([[1], [0]], dtype=torch.int32),
        torch.tensor([[0], [0]], dtype=torch.int64),
        torch.zeros(b, big_l, c_size, d),
        torch.zeros(b, big_l, d, k),
        torch.zeros(b, 1),
        torch.zeros(b, r, hid),
        torch.zeros(b, r, hid),
        torch.full((b, 1), float(geo["blank_id"])),
    )
    geo["cache_last_time_k"] = k
    geo["state_shapes"] = {
        "cache_last_channel": [big_l, c_size, d],
        "cache_last_time": [big_l, d, k],
        "cache_last_channel_len": [1],
        "dec_h": [r, hid],
        "dec_c": [r, hid],
        "last_token": [1],
    }
    in_names = ["audio_signal", "length", "start", "prompt"] + [STATE_NAMES[s][0] for s in STATE_ORDER]
    out_names = ["tokens", "encoded_len"] + [STATE_NAMES[s][1] for s in STATE_ORDER]
    tmp = scratch / "onnx-trace"
    tmp.mkdir(parents=True, exist_ok=True)
    with torch.no_grad():
        torch.onnx.export(
            step,
            example,
            str(tmp / "step.onnx"),
            input_names=in_names,
            output_names=out_names,
            dynamic_axes={n: {0: "B"} for n in in_names + out_names},
            opset_version=OPSET,
            dynamo=False,
            do_constant_folding=True,
        )
    geo["export_seconds"] = round(time.perf_counter() - t0, 1)
    m = onnx.load(str(tmp / "step.onnx"))
    geo["onnx_nodes"] = len(m.graph.node)
    got_in = [i.name for i in m.graph.input]
    got_out = [o.name for o in m.graph.output]
    if got_in != in_names or got_out != out_names:
        raise RuntimeError(
            f"the exported graph has inputs {got_in} and outputs {got_out}, not {in_names} / {out_names}"
        )
    out.mkdir(parents=True, exist_ok=True)
    for f in ("step.onnx", "step.onnx.data"):  # onnx appends external data to an existing file
        (out / f).unlink(missing_ok=True)
    onnx.save_model(
        m, str(out / "step.onnx"), save_as_external_data=True, all_tensors_to_one_file=True, location="step.onnx.data"
    )
    shutil.rmtree(tmp, ignore_errors=True)
    del m
    onnx.checker.check_model(str(out / "step.onnx"))
    write_streaming_cfg(out / STREAMING_CFG, geo)
    return geo
