"""Every step kind a bundled pipeline pins (control-plane/templates/pipelines, the example project's pipelines) is the
version the installed packs publish now: the control plane refuses to plan a version no registered worker publishes
(it would wait in the queue for ever), so a stale pin in a template breaks every new project. Kinds no pack carries
yet are allowed only when listed below with the phase that brings them."""

from __future__ import annotations

import re
from pathlib import Path

import yaml

from cadence_worker.registry import registry

ROOT = Path(__file__).resolve().parents[2]
PIPELINE_DIRS = [
    ROOT / "control-plane" / "templates" / "pipelines",
    *sorted((ROOT / "recipes" / "projects").glob("*/pipelines")),
]

# The pseudo-label ensemble arrives with phase 4 · stream X; no worker publishes it yet.
PLANNED = {
    "pseudolabel_ensemble",
}

PIN = re.compile(r"^([a-z][a-z0-9_]*)@([0-9]+)$")


def pins() -> list[tuple[str, str, str]]:
    out: list[tuple[str, str, str]] = []
    for d in PIPELINE_DIRS:
        for f in sorted(d.glob("*.yaml")):
            doc = yaml.safe_load(f.read_text(encoding="utf-8")) or {}
            for step in doc.get("steps") or []:
                m = PIN.match(str(step.get("kind", "")))
                assert m, f"{f.relative_to(ROOT)}: step {step.get('id')!r} pins no name@version"
                out.append((str(f.relative_to(ROOT)), m.group(1), m.group(2)))
    return out


def test_bundled_pipelines_pin_published_versions() -> None:
    published = {name: d["version"] for name, d in registry().items()}
    found = pins()
    assert found, "no bundled pipelines found"
    stale = [
        f"{where}: {name}@{ver} (installed: {name}@{published[name]})"
        for where, name, ver in found
        if name in published and published[name] != ver
    ]
    unknown = [f"{where}: {name}@{ver}" for where, name, ver in found if name not in published and name not in PLANNED]
    assert not stale, f"bundled pipelines pin versions no pack publishes now: {stale}"
    assert not unknown, f"bundled pipelines pin kinds no pack carries (add them to PLANNED with their phase): {unknown}"
