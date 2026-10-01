---
title: echo (step kind)
summary: A trivial step that copies its text input to its output — the template every real step kind follows.
contexts: [step:echo]
---

## What this is

`echo@1` reads one `text` artifact (input `text`) and writes it, optionally prefixed, as a `text` artifact (output
`text`). It is a runtime-neutral core kind shipped in every worker image and exercises the whole step contract: the
`cadence.steps` entry point, named inputs and outputs with artifact types, resources (`jobKind: data`, no card), the
parameter schema with `x-cadence` metadata, the step context (a log line and progress) and the input hash that lets a
re-run skip it.

## Place in the loop

Run — as a stand-in step in pipeline tests, and in the `echo` starter pipeline that checks a worker runs steps end
to end (copy any text artifact twice). Never part of a training or data pipeline.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `prefix` | `""` | Cadence recommendation | 0–64 characters |

## Commands

`pipelines.run` runs pipelines that contain step kinds; `pipelines/echo` is the smallest one (see the
[pipelines guide](../guides/pipelines.md)); `stepKinds.list` shows what the workers published.

## Playbooks

None.

## Sources

Cadence recommendation — docs/spec/03-pipelines-defaults.md "Pipelines and extension points".
