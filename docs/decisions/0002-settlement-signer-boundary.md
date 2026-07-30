# ADR 0002: External settlement signer boundary

Status: accepted — 2026-07-27.

Official exact-EVM settlement requires the facilitator to sign/broadcast the
outer transaction and pay gas. ETH402 exposes a signer interface and defaults
to disabled. Production integration will target KMS/HSM/Vault/external policy
signers. Raw keys are development-only and rejected in production absent an
explicit dangerous override.

Payment intent, calldata identity, and Ethereum nonce are persisted before
broadcast. Ambiguous broadcasts are reconciled, never blindly retried.

Amendment — 2026-07-30: production no longer permits the dangerous override
this ADR allowed. `Config.Validate` rejects `ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER`
and any raw signer key in production outright, and requires
`ETH402_SIGNER_MODE` to be `policy` (the KMS-fronted policy signer of ADR-0004
decision 8) or `disabled`; the direct `external`/KMS and `development` modes are
rejected in production.
