# Changelog

All notable changes use semantic versioning. Public schemas are separately
versioned where noted.

## [Unreleased]

### Added

- Milestone 0 foundation and validated Ethereum-mainnet-only architecture.
- Version 1 public statistics schema.
- Initial PostgreSQL schema and append-only security history.
- Merchant registration, one-time email verification, EIP-4361 recipient
  proof, API-key lifecycle, recipient changes, and operator suspension.
- Version 0.2 ETH402 merchant OpenAPI contract.
- Standard x402 v2 `/supported` and `/verify` endpoints, pinned official Go
  v2.19.0 types/verifier, durable verification attempts, and version 0.3
  OpenAPI contract.
- Worker lease primitive over the `claimed_by`/`claimed_until` columns: claim by
  state with a bounded limit, renew only while still held, release, and automatic
  reclaim of leases left behind by a dead worker. Claiming is one atomic statement
  that re-checks ownership under the row lock, so two workers can never hold the
  same payment. `ErrLeaseLost` tells an overrunning worker to stop rather than
  risk duplicating work.
- Durable settlement intent. One transaction locks the payment, applies admission
  (verified state, recipient is an active registered merchant, authorization not
  inside the expiry margin), allocates the nonce, moves the payment to
  `broadcasting`, and writes the `intent` transaction row. Idempotent per payment:
  a second request returns the existing intent and its nonce rather than
  allocating another. Refusals are committed as `settlement_attempts` rows with a
  stable reason code and consume no nonce. Nothing is signed or broadcast.
- Durable Ethereum nonce allocation (`signer_accounts`) and worker lease columns
  in migration `000002`. Allocation joins the caller's transaction, so a nonce is
  never consumed without a durable record and a rolled-back settlement reissues
  rather than gaps the sequence.
- `signer.Transaction` now carries nonce and EIP-1559 fields with fail-closed
  validation rejecting non-mainnet chains, non-zero ether value, absent gas
  ceilings, and a priority fee above the total fee.
- Milestone 3 settlement execution model as ADR-0004 (accepted), covering durable
  nonce allocation, a single signer address on GCP Cloud KMS, recipient-gated
  `/settle` admission, lease-based workers, ambiguous-broadcast handling, an
  expiry margin, and the finality cut. A bounded signer hot balance is the
  operative signer-compromise control; a KMS-fronted policy signer carrying the
  calldata allowlist is deferred to Milestone 4.
- `POST /settle` (version 0.4 OpenAPI contract). Admission reuses the strict
  `/verify` parser and requires a prior verified payment record — the durable
  row is what binds the recipient to an active registered merchant (ADR-0004
  decision 9). The endpoint claims the payment lease and runs the broadcast
  pipeline inline: EIP-3009 `transferWithAuthorization` calldata is built from
  the durable record, signed under `ETH402_SIGNING_TIMEOUT`, broadcast once
  against the primary RPC, and the transaction hash is returned in the official
  `SettleResponse`. Calls are idempotent per payment; policy rejections return
  HTTP 200 with `success: false` and a stable `errorReason`.
- `payment_records.payer_signature` (migration `000003`): the EIP-3009
  signature the payment identity binds, persisted atomically with the
  settlement intent so calldata can be rebuilt after a crash.
- Broadcast and confirmation workers over the worker lease. The broadcast
  worker retries committed intents on `ETH402_WORKER_INTERVAL`; a signing
  failure leaves the intent untouched, an unknown broadcast outcome marks the
  transaction `ambiguous` and moves the payment to `manual_review` (never a
  re-sign, never a fresh nonce), and an authorization expiring before broadcast
  retires the intent as `expired` with the transaction `dropped`. The
  confirmation worker advances receipts to `confirming`, `confirmed` at
  `ETH402_REQUIRED_CONFIRMATIONS`, or `reverted`.
- Development signer backend (raw key, EIP-1559) behind the existing signer
  interface for local use against Anvil; production rejects it without
  `ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER`. The `external` mode is reserved for
  the Cloud KMS backend and now rejected at startup.
