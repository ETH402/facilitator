# Privacy posture

What this facilitator sees, what it publishes, and what it deliberately withholds.

The short version: **aggregating is not anonymizing.** Most of the decisions below
follow from taking that seriously rather than from a general preference for less
data.

## What the facilitator necessarily sees

Verification requires the authorization, so payer addresses, amounts, recipients,
and validity windows are unavoidably processed and stored. That is the function,
not a design choice. Payer addresses are pseudonymous but not anonymous: they are
public on chain and frequently linkable to a person.

Merchant records additionally hold an email address and a recipient address.

The merchant panel can show private per-merchant aggregates only after explicit
opt-in. Opt-in starts the observation window; the panel does not retroactively
query earlier payments. Turning it off immediately removes access. This does not
disable payment and audit records required for verification, settlement,
idempotency, abuse controls, or incident response.

Public merchant discovery is a separate opt-in and is never inferred from
private analytics consent. When enabled, `/` and `/explore` may show the
merchant's name, declared website, confirmed-settlement count since that public
opt-in, and the date of its latest counted settlement. They do not show email,
merchant or payment identifiers, payer or recipient addresses, amounts, or
volume. Opting out removes the profile immediately. Because even a per-merchant
count reveals business activity, the setting requires the same fresh recipient
wallet authentication as API-key and analytics administration.

## What is published without authentication

`/stats` and `/status` are public. They publish:

- component health, with coarse detail (`unreachable`, never the underlying error,
  which can carry hostnames and database usernames)
- counts: verifications, settlement requests, confirmed and failed settlements,
  registered and verified merchants
- block heights and confirmation lag

The landing and network pages also publish separately opted-in merchant names,
declared websites, and post-consent confirmed-payment counts as described above.
They do **not** publish payer addresses, internal merchant identifiers, email
addresses, payment identities, recipient wallets, individual amounts, or volume.

## Why settled volume is withheld by default

`total_payment_volume_atomic`, `total_payment_volume_usdc`, and
`volume_last_24h_atomic` are omitted unless the operator sets
`ETH402_PUBLISH_STATS_VOLUME=true`.

The reason is specific, not precautionary. A cumulative total published without
authentication can be polled, and **polling a cumulative total yields its own
deltas**. A delta spanning a single settlement is that payment's exact amount — to
the atomic unit, recoverable by anyone, and correlatable with USDC `Transfer`
events in the same window to identify the payer and merchant. The 24-hour figure
has the same problem whenever volume is low, which for a new facilitator is always:
with one settlement in the window, the "aggregate" *is* the payment.

Rounding does not fix this. Repeated polling recovers each crossing of a rounding
boundary, which still discloses that a payment of roughly that size occurred and
when. Suppressing below a count threshold does not fix it either, because the
cumulative total leaks across the threshold.

So publishing volume is treated as an operator's deliberate choice to disclose
business figures, and the privacy-preserving default is to withhold them. Counts
are kept, because they carry no amount and without them the status page could not
say whether settlement is progressing at all.

## Analytics for the operator

Aggregates for the operator's own use come from the same source, read from the
database directly or scraped from `/metrics` on the internal interface. `/metrics`
is refused by the bundled reverse proxy on the public listener and carries no payer
or merchant identifiers — worker heartbeats, RPC failure counts, HTTP status
counts, and the signer balance.

There is deliberately **no third-party analytics**, no tracking script, font,
or external asset on any page this service serves: `VISION.md` commits to
operating without proprietary dependencies. Product pages use only same-origin
CSS and JavaScript; the status page remains fully inline so it keeps working
during the network failures it exists to report. Tests enforce both boundaries.

## Logs

Logs carry payment identities and merchant identifiers, because incident response
needs them, and are the reason [operations](OPERATIONS.md) requires that logs never
contain full authorizations, signed transactions, or unredacted email addresses.
Treat log storage as holding pseudonymous personal data.

## Retention

The privacy-first defaults are:

| Data | Default | Action |
|---|---:|---|
| terminal payment authorization | 30 days | remove merchant linkage, payer/recipient addresses, authorization nonce and times, payload hash, payer signature, leases, and stored raw transaction bytes |
| expired email tokens | 24 hours after expiry | delete |
| unreferenced expired wallet challenges | 24 hours after expiry | delete |
| revoked API keys | 30 days after revocation | delete |
| expired/revoked merchant admin sessions | 24 hours after expiry/revocation | delete |
| fair-use counters | two completed windows | delete |

`ETH402_PAYMENT_RETENTION`, `ETH402_EPHEMERAL_RETENTION`,
`ETH402_REVOKED_KEY_RETENTION`, `ETH402_RETENTION_INTERVAL`, and
`ETH402_RETENTION_BATCH_SIZE` configure the lifecycle. Payment retention cannot
be shorter than the settlement quota window, because clearing `merchant_id`
sooner would undercount that merchant's admitted gas exposure.

Payment rows become tombstones rather than disappearing. They retain the
irreversible `payment_identity`, exact integer amount, state, timestamps, and
public transaction hash. This preserves lifetime `/stats` and allows a caller
retrying an old `/settle` request to recover the original hash. A transaction
hash is already public and can be used to recover transfer details from
Ethereum; retention removes ETH402's private merchant association and any
unbroadcast authorization material, not public-chain history.

The worker never redacts `manual_review` or an in-flight payment. A
failed/expired payment with a dropped signer nonce also keeps its authorization
until the gap filler is resolved: deleting it earlier would strand every later
transaction from that signer. Stale `verified` payments are first moved to
`expired`; they wait a full payment-retention period before becoming eligible.
Each pass uses `FOR UPDATE SKIP LOCKED` and a bounded batch, so it coordinates
across instances without blocking settlement.

Verification and settlement attempt rows, transition history, and
security-audit events remain append-only. They retain reason codes and
irreversible payment identities, not raw authorizations or payer addresses.
Used wallet challenges referenced by recipient-address history also remain as
hashes because that append-only proof history protects merchant funds.

Active merchant identity data is retained for the account's lifetime; ETH402
does not yet expose self-service account erasure. Backups and replicas retain
data according to the operator's independent backup lifecycle, so production
operators must choose values compatible with applicable obligations and
document deletion from backups as well as the primary database.
