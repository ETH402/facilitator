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

- [x] pin official Go v2.20.0 types and exact-EVM verifier
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

Status: complete on `milestone-3-settlement`. The execution
model is recorded in [ADR-0004](docs/decisions/0004-settlement-execution-model.md):
GCP Cloud KMS, a single signer address, durable nonce allocation, recipient-gated
`/settle` admission, explicit worker leases, and a configurable expiry margin.

- [x] mandatory gas policy before any signer can be enabled
- [x] accepted execution model and resolved design questions
- [x] `signer_accounts` migration, worker lease columns, durable nonce allocation
- [x] signer interface carrying nonce and EIP-1559 fields, with fail-closed validation
- [x] settlement intent persisted atomically with nonce allocation and admission
- [x] worker lease primitive: claim, renew, release, stale reclaim
- [x] Cloud KMS signer behind the existing interface
- [x] idempotent broadcast and confirmation workers over the lease
- [x] ambiguous-RPC recovery, replacements, dropped transactions, reorg handling
- [x] `/settle` endpoint and confirmed-only volume

The `/settle` endpoint requires a prior `/verify` and runs the broadcast
pipeline inline (sign once, broadcast once, record the hash), returning the
official `SettleResponse`; the broadcast worker retries durable intents and the
confirmation worker finalizes at the configured depth. The recovery worker
reconciles ambiguous broadcasts on chain (identical re-broadcast after a grace
window, proven by hash), replaces stuck pendings with same-nonce fee bumps,
fills dropped nonce gaps that block later transactions, and returns reorged
transactions to broadcast. The production signer is GCP Cloud KMS
(`ETH402_SIGNER_MODE=external`, verified end-to-end against a real key); the
development key signer remains for local use.

`internal/e2e` proves the whole path against genuine USDC on a mainnet-forked
Anvil: `/verify`, `/settle`, a real `transferWithAuthorization`, the merchant's
balance rising, the authorization nonce spent on chain, a duplicate settle
converging on the recorded hash, and confirmation reaching `confirmed`. See
[local development](docs/LOCAL_DEVELOPMENT.md).

### Residual work

Not blocking the milestone, but each one is a control that is weaker than it looks:

- **The differing-signature re-broadcast has only fake-signer coverage.** No test
  has watched a real mempool reject a same-nonce same-fee re-broadcast as
  underpriced, which is what happens if the original reappears between the
  on-chain check and the send. Anvil mines instantly by default, so reproducing it
  needs `--no-mining` and manual block control.
- **The 12-confirmation finality cut is accepted** (ADR-0004 decision 5, agreed
  2026-07-29). `confirmed` is terminal, so a reorg deeper than 12 blocks leaves a
  payment marked confirmed that no longer exists on chain. This is now a chosen
  position rather than an inherited default; raising
  `ETH402_REQUIRED_CONFIRMATIONS` narrows it at the cost of latency.
- **Proceeding without an independent read of the settlement code**, accepted by
  the operator on 2026-07-29. It signs Ethereum transactions and spends ETH, and
  the same party wrote and reviewed nearly all of it. This is a recorded risk
  acceptance, not a review: no independent read has happened.
  `internal/settlement/recovery.go` is where one should start — the most intricate
  code in the repository, whose hardest paths need adversarial chain conditions no
  test reproduces.

## Milestone 4 — public deployment

- [x] Cloud KMS signer integration
- [x] hardened image and proxy: digest-pinned bases, distroless nonroot,
      request and header caps, reverse-proxy timeouts, container resource limits,
      and an in-binary health probe for a shell-less image
- [x] real metrics and alert rules for RPC failure, worker liveness, and the signer
      balance that bounds a compromise
- [x] runbooks for the states settlement actually produces
- [x] GCP deployment documentation
- [x] public status page — self-contained, served from the same cached snapshot as
      `/stats`, with a status derived from real observations rather than the
      constant it used to be
- [x] privacy-preserving analytics — posture documented in `docs/PRIVACY.md`;
      settled volume withheld by default because polling a cumulative total
      recovers individual payment amounts
- [x] KMS-fronted policy signer, adding the signing boundary as a second,
      structural enforcement of the zero-value/USDC-selector allowlist — the
      boundary receives authorization fields rather than a transaction, so it
      builds what it signs and the restriction is structural rather than a check.
      The in-process allowlist (`signer.Transaction.Validate`) was not moved out;
      it still runs and is kept deliberately as a redundant fail-fast (ADR-0004
      decision 8)

Milestone 4 is complete.

## Milestone 5 — public beta

- [x] self-contained merchant administration — email-link sessions remain
      unprivileged until a fresh registered-recipient SIWE proof; initial
      activation, pending and active recipient replacement, API-key management,
      and opt-in private retained-window statistics are available without
      third-party frontend dependencies

- [x] first-party product surface — an ETH402-branded landing page, aggregate
      network view, and redesigned merchant dashboard share same-origin assets;
      merchant discovery is a separate wallet-authorized opt-in and publishes
      counts rather than amounts or addresses

- [x] data retention — migration `000009` plus the bounded retention worker
      expire stale un-settled verifications and tombstone terminal payer
      authorizations after 30 days by default, without changing lifetime
      statistics or `/settle` idempotency. Authorizations still needed for nonce
      recovery are retained until safe. Ephemeral tokens/challenges and revoked
      keys have separate configurable lifetimes; assumptions and residual hashes
      are documented in `docs/PRIVACY.md`

- [x] load/abuse tests — `-tags=abuse` plus fuzz targets on the payment-critical
      parsers; found and fixed a rate-limiter denial of service
- [x] responsible disclosure readiness — `SECURITY.md` now states the real scope
      instead of claiming no payments are processed
- [x] fair-use enforcement — a facilitator-wide settlement ceiling so exposure is
      not merchants × quota, plus per-merchant limits on authenticated endpoints;
      deliberately not applied where the identity is caller-supplied
- [x] incident simulations — deterministic RPC/signer outage, ambiguous
      broadcast, reorg, worker crash/lease-loss, and nonce-gap drills run against
      the destructive test database; staging alert delivery and funded mainnet
      behavior remain explicitly outside their claim
- [x] public documentation — `docs/INTEGRATION.md` covers capability discovery,
      merchant activation, the verify/settle lifecycle, stable failure handling,
      security invariants, and the self-hosting gate; README and OpenAPI identify
      the implementation status and exact public contract
- [ ] limited mainnet dry runs — signing is enabled and the bounded procedure,
      abort conditions, and evidence checklist are ready in
      `docs/MAINNET_DRY_RUN.md`; no funded settlement has yet reached the
      required confirmation depth. Signed release `v0.1.0-rc.6` is deployed for
      the facilitator and the unchanged policy signer remains on its immutable
      rc4 digest; `/supported` advertises the configured signer and production
      remains at nonce zero
- [x] independent security review — completed by a third party against the
      frozen target; all findings dispositioned and applied. `SECURITY.md`
      scopes reports and `docs/SECURITY_REVIEW.md` records the handoff,
      invariants, adversarial paths, evidence commands, and exit criteria

Production runs with `ETH402_SIGNER_MODE=policy`; Milestone 5 completes after
the controlled funded settlement evidence is captured.
