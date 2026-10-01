"""A step kind's version names one parameter schema for good (R45; the control plane refuses a neutral kind whose
schema differs between runtimes). This test keeps worker/step-kinds.lock.json: every installed kind@version with the
hash of its schema identity. Changing a kind's parameters, types, constraints, literal defaults, ranges or artifact
types without bumping its version fails here; wording and defaults taken from defaults.yaml (defaultRef) do not count,
the same rule as the control plane's workers.SchemaHash. Update the lock for a new kind or version with
CADENCE_UPDATE_SCHEMA_LOCK=1 uv run pytest tests/test_schema_lock.py."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
from typing import Any

from cadence_worker.registry import registry

LOCK = Path(__file__).resolve().parents[1] / "step-kinds.lock.json"


def _identity(v: Any) -> Any:
    if isinstance(v, dict):
        xc = v.get("x-cadence")
        ref = xc.get("defaultRef", "") if isinstance(xc, dict) else ""
        out: dict[str, Any] = {}
        for k, val in v.items():
            if k in ("description", "title", "examples") or (k == "default" and ref):
                continue
            if k == "x-cadence":
                if isinstance(val, dict):
                    out[k] = {"defaultRef": ref} if ref else {kk: val[kk] for kk in ("default", "range") if kk in val}
                continue
            out[k] = _identity(val)
        return out
    if isinstance(v, list):
        return [_identity(x) for x in v]
    return v


def identity_hash(desc: Any) -> str:
    doc = {"params": _identity(desc["params"]), "consumes": desc["consumes"], "produces": desc["produces"]}
    return hashlib.sha256(json.dumps(doc, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def current() -> dict[str, str]:
    return {f"{name}@{d['version']}": identity_hash(d) for name, d in sorted(registry().items())}


def test_step_kind_versions_keep_their_schema() -> None:
    now = current()
    if os.environ.get("CADENCE_UPDATE_SCHEMA_LOCK") == "1":
        old = json.loads(LOCK.read_text(encoding="utf-8")) if LOCK.exists() else {}
        changed = [k for k in now if k in old and old[k] != now[k]]
        assert not changed, f"refusing to rewrite the schema of an existing version: {changed} — bump the version"
        LOCK.write_text(json.dumps({**old, **now}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    lock = json.loads(LOCK.read_text(encoding="utf-8"))
    changed = sorted(k for k in now if k in lock and lock[k] != now[k])
    missing = sorted(k for k in now if k not in lock)
    assert not changed, f"step kinds changed their schema without a new version: {changed} — bump each version"
    assert not missing, (
        f"step kinds missing from step-kinds.lock.json: {missing} — run with CADENCE_UPDATE_SCHEMA_LOCK=1"
    )


def test_identity_ignores_wording_and_referenced_defaults() -> None:
    a = {
        "params": {
            "properties": {
                "n": {
                    "type": "integer",
                    "default": 3,
                    "description": "x",
                    "x-cadence": {"defaultRef": "training.steps", "default": 3, "source": "s"},
                }
            }
        },
        "consumes": {},
        "produces": {},
    }
    b = json.loads(json.dumps(a))
    b["params"]["properties"]["n"].update({"default": 9, "description": "y"})
    c = json.loads(json.dumps(a))
    c["params"]["properties"]["n"]["type"] = "number"
    assert identity_hash(a) == identity_hash(b) != identity_hash(c)
