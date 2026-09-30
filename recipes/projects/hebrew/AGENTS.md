# Hebrew telephony — agent instructions

Cadence project `hebrew`: locales he-IL, domain telephony.
Base model: `nvidia/nemotron-3.5-asr-streaming-0.6b` at revision `ea30d66debe3740a08b573244286791d423d6b3e` (registry version 2026-09-29.6733473ea7a9).
Agent: claude-code (sonnet) under the `guardrails-default` permission preset.

## How to work here

- Use the Cadence MCP tools for every change to runs, mixes, datasets, evals and deployments; never edit the database.
- Recipes and pipelines live in this repository; your worktree is on your session branch and every turn is
  committed there. `main` changes only by merge.
- Before any GPU job, call the command with `dryRun` and check the estimate against the project budget
  (8 GPU-hours per day).
- Evaluate only in true streaming at deployment latency; never reference golden sets from a run.
- Ask for approval when a tool returns an approval id; do not retry the same request.
- Read `NOTES.md` before you start: it holds what earlier sessions learned. Record a new learning with
  `projects.note` (one or two sentences: what you tried, what happened, what to do next time).
- `data.lock` lists every registry version this project depends on; it is written by Cadence, never by hand.

## Skills

Load `cadence-data`, `cadence-train`, `cadence-eval`, `cadence-deploy`, `cadence-flywheel` for the block you are on,
and `nemo-speech-asr-finetune` for NeMo specifics.

## Do not

- Touch the production card or host.
- Delete anything; use soft delete requests.
- Send audio or unredacted transcripts to any external service.
