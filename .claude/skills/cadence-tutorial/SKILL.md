---
name: cadence-tutorial
description: How to write or edit a chapter of the Cadence book "Fine-Tuning ASR Models with Cadence" (docs/tutorial) — the method, the term map, chapter shape, tours and screenshots. Load when asked to write, continue, review or restructure the tutorial/methodology/книгу/методичку.
---

# cadence-tutorial

Read `docs/tutorial/GUIDELINES.md` (the method) and `docs/tutorial/OUTLINE.md` (contents, term map, status) first.

1. Pick the chapter from OUTLINE.md; read its spec sections, help articles and the previous chapter's summary.
2. Do the hands-on steps on the stand in the running project (Serbian, project `test`); record ids, numbers and
   real error messages. Never invent results; cite each number's entity id in an HTML comment.
3. Write `docs/tutorial/chapters/NN-slug.md` with the front matter of GUIDELINES §5.1 and the chapter shape of §2.3:
   opening · "In this chapter you will" · "Before you start" · concept/hands-on alternating · sidebars · "What you
   learned" · exercises · further reading.
4. Terms: introduce only those the term map assigns to this chapter — italic on first use, defined in plain words
   with an example; never use a later chapter's term. Change the map first if needed.
5. Why → how → guardrail for every action; never teach a workaround for a gate or approval.
6. Annotated screenshots (```annotated``` blocks, GUIDELINES §5.3) the first time a panel appears: ≤ 7 numbered
   regions with `target`s, a label and a 1–3 sentence note each, referenced from the text by number.
7. Tours: ```tour``` blocks with `data-command` / `data-panel` / `data-tour` targets only; a tour never performs a
   spending or gated action.
8. Facts against the code at `written-against`; defaults from defaults.yaml with their source; ML claims cited.
9. Update OUTLINE.md (status, terms introduced, open questions). English, British spelling, second person.
