"""Generate cadence_worker/protocol_gen.py from api/openapi.yaml (run by `make gen`; never edit the output).

The worker's client types are the schemas reachable from the operations tagged `worker`, emitted as TypedDicts so
mypy checks every request and response against the contract (CLAUDE.md: never hand-write client types).
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml

ROOT = Path(__file__).resolve().parents[2]
SPEC = ROOT / "api" / "openapi.yaml"
OUT = ROOT / "worker" / "cadence_worker" / "protocol_gen.py"
PREFIX = "#/components/"

HEADER = '''"""Worker protocol types generated from api/openapi.yaml (tag `worker`) by worker/scripts/gen_protocol.py.

Do not edit: change the contract and run `make gen`.
"""

from __future__ import annotations

from typing import Any, Literal, NotRequired, TypedDict

OPERATIONS: dict[str, tuple[str, str]] = {
'''


def resolve(spec: dict[str, Any], ref: str) -> tuple[str, dict[str, Any]]:
    assert ref.startswith(PREFIX), ref
    section, name = ref[len(PREFIX) :].split("/", 1)
    return name, spec["components"][section][name]


def camel(s: str) -> str:
    return s[:1].upper() + s[1:]


class Gen:
    def __init__(self, spec: dict[str, Any]) -> None:
        self.spec = spec
        self.classes: dict[str, list[str]] = {}
        self.pending: list[tuple[str, dict[str, Any]]] = []

    def want(self, ref: str) -> str:
        name, schema = resolve(self.spec, ref)
        if name not in self.classes and all(n != name for n, _ in self.pending):
            self.pending.append((name, schema))
        return name

    def type_of(self, schema: dict[str, Any], owner: str) -> str:
        if "$ref" in schema:
            name, target = resolve(self.spec, schema["$ref"])
            if target.get("type") == "object" or "properties" in target:
                return self.want(schema["$ref"])
            return self.type_of(target, name)
        if "enum" in schema:
            return "Literal[" + ", ".join(repr(v) for v in schema["enum"]) + "]"
        t = schema.get("type")
        if t == "string":
            return "str"
        if t == "integer":
            return "int"
        if t == "number":
            return "float"
        if t == "boolean":
            return "bool"
        if t == "array":
            return f"list[{self.type_of(schema.get('items', {}), owner + 'Item')}]"
        if t == "object" or "properties" in schema or "additionalProperties" in schema:
            if "properties" in schema:
                self.pending.append((owner, schema))
                return owner
            extra = schema.get("additionalProperties")
            if isinstance(extra, dict) and extra:
                return f"dict[str, {self.type_of(extra, owner + 'Value')}]"
            return "dict[str, Any]"
        return "Any"

    def emit(self, name: str, schema: dict[str, Any]) -> None:
        if name in self.classes:
            return
        lines: list[str] = []
        self.classes[name] = lines
        lines.append(f"class {name}(TypedDict):")
        if desc := schema.get("description"):
            lines.append(f'    """{" ".join(str(desc).split())}"""')
            lines.append("")
        required = set(schema.get("required", []))
        props: dict[str, Any] = schema.get("properties", {})
        if not props:
            lines.append("    pass")
        for prop, sub in props.items():
            t = self.type_of(sub, name + camel(prop))
            lines.append(f"    {prop}: {t}" if prop in required else f"    {prop}: NotRequired[{t}]")

    def run(self) -> str:
        ops: list[tuple[str, str, str]] = []
        for path, item in self.spec["paths"].items():
            for method, op in item.items():
                if not isinstance(op, dict) or "worker" not in op.get("tags", []):
                    continue
                ops.append((op["operationId"], method.upper(), path))
                body = op.get("requestBody", {}).get("content", {}).get("application/json", {}).get("schema")
                if body and "$ref" in body:
                    self.want(body["$ref"])
                for resp in op.get("responses", {}).values():
                    if "$ref" in resp:
                        _, resp = resolve(self.spec, resp["$ref"])
                    schema = resp.get("content", {}).get("application/json", {}).get("schema")
                    if schema and "$ref" in schema:
                        self.want(schema["$ref"])
        # Types the worker writes into request bodies that are not JSON (NDJSON log lines).
        self.want(PREFIX + "schemas/WorkerLogLine")
        while self.pending:
            self.emit(*self.pending.pop(0))
        out = [HEADER]
        for op_id, method, path in sorted(ops):
            out.append(f'    "{op_id}": ("{method}", "{path}"),\n')
        out.append("}\n")
        for name in sorted(self.classes):
            out.append("\n\n" + "\n".join(self.classes[name]) + "\n")
        return "".join(out)


def main() -> int:
    spec = yaml.safe_load(SPEC.read_text(encoding="utf-8"))
    text = Gen(spec).run()
    OUT.write_text(text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
