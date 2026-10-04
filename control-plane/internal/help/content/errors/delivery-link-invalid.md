---
title: Delivery bundle link invalid or expired
summary: A signed link to a promotion's delivery bundle did not match its record and viewer, or it expired; open the promotion again for a new link.
contexts: [error:delivery-link-invalid, command:promotions.get, guide:delivery-script]
---

## What this is

A `403 Forbidden` problem of type `delivery-link-invalid` from `delivery.get`, the download of a promotion's delivery
bundle. `promotions.get` gives people a short-lived link signed for the record, the viewer and an expiry; the link was
changed, minted for another record or person, or it expired (`media.signed_link_ttl_s`). Only the start of a
download is checked: a download that started before the expiry finishes.

## Place in the loop

Block 4, Deploy: fetching the bundle a person copies to the production host.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/delivery-link-invalid` |
| `status` | `403` |
| `media.signed_link_ttl_s` | How long a link lasts (300 s) |

## Commands

- `promotions.get` — read the record again for a fresh link (people only; agents and API keys get none).

## Playbooks

- Agents never download bundles: delivery is for people.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployment entities" (`promotions.get`); docs/spec/08-resolutions.md R25
  (signed links); [Delivery script](../guides/delivery-script.md).
