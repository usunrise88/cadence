---
title: Promotion receipt mismatch
summary: The receipt pasted into promotions.verify does not confirm this promotion — another record's script, other installed files, a failed smoke check, or a record that is no longer pending. Nothing was changed.
contexts: [error:promotion-receipt-mismatch, command:promotions.verify, guide:delivery-script]
---

## What this is

A `422 Unprocessable Entity` problem of type `promotion-receipt-mismatch` from `promotions.verify`. Cadence checked
the `CADENCE-RECEIPT` line against the promotion record and its delivery bundle and did not append a confirmation.
The detail says which check failed:

| Detail says | Why | What to do |
| --- | --- | --- |
| no `CADENCE-RECEIPT` line | The script stopped early (it prints no receipt then) or the wrong text was pasted | Read the script's `STOPPED:` line on the host and fix that first |
| the receipt names record hash … | The receipt comes from another record's script | Run the bundle of this record (`promotions.get` names its hash) |
| the installed files hash to … | The model directory on the host is not the approved one | Do not route traffic to it; run this record's bundle again |
| the smoke check passed … of … | Fewer smoke utterances matched the staging text than `deploy.parity_min_identical_share` asks | The script should have stopped; check that the bundle was not edited |
| counts … smoke utterances, the bundle holds … | The receipt and the bundle disagree | Run the unedited bundle |
| already confirmed, was withdrawn | The record is closed | A withdrawn promotion needs a new promotion: a new record and bundle |
| not the slot's pending one | A later promotion or rollback of the slot came after it | Confirm the newest one |
| does not verify | The stored record no longer verifies (an edit behind Cadence's back) | Stop and tell the admin; `promotions.list` shows the chain's problems |
| no finished delivery bundle | The bundle job has not finished or failed | `promotions.get` shows the delivery's state and error |

## Place in the loop

Block 4, Deploy: the last step of a canary, production or rollback promotion.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/promotion-receipt-mismatch` |
| `status` | `422` |
| `detail` | Which check failed |

## Commands

- `promotions.get` — the record, its hash, the delivery bundle's state and smoke size.
- `promotions.verify` with `dryRun=true` — checks a receipt without confirming.

## Playbooks

- Agents never confirm a delivery (rule `delivery-is-for-people`); report the record id to the person who runs the
  script.

## Sources

- docs/spec/02-domain-projects-registry.md "Promotion records" (Confirmation); [Delivery script](../guides/delivery-script.md).
