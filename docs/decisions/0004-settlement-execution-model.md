# ADR 0004: Settlement execution model

Status: **accepted** — 2026-07-27. Extends
[ADR-0002](0002-settlement-signer-boundary.md).

Milestone 3 makes ETH402 spend money for the first time. This ADR fixes the
execution model before any settlement code is written, and records the gaps
found between the Milestone 0–2 scaffolding and what settlement actually needs.

## Context

The existing scaffolding already provides more than it appears to:

- `payment_records` carries `settlement_requested_at`, `confirmed_at`, and the
  `state` enum, plus `UNIQUE (network, asset, payer_address, authorization_nonce)`.
- `ethereum_transactions` carries `tx_hash UNIQUE`, `transaction_nonce`,
  `raw_transaction_hash`, `replaced_by_id`, `block_hash`, and the `ambiguous`
  status, plus `UNIQUE (payment_id, transaction_nonce)` and the partial index
  `ethereum_transactions_active_payment_unique` over the non-terminal statuses.
- `settlement_attempts` mirrors `verification_attempts` with
  `result IN ('accepted','duplicate','rejected')`.
- `internal/settlement/state.go` encodes the payment state machine.

That partial unique index is the idempotency backbone: it makes "at most one
in-flight Ethereum transaction per payment" a database invariant rather than an
application convention. The design below builds on it rather than adding
coordination of its own.

## Decisions

### 1. Ethereum nonce allocation

Settlement must assign each transaction a distinct account nonce for a single
signer address, with no gaps and no reuse, across process restarts and
concurrent requests.

**Allocate from a dedicated durable row, not from the chain.**

Add a `signer_accounts` table keyed by signer address holding `next_nonce`.
Allocation happens inside the settlement transaction with `SELECT … FOR UPDATE`,
so nonce assignment commits atomically with the settlement intent that uses it.
`eth_getTransactionCount` is used only to *initialise* the row and to reconcile
during recovery — never as the live allocator.

Rejected alternatives:

- **`eth_getTransactionCount` per settlement.** Pending-vs-latest semantics vary
  by provider, and two concurrent settlements read the same value. This is the
  standard way to produce two transactions sharing a nonce, one of which is
  silently dropped.
- **`max(transaction_nonce) + 1` over `ethereum_transactions`.** Correct only if
  every historical row is still present and no row was ever created for a
  transaction that was never broadcast. Both assumptions fail during recovery.

Replacements deliberately reuse the same nonce and so must **not** allocate.

### 2. Signer account topology

**One signer address to begin with.** One nonce sequence and one gas balance to
fund, monitor, and alert on.

The cost is head-of-line blocking: because nonces are strictly sequential, a
stuck transaction at nonce N prevents every later settlement from mining until
it clears or is replaced. Replacement (same nonce, higher fee) is the designed
escape hatch, and it is why decision 1 forbids allocating a fresh nonce for a
replacement.

`signer_accounts` is keyed by address and `ethereum_transactions.signer_address`
is already `NOT NULL`, so a pool of addresses can be introduced later without a
painful migration. The signer address must be resolved once at startup and
pinned; a mid-flight change would corrupt the nonce sequence.

### 3. Commit before sign, sign before broadcast

The order is: verify → allocate nonce and insert intent (`state = broadcasting`,
`ethereum_transactions.status = intent`) → commit → sign → record
`raw_transaction_hash` → broadcast once → record `tx_hash`.

A crash after commit but before broadcast leaves a durable `intent` row that
recovery can resolve. A crash after broadcast but before recording the hash is
the ambiguous case below. Nothing is ever signed without a committed intent,
which is what makes "never blindly retry" enforceable.

### 4. Ambiguous broadcast

An RPC error that leaves broadcast outcome unknown sets
`ethereum_transactions.status = 'ambiguous'` and moves the payment to
`manual_review`.

`payment_records.state` deliberately gets **no** `ambiguous` value. Ambiguity is
a property of a transaction, not of a payment, and `broadcasting → manual_review`
already exists in the state machine. Recovery resolves ambiguity by looking up
`raw_transaction_hash` on chain; it never re-signs and never re-broadcasts with
a fresh nonce.

### 5. Finality and reorgs

`confirmed` is terminal, with **no** `confirmed → confirming` edge. Confirmation
requires `ETH402_REQUIRED_CONFIRMATIONS` (default 12) canonical confirmations,
and a reorg deeper than that is **accepted as residual risk** — already recorded
as "deep reorg" in the threat model.

This is a deliberate risk acceptance, stated here because
`docs/ARCHITECTURE.md`'s "reorgs return non-final transactions to confirmation"
reads more broadly than the state machine allows: it applies only to
`broadcast`/`confirming`, never to `confirmed`. Reorg handling compares
`block_hash` against the canonical chain and returns *non-final* transactions to
`confirming`.

### 6. Gas policy is mandatory