- `ETH402_SETTLEMENT_EXPIRY_MARGIN` (60s), `ETH402_SIGNING_TIMEOUT` (10s), and
  `ETH402_SETTLEMENT_LEASE_DURATION` (2m).
- Settlement recovery worker and migration `000004`. Ambiguous broadcasts are
  reconciled on chain by their signed-transaction hash and, after
  `ETH402_SETTLEMENT_RECOVERY_GRACE` (2m) without a sighting, re-broadcast as
  the byte-identical transaction — same nonce, gas, and fees, proven by hash —
  never a fresh nonce. Broadcasts pending beyond
  `ETH402_SETTLEMENT_REPLACEMENT_AFTER` (5m) are replaced by fee-bumped
  transactions on the same nonce (tip ×1.125, capped by the configured
  ceiling); an original the network mines anyway becomes the recorded truth
  and its replacement is dropped. Dropped nonces blocking a later in-flight
  nonce are filled by re-broadcasting the original expired intent, and
  reorged-out transactions return to `broadcast`. Migration `000004` persists
  the signing gas limit and fee pair at signing time and drops the
  `(payment_id, transaction_nonce)` uniqueness that replacement chains
  violate.
- EIP-1559 fee estimation: the initial max fee is `min(2·baseFee + tip,
  ETH402_MAX_FEE_PER_GAS_WEI)` from the latest block instead of the configured
  ceiling verbatim, so settlement no longer overpays beneath its own cap and
  replacements have bump headroom. The ceiling remains the hard spend bound.
- Real `eth402_settlement_requests_total` and `eth402_settlement_failures_total`
  metrics replacing the zero placeholders.

### Changed

- Enabling a settlement signer now requires non-zero max fee per gas and max gas
  limit, and a priority fee may not exceed the total fee ceiling.
- Rate limits key on the client address resolved through `ETH402_TRUSTED_PROXIES`
  instead of the direct peer, which collapsed all traffic behind a reverse proxy
  into a single bucket. IPv6 clients are grouped by `/64`.
- `ETH402_METRICS_ENABLED` now actually gates the `/metrics` route; it was parsed
  and validated but never read.
- The recipient-change cooldown is measured from the last verified recipient
  proof rather than `merchants.updated_at`, which unrelated writes such as
  operator reinstatement silently pushed forward.
- Malformed numeric, duration, and boolean environment values are reported by
  variable name instead of collapsing to a sentinel that validation either
  misattributed or accepted.
- Per-route metrics report identifier-bearing paths as their registered pattern
  rather than `unknown`.
- Payment-to-merchant attribution orders active claimants deterministically when
  more than one merchant shares a recipient address.
- Registration surfaces unexpected database errors instead of reporting success,
  while keeping duplicate registrations enumeration-resistant.
- Concurrent duplicate verification now converges instead of deadlocking. Writers
  for one authorization serialise on an advisory transaction lock, and transient
  deadlock or serialization aborts are retried, so two simultaneous `/verify`
  requests no longer risk one caller receiving `503`.

### Fixed

- Worker-driven payment transitions no longer fail the
  `payment_transitions.actor_type` check: workers audited transitions with
  their full lease identity (for example `confirmation/host/pid`), which the
  schema rejects, so every worker-side transition would have rolled back in
  production. Transitions are now audited with the coarse `worker` actor type;
  the full identity stays on the lease.

### Security

- Live settlement is intentionally absent and transaction signing defaults to disabled.
- `X-Forwarded-For` is honoured only from configured trusted proxies, using the
  rightmost untrusted entry, so a forged header cannot select another client's
  rate-limit bucket. The bundled Caddy configuration replaces the header and
  refuses `/metrics` on the public listener.
- API keys are stored as keyed hashes; email tokens and wallet messages are
  stored only as hashes. Wallet challenges are single-use and time-bound.
- Verification rejects non-mainnet networks, non-native-USDC assets, Permit2,
  extensions, contract-wallet payers, used EIP-3009 nonces, and non-exact
  requirement echoes before any settlement action.
