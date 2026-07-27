# ETH402

ETH402 is an open, self-hostable x402 v2 facilitator for exact payments in
native USDC on Ethereum mainnet. It is a modular Go service designed for
machine clients, APIs, merchants, and autonomous agents.

Milestones 0 and 1 provide the validated architecture, database schema,
operational skeleton, merchant onboarding, EIP-4361 wallet proof, API-key
lifecycle, health checks, metrics, and database-backed public statistics.
ETH402 still does **not** expose x402 `/verify` or `/settle`, sign transactions,
or broadcast to Ethereum mainnet.

Buyer USDC is designed to move directly from buyer to merchant through USDC's
EIP-3009 `transferWithAuthorization`; ETH402 never holds buyer or merchant
USDC. Future settlement requires the facilitator operator to pay Ethereum gas,
as required by the official exact-EVM scheme.

## Scope

- x402 v2 only
- `exact` scheme only
- Ethereum mainnet (`eip155:1`, chain ID `1`) only
- native Ethereum USDC
  (`0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`) only
- non-custodial, open source, and self-hostable

## Local development

Requirements: Go 1.25+ (toolchain pinned to Go 1.26.5), Docker, and Docker Compose.

```sh
make setup
docker compose up -d postgres anvil
make migrate-up
make test
make dev
```

See [Local development](docs/LOCAL_DEVELOPMENT.md), [architecture](docs/ARCHITECTURE.md),
and [protocol research](docs/PROTOCOL_RESEARCH.md).

## Implemented endpoints

- `GET /health/live`
- `GET /health/ready`
- `GET /metrics`
- `GET /stats`
- merchant registration and email/wallet verification under `/v1/merchants/*`
- authenticated profile, API-key, and recipient-change APIs under `/v1/*`
- operator suspension and reinstatement under `/v1/admin/*`

The OpenAPI contract is in [openapi/eth402.yaml](openapi/eth402.yaml).
Wallet proof currently supports EOA/EIP-191 signatures. ERC-1271 contract
wallet proof is an explicit future security review item.

## Independence notice

ETH402 is an independent open-source project. It is not officially endorsed
by, affiliated with, or sponsored by Ethereum, Coinbase, Circle, or the x402
maintainers. USDC is a product of Circle; x402 is an open protocol.

Licensed under Apache License 2.0.