Implemented ahead of this ADR: `Validate` rejects any non-disabled
`ETH402_SIGNER_MODE` unless both `ETH402_MAX_FEE_PER_GAS_WEI` and
`ETH402_MAX_GAS_LIMIT` are non-zero, and rejects a priority fee exceeding the
total fee ceiling. Zero means unset, not unlimited, so the signer cannot be
switched on with an unbounded spend ceiling.

### 7. The signer interface must change

`signer.Transaction{ChainID, To, Data, Value}` cannot express a settlement
transaction. It is missing `Nonce`, `GasLimit`, `MaxFeePerGas`, and
`MaxPriorityFeePerGas`. Because decision 3 requires the nonce to be persisted
before signing, the nonce is an **input** chosen by the caller, not something
the signer may pick. The interface gains those fields and stays value-typed so a
signer implementation cannot reach back for chain state.

`signer.Disabled` remains the default and `TestSigner` remains deliberately
non-broadcastable.

### 8. Signer backend: GCP Cloud KMS

Milestone 4 already targets GCP deployment, and Cloud KMS supports secp256k1.
Key material never leaves KMS, and per-key IAM plus audit logging come with it.

Signing becomes a network round trip inside the settlement path, so the signer
call needs its own timeout and must be treated as a failure mode distinct from
RPC failure: a signing timeout leaves a committed `intent` row with no signed
transaction, which recovery resolves by signing again — safe precisely because
the nonce is already fixed and stored.

**Important consequence:** a raw KMS cannot enforce a calldata allowlist. The
"zero-value/USDC selector allowlist" that `docs/THREAT_MODEL.md` lists as a
signer-compromise control therefore lives **inside** ETH402, not inside the
signing boundary. A compromised ETH402 process can ask KMS to sign an arbitrary
transaction. This weakens that control from a boundary to an in-process check,
and the threat model has been updated to say so. Moving the allowlist behind an
external policy signer remains the upgrade path if that residual risk becomes
unacceptable.

### 9. `/settle` admission: the recipient must be a registered merchant

An attacker can construct a genuinely valid EIP-3009 authorization moving USDC
between two wallets they control. Verification cannot reject it — nothing about
the payment is invalid — and ETH402 pays the gas. Attacker cost is zero,
operator cost is unbounded. **No protocol-level check can prevent this.**

`/settle` therefore requires `payTo` to resolve to an **active registered
merchant**: `payment_records.merchant_id` must be non-null. `internal/store`
already performs that lookup; only the nullability is permissive today.

This is a policy gate on the payment's destination, not authentication of the
caller. `/settle` stays callable without merchant credentials, which preserves
the x402 facilitator shape and keeps merchant identity separate from protocol
logic per `AGENTS.md` #15. Gas spend becomes attributable to a party that
accepted terms, can carry a quota, and can be suspended through the existing
`merchant_suspensions` machinery.

Residual risk: anyone completing email and wallet proof can still drain gas,
bounded by per-merchant quotas. `docs/FAIR_USE_POLICY.md` already anticipates
this, and ETH402 explicitly does not claim Sybil resistance.

### 10. Worker leasing: an explicit lease

Confirmation workers claim rows with `claimed_by` and `claimed_until` columns,
take the lease, release the database transaction, and only then perform RPC
work.

`FOR UPDATE SKIP LOCKED` was rejected: it holds a transaction open across each
RPC call, so with `ETH402_DATABASE_MAX_CONNS` at 10 and `ETH402_RPC_TIMEOUT` at
5s, a handful of slow providers can starve the pool and take HTTP request
handling down with it. Leases cost more code and require stale-lease reclaim for
workers that die mid-lease.

### 11. Expiry margin

Settlement refuses to broadcast when `valid_before` falls inside a configured
margin, defaulting conservatively (60s). EIP-3009 enforces `validBefore`
on-chain, so a late transaction reverts and the operator pays gas for a
predictably doomed transaction. The official verifier already applies a 6s
margin, so this extends existing precedent rather than inventing policy;
configurable because the safe window widens during gas spikes.

A payment that expires before broadcast becomes `expired`. One already
broadcast is left to its receipt, which yields `reverted` — the chain decides,
not the application.

## Consequences

Settlement becomes the first component that can lose money through a bug rather
than merely return a wrong answer. The controls above are database invariants or
fail-closed configuration wherever possible, chosen over application-level
discipline for that reason.

New surface this implies, none of which exists yet:

- migrations for `signer_accounts` and the worker lease columns
- `ETH402_SIGNER_ADDRESS` (or KMS key resolution) pinned at startup
- `ETH402_SETTLEMENT_EXPIRY_MARGIN`, a signing timeout, and a per-merchant
  settlement quota
- extended `signer.Transaction` per decision 7

These configuration keys are deliberately **not** added ahead of the code that
reads them, so that no documented option silently does nothing.
