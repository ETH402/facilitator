# Key management

The facilitator settlement key pays gas and authorizes only outer Ethereum
transactions. Buyer EIP-3009 authorization constrains the USDC movement, but a
compromised signer can still drain its ETH and call arbitrary contracts.

Production uses GCP Cloud KMS (`ETH402_SIGNER_MODE=external`) with an
`EC_SIGN_SECP256K1_SHA256` key; key material never leaves KMS, and per-key IAM
plus audit logging come with it. Policy enforcement for chain ID 1, canonical
USDC destination, allowed calldata selector, zero ETH value, and gas limits
lives inside ETH402 (`signer.Transaction.Validate`), because KMS signs digests
and cannot inspect calldata; a KMS-fronted policy signer that moves the
allowlist into the signing boundary is Milestone 4 hardening. Rate limits,
audited access, separate staging and production keys, dual control for policy
changes, and emergency disable remain operational requirements. See
[ADR-0004](decisions/0004-settlement-execution-model.md).
KMS DER signatures are treated as untrusted boundary data: both scalars must be
strictly inside the secp256k1 order, trailing DER is rejected, and only a
signature that recovers the configured signer address is accepted.

Raw private keys are permitted only in explicit development/test mode and are
never printed, stored in database, embedded in images, or committed. No funded
key appears in this repository. Rotation must coordinate pending Ethereum
nonces; never abandon transactions silently.

Merchant API keys use a separate 32-byte-or-longer application pepper and are
stored only as HMAC-SHA-256 values with non-secret lookup prefixes. Store the
pepper in a production secret manager and never log it. Changing it immediately
invalidates every existing merchant API key; coordinated pepper rotation is
not implemented in Milestone 1 and requires an explicit migration/runbook.
