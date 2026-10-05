"""E1 helper: what an exported graph holds — initializer bytes by dtype, op counts, Cast nodes.
  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/inspect_onnx.py /work/onnx/base-80/step.onnx"""

import collections
import sys

import onnx
from onnx import numpy_helper  # noqa: F401

m = onnx.load(sys.argv[1], load_external_data=False)
by = collections.Counter()
for t in m.graph.initializer:
    n = 1
    for d in t.dims:
        n *= d
    by[onnx.TensorProto.DataType.Name(t.data_type)] += n
print("initializer elements by dtype", dict(by))
ops = collections.Counter(n.op_type for n in m.graph.node)
print("nodes", len(m.graph.node), ops.most_common(25))
casts = collections.Counter(
    onnx.TensorProto.DataType.Name(a.i) for n in m.graph.node if n.op_type == "Cast" for a in n.attribute if a.name == "to"
)
print("casts to", dict(casts))
