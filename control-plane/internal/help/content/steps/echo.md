---
title: echo (step kind)
summary: A trivial step that copies its input to its output — the template every real step kind follows.
contexts: [step:echo]
---

## What this is

`echo@1` reads one text artifact and writes it unchanged. It exists to exercise the worker's step registry: the
entry point, the parameter schema with `x-cadence` metadata, and the input hash that lets a re-run skip it.

## Place in the loop

Run — as a stand-in step in pipeline tests, and in the `echo` starter pipeline that checks a worker runs steps end
to end (copy any text artifact twice). Never part of a training or data pipeline.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `prefix` | `""` | Cadence recommendation | 0–64 characters |

## Commands

`pipelines.run` runs pipelines that contain step kinds; `pipelines/echo` is the smallest one (see the
[pipelines guide](../guides/pipelines.md)).

## Playbooks

None.

## Sources

Cadence recommendation — docs/spec/03-pipelines-defaults.md "Pipelines and extension points".
