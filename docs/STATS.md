# Public statistics

`GET /stats` is an unauthenticated, briefly cached product endpoint with stable
schema version `"1"`. It contains only aggregate data. Lifetime values come
from PostgreSQL, so they survive restarts and multiple instances.

Definitions:

- verification request: a persisted x402 authorization verification attempt.
- successful verification: an attempt accepted by all verification checks.
- failed verification: a persisted malformed/invalid attempt or an attempt
  that could not safely complete because a dependency was unavailable.
- settlement request: a persisted `/settle` attempt, including duplicates and
  rejected attempts after a parseable identity is available.
- broadcast settlement: a transaction accepted/observed by an Ethereum RPC.
- confirmed settlement: successful receipt plus required canonical confirmations.
- failed settlement: terminal failed or reverted payment, not a transient RPC error.
- payment volume: sum of exact integer USDC atomic amounts for confirmed settlements only.

`total_payment_volume_atomic` and `volume_last_24h_atomic` are integer strings.
`total_payment_volume_usdc` is a derived six-decimal display string.
`confirmation_lag_blocks` is indexed minus last confirmed, floored at zero.

No email, key, address, transaction hash, per-merchant data, raw error,
internal IP, or high-cardinality value is exposed. Rate limiting and a 10-second
default cache reduce abuse.

`/metrics` is operational Prometheus exposition and is not a stable product
contract. Operators should restrict it at Caddy/firewall level in production.
