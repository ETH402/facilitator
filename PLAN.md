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

Status: complete in the `milestone-1-onboarding` branch, subject to the
validation record in the milestone report.

- [x] enumeration-resistant registration and provider-neutral email delivery
- [x] hashed, expiring, one-time email tokens with resend throttling
- [x] EIP-4361 EOA recipient proof bound to domain, chain, merchant, and action
- [x] API-key issuance, multiple named keys, rotation, revocation, and last use
- [x] authenticated, cooldown-controlled recipient changes with append-only history
- [x] operator suspension/reinstatement and append-only audit events
- [x] domain controls, registration rate limits, OpenAPI, and integration tests

ERC-1271 contract-wallet recipient proof is not included; it requires
RPC-aware verification and a separate security review.

## Milestone 2 — x402 verification

Status: implementation complete on `milestone-2-verification`; final validation
is recorded in the milestone report.

- [x] pin official Go v2.19.0 types and exact-EVM verifier
- [x] expose `/supported` and `/verify` without merchant authentication
- [x] enforce only v2 + exact + eip155:1 + native USDC + EIP-3009
- [x] validate exact amount/recipient, authorization time, EIP-712 signature,
      payer type, asset code, on-chain nonce state, and transfer simulation
- [x] persist aggregate verification attempts and successful payment identity
- [x] converge concurrent duplicate records through an authorization advisory lock
      plus PostgreSQL uniqueness
- [x] expose verification metrics and document the stable HTTP contract

Permit2, ERC-1271, ERC-6492, extensions, settlement, and transaction broadcast
remain deliberately unavailable.

## Milestone 3 — settlement

Status: design accepted on `milestone-3-settlement`. The execution model is
recorded in [ADR-0004](docs/decisions/0004-settlement-execution-model.md):
GCP Cloud KMS, a single signer address, durable nonce allocation, recipient-gated
`/settle` admission, explicit worker leases, and a configurable expiry margin.

- [x] mandatory gas policy before any signer can be enabled
- [x] accepted execution model and resolved design questions
- [x] `signer_accounts` migration, worker lease columns, durable nonce allocation
- [x] signer interface carrying nonce and EIP-1559 fields, with fail-closed validation
- [x] settlement intent persisted atomically with nonce allocation and admission
- [x] worker lease primitive: claim, renew, release, stale reclaim
- [ ] Cloud KMS signer behind the existing interface
- [ ] idempotent broadcast and confirmation workers over the lease
- [ ] ambiguous-RPC recovery, replacements, dropped transactions, reorg handling
- [ ] `/settle` endpoint and confirmed-only volume

## Milestone 4 — public deployment

Harden Caddy and image, document GCP deployment, integrate Cloud KMS, alerts,
runbooks, public status, and privacy-preserving analytics. Add the KMS-fronted
policy signer deferred from Milestone 3, which moves the zero-value/USDC-selector
allowlist out of the ETH402 process and into the signing boundary.

## Milestone 5 — public beta

Independent security review, load/abuse tests, limited mainnet dry runs,
incident simulations, public documentation, fair-use enforcement, and
responsible disclosure readiness.
