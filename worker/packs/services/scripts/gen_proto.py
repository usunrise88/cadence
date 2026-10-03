"""Regenerate the OASIS protobuf module from the vendored contract (proto/oasis_contract/v1/asr.proto, its source in
proto/SOURCE.yaml) into cadence_services/oasis_gen: asr_pb2.py and its type stubs asr_pb2.pyi. Only the messages are
generated; the client calls the two unary methods by their full names (cadence_services/oasis.py), so no gRPC stub
module is needed. Run from worker/: ``uv run python packs/services/scripts/gen_proto.py``."""

from __future__ import annotations

import sys
from pathlib import Path

from grpc_tools import protoc

PACK = Path(__file__).resolve().parents[1]
PROTO_DIR = PACK / "proto" / "oasis_contract" / "v1"
OUT = PACK / "cadence_services" / "oasis_gen"


def main() -> int:
    OUT.mkdir(parents=True, exist_ok=True)
    args = ["protoc", f"-I{PROTO_DIR}", f"--python_out={OUT}", f"--pyi_out={OUT}", str(PROTO_DIR / "asr.proto")]
    code = int(protoc.main(args))
    if code == 0:
        print(f"generated {sorted(p.name for p in OUT.glob('asr_pb2*'))}")
    return code


if __name__ == "__main__":
    sys.exit(main())
