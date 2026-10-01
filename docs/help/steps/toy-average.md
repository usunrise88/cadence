---
title: toy_average (step kind)
summary: The toy pack's average role — the element-wise mean of two or more toy checkpoints.
contexts: [step:toy_average]
---

## What this is

`toy_average@1` fills the `average` role of the `toy-ctc` family. It takes two or more `checkpoint` artifacts as
`checkpoints.0`, `checkpoints.1`, … (the declared input `checkpoints` receives several artifacts that way), averages
their weights and writes one `checkpoint` whose meta names the family, the largest step, the weights hashes it was
averaged from (`averagedFrom`) and its own `weightsHash`. It refuses checkpoints of another family.

## Place in the loop

Train — after a run, before evaluating the averaged weights; the conformance suite's average stage.

## Fields and defaults

No parameters: the inputs say what to average.

## Commands

`pipelines.run`; `checkpoints.average` once runs use a toy base model.

## Playbooks

None.

## Sources

docs/spec/08-resolutions.md R41 (the average role), R45.
