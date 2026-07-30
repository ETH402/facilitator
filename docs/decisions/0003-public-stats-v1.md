# ADR 0003: Database-backed aggregate public stats v1

Status: accepted — 2026-07-27.

`/stats` is a stable aggregate product API with schema version `1`.
PostgreSQL—not process counters—is authoritative. Only confirmed settlements
contribute volume. Monetary values are integer strings; a six-decimal USDC
display value is derived. Prometheus `/metrics` is a separate operational
surface and may evolve independently.

Amendment — 2026-07-30: the schema is now version `2` (`StatsV2` in OpenAPI).
Settled-volume figures are omitted from the public response unless the operator
opts in with `ETH402_PUBLISH_STATS_VOLUME=true`, because polling a cumulative
total yields its own deltas and a delta spanning one settlement is that
payment's exact amount. Confirmed-only volume, integer strings with a derived
USDC display value, and the separate Prometheus surface are unchanged. The
`/status` page renders the same cached snapshot.
