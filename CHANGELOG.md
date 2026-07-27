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
