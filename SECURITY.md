# Security policy

Please report vulnerabilities privately to `security@eth402.org`. Do not open a
public issue containing an exploit, secret, personal information, or
mainnet-impacting reproduction.

Include affected revision, impact, reproduction steps, and suggested
mitigation. The project will acknowledge reports as operational capacity
allows; no bounty is currently promised.

Never send private keys, seed phrases, API keys, email tokens, or funded
authorizations. Use unfunded, local-only test material.

## Scope

**This implementation verifies and settles real x402 v2 payments.** It signs
Ethereum mainnet transactions that move native USDC via EIP-3009
`transferWithAuthorization`, holds a funded hot signer balance, and stores payer
authorizations. Treat findings as financially material.

In scope, roughly in order of severity:

- anything that causes USDC to move to the wrong recipient, in the wrong amount,
  or more than once for one authorization
- anything that gets a transaction signed other than `transferWithAuthorization`
  to canonical mainnet USDC with zero ether value
- authorization replay, or acceptance of an authorization the payer did not sign
- draining the signer's ether balance, including via gas spent on settlements that
  predictably revert
- merchant account takeover, recipient-address substitution, or API-key forgery
- disclosure of payer addresses, amounts, or merchant details from any
  unauthenticated surface

Out of scope: findings that require already holding the operator token, KMS
credentials, or database access; volumetric denial of service against a
self-hosted deployment; and the residual risks the
[threat model](docs/THREAT_MODEL.md) already records, which it states explicitly
rather than implying there are none.

## What to read first

- [threat model](docs/THREAT_MODEL.md) — assets, controls, and stated residual risk
- [ADR-0004](docs/decisions/0004-settlement-execution-model.md) — the settlement
  execution model, including which invariants are load-bearing and why
- [key management](docs/KEY_MANAGEMENT.md) and [privacy](docs/PRIVACY.md)
- [incident response](docs/INCIDENT_RESPONSE.md) and [runbooks](docs/RUNBOOKS.md)
- [independent review packet](docs/SECURITY_REVIEW.md) — frozen target,
  invariants, adversarial paths, evidence, and exit criteria

## Reproducing safely

Everything can be exercised without touching mainnet or any funded key.
[Local development](docs/LOCAL_DEVELOPMENT.md) describes a mainnet-*forked* Anvil,
which gives real USDC contract behaviour against fake balances; the end-to-end test
moves genuine USDC there. Please reproduce on the fork, not on mainnet.
