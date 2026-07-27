# ETH402 delivery plan

## Milestone 0 — project foundation and validated architecture

Status: complete in the `milestone-0-foundation` branch, subject to the
validation results recorded in the final report and changelog.

- [x] Go module, license, editor and environment defaults, Makefile, CI
- [x] Docker, Compose, PostgreSQL, Caddy, and local Anvil foundation
- [x] primary-source x402/Ethereum/USDC research
- [x] architecture, threat model, data model, flows, and operations docs
- [x] typed configuration with production safety checks
- [x] structured logging, graceful HTTP server, request IDs, recovery, limits
- [x] liveness, readiness, Prometheus exposition, and stable `/stats`
- [x] PostgreSQL pool, explicit SQL migrations, migration command
- [x] Ethereum read-only RPC abstraction, development email abstraction
- [x] disabled production signer interface and deliberately non-broadcastable test signer
- [x] tests for configuration, secrets, challenges, state transitions, IDs, stats,
      USDC formatting, health, and RPC-aware readiness
- [x] quality and container validation

No live x402 verification or settlement is implemented in Milestone 0.

## Milestone 1 — merchant onboarding

Registration, provider-backed email verification, SIWE recipient proof,
API-key issuance/rotation/revocation, append-only recipient changes, operator
suspension/reinstatement, audit events, domain controls, and abuse limits.

## Milestone 2 — x402 verification

Pin and integrate official Go v2 types; implement `/supported` and `/verify`
for only `exact` + `eip155:1` + canonical USDC + EIP-3009. Add signature,
balance, authorization-state, time, amount, recipient, asset, and simulation
checks; persist replay identity and verification metrics.

## Milestone 3 — settlement

Implement `/settle`, durable intent, transaction nonce coordination,
simulation, gas estimation and policy, signer integration, idempotent
broadcast/confirmation workers, ambiguous-RPC recovery, replacements, dropped
transactions, reorg handling, and confirmed-only volume.

## Milestone 4 — public deployment

Harden Caddy and image, document GCP deployment, integrate KMS/HSM/external
signer, alerts, runbooks, public status, and privacy-preserving analytics.

## Milestone 5 — public beta

Independent security review, load/abuse tests, limited mainnet dry runs,
incident simulations, public documentation, fair-use enforcement, and
responsible disclosure readiness.
