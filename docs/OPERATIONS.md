# Operations

Run migrations as a distinct deployment step. The application never creates
schema and refuses to start when applied and embedded versions differ. Use
separate PostgreSQL owner, migration, and runtime roles; runtime needs only
required table/sequence operations and no DDL.

Readiness requires PostgreSQL ping and RPC `eth_chainId == 1`. Liveness only
asserts the process can serve HTTP. Remove an instance from traffic on
readiness failure; do not restart-loop solely for an upstream outage.

Use two independently operated Ethereum RPC providers. Payment-critical reads
query both concurrently and fail closed unless they agree. Each provider may
retry within bounds, but transaction broadcast remains one primary-only attempt
because a failed send has an unknown outcome. The only relaxed comparison is
the moving latest head: a difference of at most two blocks is accepted and the
lower height is used. Latest-block fee reads are then repeated at that fixed
height and must agree exactly.

Production configuration rejects fragments and requires the two RPC URLs to
have different canonical host identities; credentials in URL userinfo, paths,
and query parameters remain supported and are never included in validation
errors. Different hostnames are only a syntactic guard, not proof of independent
operation. Record each provider's operator and account ownership in deployment
evidence and do not use two aliases, regions, or products of the same operator.

Nonce, balance, simulation, contract-state, and bytecode reads deliberately ask
both providers for `latest` and require the returned state to agree. During
ordinary one-block propagation skew, a state transition visible to only one
provider can therefore refuse verification or settlement transiently. This is
an accepted availability tradeoff: pinning authorization state to the older
head could approve a nonce or balance that the newer head has already consumed.
Do not bypass the disagreement by removing a provider; retry after convergence
and investigate sustained or repeated events.

What `/metrics` actually publishes, and therefore what can be alerted on today:

| Metric | Use |
|---|---|
| `eth402_rpc_requests_total`, `eth402_rpc_errors_total` | RPC attempt failure rate; each provider attempt and retry is counted independently |
| `eth402_rpc_provider_disagreements_total` | fail-closed disagreements between independently configured RPCs; any increase requires investigation |
| `eth402_worker_last_tick_timestamp_seconds{worker}` | worker liveness, per worker, from the last *completed* tick |
| `eth402_signer_balance_wei` and its freshness | the bound on a signer compromise |
| `eth402_verification_total`, `eth402_settlement_requests_total`, and their failure counters | request volume and failure rate |
| `eth402_panics_total` | recovered HTTP panics |
| `eth402_retention_last_tick_timestamp_seconds`, `eth402_retention_errors_total`, `eth402_retention_redacted_payments_total` | privacy-worker liveness, failures, and completed tombstones |
| `eth402_email_outbox_pending`, `eth402_email_outbox_oldest_pending_age_seconds` | current mail backlog and its oldest age, without merchant/recipient labels |
| `eth402_email_delivery_last_tick_timestamp_seconds`, `eth402_email_delivery_failures_total` | last successful outbox observation and SMTP/authenticated-decryption failures |

Example rules are in `deploy/alerts.yml`.

Confirmed and failed settlement counts come from `/stats`, which derives them from
the database rather than from process-local counters, so they survive a restart.

Not yet instrumented, and deliberately not published as zeros: confirmation lag,
settlement latency, pending age, database error counts, and gas-policy rejections.
A metric that never moves is worse than an absent one — an alert on it never fires
however broken the system is, which is precisely the false assurance an operator
cannot detect.

Verification classifies a provider value versus another provider error as a
disagreement and increments the disagreement metric after collecting both
outcomes. If every provider errors, the read still fails closed but is classified
as a dependency outage rather than evidence that providers reported different
chain state.

## Merchant panel

`/merchant` uses only same-origin application assets. Production TLS is mandatory
because the admin cookie is `Secure`; it is also HttpOnly, SameSite=Strict, and
expires after `ETH402_MERCHANT_SESSION_TTL` (12 hours by default). An email link
creates only an unprivileged session. API-key management and private statistics
remain unavailable until the registered recipient wallet elevates that session
with a fresh SIWE signature. Session and wallet-authentication events are audit
events; raw tokens and signatures must never be logged.

Merchant statistics are not the public `/stats` product. They are private,
disabled by default, and bounded by both the consent timestamp and payment
retention. Turning consent off takes effect immediately and does not change the
protocol/audit records required for correctness.

Public merchant discovery is separately disabled by default and requires the
same wallet-elevated session. `/` shows at most three profiles and `/explore`
shows at most 50. The directory query is cached on the public-stats cache
interval to prevent unauthenticated requests from driving database work;
consent changes invalidate it immediately. Public rows contain name, declared
HTTPS website, post-consent confirmed count, and last activity date only.

