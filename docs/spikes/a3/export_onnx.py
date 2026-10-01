"""A3 step 4a: cache-aware streaming ONNX export for the prompt-conditioned Nemotron 3.5 model, one latency profile.

  docs/spikes/a3/nemo.sh python /spike/export_onnx.py <model.nemo> <out_dir> [left,right]   (default 56,0 = 80 ms)

Why not just `model.export()`: NeMo exports RNN-T models as two subnets, `encoder` and `decoder_joint`, but the
language prompt of EncDecRNNTBPEModelWithPrompt is applied BETWEEN them (one-hot(prompt) ++ encoder output ->
`prompt_kernel` MLP; see PromptStreamingMixin._apply_prompt_to_encoded). The stock encoder ONNX therefore feeds the
joint un-prompted features and the output is garbage. We export:
  * encoder_prompt.onnx  = ConformerEncoder.forward_for_export (cache-aware, export_cache_support=True) + prompt
                           kernel, with `prompt_index` (int64[B]) as an extra input; caches in/out as NeMo exports
                           them (batch-first: cache_last_channel [B,L,C,D], cache_last_time [B,L,D,K]).
  * decoder_joint.onnx   = NeMo's RNNTDecoderJoint export (unchanged): one prediction-network step + joint.
The streaming geometry baked into the graph is printed and saved as streaming_cfg.json - the family descriptor needs
exactly these numbers per latency profile (chunk/shift frames, pre-encode cache, drop_extra_pre_encoded, caches).
Export runs on CPU in fp32 (the cache-aware path force-casts to fp32 anyway), so it needs no GPU memory.
"""

from __future__ import annotations

import json
import os
import sys

import torch
import torch.nn.functional as F

from nemo.collections.asr.models import ASRModel


class EncoderWithPrompt(torch.nn.Module):
    def __init__(self, model: ASRModel) -> None:
        super().__init__()
        self.encoder = model.encoder
        self.kernel = model.prompt_kernel
        self.num_prompts = int(model.num_prompts)

    def forward(self, audio_signal, length, cache_last_channel, cache_last_time, cache_last_channel_len,
                prompt_index):  # type: ignore[no-untyped-def]
        enc, enc_len, c_ch, c_t, c_len = self.encoder.forward_for_export(
            audio_signal, length, cache_last_channel, cache_last_time, cache_last_channel_len)
        x = enc.transpose(1, 2)  # (B, T, D)
        p = F.one_hot(prompt_index, self.num_prompts).to(x.dtype).unsqueeze(1).expand(-1, x.shape[1], -1)
        x = self.kernel(torch.cat([x, p], dim=-1)).transpose(1, 2)  # back to (B, D, T)
        return x, enc_len, c_ch, c_t, c_len


def main() -> None:
    src, out = sys.argv[1], sys.argv[2]
    ctx = [int(v) for v in (sys.argv[3] if len(sys.argv) > 3 else "56,0").split(",")]
    os.makedirs(out, exist_ok=True)
    model = ASRModel.restore_from(src, map_location="cpu").eval()
    model.encoder.set_default_att_context_size(ctx)
    model.set_export_config({"cache_support": "True"})  # -> encoder.export_cache_support + setup_streaming_params()
    sc = model.encoder.streaming_cfg
    geo = {k: (v if not hasattr(v, "tolist") else v.tolist()) for k, v in vars(sc).items()}
    geo.update(att_context_size=ctx, subsampling_factor=model.encoder.subsampling_factor,
               window_stride_s=model.cfg.preprocessor.window_stride, n_mels=model.cfg.preprocessor.features,
               sample_rate=model.cfg.sample_rate, d_model=model.encoder.d_model,
               blank_id=model.decoder.blank_idx, vocab_size=model.decoder.vocab_size,
               pred_rnn_layers=model.cfg.decoder.prednet.pred_rnn_layers, pred_hidden=model.cfg.decoder.prednet.pred_hidden,
               prompt_dictionary_he_IL=model.cfg.model_defaults.prompt_dictionary["he-IL"],
               max_symbols=model.cfg.decoding.greedy.get("max_symbols", 10))
    print(json.dumps(geo, indent=1, default=str))
    with open(os.path.join(out, "streaming_cfg.json"), "w") as f:
        json.dump(geo, f, indent=1, default=str)

    wrapper = EncoderWithPrompt(model).eval()
    ex = model.encoder.input_example(max_batch=2)  # feats, len, cache_ch [B,L,C,D], cache_t [B,L,D,K], cache_len
    ex = (*ex, torch.tensor([64, 64]))
    names_in = ["audio_signal", "length", "cache_last_channel", "cache_last_time", "cache_last_channel_len",
                "prompt_index"]
    names_out = ["encoded", "encoded_len", "cache_last_channel_next", "cache_last_time_next",
                 "cache_last_channel_next_len"]
    dyn = {"audio_signal": {0: "B", 2: "T"}, "length": {0: "B"}, "cache_last_channel": {0: "B"},
           "cache_last_time": {0: "B"}, "cache_last_channel_len": {0: "B"}, "prompt_index": {0: "B"},
           "encoded": {0: "B", 2: "T_out"}, "encoded_len": {0: "B"}, "cache_last_channel_next": {0: "B"},
           "cache_last_time_next": {0: "B"}, "cache_last_channel_next_len": {0: "B"}}
    tmp = os.path.join(out, "_tmp")
    os.makedirs(tmp, exist_ok=True)
    with torch.no_grad():
        # > 2 GB fp32 -> the TorchScript exporter scatters one external-data file per tensor; re-save as one file.
        torch.onnx.export(wrapper, ex, os.path.join(tmp, "encoder_prompt.onnx"), input_names=names_in,
                          output_names=names_out, dynamic_axes=dyn, opset_version=17, dynamo=False,
                          do_constant_folding=True)
        model.decoder_joint.export(os.path.join(out, "decoder_joint.onnx"), check_trace=False)
    import shutil

    import onnx

    m = onnx.load(os.path.join(tmp, "encoder_prompt.onnx"))
    onnx.save_model(m, os.path.join(out, "encoder_prompt.onnx"), save_as_external_data=True,
                    all_tensors_to_one_file=True, location="encoder_prompt.onnx.data")
    shutil.rmtree(tmp)
    for f in sorted(os.listdir(out)):
        print(f, os.path.getsize(os.path.join(out, f)))


if __name__ == "__main__":
    main()
