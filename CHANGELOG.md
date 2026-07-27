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

### Changed

- Rate limits key on the client address resolved through `ETH402_TRUSTED_PROXIES`
  instead of the direct peer, which collapsed all traffic behind a reverse proxy
  into a single bucket. IPv6 clients are grouped by `/64`.
- `ETH402_METRICS_ENABLED` now actually gates the `/metrics` route; it was parsed
  and validated but never read.

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
