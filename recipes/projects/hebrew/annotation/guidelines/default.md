# Annotation guidelines — default (Hebrew)

<!-- written by Cadence at bootstrap (templates/annotation/guidelines/default.md) and adapted by this project for
he-IL. Edit freely: every annotation batch pins the commit of the guidelines it names (batches.new guidelines:
default), and the golden set it freezes into cites that commit. Change the rules here, then annotate a new batch; a
batch never changes its guidelines. The language rules below follow the project's language pack, lang/he-IL/
(normalizer.yaml, itn.yaml, lid.yaml); when the pack changes, change this file in the same commit. -->

These rules decide what a reference transcript is. A golden set made from them is what every model of the project is
scored against, so two careful annotators following them must write the same words. Where this file is silent, write
what was said, as it is normally spelled in modern unpointed Hebrew, and flag the item.

## What you hear and what you annotate

- You annotate **one party**: the channel the batch targets (usually the caller). The player starts on that channel;
  switch channels (`Alt+C`) to hear the other party when you need the context.
- The text above the field is the **best guess** of a machine (a pseudo-label or a model's output). It is often wrong
  — in Hebrew most often in a prefix letter or a plene/defective spelling. Listen first, then correct it; never accept
  it unheard.
- Transcribe **only speech in the segment** — from its start to its end mark — not what the other party says.

## Hebrew script

1. **No niqqud.** Write unpointed Hebrew: no vowel points, no shin/sin dots, no cantillation. Scoring and the training
   style remove them anyway (`lang/he-IL/normalizer.yaml`), so a pointed word only costs time.
2. **Plene spelling** (ktiv male) as the Academy of the Hebrew Language's rules for unpointed text give it: תוכנית,
   מילה, צהריים. When the predicted text uses the other spelling of a word listed in the scoring normalizer's
   mappings, either is fine; anything else, write the plene form.
3. **Final letters** (ך ם ן ף ץ) at the end of a word only, never in the middle. Never "fix" a final form the speaker's
   word needs.
4. **Prefixes are part of the word**: ו, ה, ב, כ, ל, מ, ש attach to the next word with no space (ובבית, שהלכתי).
   Listen for them: one missing or extra prefix letter is a whole word error.
5. **Geresh and gershayim** in abbreviations and loan sounds: type ASCII `'` and `"` (צ'יפס, צה"ל); the Hebrew marks ׳
   and ״ are accepted and mapped to the same.
6. **Maqaf**: write compounds the way the speaker's standard spelling has them; a maqaf (־) or `-` both become a
   space in scoring.
7. **English and other Latin words** stay in Latin script as spoken (WhatsApp, Excel, iPhone) unless the brand is
   Hebrew. Code switching is normal Israeli speech: such items are kept (`lang/he-IL/lid.yaml` codeSwitch: keep); tag
   `foreign` only when most of the segment is not Hebrew.

## Numbers, dates, amounts

The language pack writes numbers in their **written forms** (`lang/he-IL/itn.yaml`), and the entity scorer finds them
in references by those patterns, so references use them too:

| Class | Write | Not |
| --- | --- | --- |
| Number | `320`, `1,500` | שלוש מאות ועשרים |
| Number with a prefix | `ב-3`, `מ-20` | בשלוש, ב3 |
| Phone | `054-1234567` | אפס חמש ארבע… |
| Date | `1.10.2026` (day first) | אחד באוקטובר… |
| Time | `8:30` | שמונה וחצי |
| Amount | `200 ש"ח` or `₪200` | מאתיים שקלים |
| Percent | `20%` | עשרים אחוז |

- Digits hide the gender of the numeral (שלוש / שלושה) and the construct state: that is accepted, the scorer compares
  written forms.
- Numbers inside **names and fixed expressions** stay words (מאה שערים, שלושת הימים); ordinals as words (השלישי).
- When you cannot tell which number was said, write what you hear and flag the item.

## Names and addresses

- **Names, addresses, product names**: as the speaker said them, in standard Hebrew spelling (or Latin for a Latin
  brand). Mark them as entities (select the span, `Alt+E`): class `name` for people and organisations, `address` for
  streets, towns and the numbers of an address; numbers, dates, phone numbers and amounts use the classes in the
  table above.
- A prefix stays on the name and inside the span (בתל אביב: the span is the whole word with ב).

## Speech

1. **Hesitations and fillers** (אה, אמ, אממ, mm, hm): leave them out. Repetitions and false starts that are words: keep
   them.
2. **Cut-off words** at the segment's edges: write the part you hear only when it is clearly a word; otherwise leave
   it out.
3. **Colloquial forms** as said, in their usual spelling (כאילו, סבבה, יאללה); do not "correct" grammar — a missing
   את or a masculine numeral with a feminine noun is what was said.
4. **Punctuation and capitals** are optional: the agreement and the scorer ignore them.

## Tags

| Tag | Use it when | Effect |
| --- | --- | --- |
| `noise` | Noise, music or a line problem covers part of the speech, but you can transcribe it | Kept |
| `crosstalk` | The other party speaks at the same time | Kept |
| `foreign` | The speech is mostly not Hebrew (Arabic, Russian, English); a few English words are not foreign | The item is excluded |
| `unintelligible` | You cannot make out what was said | The item is excluded |

## Done, skip, flag

- **Done** (`Ctrl+Enter`): the transcript is right as far as you can tell.
- **Flag** (`Ctrl+Shift+Enter`): you wrote a transcript but are unsure — a second annotator takes the item blind and
  disagreements go to adjudication.
- **Skip** (`Alt+S`): someone else should take it (the language, the domain, the audio). Skips do not count; an item
  skipped by two people is excluded.

## Agreement

A share of every batch (10 %) is annotated twice, blind. The batch freezes as a golden set only when the two
transcripts agree to within 5 % word error rate (case and punctuation folded). In Hebrew most disagreements are a
prefix letter, plene versus defective spelling, or a number in words versus digits: settle them with the rules above,
and when a disagreement recurs, add the rule here.
