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

Gas maximums are typed decimal configuration but remain zero/disabled in the
current build. Milestone 3 must require explicit non-zero policy before enabling
any settlement signer.

Logs are structured JSON. Never log keys, tokens, signatures, raw
authorizations, signed transactions, or unredacted email. Back up PostgreSQL,
test restoration, retain immutable audit copies, and synchronize clocks.
