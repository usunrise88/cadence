# How this book is written

Working title: **Fine-Tuning ASR Models with Cadence: From Start to Finish**.

This file is the method. `OUTLINE.md` holds the table of contents, the term map and the status of each chapter.
`.claude/skills/cadence-tutorial/SKILL.md` gives agents the same rules as a checklist. Change the method here
first, then the skill.

## 1. What the book is for

The book has two jobs, and every chapter serves both.

1. **Teach Cadence.** A reader who finishes a chapter can do that part of the loop in Cadence on their own: data,
   training, evaluation, and later deployment.
2. **Teach fine-tuning speech recognition.** The same reader understands *why* each step exists. They know what a
   word error rate hides, why a model forgets, and what a confidence interval is for. This holds even if they never
   use Cadence again.

The readers:

| Reader | Has | Wants |
| --- | --- | --- |
| A student (course, workshop, self-study) | Python, basic ML: what a training loop, a loss and a validation set are | A first real fine-tune and an honest evaluation of it |
| An engineer joining a team that runs Cadence | Software engineering; little or no speech | To run the loop safely without breaking budgets or gates |
| A speech practitioner new to Cadence | NeMo or similar, WER, streaming | To map what they know onto Cadence's entities and guardrails |

Write for the first reader and let the other two skim. Sidebars (§4) carry what only the second or third reader
needs.

We assume the reader knows what a neural network is and what training, a loss and a validation set mean. We do
not assume any speech knowledge. We do not teach Python, Docker or Git; the reader uses Cadence's UI, its agent
or its CLI, and the appendix covers installing a stand.

## 2. Pedagogy

The model is the O'Reilly learning book: a practitioner's voice, learning by doing, one running example, and
concepts introduced at the moment the reader needs them.

### 2.1 One running example, real numbers

The whole book follows one project.

- **The project:** fine-tune NVIDIA Nemotron 3.5 ASR Streaming 0.6B for **Serbian**, which the base model does not
  list as a language, using the public FLEURS corpus. Cadence's own phase-2 and phase-3 runs did exactly this on
  the staging stand, so every number in the book can be real.
- **Why Serbian:**
  - It has two scripts, so text normalisation is a real problem, not a toy.
  - It has a close neighbour the model already knows (Croatian), which motivates language prompts.
  - Fine-tuning it without replay made the model forget other languages. That is the best lesson the book has.
- **Hebrew,** Cadence's primary production language, appears in sidebars ("In the field") for what Serbian
  cannot show: right-to-left text, niqqud, telephone audio.
- **Real numbers:** every number the book shows comes from a recorded run. Cite the entity id and date in a
  comment (`<!-- evl_01a0fd78…, 2026-10-02 -->`) so the number can be traced and refreshed. Never invent results.
  If a number is illustrative, say so in the text.

### 2.2 Terms arrive just in time

The most important rule. A term appears only when the reader needs it to do the next thing, and never before
its chapter introduces it.

- **First use:**
  - Set the term in *italics*.
  - Define it in the same or the next sentence, in plain words, before any formula.
  - Give it one concrete example from the running project.
  - Add it to the term map in `OUTLINE.md` with its chapter.
- **Later uses:** plain text, no italics.
- **Before its chapter:** a term may not appear at all. If a sentence needs an idea that comes later, describe it
  in everyday words ("a held-out set of recordings the model never trains on") and say where it gets its name
  ("Chapter 7 calls this a *golden set*").
- **One name per concept:** use the glossary names of `docs/spec/10-ui-shell.md` (version vs revision; freeze,
  register, promote, adopt, alias, draft, note). Don't say "snapshot" for a version, or "test set" for a golden set
  once golden sets are introduced.
- **Acronyms:** spell out the first time (*word error rate*, WER). After that use the acronym.
- **Ceiling:** no more than about eight new terms per chapter. If a chapter needs more, split it.
- **Glossary:** Appendix A is generated from the term map. Each entry gives its definition and "introduced in
  Chapter N".

### 2.3 The shape of a chapter

Every chapter follows the same skeleton so readers know where they are.

