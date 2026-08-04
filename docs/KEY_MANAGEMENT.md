# Key management

The facilitator settlement key pays gas and authorizes only outer Ethereum
transactions. Buyer EIP-3009 authorization constrains the USDC movement, but a
compromised signer can still drain its ETH and call arbitrary contracts.

Production is designed for the KMS-fronted policy signer
(`ETH402_SIGNER_MODE=policy`). Its `EC_SIGN_SECP256K1_SHA256` key remains in
GCP Cloud KMS, while the separate `cmd/policysigner` boundary receives
authorization fields rather than a transaction or digest and builds the
chain-1, canonical-USDC, zero-value call it signs. Grant the KMS key only to
the boundary's identity; a direct grant to the facilitator bypasses the policy
and makes the arrangement decorative. The in-process
`signer.Transaction.Validate` remains defense in depth. Direct KMS mode
(`external`) is implemented for controlled non-production validation, but KMS
alone cannot inspect calldata and production configuration rejects it. Rate limits,
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

Follow the [limited mainnet dry-run procedure](MAINNET_DRY_RUN.md) before the
first funded transaction.

Merchant API keys use a separate 32-byte-or-longer application pepper and are
stored only as HMAC-SHA-256 values with non-secret lookup prefixes. Store the
pepper in a production secret manager and never log it. Changing it immediately
invalidates every existing merchant API key; coordinated pepper rotation is
not implemented in Milestone 1 and requires an explicit migration/runbook.

Merchant admin sessions are a separate credential class. Their 256-bit random
values are stored only as SHA-256 hashes and carried in an HttpOnly, Secure,
SameSite=Strict cookie. Email creates an unprivileged
session; API-key management requires that the registered recipient wallet has
freshly authenticated that same session. Admin session tokens are not API keys,
cannot authorize x402 calls, and are pruned after expiry/revocation.

Email delivery has a third, independent secret:
`ETH402_EMAIL_OUTBOX_KEY` is exactly 32 random bytes encoded as 64 hexadecimal
characters. It encrypts the raw one-time token only between transactional enqueue
and SMTP acceptance/expiry; verification still stores only SHA-256 hashes. Keep
it in the production secret manager, never reuse the API-key pepper, operator
token, SMTP password, or settlement key, and never print it. Ciphertext is bound
to the merchant ID, token hash, and message kind to reject row swapping. Rotation
requires draining or expiring every pending outbox row before replacing the key;
delivered/abandoned rows contain no ciphertext and need no re-encryption.
