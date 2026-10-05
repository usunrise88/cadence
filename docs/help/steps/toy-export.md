---
title: toy_export (step kind)
summary: The toy pack's export role — turns a toy checkpoint into an in-process deployable (cadence.deployable/1, format toy-pt-dir) so the export, parity and benchmark seams run on a CPU.
contexts: [step:toy_export, artifact:deployable, family:toy-ctc]
---

## What this is

`toy_export@1` fills the `export` role of the `toy-ctc` family (phase 5). It reads a `checkpoint` (input `model`) and
writes a `deployable` directory: `deployable.json` (`schema` `cadence.deployable/1`, `format` `toy-pt-dir`, `family`,
`profile`, `weightsHash`, `serving` with `server.kind` `toy`, `modelDir` `model`, `memoryMb`, `maxStreams`, `chunkMs`,
`files` — the model directory's files with SHA-256 and size — and `manifestSha256`) and `model/` (the checkpoint's
files). `manifestSha256` is the SHA-256 of one `<sha256>  <path>` line per file sorted by path, what a delivery
script's `find | sort | sha256sum` prints over the installed directory. Job kind `export`, no card.

Nothing serves it but [`toy_serve`](toy-serve.md): it exists so the conformance suite checks a family's export
contract on every pull request.

## Place in the loop

Block 4, Deploy — the conformance suite's `export` stage; `models.export` for a toy model version.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.toy.profile` (`offline`) | The toy-ctc family descriptor | `offline`, `320ms` |
| `format` | `toy-pt-dir` | The family's `exportFormats` | `toy-pt-dir` |

## Commands

`models.export`.

## Playbooks

None.

## Sources

docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)"; R45.
