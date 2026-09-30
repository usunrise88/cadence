"""``python -m cadence_worker.conformance --runtime RUNTIME [--report FILE] [--help-dir DIR]``: run the suite for the
packs of one runtime; prints the report as JSON and exits 1 when a stage failed."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from cadence_worker.conformance.suite import run


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="python -m cadence_worker.conformance")
    ap.add_argument("--runtime", required=True, help="runtime whose packs to check (toy, nemo-speech)")
    ap.add_argument("--report", type=Path, help="also write the JSON report here")
    ap.add_argument("--help-dir", type=Path, help="docs/help to check help articles against (default: the checkout's)")
    args = ap.parse_args(argv[1:])
    report = run(args.runtime, help_dir=args.help_dir)
    text = json.dumps(report.to_json(), indent=2)
    print(text)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(text + "\n", encoding="utf-8")
    for s in report.stages:
        mark = "ok  " if s.ok else "FAIL"
        print(f"{mark} {s.name:<32} {s.seconds:7.2f}s", file=sys.stderr)
    return 0 if report.ok else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
