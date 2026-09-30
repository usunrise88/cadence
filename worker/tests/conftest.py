from __future__ import annotations

import sys
from pathlib import Path

# fake_steps and helpers are imported by name (here and by the step subprocess through PYTHONPATH).
sys.path.insert(0, str(Path(__file__).resolve().parent))
