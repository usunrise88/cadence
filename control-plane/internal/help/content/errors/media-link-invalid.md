---
title: Audio link invalid or expired
summary: A signed audio link was opened after it expired, or its span, channel, utterance or viewer was changed after it was minted; ask for a new link.
contexts: [error:media-link-invalid, field:sig, field:exp]
---

## What this is

A `403 Forbidden` problem of type `media-link-invalid`, answered by `audio.get` for a request that carries a link
signature (`viewer`, `exp`, `sig`) that does not hold:

- the link expired — links live `media.signed_link_ttl_s` seconds (300 by default);
- the query was edited: the signature binds the utterance, the span (`start`, `end`), the channel and the viewer, so
  a link minted for 1.2–3.4 s does not play 0–60 s;
- the link came from another Cadence instance, or this instance's master key changed (links are signed with a key
  derived from it, so they all stop working with it).

Nothing was played and nothing was recorded.

## Place in the loop

Review. People hear audio through short-lived links (R25, docs/spec/06-platform.md "Media"): a media element needs no
headers, and a copied link soon stops working. The audio view mints a new link (`audio.sign`) each time it opens a
span, so this error mostly means an old tab or a link pasted from elsewhere.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/media-link-invalid` |
| `status` | `403` |
| `detail` | Expired (with the time) or not matching |

| Default | Value | Meaning |
| --- | --- | --- |
| `media.signed_link_ttl_s` | 300 s | How long a link plays |

## Commands

- `audio.sign` — a new link for the same span (signed in; agents are refused).
- `audio.get` without `sig`, signed in — the span through your session.

## Playbooks

- **The Audio panel stopped playing after a long pause.** Press Space again: the view asks for a fresh link.
- **Sharing a moment with a colleague.** Share the selection (`@utt:<id>#t=1.20,2.35` in chat, or the deep link), not
  the audio URL: they open it with their own session and their own link.

## Sources

- docs/spec/06-platform.md "Media: audio and the live channel (phase 3)" — signed URLs bound to the utterance, span,
  viewer and expiry.
- docs/spec/08-resolutions.md R25.
