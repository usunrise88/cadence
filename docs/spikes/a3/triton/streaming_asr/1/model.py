"""Triton Python-backend model `streaming_asr`: cache-aware streaming RNN-T over the two A3 ONNX graphs.

One request = one 80 ms feature chunk of one stream (sequence batcher, CORRID/START/END control inputs). Per
correlation id we keep the encoder caches, the prediction-network LSTM state, the last token and the hypothesis.
Each execute() gets up to max_batch_size requests from DIFFERENT streams (sequence batcher, oldest strategy):
  1. stack their chunks + caches -> one BLS call to `encoder_prompt` (ONNX Runtime, CUDA EP)
  2. batched greedy RNN-T: for each encoder frame, call `decoder_joint` for all streams still emitting,
     up to max_symbols times (blank = vocab size; SOS = blank because blank_as_pad).
Output: TOKENS (int32 ids emitted for this chunk) - detokenisation stays in the client (the Triton image has no
sentencepiece). Mel features are computed by the client with NeMo's streaming buffer (the preprocessor is not in
the export) - phase 5 needs a feature model in front of this one.
"""

import json

import numpy as np
import triton_python_backend_utils as pb_utils


class TritonPythonModel:
    def initialize(self, args):
        cfg = json.loads(args["model_config"])
        p = {k: v["string_value"] for k, v in cfg.get("parameters", {}).items()}
        self.blank = int(p["blank_id"])
        self.max_sym = int(p["max_symbols"])
        self.prompt = int(p["prompt_index"])
        self.L, self.C, self.D, self.K = 24, int(p["last_channel_cache_size"]), 1024, int(p["conv_cache"])
        self.H, self.R = 640, 2
        self.state = {}
        # BLS outputs of the ONNX (CUDA EP) models come back on the GPU; ask for host memory. This means the
        # encoder caches (~5.6 MB per stream) cross PCIe twice per 80 ms chunk - the phase-5 design should keep
        # them on the device (ORT backend implicit state via the sequence batcher, or DLPack).
        self.cpu = pb_utils.PreferredMemory(pb_utils.TRITONSERVER_MEMORY_CPU, 0)

    def _new(self):
        return {"ch": np.zeros((self.L, self.C, self.D), np.float32), "t": np.zeros((self.L, self.D, self.K), np.float32),
                "len": np.int64(0), "h": np.zeros((self.R, self.H), np.float32), "c": np.zeros((self.R, self.H), np.float32),
                "last": self.blank}

    def execute(self, requests):
        ids, feats, lens = [], [], []
        for r in requests:
            cid = int(pb_utils.get_input_tensor_by_name(r, "CORRID").as_numpy().reshape(-1)[0])
            if bool(pb_utils.get_input_tensor_by_name(r, "START").as_numpy().reshape(-1)[0]) or cid not in self.state:
                self.state[cid] = self._new()
            ids.append(cid)
            feats.append(pb_utils.get_input_tensor_by_name(r, "FEATS").as_numpy()[0])
            lens.append(pb_utils.get_input_tensor_by_name(r, "LEN").as_numpy().reshape(-1)[0])
        st = [self.state[c] for c in ids]
        B = len(st)
        enc_in = [
            pb_utils.Tensor("audio_signal", np.stack(feats).astype(np.float32)),
            pb_utils.Tensor("length", np.array(lens, np.int64)),
            pb_utils.Tensor("cache_last_channel", np.stack([s["ch"] for s in st])),
            pb_utils.Tensor("cache_last_time", np.stack([s["t"] for s in st])),
            pb_utils.Tensor("cache_last_channel_len", np.array([s["len"] for s in st], np.int64)),
            pb_utils.Tensor("prompt_index", np.full((B,), self.prompt, np.int64)),
        ]
        out = pb_utils.InferenceRequest(
            model_name="encoder_prompt", inputs=enc_in,
            requested_output_names=["encoded", "encoded_len", "cache_last_channel_next", "cache_last_time_next",
                                    "cache_last_channel_next_len"], preferred_memory=self.cpu).exec()
        if out.has_error():
            raise pb_utils.TritonModelException(out.error().message())
        g = lambda n: pb_utils.get_output_tensor_by_name(out, n).as_numpy()  # noqa: E731
        enc, enc_len = g("encoded"), g("encoded_len")
        ch, t, cl = g("cache_last_channel_next"), g("cache_last_time_next"), g("cache_last_channel_next_len")
        emitted = [[] for _ in range(B)]
        for i, s in enumerate(st):
            s["ch"], s["t"], s["len"] = ch[i], t[i], cl[i]
        for f in range(int(enc_len.max()) if B else 0):
            active = [i for i in range(B) if f < enc_len[i]]
            for _ in range(self.max_sym):
                if not active:
                    break
                dj_in = [
                    pb_utils.Tensor("encoder_outputs", np.ascontiguousarray(enc[active][:, :, f:f + 1])),
                    pb_utils.Tensor("targets", np.array([[st[i]["last"]] for i in active], np.int32)),
                    pb_utils.Tensor("target_length", np.ones((len(active),), np.int32)),
                    pb_utils.Tensor("input_states_1", np.stack([st[i]["h"] for i in active], axis=1)),
                    pb_utils.Tensor("input_states_2", np.stack([st[i]["c"] for i in active], axis=1)),
                ]
                o = pb_utils.InferenceRequest(model_name="decoder_joint", inputs=dj_in,
                                              requested_output_names=["outputs", "output_states_1",
                                                                      "output_states_2"],
                                              preferred_memory=self.cpu).exec()
                if o.has_error():
                    raise pb_utils.TritonModelException(o.error().message())
                logits = pb_utils.get_output_tensor_by_name(o, "outputs").as_numpy()
                h2 = pb_utils.get_output_tensor_by_name(o, "output_states_1").as_numpy()
                c2 = pb_utils.get_output_tensor_by_name(o, "output_states_2").as_numpy()
                k = logits[:, 0, 0, :].argmax(-1)
                still = []
                for j, i in enumerate(active):
                    if int(k[j]) != self.blank:
                        emitted[i].append(int(k[j]))
                        st[i]["last"], st[i]["h"], st[i]["c"] = int(k[j]), h2[:, j], c2[:, j]
                        still.append(i)
                active = still
        responses = []
        for i, r in enumerate(requests):
            responses.append(pb_utils.InferenceResponse(output_tensors=[
                pb_utils.Tensor("TOKENS", np.array([emitted[i] or [-1]], np.int32))]))
            if bool(pb_utils.get_input_tensor_by_name(r, "END").as_numpy().reshape(-1)[0]):
                self.state.pop(ids[i], None)
        return responses
