# Cadence spec — Agent-native rules

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Agent-native rules

The UI is one client of the API among three; if a thing can only be done by clicking, it is a bug.

1. One contract. OpenAPI 3.1 is the source of truth; the UI client, the MCP tools and the `cadence` CLI are generated from it. The only UI-only endpoints are workspace layouts and the media endpoints (tag `media`, 06 "Media"): the manual transcription test (R47), which needs a person at the microphone and stores nothing, and utterance audio and peaks (R25), because agents read no raw audio.
2. Commands, not writes. Every mutation is a command with an id, an actor (user, agent session or automation), an idempotency key and a `dryRun` flag, and it emits a domain event.
3. Jobs for anything long. The call returns a job id at once; progress arrives as events. Agents wait with a `jobs.wait` tool that has a timeout, never by sleeping in a shell.
4. Estimate before spending. GPU-consuming commands answer `dryRun` with GPU-hours, card, data volume and expected duration; the estimate is what approval gates check.
5. Same data, same shape. Logs are structured JSON lines, metrics are series, eval results are tables. Agents read exactly what panels show, through MCP resources and tools.
6. Files where files fit. Recipes are files in the recipes repository, edited by people and agents alike; database entities point at commit SHAs.
7. A context bridge both ways. A selection in the UI attaches to the prompt as a reference (`@run:123`, `@eval:45#he-IL/[56,0]`); references the agent writes render as links that open the panel.
8. Skills carry the workflows. Cadence ships `cadence-data`, `cadence-train`, `cadence-eval`, `cadence-deploy` and `cadence-flywheel`, plus NVIDIA's `nemo-speech-asr-finetune` skill.
9. One permission model. People and agents go through the same policy; an agent session gets a scoped token that expires with the session.
10. Learnings are written down. An agent records what it learned about a project (a batch duration that OOMs, a mount that is slow) through projects.note, which commits to NOTES.md in the project repository and shows in the Project document; the next session reads it through AGENTS.md.
11. One name in three places. An action is <entity>.<verb> — the OpenAPI operationId, the MCP tool name and the UI command id are the same string, and the verb comes from the vocabulary in the UI shell tab (new, preview, run, freeze, register, adopt, promote, accept, approve, note, archive). The generator refuses an operation whose verb is not in the vocabulary, so the agent learns one set of words and the user sees the same words in every menu.
12. Errors teach. Every problem+json body carries a type URI that resolves to a help article; an agent that hits an error reads the article through help.get before retrying, and a person sees the same page from the error toast.
