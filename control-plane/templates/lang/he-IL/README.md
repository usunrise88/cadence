# Language pack he-IL — Hebrew (Israel)

Everything Cadence needs to know about Hebrew for this project, versioned with the project's recipes. Files:

| File | What it holds |
| --- | --- |
| `normalizer.yaml` | `scoring.normalizer`: the registry normalizer WER is computed after (`normalizer/he-il`); `training`: the text style transcripts are trained in |
| `itn.yaml` | Written forms of numbers, prefixed numbers, phone numbers, dates, times, amounts and percentages, each with a pattern an entity scorer can find |
| `translit.yaml` | Script maps — none for Hebrew |
| `lid.yaml` | Accepted locales and code-switch handling |
| `boost/<domain>.txt` | Boost lists: a `# weight: <float>` header, then one term per line (`boost.edit`) |
| `golden-recipe.yaml` | How a he-IL golden set is sampled and sized |

## Pitfalls

- **Niqqud.** Modern Hebrew is written without vowel points; a pointed reference against an unpointed hypothesis
  is wrong on every pointed word. Scoring removes combining marks (Unicode Mn: niqqud, the shin and sin dots,
  cantillation), and the training style removes them too. ivrit.ai, the largest open Hebrew speech corpus, is
  unpointed and leaves vowels to predictors such as Dicta's Nakdan
  ([discussion](https://huggingface.co/datasets/ivrit-ai/audio-transcripts/discussions/3)).
- **Plene and defective spelling.** The same word is written with or without matres lectionis: תוכנית / תכנית,
  מילה / מלה, צהריים / צהרים. The Academy of the Hebrew Language's rules for unpointed text
  ([ktiv male](https://hebrew-academy.org.il/topic/hahlatot/missingvocalizationspelling/)) are the reference;
  everyday text mixes both. Never fold by deleting every medial vav and yod: it merges unrelated words (חודש "month"
  and חדש "new"). Fold only listed pairs, in the scoring normalizer's mappings, so the rule is versioned.
- **Prefixes are part of the word.** The conjunction and prepositions ו, ה, ב, כ, ל, מ, ש attach to the next word
  (ובבית "and in the house" is one word), so one wrong prefix letter is a whole word error. Read CER beside WER.
- **Geresh and gershayim.** Abbreviations and loan sounds use ׳ (U+05F3) and ״ (U+05F4), but most typists use
  ASCII ' and ". The training style maps all of them to ASCII; the scoring normalizer `normalizer/he-il` should map
  them away entirely (צה״ל, צה"ל and צהל score alike) — with punctuation stripped as Unicode P* they would split a
  word in two (צ׳יפס → צ יפס).
- **Maqaf.** The Hebrew hyphen (U+05BE) joins words (בית־ספר); scoring turns it into a space, training writes `-`.
- **Final letters.** ך ם ן ף ץ are distinct code points from כ מ נ פ צ; never fold them — a final form in the middle
  of a word is a real transcription error.
- **Bidirectional controls.** Text pasted from the web carries LRM/RLM (U+200E, U+200F) and ALM (U+061C); they are
  invisible, make equal-looking strings differ, and are removed by the training style.
- **Numbers.** Hebrew numerals agree in gender (שלוש / שלושה) and take the construct state; digits hide both, which
  is why the written form (`itn.yaml`) uses digits. A prefixed number is written `ב-3`. Phone numbers group as
  `054-1234567`, dates day first `1.10.2026`.
- **Code switching.** Israeli speech mixes in English terms, brands and names; they stay in Latin script as spoken
  (WhatsApp, not וואטסאפ) unless the brand is Hebrew. `lid.yaml` keeps such utterances.
- **Read versus conversational speech.** FLEURS Hebrew is read Wikipedia sentences recorded on good microphones;
  call-centre audio is 8 kHz conversational speech. A golden set should match the deployment's channel and style.
