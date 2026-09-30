"""Cadence worker command line.

python -m cadence_worker                     print the step-kind registry (every installed kind)
python -m cadence_worker registry [RUNTIME]  the same, limited to what a worker of RUNTIME publishes
python -m cadence_worker families [RUNTIME]  the model families
python -m cadence_worker serve               register with the control plane and run leases (cadence_worker.config)
"""

from __future__ import annotations

import json
import sys

from cadence_worker.registry import RegistryError, load_families, registry

__all__ = ["RegistryError", "main", "registry"]


def main(argv: list[str]) -> int:
    cmd = argv[1] if len(argv) > 1 else "registry"
    runtime = argv[2] if len(argv) > 2 else None
    if cmd == "registry":
        print(json.dumps(registry(runtime), indent=2))
        return 0
    if cmd == "families":
        print(json.dumps([f.descriptor for f in load_families(runtime)], indent=2))
        return 0
    if cmd == "serve":
        from cadence_worker.serve import serve

        return serve()
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
