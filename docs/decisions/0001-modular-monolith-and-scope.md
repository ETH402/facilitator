# ADR 0001: Modular monolith and narrow protocol scope

Status: accepted — 2026-07-27.

ETH402 will deploy as a Go modular monolith with PostgreSQL as its durable
coordination boundary. It supports only x402 v2 `exact`, EIP-3009 native USDC,
and Ethereum mainnet. Merchant identity and future commercial policy remain
outside protocol types.

This minimizes distributed failure modes and attack surface while permitting
separate workers or adapters later. Multi-chain abstractions, Permit2,
ERC-7710, custom contracts, custody, and live settlement are excluded from
Milestone 0. A future change requires a new ADR and primary-source review.
