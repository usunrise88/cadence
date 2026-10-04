---
title: Delivery script and signed promotions
summary: How a promoted model reaches a production host Cadence never touches — the signed promotion record, the delivery bundle, deliver.sh, the pinned instance key and the receipt a person pastes back into promotions.verify.
contexts: [guide:delivery-script, command:promotions.list, command:promotions.get, command:promotions.verify, command:deploymentTargets.list, command:deploymentTargets.new, command:deploymentTargets.edit, command:deploymentTargets.archive, error:promotion-receipt-mismatch, error:delivery-link-invalid, entity:promotion, entity:deployment_target]
---

## What this is

Cadence never connects to a production host. A model reaches one only through a **delivery bundle** that a person
copies there and runs. What makes that safe is the **promotion record**: a JSON document in canonical form (RFC 8785),
hashed with SHA-256 and signed with the instance's Ed25519 key, appended to the delivery target's chain.

| Piece | What it is |
| --- | --- |
| Deployment target | Where models are served (`deploymentTargets.*`, instance-wide). `staging` is the server Cadence reaches; a `delivery` target is a production server, with no endpoint, the `repositoryPath` the script installs into and its `slots` (the model names the production pipeline calls) |
| Instance key | An Ed25519 key pair created at the control plane's first start. The private half is a sealed secret of kind `signing`; the public half is in `deploymentTargets.list` (`signingKeys`), in every record and in the bundle as `instance.pub` |
| Chain | One per delivery target, oldest first: `genesis` (the target's payload and the public key, when the target is created), then `promotion`, `rollback`, `confirmation`, `withdrawal`, `target-changed`, `key-rotation`. Each record names the previous one's hash, so a removed or edited record breaks the chain. Records are append-only; `promotions.list` verifies every one again on each read |
| Delivery bundle | `cadence.delivery/1`: `deliver.sh`, `record.json` (the record exactly as signed), `record.sig`, `instance.pub`, `models/<modelName>/` with `models/<modelName>.sha256`, `decoding/` and `smoke/` (up to 20 utterances of the parity sample with the text the staging server wrote, and the family's smoke client) |
| Receipt | The last line `deliver.sh` prints when it ran to the end: `CADENCE-RECEIPT 1 <recordHash> <servedSha256> <ok>/<total> <host> <UTC time>` |

**Pin the key once.** On each production host a person installs the public key from Settings → Deployment targets as
`/etc/cadence/instance.pub` (or the path in `CADENCE_PIN`). The script compares the record's key id with the pin's id
(`ed25519:` and the first 16 bytes of the SHA-256 of the 32-byte key) and checks the signature with the pin, not with
the bundle's own copy, so a bundle signed by any other key is refused.

**What deliver.sh does, stopping at the first failure without a receipt:**

1. The record's key must be the pinned key; with no pin it says how to install one.
2. `record.sig` must verify over the SHA-256 of `record.json` under the pinned key (`openssl pkeyutl -verify
   -rawin`), and every model file must match the record (`servedSha256` is the SHA-256 of the sorted `sha256sum` lines
   of the model directory, the record's `deployable.manifestSha256`).
3. It installs `models/<modelName>` under the repository path (never over a directory with other files) and loads it
   through the server beside the version before it. A config-only promotion or a rollback ships no model: the
   installed directory must already match.
4. It runs the smoke utterances through the new model; at least `deploy.parity_min_identical_share` of them must match
   the staging text (all 20 of 20), or it unloads the new model.
5. It routes the slot's traffic through `/etc/cadence/route` (or `CADENCE_ROUTE_HOOK`) when that exists, called as
   `route <slot> <modelName> <trafficShare> <stage> <decoding dir>`; otherwise it tells you to route it yourself
   (Эра's routing is not settled yet, R32).
6. It prints the receipt.

**Confirm.** Paste the receipt into Cadence (`promotions.verify`, the Model document's Confirm delivery box). Cadence
accepts it when the record is the slot's pending one, the hash matches, the served files are the approved ones and the
smoke check passed; it appends a signed `confirmation` naming you. A promotion with no receipt within
`deploy.delivery_pending_days` is withdrawn and its deployment returns to the stage before.

**What it guarantees, and what it does not.** The production host runs only records your instance signed; the files
it installed are the ones the approver saw; the person who pasted the receipt had that record's script and it ran to
the end. A receipt does not prove which host ran it: that rests on the person named in the confirmation.

## Place in the loop

Block 4, Deploy: after a model version is exported, passes parity and its benchmark and has enough shadow hours, a
promotion to canary and then production goes through an approval, a signed record and this script.

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `deploy.delivery_pending_days` | 7 | A promotion or rollback without a confirmed receipt is withdrawn after this many days |
| `deploy.parity_min_identical_share` | 0.995 | Share of smoke utterances that must match the staging text (rounded up: 20 of 20) |
| `serving.staging_target` | `staging`, `http://triton:8000` | The staging target seeded at first start |
| `media.signed_link_ttl_s` | 300 | How long the bundle's signed download link in `promotions.get` lasts |

Environment of `deliver.sh`: `CADENCE_SERVER_URL` (default `http://localhost:8000`), `CADENCE_PIN` (default
`/etc/cadence/instance.pub`), `CADENCE_ROUTE_HOOK` (default `/etc/cadence/route`). It needs a POSIX shell,
`sha256sum`, OpenSSL 3.0 or newer and `curl`.

## Commands

- `deploymentTargets.new`, `deploymentTargets.edit` — an approval for everyone, the admin decides; a delivery target's
  creation appends its genesis record, a change to what records name appends `target-changed`.
- `deploymentTargets.archive` — the admin's; the chain stays.
- `promotions.list` — the chain with each record verified; `promotions.get` — one record, its script and (for people)
  a signed link to download the bundle (`delivery.get`).
- `promotions.verify` — people only: paste the receipt.
- `cadence admin rotate-signing-key --reason …` — a new instance key; every chain gets a `key-rotation` record signed
  by the old key. Install the new public key as the pin on every production host before the next delivery.

## Playbooks

- Agents read targets and records (`deploymentTargets.list|get`, `promotions.list|get`) but never get a bundle link
  and never confirm a delivery (rule `delivery-is-for-people`); creating or changing a target waits for the admin
  (rule `deployment-targets`).

## Sources

- docs/spec/02-domain-projects-registry.md "Deployment entities", "Promotion records"; docs/spec/08-resolutions.md
  R33, R46.
- RFC 8785, JSON Canonicalization Scheme; RFC 8032, Edwards-Curve Digital Signature Algorithm (Ed25519); OpenSSL 3.0
  `pkeyutl -rawin` for Ed25519 verification.
