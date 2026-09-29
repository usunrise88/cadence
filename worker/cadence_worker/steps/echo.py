"""Smallest possible step kind — the template for every real one.

A step declares: a params schema (JSON Schema), the artifact types it consumes and produces,
resource needs, and a run() that reads inputs, writes outputs and returns nothing else.
Steps never touch the database; inputs and outputs are artifacts."""

from pydantic import BaseModel


class EchoParams(BaseModel):
    text: str = "hello"


class EchoStep:
    version = "1"
    consumes: list[str] = []
    produces = ["text"]
    resources = {"gpu": False}

    @staticmethod
    def params_schema() -> dict:
        return EchoParams.model_json_schema()

    def run(self, params: dict, inputs: dict, outputs: dict) -> None:
        p = EchoParams(**params)
        outputs["text"] = p.text