## Email delivery outbox

Registration and admin-login mail uses `email_delivery_outbox`. The request
transaction commits the token hash and outbox row together; SMTP is attempted
after commit and retried by an in-process worker on `ETH402_WORKER_INTERVAL`.
`email_verification_tokens.sent_at` remains null until SMTP accepts the message,
so delivery outages do not consume the resend cooldown. A live pending request
still suppresses duplicate enqueues until its token expires. Retry delay starts
at five seconds and doubles to five minutes; expired items are abandoned without
delivery.

Pending raw tokens are AEAD-encrypted with the dedicated
`ETH402_EMAIL_OUTBOX_KEY`, authenticated against merchant ID, token hash, and
message kind, and erased on delivery or expiry. Store that independent 32-byte
key in the production secret manager. Rotating it makes already-pending messages
undecryptable and causes immediate permanent abandonment: first drain the outbox
or allow pending tokens to expire, then rotate and restart. Authenticated-
decryption failure is not retried because the same key/ciphertext can never
succeed; the worker erases it, increments the failure metric, and logs delivery
ID, sanitized reason, and the
originating request ID, never recipient address, link, token, or provider text.
Each lease claim has a fresh UUID fencing token. A worker may update success or
retry state only while it still owns that token; once another instance reclaims
an expired lease, the stale worker's eventual SMTP result cannot overwrite the
new owner's state.

An SMTP server can accept a message immediately before ETH402 fails to persist
the success. The lease then expires and the worker submits the same token again.
This is the unavoidable at-least-once boundary; one-time token consumption makes
the duplicate harmless. Alert rules cover a missing/stale worker observation,
old backlog, and any delivery failure. The metrics contain no dynamic labels or
merchant, recipient, token, request, or delivery identifiers.

## Signer balance

The settlement signer address holds a deliberately small working balance, topped
up from a source ETH402 cannot spend from, with alerting on both absolute balance
and burn rate. A compromised process can settle real payments of its choosing
whatever the signing boundary allows, so bounding the hot balance is what caps
that loss. This is required before enabling any signer, and is superseded neither
by the in-process calldata allowlist nor by the policy boundary. See
[ADR-0004](decisions/0004-settlement-execution-model.md).

## Cloud KMS and the policy signer

Production key custody is GCP Cloud KMS with an
`EC_SIGN_SECP256K1_SHA256` key. The production signing path is the separate
policy boundary described in [Deployment](DEPLOYMENT.md); direct KMS mode is
implemented but does not move transaction policy outside the facilitator.
Provision the key once per environment:

```sh
gcloud services enable cloudkms.googleapis.com --project=PROJECT
gcloud kms keyrings create eth402-settlement --location=REGION --project=PROJECT
gcloud kms keys create eth402-settlement-signer \
  --keyring=eth402-settlement --location=REGION --project=PROJECT \
  --purpose=asymmetric-signing \
  --default-algorithm=ec-sign-secp256k1-sha256 \
  --protection-level=hsm
```

Cloud KMS offers secp256k1 only at HSM protection level — this is still the
plain Cloud KMS API, IAM, and audit logging of ADR-0004 decision 8, not the
dedicated Cloud HSM product the ADR weighed as a signer. The policy-signer
identity needs `roles/cloudkms.signerVerifier` and
`roles/cloudkms.publicKeyViewer` on the key (the public key read resolves the
signer address at startup). In production that identity is a dedicated service
account; locally, `gcloud auth application-default login` provides it through
ADC.

Set `POLICYSIGNER_KMS_KEY_NAME` to the full key *version* resource
(`projects/…/locations/…/keyRings/…/cryptoKeys/…/cryptoKeyVersions/N`).
Naming a version makes rotation an explicit config change: create the new
version, point the variable at it, restart. Startup resolves and logs the
derived signer address — fund it with the bounded hot balance only, and verify
the address out-of-band before the first top-up.

The facilitator uses `ETH402_SIGNER_MODE=policy`,
`ETH402_POLICY_SIGNER_URL`, and `ETH402_POLICY_SIGNER_TOKEN`; it must not hold
the KMS signing grant. `ETH402_SIGNER_MODE=external` and
`ETH402_KMS_KEY_NAME` connect the facilitator directly to KMS and therefore
bypass the separate policy boundary. Production configuration rejects that
mode; it remains available only for controlled non-production validation.

Key destruction in Cloud KMS is scheduled (24h minimum by default) rather than
immediate, so an accidental destroy is recoverable within the window; never
destroy the only enabled version while its nonce sequence has in-flight
transactions.

