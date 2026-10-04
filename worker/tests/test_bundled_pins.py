"""Every step kind a bundled pipeline pins (control-plane/templates/pipelines, the example project's pipelines) is the
version the installed packs publish now: the control plane refuses to plan a version no registered worker publishes
(it would wait in the queue for ever), so a stale pin in a template breaks every new project. Kinds no pack carries
yet are allowed only when listed below with the phase that brings them."""

from __future__ import annotations

import json
import os
import re
from pathlib import Path

import yaml

from cadence_worker.registry import load_kinds, registry
from cadence_worker.steps.base import runtime_of

ROOT = Path(__file__).resolve().parents[2]
PIPELINE_DIRS = [
    ROOT / "control-plane" / "templates" / "pipelines",
    *sorted((ROOT / "recipes" / "projects").glob("*/pipelines")),
]

# Kinds a bundled pipeline may pin before a pack publishes them, with the phase that brings them (none now).
PLANNED: set[str] = set()

PIN = re.compile(r"^([a-z][a-z0-9_]*)@([0-9]+)$")

# The descriptors of every kind a bundled pipeline pins, as the workers publish them: the control plane's integration
# test (internal/pipelines, TestBundledPipelinesPlan) plans every bundled pipeline against them, so a template that
# miswires a step or sets a parameter out of range fails in CI. Regenerate with
# CADENCE_UPDATE_BUNDLED_KINDS=1 uv run pytest tests/test_bundled_pins.py.
BUNDLED_KINDS = ROOT / "control-plane" / "internal" / "pipelines" / "testdata" / "bundled-kinds.json"


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


def bundled_kinds() -> list[dict[str, object]]:
    entries = load_kinds()
    published = registry()
    out: list[dict[str, object]] = []
    for name in sorted({name for _, name, _ in pins() if name in published}):
        d: dict[str, object] = {"name": name, **published[name]}
        if rt := runtime_of(entries[name].cls):
            d["runtime"] = rt
        out.append(d)
    return out


def test_bundled_kinds_fixture_is_current() -> None:
    text = json.dumps(bundled_kinds(), indent=1, sort_keys=True, ensure_ascii=False) + "\n"
    if os.environ.get("CADENCE_UPDATE_BUNDLED_KINDS"):
        BUNDLED_KINDS.parent.mkdir(parents=True, exist_ok=True)
        BUNDLED_KINDS.write_text(text, encoding="utf-8")
    assert BUNDLED_KINDS.is_file(), f"{BUNDLED_KINDS.relative_to(ROOT)} is missing"
    assert BUNDLED_KINDS.read_text(encoding="utf-8") == text, (
        f"{BUNDLED_KINDS.relative_to(ROOT)} is stale: CADENCE_UPDATE_BUNDLED_KINDS=1 uv run pytest "
        "tests/test_bundled_pins.py"
    )