1. **Opening, one paragraph.** A situation from the running project that creates the need ("Your model's
   validation error went down. Is it better?").
2. **"In this chapter you will"**: three to five outcomes, each a verb the reader can check ("start a training
   run and read its estimate before it spends anything").
3. **"Before you start"**: what must exist (the project from Chapter 3; a frozen dataset) and the time and GPU it
   costs.
4. **Body:** alternating sections of
   - *concept*: why, the smallest amount of theory that explains the next action;
   - *hands-on*: numbered steps in Cadence, each with the expected result and a screenshot or a tour (§5).
   Never more than two concept sections in a row.
5. **Sidebars** (§4) where they help, at most one per section.
6. **"What you learned"**: the outcomes again, as statements, plus the new terms.
7. **Exercises:** two to four, from "do it again with a change" to "explain why". Each has a checkable answer in
   Appendix C.
8. **Further reading:** papers, NeMo docs, Cadence help articles, by name and link.

Length: 3 000–6 000 words of prose per chapter, plus figures. If a chapter needs more, split it.

### 2.4 Teach the why, then the how, then the guardrail

For every Cadence action, give three answers in this order:

1. Why the step exists in fine-tuning at all (the ML reason).
2. How Cadence does it (the action).
3. What Cadence protects you from, and why that guardrail exists. Examples: the dry run before spending, approval
   of a golden-set freeze, the leakage check.

Guardrails are part of the lesson, not obstacles to route around. Never teach a workaround for a gate or an
approval.

### 2.5 Show failure on purpose

The best lessons in this book come from real failures:

- the leakage check refusing a test split;
- a model asked to decode a language it has no prompt for;
- word error rate over Chinese text that has no spaces;
- catastrophic forgetting of 32 languages.

Each chapter that has such a story tells it as an **"In the field"** sidebar with the real error message and how
it was fixed. Readers trust a book that shows its own mistakes.

## 3. Voice and style

- **Person, voice, tense:** second person ("you"), active voice, present tense. "Cadence freezes the set", not
  "the set is frozen by Cadence".
- **Paragraphs:** short, three to five sentences, one idea each.
- **Pronouns:** "we" only for the authors' recommendations ("we recommend starting at 160 ms").
- **Explain before naming:** "a check that the training data and the test data share no recordings (a *leakage
  check*)".
- **Numbers:**
  - Always with units and with what they were measured on: "WER 0.256 on the 700 FLEURS Serbian test utterances at
    160 ms".
  - Rates as decimals in tables (0.256) and as percentages in prose when that reads better (25.6 %). Be
    consistent inside one paragraph.
- **No hype:** no "powerful", "seamless", "simply", "just". If a step is easy, the steps show it.
- **Hedging:** hedge only where it is true. Where Cadence's behaviour is decided, state it. Where a default is a
  Cadence recommendation rather than a published result, say "Cadence recommends".
- **Language and spelling:** English, the same spelling as the rest of the docs (British: normalise, behaviour,
  licence). A Russian edition, if made, is a translation of a finished chapter, never a parallel draft.

## 4. Conventions (typography and boxes)

| Element | Convention |
| --- | --- |
| New term (first use) | *italic* |
| UI labels: buttons, panel names, menu items | **bold**, exactly as the UI writes them: **Run eval matrix**, the **Eval report** panel |
| Commands, operation ids, entity ids, paths, YAML keys, file names | `constant width`: `evals.new`, `gates.yaml`, `ckp_01a0…` |
| Text the reader types | `constant width` in a fenced block when longer than a few words |
| Placeholders | `<angle brackets>`: `golden-set/<name>` |
| Keyboard | Ctrl+K / ⌘K, written as in `docs/help/shell/keyboard.md` |

Boxes, written as blockquotes that start with a bold label, so they render everywhere (GitHub, the Help panel,
the HTML edition):

```markdown
> **Note** — background or a side fact.
> **Tip** — a faster or safer way.
> **Warning** — something that costs money, data or trust if done wrong.
> **Under the hood** — what Cadence or NeMo does internally; skippable.
> **The ML behind it** — the concept for readers new to speech or ML; skippable for practitioners.
> **In the field** — a real incident from the stand, with the real message.
```

Figures:

- A screenshot or diagram has a numbered caption ("Figure 6-2. The run estimate before anything is spent") and
  the text refers to it by number.
- Diagrams are Mermaid or SVG kept in `docs/tutorial/figures/`, never images of text.
- Every figure has alt text that says what it shows, not "screenshot".

## 5. Formats and delivery

### 5.1 One source

Chapters are Markdown files in `docs/tutorial/chapters/NN-slug.md`, with front matter:

```yaml
---
title: Measuring quality honestly
chapter: 7
part: II          # Part I Getting started, II The loop, III Going further, IV Production
phase: 3          # the Cadence ROADMAP phase whose features the chapter needs
status: draft     # planned | draft | reviewed | published
written-against: 6c301ac   # the Cadence commit the facts were checked against
terms: [golden set, leakage, scoring normalizer, CER, bootstrap, confidence interval, baseline]
prerequisites: [6]
summary: One sentence for the Help panel and the HTML table of contents.
---
```

The source feeds three outputs. None of them is edited by hand.

1. **The Help panel inside Cadence.** The chapters join the help bundle as section `tutorial`. Article id
   `tutorial.<NN-slug>`; contexts such as `panel:eval` make the right chapter show up from a panel's help.
   - This needs a small change: `cmd/helpsync` and `internal/help` know five sections today.
   - It is listed under "Building the book" in `OUTLINE.md`.
2. **The interactive HTML edition outside Cadence.** One self-contained site:
   - chapters, glossary with hover definitions, a search box;
   - screenshots: annotated ones with numbered regions and linked notes (§5.3), and the tours played as numbered
     highlights on them;
   - It is built by a script from the same Markdown and the tour captures (§5.4) and published as an Artifact or
     static files.
3. **Plain Markdown on GitHub**, readable without either.

### 5.2 Tours

A tour is the book's way to point at the screen. In the HTML edition it steps through highlighted regions of
screenshots. Inside Cadence it highlights the live UI: the "Show me" button in the Help panel starts it. Write a
tour as a fenced block:

````markdown
```tour id=first-run-estimate
title: Read the estimate before you spend
steps:
  - target: '[data-panel="run"]'
    say: The Run document opens with the plan, not a running job.
  - target: '[data-command="runs.new"]'
    say: Check runs a dry run. Nothing is queued and no GPU time is spent.
    action: click            # optional: click | open-panel <id> | command <id> | none (default)
  - target: '[data-tour="run-estimate"]'
    say: GPU-hours, the basis (table or measured) and the budget it counts against.
```
````

Rules:

- **Targets** are stable attributes the product already renders, in this order of preference:
  1. `data-command="<operationId>"`;
  2. `data-panel="<panel id>"`;
  3. `data-tour="<id>"`, added to product code for anything else, named `<panel>-<thing>`.
  Never use CSS classes, text or DOM position.
- **Size:** three to seven steps; one action per step; `say` is one or two sentences.
- **Side effects:** a tour never performs a spending or gated action itself. It points at the button and the
  reader clicks.
- **In-app tours:** they need a tour engine in the web shell. That is a product feature: spec it in
  `docs/spec/11-ui-panels.md` "First run and progressive disclosure", with a decision-log row, before it is built.
  Until then tours exist only in the HTML edition.

### 5.3 Annotated screenshots (screen walkthroughs)

A tour leads the reader through steps. An *annotated screenshot* explains one screen at once:

- the screenshot with numbered regions outlined on it;
- a comment for each region, saying what it is and why it matters.

It is the book's main way to teach a new panel. Use it the first time a panel appears, and whenever a screen
holds more than two things worth reading. Here is one, as a fenced block:

````markdown
```annotated id=run-estimate
image: screens/run-estimate.png          # from the capture script (§5.4); never a hand-made screenshot
caption: "Figure 7-3. The Run document before anything is spent"
alt: The Run document showing the plan, the GPU-hour estimate, the budget bar and the Check and Start buttons.
regions:
  - target: '[data-tour="run-estimate"]'  # the region is the element's box, recorded at capture time
    label: Estimate
    note: >
      GPU-hours this run will spend and how they were estimated: `table` (from defaults.yaml) until a calibration
      has measured this card, then `measured` with a ± range.
  - target: '[data-tour="run-budget"]'
    label: Budget
    note: What the project has already spent today and what this run would add. Over the budget, Start asks for an
      approval instead of starting.
  - target: '[data-command="runs.new"]'
    label: Check, then Start
    note: Check is a dry run, which spends nothing. Start is enabled only after a Check of the same settings.
    style: focus                         # optional: focus (default) | subtle | warning
  - rect: [1180, 96, 220, 40]            # x, y, width, height in image pixels, when there is no target
    label: Departures
    note: Chips for every value that differs from its default; click one to see the default and its source.
```
````

Rules:

- **Regions:**
  - Number them in reading order: left to right, top to bottom, unless the lesson needs another order.
  - Use at most seven per screenshot. More means the screen needs two figures, or a crop.
  - Prefer `target` (the same stable attributes as tours, §5.2) to `rect`. A target re-measures itself when the
    capture re-runs; a rect goes stale silently.
  - A `rect` needs a comment saying what it frames.
- **Labels:** two or three words, the UI's own name for the thing when it has one.
- **Notes:**
  - One to three sentences.
  - Explain meaning and consequence, not what is visible ("Start asks for an approval when…", not "This is the
    Start button").
  - A note may introduce a term only if the term map assigns it to this chapter.
- **Styles:** `focus` is the default outline. `subtle` marks context. `warning` marks something that spends,
  deletes or needs an approval.
- **The text around the figure** refers to regions by number ("the estimate ①") so the reader can follow either
  way.

How each output renders it:

| Output | Rendering |
| --- | --- |
| HTML edition | The image with an SVG overlay: each region outlined and given a numbered badge, the rest of the image slightly dimmed while a region is active. Beside or below it, a numbered list of labels and notes. Hovering or focusing an item highlights its region and the other way round; ←/→ step through the regions; click zooms the region. Works with keyboard and screen reader (each region is a list item; the image has the alt text) |
| Help panel inside Cadence | The same component, from the same block. "Show me on screen" turns the figure into a tour of the live UI when every region has a `target` |
| Plain Markdown (GitHub) | The image, then a numbered list `① **Estimate** — note…`; the regions are not drawn |

The capture script (§5.4) records each target's box in the image's pixels next to the screenshot
(`screens/<id>.json`). The builder reads the boxes from there, so a block never holds coordinates for a `target`.

### 5.4 Screenshots and tour captures

- **Capture by script, never by hand.**
  - A Playwright script (`docs/tutorial/capture/`) opens the UI, plays each tour and saves
    `docs/tutorial/screens/<tour-id>/<step>.png`.
  - With each screenshot it saves a JSON file with the targets' bounding boxes, which the HTML edition uses to draw
    highlights.
  - When the UI changes, re-run the script. A missing target fails the capture, which is how the book finds out
    the product moved.
- **What the screens show:**
  - Data comes from a stand that holds the running project: the real eval and run ids cited in the text.
  - No personal data appears: no real call audio, no names and no tokens.
  - Light theme, 1440×900, browser zoom 100 %.
- **Login:** the capture logs in with a dedicated documentation account. The owner provides it; never use the
  admin's password or the `.dev-key`.

## 6. Facts and sources

- **Check Cadence facts against the code** at the commit in `written-against`: the contract (`api/openapi.yaml`),
  `defaults.yaml`, the help articles and the spec. When the spec and the code disagree, the code wins, and the
  disagreement is reported. Don't paper over it.
- **Defaults:** read them from `defaults.yaml` with their source. Write "the default is 15 % replay (Cadence
  recommendation)", never just "use 15 %".
- **ML claims:** cite the paper or the vendor doc, the same way the spec does. Examples:
  - Bisani & Ney 2004 for the bootstrap;
  - Liu & Peng for the blockwise bootstrap;
  - Yu et al. 2021 (FastEmit) for emission delay;
  - the NeMo documentation for cache-aware streaming.
- **Future features:** don't describe a feature that isn't built as if it were. A chapter of a phase that isn't
  built yet stays `planned` with its outline only.

## 7. Writing a chapter (process)

1. Read the chapter's row in `OUTLINE.md`, the spec sections and help articles it names, and the previous chapter's
   "What you learned".
2. Do the hands-on part yourself on the stand, in the running project. Record ids, numbers and the error messages
   you hit.
3. Write the chapter in the shape of §2.3. Introduce only the terms the term map assigns to it. If you need
   another, add it to the map first, or move it from a later chapter.
4. Write the tours, add any missing `data-tour` attributes in a separate product PR, and run the capture.
5. Run the checks:
   - every term used is introduced in this or an earlier chapter (the term-order check, planned under "Building
     the book");
   - links resolve;
   - every number has its id comment.
6. Review:
   - a reader who knows nothing of speech reads it start to finish;
   - a practitioner checks the ML claims.
   Then set `status: reviewed`.
7. Update `OUTLINE.md`: the status, the terms actually introduced, and the open questions found.

## 8. Keeping it true

- Each release of Cadence re-runs the capture and the term-order check.
- A chapter whose `written-against` is more than one phase behind gets a banner: "Written for Cadence phase N;
  some screens may differ".
- When a feature a chapter describes changes behaviour, the PR that changes it updates the chapter, or opens an
  issue labelled `tutorial` naming the chapter and section.
