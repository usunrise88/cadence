"""A3 step 4b: ONNX vs NeMo streaming parity on N utterances (acceptance: |WER delta| <= 0.1 point).

  docs/spikes/a3/nemo.sh [--gpu] python /spike/parity.py <model.nemo> <onnx_dir> [manifest] [N=200] [out.json]

Both paths see the SAME chunks: NeMo's CacheAwareStreamingAudioBuffer (pad_and_drop_preencoded=True, the mode an
exported graph needs because drop_extra_pre_encoded is baked in) produces the mel chunks once per utterance.
  * NeMo path: model.conformer_stream_step() with encoder caches + prompt he-IL + NeMo greedy RNN-T (the reference).
  * ONNX path: encoder_prompt.onnx per chunk (caches carried in numpy) + greedy RNN-T over decoder_joint.onnx,
    max_symbols per frame from the model config; blank = vocab_size; SOS = blank (blank_as_pad).
Feature extraction stays in NeMo for both (the preprocessor is not part of the export; Triton would need it as a
separate model or in the client). WER uses wer.py's normalisation. The NeMo path runs on GPU when available (under
cap.py), ONNX Runtime on CPU (the image has no onnxruntime; pip --target /work/pylib, CPU wheel).
"""

from __future__ import annotations

import json
import os
import re
import sys
import time

sys.path.insert(0, "/spike")
sys.path.insert(1, "/work/pylib")

import numpy as np  # noqa: E402
import onnxruntime as ort  # noqa: E402
import torch  # noqa: E402

import wer  # noqa: E402
from nemo.collections.asr.models import ASRModel  # noqa: E402
from nemo.collections.asr.parts.utils.streaming_utils import CacheAwareStreamingAudioBuffer  # noqa: E402

TAG = re.compile(r"\s*<[a-z]{2}-[A-Z]{2}>")


def main() -> None:
    src, odir = sys.argv[1], sys.argv[2]
    man = sys.argv[3] if len(sys.argv) > 3 else "/work/data/manifests/test_fleurs.json"
    n = int(sys.argv[4]) if len(sys.argv) > 4 else 200
    out_path = sys.argv[5] if len(sys.argv) > 5 else os.path.join(odir, "parity.json")
    geo = json.load(open(os.path.join(odir, "streaming_cfg.json")))
    dev = "cuda" if torch.cuda.is_available() else "cpu"
    if dev == "cuda":
        import cap

        cap.apply()
    model = ASRModel.restore_from(src, map_location=dev).eval()
    model.encoder.set_default_att_context_size(geo["att_context_size"])
    model.encoder.setup_streaming_params()
    model.set_inference_prompt("he-IL")
    model.decoding.set_strip_lang_tags(True)
    buf = CacheAwareStreamingAudioBuffer(model=model, online_normalization=False, pad_and_drop_preencoded=True)
    drop = model.encoder.streaming_cfg.drop_extra_pre_encoded

    so = ort.SessionOptions()
    so.intra_op_num_threads = int(os.environ.get("ORT_THREADS", "12"))
    enc = ort.InferenceSession(os.path.join(odir, "encoder_prompt.onnx"), so, providers=["CPUExecutionProvider"])
    dj = ort.InferenceSession(os.path.join(odir, "decoder_joint.onnx"), so, providers=["CPUExecutionProvider"])
    dj_in = [i.name for i in dj.get_inputs()]
    blank, max_sym, prompt = geo["blank_id"], geo["max_symbols"], geo["prompt_dictionary_he_IL"]
    L, H = geo["pred_rnn_layers"], geo["pred_hidden"]

    rows = [json.loads(x) for x in open(man, encoding="utf-8")][:n]
    res, t_nemo, t_onnx = [], 0.0, 0.0
    for i, r in enumerate(rows):
        buf.reset_buffer()
        buf.append_audio_file(r["audio_filepath"], stream_id=-1)
        chunks = []
        for chunk, lens in buf:
            chunks.append((chunk.clone(), lens.clone(), buf.is_buffer_empty()))

        # --- NeMo reference ---------------------------------------------------------------------------------
        t0 = time.time()
        c_ch, c_t, c_len = model.encoder.get_initial_cache_state(batch_size=1)
        hyps, pred = None, None
        with torch.inference_mode():
            for chunk, lens, last in chunks:
                pred, texts, c_ch, c_t, c_len, hyps = model.conformer_stream_step(
                    processed_signal=chunk, processed_signal_length=lens, cache_last_channel=c_ch,
                    cache_last_time=c_t, cache_last_channel_len=c_len, keep_all_outputs=last,
                    previous_hypotheses=hyps, previous_pred_out=pred, drop_extra_pre_encoded=drop,
                    return_transcription=True)
        nemo_text = texts[0].text if hasattr(texts[0], "text") else texts[0]
        t_nemo += time.time() - t0

        # --- ONNX ---------------------------------------------------------------------------------------------
        t0 = time.time()
        a, b, cl = model.encoder.get_initial_cache_state(batch_size=1)
        o_ch = a.transpose(0, 1).cpu().numpy()
        o_t = b.transpose(0, 1).cpu().numpy()
        o_len = cl.cpu().numpy().astype(np.int64)
        h = np.zeros((L, 1, H), np.float32)
        c = np.zeros((L, 1, H), np.float32)
        last_tok, toks = blank, []
        for chunk, lens, _ in chunks:
            e, e_len, o_ch, o_t, o_len = enc.run(None, {
                "audio_signal": chunk.cpu().numpy().astype(np.float32), "length": lens.cpu().numpy().astype(np.int64),
                "cache_last_channel": o_ch, "cache_last_time": o_t, "cache_last_channel_len": o_len,
                "prompt_index": np.array([prompt], np.int64)})
            for t in range(int(e_len[0])):
                frame = e[:, :, t:t + 1]
                for _ in range(max_sym):
                    logits, _, h2, c2 = dj.run(None, dict(zip(dj_in, [
                        frame, np.array([[last_tok]], np.int32), np.array([1], np.int32), h, c])))
                    k = int(np.argmax(logits[0, 0, 0]))
                    if k == blank:
                        break
                    toks.append(k)
                    last_tok, h, c = k, h2, c2
        onnx_text = TAG.sub("", model.tokenizer.ids_to_text(toks)).strip()
        t_onnx += time.time() - t0
        res.append({"text": r["text"], "nemo": nemo_text, "onnx": onnx_text, "same": nemo_text == onnx_text})
        if i % 20 == 0:
            print(f"[{i}/{len(rows)}] same={sum(x['same'] for x in res)}/{len(res)} "
                  f"nemo {t_nemo:.0f}s onnx {t_onnx:.0f}s", flush=True)

    s_nemo = wer.score([{"text": x["text"], "pred_text": x["nemo"]} for x in res])
    s_onnx = wer.score([{"text": x["text"], "pred_text": x["onnx"]} for x in res])
    summary = {"utts": len(res), "identical_transcripts": sum(x["same"] for x in res), "nemo": s_nemo,
               "onnx": s_onnx, "wer_delta_points": round(s_onnx["wer"] - s_nemo["wer"], 2),
               "seconds": {"nemo": round(t_nemo, 1), "onnx_cpu": round(t_onnx, 1)}}
    print(json.dumps(summary, ensure_ascii=False))
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"summary": summary, "rows": res}, f, ensure_ascii=False, indent=0)


if __name__ == "__main__":
    main()