Gas maximums are typed decimal configuration. Enabling any non-disabled
`ETH402_SIGNER_MODE` requires non-zero `ETH402_MAX_FEE_PER_GAS_WEI` and
`ETH402_MAX_GAS_LIMIT`: zero means unset, not unlimited, so a signer cannot be
switched on without an explicit spend ceiling. A non-zero gas limit below
100,000 is rejected outright: a USDC `transferWithAuthorization` costs well
above the 21,000-gas plain-transfer floor, so a lower limit only guarantees
out-of-gas reverts the operator still pays for. A priority fee above the total
fee ceiling is also rejected. Settlement transactions are estimated beneath the
ceiling — initial max fee is `min(2·baseFee + tip, ETH402_MAX_FEE_PER_GAS_WEI)`
— so the ceiling is a bound, not the spend; the worst-case per-settlement cost
remains known in advance.

## Settlement workers

With a signer enabled, three workers run in-process on `ETH402_WORKER_INTERVAL`
(default 15s): the broadcast worker retries durable intents whose inline
`/settle` broadcast did not happen, the confirmation worker advances broadcast
transactions to `confirming`, `confirmed` (at
`ETH402_REQUIRED_CONFIRMATIONS`), or `reverted` and returns reorged-out
transactions to `broadcast`, and the recovery worker reconciles the failure
modes below. Workers claim payments with leases of
`ETH402_SETTLEMENT_LEASE_DURATION` (default 2m); a dead worker's payments are
reclaimed when the lease lapses. Batched workers renew each payment immediately
before acting and skip it if ownership has already lapsed. A signing failure leaves the intent untouched
for the next tick; a broadcast failure marks the transaction `ambiguous` and
moves the payment to `manual_review` (ADR-0004 decision 4).
`ETH402_SETTLEMENT_EXPIRY_MARGIN` (default 60s) retires intents whose
authorization would expire before broadcast as `expired` instead of buying a
predictable revert.

Recovery handles four cases automatically:

- **Ambiguous broadcasts.** The transaction is looked up on chain by its
  signed-transaction hash; a sighting re-attaches the hash and returns the
  payment to the pipeline. After `ETH402_SETTLEMENT_RECOVERY_GRACE` (default
  2m) without a sighting, the identical transaction — same nonce, gas, and
  fees, never a fresh nonce — is re-signed and re-broadcast, proven by the
  stored deterministic sighash. Cloud KMS randomizes the ECDSA nonce, so the
  re-signed hash legitimately differs from the stored one: the fresh signature
  is then recorded as the replacement of the ambiguous original (payment moves
  `manual_review → replaced`), and whichever signature the network mines
  resolves the payment.
- **Stuck pendings.** A broadcast pending beyond
  `ETH402_SETTLEMENT_REPLACEMENT_AFTER` (default 5m) is replaced with a
  fee-bumped transaction on the same nonce (tip ×1.125, both fee cap and tip
  raised to the mempool's 110% price-bump floor, capped by
  `ETH402_MAX_FEE_PER_GAS_WEI`). Whichever version mines, the recorded history
  is corrected to match.
- **Nonce gaps.** A dropped `expired` or simulation-`failed` intent blocking a
  later in-flight nonce waits until `validBefore` plus the settlement safety
  margin, then is signed and re-broadcast. Its exact signed bytes are stored
  before the first send and reused after ambiguity; its predictable revert
  consumes the signer nonce without moving USDC.
- **Reorgs.** A transaction whose block leaves the canonical chain returns to
  `broadcast` and is observed from scratch.

