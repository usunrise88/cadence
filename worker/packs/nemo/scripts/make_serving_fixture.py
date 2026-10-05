"""Regenerate the serving fixture: a deployable whose step graph has the stream protocol of the family's export (spike
E1) but no model — every request of a sequence returns one token, ``100 x prompt + n`` for the sequence's n-th chunk.
The sequence batcher keeps ``n`` as implicit state and ``start`` resets it, so a test sees at once whether the server
kept each stream's state apart, reset it on a new sequence and passed the prompt (E1's identity approach).

    python packs/nemo/scripts/make_serving_fixture.py [--out DIR]   # needs the onnx package (the NeMo image has it)

Writes ``deployable.json``, ``model/config.pbtxt`` (CPU instance: no card needed), ``model/1/model.onnx`` and
``model/streaming_cfg.json`` (the 80 ms geometry with 8 mel rows and the fixture front end).
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

OUT = Path(__file__).resolve().parents[1] / "tests" / "fixtures" / "serving"
N_MELS, PRE, CHUNK = 8, (0, 9), (1, 8)
T = PRE[1] + CHUNK[1]
SLOTS = 2
VOCAB = 1024

CONFIG = f"""# The serving fixture (packs/nemo/scripts/make_serving_fixture.py): E1's stream protocol, no model.
name: "fixture_step"
backend: "onnxruntime"
max_batch_size: 64
sequence_batching {{
  max_sequence_idle_microseconds: 60000000
  oldest {{ max_candidate_sequences: 1024 max_queue_delay_microseconds: 1000 }}
  control_input [
    {{ name: "start" control [ {{ kind: CONTROL_SEQUENCE_START int32_false_true: [ 0, 1 ] }} ] }}
  ]
  state [
    {{ input_name: "count_in" output_name: "count_out" data_type: TYPE_FP32 dims: [ 1 ]
      initial_state: {{ data_type: TYPE_FP32 dims: [ 1 ] zero_data: true name: "zero_count_in" }} }}
  ]
}}
input [
  {{ name: "audio_signal" data_type: TYPE_FP32 dims: [ {N_MELS}, {T} ] }},
  {{ name: "length" data_type: TYPE_INT64 dims: [ 1 ] }},
  {{ name: "prompt" data_type: TYPE_INT64 dims: [ 1 ] }}
]
output [
  {{ name: "tokens" data_type: TYPE_INT32 dims: [ {SLOTS} ] }},
  {{ name: "encoded_len" data_type: TYPE_INT64 dims: [ 1 ] }}
]
instance_group [ {{ count: 1 kind: KIND_CPU }} ]
"""


def graph() -> bytes:
    import onnx  # type: ignore[import-not-found,unused-ignore]
    from onnx import TensorProto, helper

    f32, i32, i64 = TensorProto.FLOAT, TensorProto.INT32, TensorProto.INT64
    inputs = [
        helper.make_tensor_value_info("audio_signal", f32, ["B", N_MELS, T]),
        helper.make_tensor_value_info("length", i64, ["B", 1]),
        helper.make_tensor_value_info("start", i32, ["B", 1]),
        helper.make_tensor_value_info("prompt", i64, ["B", 1]),
        helper.make_tensor_value_info("count_in", f32, ["B", 1]),
    ]
    outputs = [
        helper.make_tensor_value_info("tokens", i32, ["B", SLOTS]),
        helper.make_tensor_value_info("encoded_len", i64, ["B", 1]),
        helper.make_tensor_value_info("count_out", f32, ["B", 1]),
    ]
    nodes = [
        helper.make_node("Constant", [], ["one"], value=helper.make_tensor("one", f32, [], [1.0])),
        helper.make_node("Constant", [], ["hundred"], value=helper.make_tensor("hundred", f32, [], [100.0])),
        helper.make_node("Constant", [], ["minus_one"], value=helper.make_tensor("minus_one", f32, [], [-1.0])),
        helper.make_node("Add", ["count_in", "one"], ["next"]),
        helper.make_node("Cast", ["start"], ["is_start"], to=TensorProto.BOOL),
        helper.make_node("Where", ["is_start", "one", "next"], ["count_out"]),
        helper.make_node("Cast", ["prompt"], ["prompt_f"], to=f32),
        helper.make_node("Mul", ["prompt_f", "hundred"], ["base"]),
        helper.make_node("Add", ["base", "count_out"], ["token_f"]),
        helper.make_node("Mul", ["count_out", "minus_one"], ["neg"]),
        helper.make_node("Div", ["neg", "count_out"], ["none_f"]),  # -1 in every row
        helper.make_node("Concat", ["token_f", "none_f"], ["tokens_f"], axis=1),
        helper.make_node("Cast", ["tokens_f"], ["tokens"], to=i32),
        helper.make_node("Identity", ["length"], ["encoded_len"]),
    ]
    g = helper.make_graph(nodes, "fixture_step", inputs, outputs)
    m = helper.make_model(g, opset_imports=[helper.make_opsetid("", 17)], producer_name="cadence-serving-fixture")
    m.ir_version = 8
    onnx.checker.check_model(m)
    return bytes(m.SerializeToString())


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", type=Path, default=OUT)
    a = ap.parse_args()
    model = a.out / "model"
    (model / "1").mkdir(parents=True, exist_ok=True)
    (model / "1" / "model.onnx").write_bytes(graph())
    (model / "config.pbtxt").write_text(CONFIG, encoding="utf-8")
    vocab = [f"▁w{i}" for i in range(VOCAB - 1)] + ["<blank>"]
    geo = {
        "chunk_size": list(CHUNK),
        "pre_encode_cache_size": list(PRE),
        "buffer_frames": T,
        "chunk_ms": 80,
        "att_context_size": [56, 0],
        "n_mels": N_MELS,
        "window_stride_s": 0.01,
        "sample_rate": 16000,
        "prompt_dictionary": {"en-US": 1, "he-IL": 3, "auto": 0},
        "blank_id": VOCAB - 1,
        "vocab_size": VOCAB,
        "valid_out_len": 1,
        "max_symbols": SLOTS,
        "precision": "fp32",
        "float_state": True,
        "state_io": {"count": ["count_in", "count_out"]},
        "state_shapes": {"count": [1]},
        "frontend": {"kind": "fixture"},
        "vocabulary": vocab,
    }
    (model / "streaming_cfg.json").write_text(json.dumps(geo, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    dep = {
        "schema": "cadence.deployable/1",
        "format": "fixture-step-graph",
        "family": "nemo.fastconformer-rnnt.cache-aware",
        "profile": "80ms",
        "weightsHash": "fixture",
        "serving": {
            "server": {"kind": "triton", "minVersion": "26.08"},
            "modelDir": "model",
            "memoryMb": 512,
            "maxStreams": 64,
            "sampleRate": 16000,
            "chunkMs": 80,
            "boost": {"static": False, "dynamic": False},
        },
    }
    (a.out / "deployable.json").write_text(json.dumps(dep, indent=1) + "\n", encoding="utf-8")
    print(f"wrote {a.out}")


if __name__ == "__main__":
    main()
