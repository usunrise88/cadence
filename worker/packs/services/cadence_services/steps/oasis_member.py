"""oasis_transcribe — the OASIS pseudo-label member (docs/review/2026-10-03-phase-4-plan.md "Owner decisions" 1,
"Decisions taken for phase 4" 6): send every utterance of a ``dataset`` to the running OASIS service the auxiliary
names and write its texts as ``hypotheses``.

The service is the auxiliary's payload ``service`` (``{kind: grpc-asr, endpoint, protocol: oasis.v1, tokenSecret}``),
resolved by the control plane for the project (``auxiliary/oasis``). Before any audio is sent, ``GetModelInfo`` must
answer within ``health_timeout_s``, else the step fails with ``auxiliary-unavailable`` (Cadence never starts OASIS:
``scripts/serve.sh ensemble no-300m`` on the host); the languages it reports must include each utterance's. Each
utterance goes as one unary ``Transcribe(lang, 16 kHz PCM16)``, ``concurrency`` calls at a time. OASIS is itself a
vote of several models, so its rows carry ``vote: true`` and the ensemble prefers its text when it agrees. Its text is
spoken form without punctuation or capitals. Rows: ``{audio, text, member, language, confidence, reason, vote, model,
decodingHash}``; a ``NON_SPEECH`` answer is an empty text. Help: docs/help/steps/oasis-transcribe.md.
"""

from __future__ import annotations

import os
from collections.abc import Mapping
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_services import oasis
from cadence_worker.members import (
    Utterance,
    decoding_hash,
    member_label,
    pcm16_16k,
    primary,
    read_utterances,
    write_jsonl,
)
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext
from cadence_worker.translit import transliterate

RUNTIME = "services"
TOKEN_SECRET = "oasis-token"  # the lease injects it as OASIS_TOKEN
TOKEN_ENV = "OASIS_TOKEN"


class OasisParams(BaseModel):
    auxiliary: str = cadence_field(
        description="The OASIS service to call (auxiliary/<name>, ver_… or @alias): an auxiliary with the "
        "pseudolabel role and a service of protocol oasis.v1 the project adopted",
        default_ref="packs.services.oasis_auxiliary",
        registry_ref={"kind": "auxiliary", "role": "pseudolabel"},
    )
    timeout_s: float = cadence_field(default_ref="packs.services.oasis_timeout_s")
    health_timeout_s: float = cadence_field(default_ref="packs.services.oasis_health_timeout_s")
    concurrency: int = cadence_field(default_ref="packs.services.oasis_concurrency")
    campaign_id: str = cadence_field(default_ref="packs.services.oasis_campaign_id")
    target_lang: str = cadence_field(
        "",
        description="Send every utterance in this language (BCP-47) instead of each utterance's own; empty uses the "
        "utterance's language",
        source="Cadence recommendation",
        range={"maxLength": 35},
    )
    transliterate: Literal["", "sr-Cyrl-Latn"] = cadence_field(
        "",
        description="Convert the text to another script (sr-Cyrl-Latn: Serbian Cyrillic to Gaj Latin, as "
        "dataset_import does); empty keeps the service's script",
        source="Cadence recommendation (the Serbian fine-tune on the test stand, 2026-10-01)",
        range={"values": ["", "sr-Cyrl-Latn"]},
    )


def service_of(aux: Mapping[str, Any]) -> Mapping[str, Any]:
    svc = aux["payload"].get("service")
    if not isinstance(svc, Mapping) or not svc.get("endpoint"):
        raise StepInputError(f"{aux.get('name')} names no running service; this step calls one")
    if svc.get("protocol") != oasis.PROTOCOL:
        raise StepInputError(f"{aux.get('name')} speaks {svc.get('protocol')!r}; this step speaks {oasis.PROTOCOL}")
    return svc


class OasisTranscribeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": False, "memoryGb": 2, "diskGb": 2, "jobKind": "data"}
    runtime: ClassVar[str] = RUNTIME
    secrets: ClassVar[tuple[str, ...]] = (TOKEN_SECRET,)
    Params: ClassVar[type[BaseModel]] = OasisParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = OasisParams.model_validate(params.model_dump())
        aux = ctx.auxiliary("auxiliary")
        svc = service_of(aux)
        utts = read_utterances(inputs["data"])
        member = member_label(aux)
        endpoint = str(svc["endpoint"])
        with oasis.Client(endpoint, os.environ.get(TOKEN_ENV, ""), p.timeout_s, p.health_timeout_s) as client:
            info = client.model_info()  # auxiliary-unavailable when it does not answer
            ctx.log("service answers", endpoint=endpoint, engine=info.engine_id, modelVersion=info.model_version)

            def lang(u: Utterance) -> str:
                code = primary(p.target_lang or u.language)
                if not code:
                    raise StepInputError(f"utterance {u.hash} has no language; OASIS needs one (or set target_lang)")
                if info.supported_languages and code not in info.supported_languages:
                    raise StepInputError(
                        f"the service transcribes {', '.join(info.supported_languages)}, not {code!r} "
                        "(OASIS answers a language it never learned with fluent, wrong text)"
                    )
                return code

            langs = [lang(u) for u in utts]  # every language checked before any audio is sent
            decoding = {
                "decoder": "oasis.v1/Transcribe",
                "engine": info.engine_id,
                "modelVersion": info.model_version,
                "campaignId": p.campaign_id,
                "transliterate": p.transliterate,
            }
            dhash = decoding_hash(decoding)
            model = {
                "auxiliary": aux.get("name"),
                "versionId": aux.get("versionId"),
                "endpoint": endpoint,
                "engine": info.engine_id,
                "modelVersion": info.model_version,
            }

            def call(i: int) -> dict[str, Any]:
                u = utts[i]
                r = client.transcribe(pcm16_16k(u.path), langs[i], p.campaign_id)
                text = "" if r.reason == "NON_SPEECH" else transliterate(r.text.strip(), p.transliterate)
                return {
                    "audio": u.hash,
                    "text": text,
                    "member": member,
                    "language": langs[i],
                    "confidence": round(r.confidence, 4),
                    "reason": r.reason,
                    "vote": True,
                    "model": model,
                    "decodingHash": dhash,
                }

            rows: list[dict[str, Any]] = []
            with ThreadPoolExecutor(max_workers=p.concurrency) as pool:
                for row in pool.map(call, range(len(utts))):
                    rows.append(row)
                    if len(rows) % 20 == 0 or len(rows) == len(utts):
                        ctx.progress(len(rows) / len(utts), f"{len(rows)}/{len(utts)} utterances")
                    if ctx.should_stop():
                        pool.shutdown(cancel_futures=True)
                        return
        write_jsonl(outputs["hypotheses"], rows)
        ctx.set_meta("hypotheses", {"member": member, **model, "decodingHash": dhash, "utterances": len(rows)})
        ctx.log("transcribed", utterances=len(rows), member=member)
