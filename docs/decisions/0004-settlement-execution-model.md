# ADR 0004: Settlement execution model

Status: **proposed** — 2026-07-27. Supersedes nothing. Extends
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

## Decision

### 1. Ethereum nonce allocation

Settlement must assign each transaction a distinct account nonce for a single
signer address, with no gaps and no reuse, across process restarts and
concurrent requests.

**Decided:** allocate from a dedicated durable row, not from the chain.

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

Consequence: the signer address becomes load-bearing configuration. It must be
resolved once at startup and pinned, because `ethereum_transactions.signer_address`
is `NOT NULL` and a mid-flight signer change would corrupt the nonce sequence.
Replacements deliberately reuse the same nonce and so must *not* allocate.

### 2. Commit before sign, sign before broadcast

The order is: verify → allocate nonce and insert intent (`state = broadcasting`,
`ethereum_transactions.status = intent`) → commit → sign → record
`raw_transaction_hash` → broadcast once → record `tx_hash`.

A crash after commit but before broadcast leaves a durable `intent` row that
recovery can resolve. A crash after broadcast but before recording the hash is
the ambiguous case below. Nothing is ever signed without a committed intent,
which is what makes "never blindly retry" enforceable.

### 3. Ambiguous broadcast

An RPC error that leaves broadcast outcome unknown sets
`ethereum_transactions.status = 'ambiguous'` and moves the payment to
`manual_review`.

`payment_records.state` deliberately gets **no** `ambiguous` value. Ambiguity is
a property of a transaction, not of a payment, and `broadcasting → manual_review`
already exists in the state machine. Recovery resolves ambiguity by looking up
`raw_transaction_hash` on chain; it never re-signs and never re-broadcasts with
a fresh nonce.

### 4. Finality and reorgs

`confirmed` is terminal, with **no** `confirmed → confirming` edge. Confirmation
requires `ETH402_REQUIRED_CONFIRMATIONS` (default 12) canonical confirmations,
and a reorg deeper than that is accepted as residual risk — it is already
recorded as "deep reorg" in the threat model.

This is a deliberate finality cut, stated here because
`docs/ARCHITECTURE.md`'s "reorgs return non-final transactions to confirmation"
reads more broadly than the state machine allows: it applies only to
`broadcast`/`confirming`, never to `confirmed`. Reorg handling compares
`block_hash` against the canonical chain and returns *non-final* transactions to
`confirming`.

### 5. Gas policy is mandatory

Implemented ahead of this ADR: `Validate` now rejects any non-disabled
`ETH402_SIGNER_MODE` unless both `ETH402_MAX_FEE_PER_GAS_WEI` and
`ETH402_MAX_GAS_LIMIT` are non-zero, and rejects a priority fee exceeding the
total fee ceiling. Zero means unset, not unlimited, so the signer cannot be
switched on with an unbounded spend ceiling.

### 6. The signer interface must change

`signer.Transaction{ChainID, To, Data, Value}` cannot express a settlement
transaction. It is missing `Nonce`, `GasLimit`, `MaxFeePerGas`, and
`MaxPriorityFeePerGas`. Because ADR-0002 requires the nonce to be persisted
before signing, the nonce is an **input** chosen by the caller, not something
the signer may pick. The interface gains those fields and remains
value-typed so a signer implementation cannot reach back for chain state.

`signer.Disabled` stays the default and `TestSigner` stays deliberately
non-broadcastable.

## Open questions

These need a decision before implementation, and are not settled here:

1. **Signer backend.** ADR-0002 names "KMS/HSM/Vault/external policy signers"
   without choosing. The choice determines whether signing is a local library
   call or a network round trip inside the settlement path, which changes the
   timeout and failure model materially.
2. **`/settle` authentication.** `/verify` is deliberately unauthenticated.
   `/settle` spends operator gas, so leaving it unauthenticated makes gas
   draining trivially cheap, while requiring a merchant API key departs from the
   x402 facilitator shape. This is the single most consequential open question
   in Milestone 3.
3. **Confirmation-worker leasing.** `FOR UPDATE SKIP LOCKED` against
   `payment_records` versus a separate lease column. The former is simpler; the
   latter survives long-running RPC calls without holding a transaction open.
4. **Expiry.** `valid_before` can pass while a transaction is in flight. Whether
   an expired-but-broadcast payment becomes `expired` or `failed` affects the
   public `/stats` counters.

## Consequences

Settlement becomes the first component that can lose money through a bug rather
than merely return a wrong answer. The controls above are all database
invariants or fail-closed configuration, chosen over application-level
discipline for that reason. `/settle` remains absent until every item under
"Open questions" is resolved.
