# Fair use

The controls that bound what one caller can consume, and — more importantly — where
they are deliberately *not* applied.

These are commercial and operational policy, not protocol. `VISION.md` keeps
protocol behaviour separate from merchant identity, authentication, rate limits, and
commercial policy, so nothing here changes what x402 v2 means or what a valid
payment is. All of it can be disabled without affecting protocol conformance.

## The controls

| Control | Scope | Bounds | Config |
|---|---|---|---|
| Per-IP rate limit | every public endpoint | requests per minute per client address | `ETH402_PUBLIC_RATE_PER_MINUTE` |
| Registration rate limit | `/v1/merchants/register` | registrations per minute per client address | `ETH402_REGISTRATION_RATE_PER_MINUTE` |
| Merchant fair use | authenticated `/v1/*` endpoints | requests per merchant per window | `ETH402_MERCHANT_REQUESTS_PER_WINDOW`, `ETH402_FAIR_USE_WINDOW` |
| Per-merchant settlement quota | `/settle` | settlement intents per merchant per window | `ETH402_MERCHANT_SETTLEMENT_QUOTA`, `ETH402_MERCHANT_QUOTA_WINDOW` |
| Facilitator settlement ceiling | `/settle` | settlement intents across **all** merchants per window | `ETH402_GLOBAL_SETTLEMENT_QUOTA` |
| Per-transaction gas ceiling | signing | gas limit and fee caps per transaction | `ETH402_MAX_*` |

## Why there is a facilitator-wide ceiling as well

The per-merchant quota alone leaves total exposure at `merchants × quota`. That
number grows every time a merchant registers, so the operator's worst-case daily gas
spend was set by the size of the merchant list rather than by anybody's decision.
Gas is the facilitator's own money and the one cost it cannot pass on, so the total
needs a ceiling somebody chose.

Both bounds are counted over a **true trailing interval**, not a tumbling window,
because they protect funds: a tumbling window would let a merchant spend a full
allowance either side of a boundary.

The facilitator-wide count is taken under an advisory lock covering every merchant.
The merchant row lock serialises one merchant's requests but says nothing about two
different merchants settling simultaneously, and without the wider lock both would
count the same pre-limit snapshot and both commit. That is measured, not assumed:
removing the lock makes `TestGlobalQuotaHoldsUnderRepeatedRaces` commit two intents
against a ceiling of one. It costs no real throughput, because every settlement
already serialises on the signer's nonce row a few statements later.

A facilitator-wide refusal is reported as `facilitator_quota_exceeded`, separately
from `merchant_quota_exceeded`. The merchant did nothing wrong and should retry
later rather than investigate itself.

## Why merchant fair use keys on the merchant, not the API key

Minting an API key is self-service. A limit keyed on the key would be multiplied by
however many keys a merchant chose to create, which is not a limit.

Key revocation (`DELETE /v1/api-keys/{id}`) is deliberately exempt. It is the
operation a merchant reaches for when a key has leaked, and refusing it because the
same merchant has been noisy would keep a compromised credential alive.

Fair use **fails open**: if accounting cannot be recorded, the request is served and
the failure is logged. This is a courtesy limit, not an authorization decision, and
failing closed would turn a bookkeeping outage into an outage for every paying
merchant.

Windows are tumbling, so a merchant can spend a full allowance at the end of one
window and again at the start of the next — a burst of up to 2× the limit across a
boundary. That is acceptable here and would not be for the settlement quotas, which
is why those are counted differently.

Responses carry `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and
`X-RateLimit-Reset`, and a refusal carries `Retry-After`, so a client can back off
correctly instead of guessing.

## Why `/verify` and `/settle` have no per-merchant *request* limit

This is the most important decision on the page, and it is a decision not to build
something.

Both endpoints are unauthenticated. The merchant is derived from the `payTo` address
in the request, which the **caller** supplies. A per-merchant request limit keyed on
that would not be a control — it would be a weapon. Anyone could send traffic naming
an innocent merchant's address and exhaust that merchant's allowance, denying service
to a merchant who did nothing and has no way to stop it.

So attribution-based limits are applied only where identity is *proven*:

- authenticated `/v1/*` endpoints, where an API key establishes who is calling
- `/settle` quotas, which count **committed settlement intents**. Reaching that point
  requires a valid payer-signed EIP-3009 authorization naming that merchant, which an
  attacker cannot fabricate. Traffic in a merchant's settlement bucket genuinely
  belongs to that merchant's payment flow.

`/verify` is therefore bounded by the per-IP limiter and by its own cost profile: it
is read-only and spends no gas. If verification RPC volume becomes an operational
problem, the answer is a global ceiling or RPC-side quota, not per-merchant
attribution of traffic an attacker chooses.

## Retention

Fair-use counters are pruned once their windows have elapsed. Retaining them would
turn a rate counter into an indefinite per-merchant activity log, which
[privacy](PRIVACY.md) would then have to account for. One extra window is kept so
clock skew between instances cannot delete a window still being written to.

## Observability

`eth402_fair_use_refusals_total` counts merchant fair-use refusals, separately from
per-IP 429s in `eth402_http_requests_total{status="429"}`. The two have different
causes and different responses, so a single counter would hide which is firing.

Settlement refusals appear in the settlement attempt records with their reason, so
`merchant_quota_exceeded` and `facilitator_quota_exceeded` are distinguishable after
the fact.
