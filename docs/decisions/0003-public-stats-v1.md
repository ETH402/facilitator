# ADR 0003: Database-backed aggregate public stats v1

Status: accepted — 2026-07-27.

`/stats` is a stable aggregate product API with schema version `1`.
PostgreSQL—not process counters—is authoritative. Only confirmed settlements
contribute volume. Monetary values are integer strings; a six-decimal USDC
display value is derived. Prometheus `/metrics` is a separate operational
surface and may evolve independently.
