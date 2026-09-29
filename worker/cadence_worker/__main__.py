import json
from importlib.metadata import entry_points


def registry() -> dict:
    kinds = {}
    for ep in entry_points(group="cadence.steps"):
        cls = ep.load()
        kinds[ep.name] = {
            "version": getattr(cls, "version", "0"),
            "params": cls.params_schema(),
            "consumes": getattr(cls, "consumes", []),
            "produces": getattr(cls, "produces", []),
            "resources": getattr(cls, "resources", {}),
        }
    return kinds


if __name__ == "__main__":
    # Spike A2/A4 stub: print the registry the control plane would receive.
    print(json.dumps(registry(), indent=2))