Keep alerting on payments entering `manual_review`: most leave on their own
once recovery reconciles them, but three cases stay and need an operator —
ambiguous rows written before migration `000004` (no stored fee fields to
re-sign from), a recomputed sighash that does not match the stored one (treat
the record as corrupt; reconcile the nonce on chain by hand — rows predating
migration `000006` compare raw hashes instead, which a randomized-nonce signer
like Cloud KMS can never satisfy, so those rows resolve by on-chain lookup
only), and a stuck
transaction already at the fee ceiling (raising `ETH402_MAX_FEE_PER_GAS_WEI`
is a spend decision, not the worker's). A gap filler that *succeeds* on an
expired authorization is logged as an error and left for investigation.

Logs are structured JSON. Never log keys, tokens, signatures, raw
authorizations, signed transactions, or unredacted email. Back up PostgreSQL,
test restoration, retain immutable audit copies, and synchronize clocks.

## Client addresses behind a proxy

Rate limits are keyed on the client address. `ETH402_TRUSTED_PROXIES` lists the
reverse proxies allowed to assert that address through `X-Forwarded-For`, as
CIDR prefixes or bare IP addresses.

The list defaults to empty, which keys every bucket on the directly-connected
peer. That is correct only when the service is exposed without a proxy. **Behind
a proxy an empty list collapses every client into a single bucket**, so the
public and registration limits apply to all traffic in aggregate and are
trivially exhausted. Set the variable to the proxy's address whenever anything
terminates connections in front of the service; the Compose deployment uses the
private container ranges.

Keep the list as narrow as the deployment allows. Every prefix is treated as
infrastructure rather than as a client, so a real client whose address falls
inside a trusted prefix is attributed to the nearest untrusted hop, or to the
peer when there is none, and shares that bucket. Trusting the broad private
ranges is therefore appropriate for the Compose topology, where Caddy is the
only ingress, but not for a deployment whose clients reach the service from the
same private network.

Given a trusted peer, the rightmost `X-Forwarded-For` entry that is not itself a
trusted proxy is used. Each proxy appends the peer it observed, so a forged
header can only prepend entries and cannot select another client's bucket. When
the peer is untrusted the header is ignored entirely. The bundled `Caddyfile`
additionally replaces the header outright, so the application receives exactly
the address Caddy observed. Operators inserting a CDN or a second proxy must
append rather than replace and extend `ETH402_TRUSTED_PROXIES` to every hop,
otherwise the CDN's address becomes the rate-limit key. IPv6 clients are grouped
by `/64` because a single subscriber is routinely assigned the whole prefix.

## Metrics exposure

`ETH402_METRICS_ENABLED` controls whether `/metrics` is registered at all; when
false the route returns 404. The bundled `Caddyfile` also refuses `/metrics` on
the public listener, and Prometheus scrapes `app:8080` directly on the container
network. Keep both controls in place: metrics are an operational disclosure
boundary, not public data.

## Signer balance monitoring

`/metrics` publishes the settlement signer's balance so the bound on a compromise
is observable. ADR-0004 decision 8 keeps that bound the operative control even
with the policy boundary deployed: the boundary constrains *what* can be signed,
but a compromised facilitator can still settle payments of its choosing, so the
limit is what the signer can spend.

| Metric | Meaning |
|---|---|
| `eth402_signer_balance_wei` | current balance; float precision, adequate for thresholds |
| `eth402_signer_balance_updated_timestamp_seconds` | when it was last read successfully |
| `eth402_signer_balance_read_errors_total` | failed reads |

Alert on **all three**. Depletion alone is not enough: a failing read leaves the
last figure in place, which looks healthy while the depletion and drain alerts are
both blind — so staleness is alerted separately. Example rules are in
`deploy/alerts.yml`; the thresholds there are illustrative and should come from
your own figures.

Burn rate is deliberately not computed in-process. Prometheus derives it from the
gauge with `deriv()`, correctly across restarts, which is exactly when a
compromise would be noticed.

The balance is only published after a successful read: an unset gauge is absent
rather than zero, because zero is indistinguishable from a drained account.

Top up from a source ETH402 cannot spend from, and keep the working balance small —
that is what caps instant-drain loss.

## Settlement gas exposure

`ETH402_MERCHANT_SETTLEMENT_QUOTA` bounds how many settlement intents one
merchant may commit per `ETH402_MERCHANT_QUOTA_WINDOW`. Because registration is
not Sybil-resistant, the recipient gate alone says nothing about volume: this
quota is what makes an admitted merchant's spend finite. Worst-case exposure per
merchant per window is quota × `ETH402_MAX_GAS_LIMIT` ×
`ETH402_MAX_FEE_PER_GAS_WEI`; compute it before raising either. Zero is rejected
rather than treated as unlimited. Admission locks the current active merchant
row before counting and keeps that lock through the intent commit. Different
payments for one merchant therefore decide in commit order instead of observing
the same pre-limit count. The same check makes a completed suspension revoke
later settlement even when `/verify` attributed the payment beforehand.
Replacements and gap fillers reuse existing rows and do not count.

`ETH402_GLOBAL_SETTLEMENT_QUOTA` bounds intents across all merchants in the
same window and must be greater than or equal to the merchant quota. Without
it, total exposure grows as merchants × per-merchant quota. The global check is
serialized under one advisory lock, then nonce allocation serializes on the
signer row. Worst-case admitted exposure per window is:

```text
ETH402_GLOBAL_SETTLEMENT_QUOTA
  × ETH402_MAX_GAS_LIMIT
  × ETH402_MAX_FEE_PER_GAS_WEI
```

Compute and approve that figure, the per-merchant figure, and the bounded hot
balance before enabling settlement. For the first funded transaction, follow
the [limited mainnet dry-run procedure](MAINNET_DRY_RUN.md).

## Running more than one instance

Every settlement path claims the payment lease before acting, including the
recovery worker's replacement, nonce-gap, and gap-filler passes. Those three
iterate over query results rather than a leased batch, so previously two instances
would each re-estimate fees for the same nonce gap and produce *different* signed
transactions — the deduplicating hash lookup misses and both broadcast, one
replacing the other. They now claim the payment first and skip whatever another
instance holds.

Nonce allocation was already safe across instances: it serialises on the
`signer_accounts` row inside the transaction that commits the intent.

Caveat before scaling out: no test runs two full application processes. What is
covered is the invariant they depend on — concurrent claimants of one payment,
exactly one of which wins.

## Retention

The retention worker runs immediately at startup and on
`ETH402_RETENTION_INTERVAL`, processing at most
`ETH402_RETENTION_BATCH_SIZE` rows per category. Defaults and the exact
tombstone fields are in [Privacy](PRIVACY.md). Monitor the oldest unredacted
eligible row and table sizes; a growing backlog means the batch/interval is too
small or the worker is failing.

Every `/verify` call still appends a `verification_attempts` row, including
malformed requests, and the table is protected by an append-only trigger.
These rows contain reason codes and optional irreversible payment identities,
not payer addresses or raw authorizations. The endpoint is unauthenticated, so
operators must still plan capacity and monitor attempt growth rather than
granting the runtime role arbitrary deletion rights.

Retention deliberately skips any authorization that recovery may need for a
dropped signer nonce. If old unredacted rows remain, inspect their state and
transaction status before changing policy; never manually clear one whose
transaction is `dropped`, `broadcast`, or otherwise unresolved.

## Rate limiting under flood

The limiter keys a bucket per client address, capped at 100,000 entries because
IPv6 makes distinct addresses free — one /32 allocation yields 2^32 distinct /64
buckets, so "use more addresses" costs an attacker nothing.

At the cap it **evicts** an entry to make room rather than sharing one bucket among
all later arrivals. That matters: sharing bounds memory equally well but lets an
attacker who fills the map deny service to every subsequent legitimate client,
which is a far stronger attack than the memory growth the cap prevents. The abuse
suite measured exactly that — a client sending 20 requests was denied all 20.

Eviction trades a little accuracy for that. An attacker can churn the map to reset
a heavy client's counter, but resetting one bucket costs roughly 100,000 requests,
so anyone able to afford the churn could have sent those requests directly instead.

Practical consequence: under a flood, per-client counts may reset early, so treat
`eth402_http_requests_total{status="429"}` as the signal that the limiter is
engaged, not as an exact count of blocked abuse.

## Status page

`/status` is a self-contained HTML page and `/stats` is its JSON equivalent. Both
are public and unauthenticated, and both render the *same cached snapshot* — health
is probed on the cache's schedule rather than per request, so neither can be used to
drive database or RPC load from outside.

The reported status is derived from observations, never assumed: a database ping, an
RPC chain-id check, and settlement-worker heartbeats — the same heartbeats
Prometheus scrapes, so the page and the alerts cannot disagree. States are
`operational`, `degraded`, `outage`, and `unknown`, where `unknown` means health
could not be observed at all and is deliberately not reported as healthy.

A stalled worker is `degraded` rather than an outage: verification keeps working and
committed intents stay durable, so payments are delayed rather than lost. Settlement
with no signer configured reports `disabled`, which does not degrade the whole —
reporting a deliberate configuration as broken trains people to ignore the page.

Volume figures are withheld unless `ETH402_PUBLISH_STATS_VOLUME=true`. See
[privacy](PRIVACY.md) for why aggregating does not anonymize them.

## Deploys

Shutdown drains the HTTP server, then waits up to 45 seconds for an in-flight
settlement worker tick. An interrupted broadcast — sent but with its hash unrecorded
— is the `ambiguous` case, and resolving one costs a human, so the send-and-record
pair is detached from shutdown cancellation and the process waits for it.

Give the platform a termination grace period longer than that wait, or it kills the
process during exactly the window the wait protects. If the drain times out, the log
says so; the recovery worker resolves whatever was in flight on the next start.

## When something is stuck

See [runbooks](RUNBOOKS.md). It starts with one triage query over every
non-terminal payment and routes from there, and it is written so the rule that
matters survives contact with an incident: an ambiguous broadcast is never resolved
by sending another transaction before reconciling by hash and nonce.
