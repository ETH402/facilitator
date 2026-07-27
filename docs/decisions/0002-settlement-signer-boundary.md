# ADR 0002: External settlement signer boundary

Status: accepted — 2026-07-27.

Official exact-EVM settlement requires the facilitator to sign/broadcast the
outer transaction and pay gas. ETH402 exposes a signer interface and defaults
to disabled. Production integration will target KMS/HSM/Vault/external policy
signers. Raw keys are development-only and rejected in production absent an
explicit dangerous override.

Payment intent, calldata identity, and Ethereum nonce are persisted before
broadcast. Ambiguous broadcasts are reconciled, never blindly retried.
