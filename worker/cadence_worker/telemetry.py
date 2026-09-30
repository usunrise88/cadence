"""Card telemetry for claims and heartbeats (CardTelemetry): pynvml when importable, else nvidia-smi, else none (a CPU
worker reports no cards)."""

from __future__ import annotations

import contextlib
import importlib
import shutil
import subprocess
from typing import Any

from cadence_worker.protocol_gen import CardTelemetry

SMI_FIELDS = "index,name,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw"


def _num(s: str) -> float | None:
    try:
        return float(s.strip())
    except ValueError:
        return None


def parse_smi(text: str) -> list[CardTelemetry]:
    cards: list[CardTelemetry] = []
    for line in text.splitlines():
        parts = [p.strip() for p in line.split(",")]
        if len(parts) != 7 or not parts[0].isdigit():
            continue
        card: CardTelemetry = {"index": int(parts[0]), "name": parts[1]}
        if (total := _num(parts[2])) is not None:
            card["memoryTotalMb"] = int(total)
        if (used := _num(parts[3])) is not None:
            card["memoryUsedMb"] = int(used)
        if (util := _num(parts[4])) is not None:
            card["utilization"] = max(0.0, min(1.0, util / 100))
        if (temp := _num(parts[5])) is not None:
            card["temperatureC"] = temp
        if (power := _num(parts[6])) is not None:
            card["powerW"] = power
        cards.append(card)
    return cards


def _smi() -> list[CardTelemetry] | None:
    exe = shutil.which("nvidia-smi")
    if exe is None:
        return None
    try:
        out = subprocess.run(
            [exe, f"--query-gpu={SMI_FIELDS}", "--format=csv,noheader,nounits"],
            capture_output=True,
            text=True,
            timeout=10,
            check=True,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    return parse_smi(out)


def _nvml() -> list[CardTelemetry] | None:
    try:
        nv: Any = importlib.import_module("pynvml")
        nv.nvmlInit()
    except Exception:
        return None
    cards: list[CardTelemetry] = []
    try:
        for i in range(nv.nvmlDeviceGetCount()):
            h = nv.nvmlDeviceGetHandleByIndex(i)
            mem = nv.nvmlDeviceGetMemoryInfo(h)
            name = nv.nvmlDeviceGetName(h)
            card: CardTelemetry = {
                "index": i,
                "name": name.decode() if isinstance(name, bytes) else str(name),
                "memoryTotalMb": int(mem.total) // (1 << 20),
                "memoryUsedMb": int(mem.used) // (1 << 20),
            }
            try:
                card["utilization"] = nv.nvmlDeviceGetUtilizationRates(h).gpu / 100
                card["temperatureC"] = float(nv.nvmlDeviceGetTemperature(h, 0))
                card["powerW"] = nv.nvmlDeviceGetPowerUsage(h) / 1000
            except Exception:
                pass
            cards.append(card)
    except Exception:
        return None
    finally:
        with contextlib.suppress(Exception):
            nv.nvmlShutdown()
    return cards


class Telemetry:
    """Reads the cards; ``enabled=False`` (a CPU runtime) reports none."""

    def __init__(self, enabled: bool = True) -> None:
        self.enabled = enabled

    def cards(self) -> list[CardTelemetry]:
        if not self.enabled:
            return []
        for probe in (_nvml, _smi):
            cards = probe()
            if cards is not None:
                return cards
        return []
