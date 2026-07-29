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

## What is published without authentication

`/stats` and `/status` are public. They publish:

- component health, with coarse detail (`unreachable`, never the underlying error,
  which can carry hostnames and database usernames)
- counts: verifications, settlement requests, confirmed and failed settlements,
  registered and verified merchants
- block heights and confirmation lag

They do **not** publish payer addresses, merchant identifiers, email addresses,
payment identities, or individual amounts, and there is no endpoint that does.

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

There is deliberately **no third-party analytics**, no tracking script, and no
external asset on any page this service serves: `VISION.md` commits to operating
without proprietary dependencies, and the status page in particular must keep
working during the network failures it exists to report. A test asserts the page
loads nothing external.

## Logs

Logs carry payment identities and merchant identifiers, because incident response
needs them, and are the reason [operations](OPERATIONS.md) requires that logs never
contain full authorizations, signed transactions, or unredacted email addresses.
Treat log storage as holding pseudonymous personal data.

## Known gap: retention

**Nothing is deleted.** Payment records, authorizations, and merchant details
persist indefinitely, so the privacy exposure grows without bound and a database
compromise years from now still discloses every payer address the facilitator ever
saw.

This is a real gap and is not addressed by the analytics posture above. Deciding it
needs an operator's answer on how long records must be kept for dispute resolution
and any applicable obligation, which is a policy question rather than a code one —
so it is tracked in `PLAN.md` for Milestone 5 rather than assumed here. Until then,
`docs/OPERATIONS.md` backup guidance applies to data that is never pruned.
