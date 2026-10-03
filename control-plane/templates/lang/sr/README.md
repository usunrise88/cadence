# Language pack sr — Serbian (Latin and Cyrillic)

Serbian is written in two scripts that map one to one: Cyrillic (official) and Gaj's Latin alphabet. Files:

| File | What it holds |
| --- | --- |
| `normalizer.yaml` | `scoring.normalizer` (`normalizer/basic` until a Serbian one exists); `training`: Latin script through `sr-Cyrl-Latn` |
| `translit.yaml` | The `sr-Cyrl-Latn` scheme: the letter map the worker's transliteration applies |
| `itn.yaml` | Written forms of numbers, ordinals, dates, times, amounts and percentages |
| `lid.yaml` | Accepted locales (sr with hr, bs, cnr) and code-switch handling |
| `boost/<domain>.txt` | Boost lists: a `# weight: <float>` header, then one term per line, in Latin script |
| `golden-recipe.yaml` | How a Serbian golden set is sampled and sized |

## Pitfalls

- **One script on both sides.** FLEURS Serbian is written in Cyrillic; the base model's tokenizer lacks ђ ј љ њ ћ џ,
  so training text is Latin (`dataset_import` with `transliterate: sr-Cyrl-Latn`). A golden set imported without
  the same transliteration compares Cyrillic references with Latin hypotheses and scores every word wrong — the
  scoring normalizer does not transliterate.
- **Digraphs are one letter.** lj, nj and dž are single Cyrillic letters (љ, њ, џ); the Latin side spells them with
  two characters, so CER counts them twice. Capitalised they are Lj / Nj / Dž, or LJ / NJ / DŽ inside an all-caps word.
- **Diacritics are letters.** č ć ž š đ must never be stripped (`removeMarks: false`); "dj" for đ and "c" for č / ć in
  informal typing are errors, not variants.
- **Ekavian and ijekavian.** mleko / mlijeko, reka / rijeka: both are standard (ekavian in Serbia, ijekavian in
  Bosnia, Montenegro and western Serbia). A golden set should state which it uses; mixing them inflates WER.
- **Neighbouring standards.** Croatian, Bosnian and Montenegrin share most of the language; identifiers confuse them,
  so `lid.yaml` accepts them together.
- **Numbers.** A decimal comma and a dot between thousands (1.500,25); ordinals are digits with a dot (1.); dates day
  first with a closing dot (1. 10. 2026.).
