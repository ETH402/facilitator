# ETH402

ETH402 is an open, self-hostable x402 v2 facilitator for exact payments in
native USDC on Ethereum mainnet. It is a modular Go service designed for
machine clients, APIs, merchants, and autonomous agents.

Milestones 0–4 provide the validated architecture, merchant onboarding,
operational APIs, x402 v2 verification, durable settlement, a policy-enforcing
signing boundary, and deployment operations. ETH402 exposes `/supported`,
`/verify`, and — with a settlement signer enabled — `/settle` for exact
EIP-3009 payments in native Ethereum-mainnet USDC. `/settle` requires a prior
successful `/verify` for the same payment and broadcasts
`transferWithAuthorization` once, returning the transaction hash; confirmation
completes asynchronously at 12 confirmations by default, with a recovery worker
reconciling ambiguous broadcasts, stuck pendings, nonce gaps, and reorgs.
Production is designed to use the KMS-fronted policy signer
(`ETH402_SIGNER_MODE=policy`); direct GCP Cloud KMS access is also implemented,
while disabled and raw-key modes cover safe defaults and local development.

Buyer USDC moves directly from buyer to merchant through USDC's
EIP-3009 `transferWithAuthorization`; ETH402 never holds buyer or merchant
USDC. The facilitator operator pays Ethereum gas, as required by the official
exact-EVM scheme.

The current build is not approved for mainnet payment processing and refuses
production startup until a real email adapter is added. See the
[public integration guide](docs/INTEGRATION.md) and
[deployment status](docs/DEPLOYMENT.md).

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

See [public integration](docs/INTEGRATION.md),
[local development](docs/LOCAL_DEVELOPMENT.md),
[architecture](docs/ARCHITECTURE.md), and
[protocol research](docs/PROTOCOL_RESEARCH.md).

## Implemented endpoints

- `GET /health/live`
- `GET /health/ready`
- `GET /metrics`
- `GET /stats`
- `GET /supported`
- `POST /verify`
- `POST /settle` (requires a settlement signer; otherwise `settlement_unavailable`)
- merchant registration and email/wallet verification under `/v1/merchants/*`
- authenticated profile, API-key, and recipient-change APIs under `/v1/*`
- operator suspension and reinstatement under `/v1/admin/*`

The OpenAPI contract is in [openapi/eth402.yaml](openapi/eth402.yaml).
Merchant wallet proof supports EOA/EIP-191 signatures, and x402 payer
authorization supports EOA/EIP-712 signatures. ERC-1271 and ERC-6492 contract
wallet signatures remain deliberately unsupported. `/verify` performs no
settlement and requires no facilitator API key. `/settle` is likewise
unauthenticated (the x402 facilitator shape); admission is gated on the
payment's recipient being an active registered merchant, per ADR-0004.

## Independence notice

ETH402 is an independent open-source project. It is not officially endorsed
by, affiliated with, or sponsored by Ethereum, Coinbase, Circle, or the x402
maintainers. USDC is a product of Circle; x402 is an open protocol.

Licensed under Apache License 2.0.
