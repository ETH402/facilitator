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

### Security

- Live settlement is intentionally absent and transaction signing defaults to disabled.
- API keys are stored as keyed hashes; email tokens and wallet messages are
  stored only as hashes. Wallet challenges are single-use and time-bound.
- Verification rejects non-mainnet networks, non-native-USDC assets, Permit2,
  extensions, contract-wallet payers, used EIP-3009 nonces, and non-exact
  requirement echoes before any settlement action.
