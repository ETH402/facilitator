# Protocol research

Protocol retrieval date: **2026-07-27**; SDK release rechecked
**2026-08-04**. Protocol decisions use primary sources only.
The official x402 specification was inspected at commit
`90688e52e58ae9185f2860988bd2c46d2801ceda`. The latest released Go v2 module
reported by the Go proxy is `v2.20.0` (2026-07-27), whose tag resolves to
commit `895f3505a6c0beb767555344cb97130c3da7c8b2`.

Foundation versions were also checked on the retrieval date: official
[Go release history](https://go.dev/doc/devel/release) identified Go `1.26.5`
as current; Go module metadata identified `pgx/v5 v5.10.0`,
`golang.org/x/crypto v0.54.0`, `golangci-lint v2.12.2`, and
`golang.org/x/vuln v1.6.0`. These are pinned directly or in CI.

## Findings

| Source | Relevant behavior | Implementation consequence | Uncertainty/discrepancy |
|---|---|---|---|
| [x402 v2 specification](https://github.com/x402-foundation/x402/blob/90688e52e58ae9185f2860988bd2c46d2801ceda/specs/x402-specification-v2.md) | Facilitator interface is `POST /verify`, `POST /settle`, `GET /supported`. Requests wrap `x402Version`, `paymentPayload`, and `paymentRequirements`. Core response fields are documented there. Networks use CAIP-2. | Milestone 2 must use the official v2 types and exact wire names; ETH402 auth must remain outside these payloads. | The prose spec and released SDK evolve. Pin a release and contract-test wire JSON before exposing endpoints. |
| [exact EVM scheme](https://github.com/x402-foundation/x402/blob/90688e52e58ae9185f2860988bd2c46d2801ceda/specs/schemes/exact/scheme_exact_evm.md) | EIP-3009 is the recommended/default transfer method for compatible USDC. Buyer signs exact authorization; facilitator calls `transferWithAuthorization`. Facilitator pays gas but cannot change amount/destination. | ETH402 will accept only `assetTransferMethod=eip3009` (or omitted default), reject Permit2/ERC-7710, simulate before broadcast, and never custody USDC. Operator needs a gas-funded signer in Milestone 3. | Official exact-EVM now supports Permit2 and ERC-7710 variants. They are deliberately outside ETH402 scope, so `/supported` must not imply them. |
| [official Go SDK v2.20.0](https://github.com/x402-foundation/x402/tree/895f3505a6c0beb767555344cb97130c3da7c8b2/go) | Module is `github.com/x402-foundation/x402/go/v2`; current release is `v2.20.0`. Facilitator package is `mechanisms/evm/exact/facilitator`; v2 core types live under `types`. Official verify checks scheme/network, recipient, exact amount, time, EIP-712 signature, deployed asset contract, and transfer simulation. It supports EIP-3009, Permit2, ERC-1271, and gated ERC-6492. | ETH402 pins the release and reuses its official v2 wire types, EIP-712 helpers, error constants, and exact-EVM verifier. A read-only RPC adapter implements its signer interface; all write methods fail closed. ETH402 pre-validation narrows the SDK to EIP-3009 EOAs only. The v2.19.0→v2.20.0 source diff was reviewed: exact-EVM facilitator code is unchanged; release changes affect SVM, builder-code, generic HTTP helpers, SIWx Solana, and unused batch settlement. | The SDK dependency graph is larger than ETH402 would otherwise choose, but using protocol-critical upstream behavior is preferable to reimplementing it. |
| [ERC-3009](https://eips.ethereum.org/EIPS/eip-3009) | `transferWithAuthorization(from,to,value,validAfter,validBefore,nonce,v,r,s)` uses EIP-712. A random 32-byte nonce is scoped to authorizer and becomes used on-chain. Time bounds are Unix seconds. | Identity includes structured authorization fields and signature; verify on-chain `authorizationState`; database uniqueness is defense-in-depth; expired/not-yet-valid requests fail before broadcast. | ERC-3009 remains Draft, while native USDC implements it. Contract behavior is authoritative and must be verified against mainnet bytecode/read calls before launch. |
| [EIP-712](https://eips.ethereum.org/EIPS/eip-712) | Typed signing binds domain fields such as chain ID and verifying contract. EIP-712 itself does not provide replay protection. | Use USDC's exact domain (`name`, `version`, chain ID 1, contract); rely on EIP-3009 nonce and durable uniqueness for replay protection. | Token proxy upgrades could affect observed domain behavior; read domain/name/version and contract implementation during Milestone 2 validation. |
| [Circle USDC addresses](https://developers.circle.com/stablecoins/usdc-contract-addresses) and [Circle stablecoin contracts](https://github.com/circlefin/stablecoin-evm) | Native Ethereum-mainnet USDC is `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`; FiatToken implements EIP-3009/EIP-712. Ethereum USDC's domain is token name `USD Coin`, version `2`, chain ID `1`, verifying contract equal to the proxy address. | Fail-fast constant and normalized comparison; require that exact domain in `paymentRequirements.extra`; no bridged USDC or other asset. USDC has 6 decimals and monetary storage is integer atomic units. | Circle can upgrade the proxy. Re-read name/version/code and run controlled mainnet verification before public deployment; configuration cannot silently substitute another address. |
| [EIP-2228](https://eips.ethereum.org/EIPS/eip-2228) and [Ethereum network docs](https://ethereum.org/developers/docs/networks/) | Ethereum Mainnet has chain ID/network ID 1. | Only chain ID `1` and CAIP-2 `eip155:1`; readiness calls `eth_chainId` and refuses mismatches. | Local Anvil uses chain ID 1 solely to exercise this guard; it is not mainnet and carries no real funds. |
| [ERC-4361](https://eips.ethereum.org/EIPS/eip-4361) | SIWE defines a human-readable, domain-bound message with address, URI, version, chain ID, random nonce, issued time, optional expiration/request ID/resources; EOA uses ERC-191 and contract accounts use ERC-1271 resolution. | Milestone 1 uses SIWE with domain `eth402.org`, URI `https://eth402.org`, chain ID 1, merchant UUID as request ID, and an action resource. Challenges are random, expiring, hashed, and consumed once. | Milestone 1 accepts EOA/ERC-191 signatures only. ERC-1271 contract-recipient proof requires RPC-aware verification and remains deliberately unsupported until its threat model and failure behavior are implemented. |
| [official SIWE Go implementation](https://github.com/signinwithethereum/siwe-go) | Module version `v1.0.0` parses EIP-4361 messages, computes the EIP-191 digest, recovers EOA signers, and offers optional ERC-1271 verification through an Ethereum client. | Pin `github.com/signinwithethereum/siwe-go v1.0.0`; use its parser and EIP-191 verifier instead of custom signature recovery. Independently enforce ETH402 domain, URI, chain, merchant, action, issue time, and expiration. | The package transitively imports go-ethereum and materially increases the dependency graph. It is retained because wallet signature parsing is security-critical; dependency scanning is mandatory. |

## Exact v2 shapes to preserve

`PaymentRequirements` fields: `scheme`, `network`, `amount`, `asset`, `payTo`,
`maxTimeoutSeconds`, optional `extra`. `PaymentPayload` fields:
`x402Version`, optional `resource`, `accepted`, `payload`, optional
`extensions`. EIP-3009 payload fields: `signature` and `authorization` with
`from`, `to`, `value`, `validAfter`, `validBefore`, `nonce`.

`VerifyResponse`: `isValid`, optional `invalidReason`, optional `payer`, and
optional extension/extra data as supported by the pinned SDK.
`SettleResponse`: `success`, optional `errorReason`, optional `payer`,
required `transaction` (empty on failure), `network`, optional `amount` and
extension data. `SupportedResponse`: `kinds`, `extensions`, and `signers`.

Official v2.20.0 exact-EVM error reason examples include
`invalid_exact_evm_insufficient_balance`,
`invalid_exact_evm_payload_authorization_valid_after`,
`invalid_exact_evm_payload_authorization_valid_before`,
`invalid_exact_evm_payload_authorization_value_mismatch`,
`invalid_exact_evm_signature`, `invalid_exact_evm_recipient_mismatch`,
`invalid_exact_evm_nonce_already_used`, and
`invalid_exact_evm_transaction_simulation_failed`.

HTTP status mapping is not exhaustively normative. The released Go HTTP client
can parse an official verification result from a non-2xx response, while the
reference facilitator examples vary in status selection. ETH402 returns `200`
with `{isValid:false,...}` for a structurally valid protocol request whose
authorization fails, `400` with the same top-level response shape for malformed
outer JSON, and `503` for dependency failures. It never exposes an internal
error message in the protocol response.

## Settlement responsibility and confirmation assumptions

The buyer signs only the USDC authorization. The facilitator's Ethereum signer
signs the outer contract-call transaction, broadcasts it, pays native ETH gas,
and waits for a receipt in the official reference implementation. Merchant
signers are not required. USDC moves from `authorization.from` directly to
`authorization.to`.

The core specification does not prescribe a production confirmation depth,
replacement algorithm, or reorganization policy. ETH402 therefore treats the
SDK's receipt success as broadcast execution, then adds a configurable
confirmation worker and canonical-block checks in Milestone 3. Default depth
is 12 but remains an operator risk parameter, not an x402 protocol field.
Blind retry after an ambiguous `eth_sendRawTransaction` response is unsafe;
recover by deriving/querying the signed transaction hash and signer nonce.

## Remaining questions before Milestone 3/public deployment

1. Confirm mainnet USDC code, EIP-712 domain, balance reads,
   `authorizationState`, and `eth_call` simulation through at least two
   production RPC providers. Milestone 2 tests use deterministic fakes and
   local infrastructure, not real-value mainnet authorization.
2. Evaluate ERC-1271/6492 payer support only after a separate threat-model and
   interoperability review. Milestone 2 intentionally accepts 65-byte EOA
   signatures only.
3. Define a sustainable gas policy outside protocol code. x402 requires the
   facilitator to pay gas, but ETH402's commercial model is intentionally open.
4. Establish confirmation depth, maximum fee, stuck-transaction, and
   replacement policies after mainnet measurements and security review.

## Comparative note: Primev FastRPC facilitator

Reviewed the primary
[Primev mainnet facilitator repository](https://github.com/primev/mainnet-x402-facilitator)
and [FastRPC documentation](https://docs.primev.xyz/v1.1.0/get-started/fastrpc)
on 2026-07-27. Its latency claim comes from sending the facilitator-signed
USDC transaction through mev-commit FastRPC, where opted-in builders can issue
a preconfirmation before the next Ethereum block. The implementation also
parallelizes the independent USDC balance and authorization-state reads.

Important differences prevent copying its result directly:

- its `/settle` code returns success when `eth_sendRawTransaction` returns a
  transaction hash; it does not itself fetch a receipt, query commitments, or
  wait for canonical Ethereum confirmations;
- FastRPC preconfirmation depends on an opted-in next proposer and a pre-funded
  gas/bid tank, and is not equivalent to canonical inclusion or finality;
- the repository defines protocol types locally, with `scheme` and `network`
  at the payment-payload top level, rather than importing the current official
  v2 Go types; compatibility must therefore be independently tested;
- its amount check accepts `value >= amount`, whereas ETH402's exact-only
  policy requires the official exact-scheme behavior;
- nonce coordination is an RPC fetch-and-retry loop with no durable intent or
  database uniqueness, which does not meet ETH402 restart and ambiguity
  requirements.

ETH402 should adopt parallel safe reads and low-latency RPC placement. A
preconfirmation provider may later be an optional broadcast transport, but
`preconfirmed`, `included`, and `canonically confirmed` must remain distinct
states. No preconfirmation result may bypass durable idempotency, simulation,
receipt processing, reorganization handling, or the configured confirmation
policy.
