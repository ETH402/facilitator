# Data model

Migration `000001_initial` creates:

- `merchants`: normalized identity, recipient, verification timestamps, review status.
- `email_verification_tokens`: SHA-256 token hashes, expiry, one-time consumption.
- `wallet_verification_challenges`: address/action-bound nonce and message hash.
- `api_keys`: lookup prefix, keyed hash, name, use and revocation timestamps.
- `recipient_address_history`: append-only old/new address and actor evidence.
- `payment_records`: canonical authorization, deterministic identity, state, exact integer amount.
- `verification_attempts` and `settlement_attempts`: append-only request
  counters so public lifetime statistics do not depend on process memory.
- `payment_transitions`: append-only state history.
- `ethereum_transactions`: intent, nonce, hash, receipt, replacement linkage.
- `audit_events`: append-only security events with secret-free JSON metadata.
- `merchant_suspensions`: operator reason and reinstatement history.

Migration `000002_signer_accounts` adds:

- `signer_accounts`: one row per settlement signer address holding `next_nonce`.
  Allocation is a single `UPDATE … RETURNING` inside the caller's transaction, so
  the row lock serialises concurrent allocations and a rolled-back settlement
  reissues rather than burns its nonce. The chain seeds and reconciles this value
  but never allocates from it; two concurrent reads of `eth_getTransactionCount`
  return the same nonce and one of the resulting transactions is silently
  dropped. See [ADR-0004](decisions/0004-settlement-execution-model.md).
- `payment_records.claimed_by` and `claimed_until`: worker leases, so a worker
  does not hold a database transaction open across Ethereum RPC calls. The pair
  is constrained to be set or unset together.

Migration `000003_settlement_signature` adds:

- `payment_records.payer_signature`: the EIP-3009 signature the deterministic
  payment identity binds, written atomically with the settlement intent (never
  at verification time). `transferWithAuthorization` calldata needs it, and
  storing it with the intent lets the broadcast worker rebuild calldata after a
  crash without trusting a caller twice. Normalized lowercase with a shape
  constraint (`0x` + 130 hex).

Money uses `numeric(78,0)` and API integer strings. Addresses are stored
lowercase for comparisons; display checksum formatting is derived. Database
constraints enforce v2, exact, `eip155:1`, state domains, time ordering,
authorization replay uniqueness, one active transaction per payment, and one
active suspension.

Deletion is restrictive for security history. Retention/anonymization must be
implemented explicitly with legal review; audit metadata must never contain
raw secrets. Payment signatures live only in `payment_records.payer_signature`,
never in audit metadata.
