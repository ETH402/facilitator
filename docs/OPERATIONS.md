# Operations

Run migrations as a distinct deployment step. The application never creates
schema. Use separate PostgreSQL owner, migration, and runtime roles; runtime
needs only required table/sequence operations and no DDL.

Readiness requires PostgreSQL ping and RPC `eth_chainId == 1`. Liveness only
asserts the process can serve HTTP. Remove an instance from traffic on
readiness failure; do not restart-loop solely for an upstream outage.

Use two independently operated Ethereum RPC providers. Safe reads may retry
within bounds; transaction broadcast must not retry blindly. Alert on RPC/DB
errors, worker health, confirmation lag, pending age, signer failures, revert
rate, gas policy blocks, and stats-query failure.

## Signer balance

The settlement signer address holds a deliberately small working balance, topped
up from a source ETH402 cannot spend from, with alerting on both absolute balance
and burn rate. Cloud KMS signs digests and cannot inspect calldata, so a
compromised process can have an arbitrary transaction signed; bounding the hot
balance is what caps that loss. This is required before enabling any signer, and
is not superseded by the in-process calldata allowlist. See
[ADR-0004](decisions/0004-settlement-execution-model.md).

Gas maximums are typed decimal configuration. Enabling any non-disabled
`ETH402_SIGNER_MODE` requires non-zero `ETH402_MAX_FEE_PER_GAS_WEI` and
`ETH402_MAX_GAS_LIMIT`: zero means unset, not unlimited, so a signer cannot be
switched on without an explicit spend ceiling. A priority fee above the total
fee ceiling is also rejected. Settlement transactions use these configured
values verbatim as their gas limit and fee caps — they are the spend, not just
a bound — so the effective per-settlement cost is always known in advance.

## Settlement workers

With a signer enabled, two workers run in-process on `ETH402_WORKER_INTERVAL`
(default 15s): the broadcast worker retries durable intents whose inline
`/settle` broadcast did not happen, and the confirmation worker advances
broadcast transactions to `confirming`, `confirmed` (at
`ETH402_REQUIRED_CONFIRMATIONS`), or `reverted`. Workers claim payments with
leases of `ETH402_SETTLEMENT_LEASE_DURATION` (default 2m); a dead worker's
payments are reclaimed when the lease lapses. A signing failure leaves the
intent untouched for the next tick; a broadcast failure marks the transaction
`ambiguous` and moves the payment to `manual_review`, where it stays until
recovery tooling reconciles it on chain — alert on payments entering
`manual_review`, since each one is a possible spent gas transaction without a
recorded hash. `ETH402_SETTLEMENT_EXPIRY_MARGIN` (default 60s) retires intents
whose authorization would expire before broadcast as `expired` instead of
buying a predictable revert.

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

## Verification attempt retention

Every `/verify` call appends a `verification_attempts` row, including malformed
requests, and the table is protected by an append-only trigger. The endpoint is
unauthenticated, so growth is bounded only by the rate limit above. Operators
must plan capacity and, if pruning becomes necessary, do it as an explicit
migration that drops and restores the trigger under audit rather than granting
the runtime role deletion rights.
