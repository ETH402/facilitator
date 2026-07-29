# Runbooks

What to do about the specific states settlement actually produces.
[Incident response](INCIDENT_RESPONSE.md) covers the general procedure; this covers
the concrete cases, with queries that run as written.

The governing rule, from [ADR-0004](decisions/0004-settlement-execution-model.md)
decision 4: **never resolve an ambiguous broadcast by sending another transaction
without reconciling by hash and nonce first.** Everything below is written to keep
that true.

## First: what is stuck, and why

```sql
SELECT p.payment_identity, p.state, p.claimed_by, p.claimed_until,
       t.status AS tx_status, t.transaction_nonce, t.tx_hash,
       t.raw_transaction_hash, t.sighash, t.broadcast_attempted_at
FROM payment_records p
LEFT JOIN ethereum_transactions t ON t.payment_id = p.id
WHERE p.state NOT IN ('confirmed', 'failed', 'reverted', 'verification_failed')
ORDER BY p.updated_at;
```

Terminal states are `confirmed`, `failed`, `reverted`, `verification_failed`, and
`expired`. Anything else should be moving; if it is not, find it below.

Before concluding a payment is stuck, check the workers are alive — a stalled
worker looks exactly like every payment being stuck:

```
eth402_worker_last_tick_timestamp_seconds{worker="broadcast"}
```

## Payment in `manual_review`

The broadcast outcome was unknown, so the transaction is `ambiguous` and recovery
would not guess. This is the case that must never be "fixed" by resending.

1. Get `raw_transaction_hash`. The keccak of a signed transaction *is* its
   transaction hash, so that value is the lookup key:

   ```sh
   cast tx 0x<raw_transaction_hash> --rpc-url "$RPC"
   cast receipt 0x<raw_transaction_hash> --rpc-url "$RPC"
   ```

2. **Found on chain, mined.** The transaction happened. The recovery worker
   re-attaches the hash and returns the payment to the broadcast pipeline on its
   own; no action needed beyond confirming it did.
3. **Found in the mempool, not mined.** Also fine — wait, or let the stuck-broadcast
   path fee-bump it after `ETH402_SETTLEMENT_REPLACEMENT_AFTER`.
4. **Not found at all.** After `ETH402_SETTLEMENT_RECOVERY_GRACE` recovery re-signs
   the identical transaction, proving identity by the stored `sighash` rather than
   the raw hash — Cloud KMS randomizes the ECDSA nonce, so a re-signature
   legitimately differs in bytes while committing to the same transaction. If
   `sighash` is null the row predates migration `000006` and only on-chain lookup
   can resolve it; escalate rather than re-signing blind.
5. **`sighash` present but recovery keeps refusing.** The stored fee fields
   (`max_fee_per_gas`, `max_priority_fee_per_gas`, `gas_limit`) are missing or the
   record is inconsistent. Do not broadcast anything. Reconcile the nonce on chain
   first:

   ```sh
   cast nonce 0x<signer_address> --rpc-url "$RPC"
   ```

   If the account nonce is already past this transaction's nonce, some transaction
   consumed it — find which, and settle the record to match reality.

## Nonce gap blocking later settlements

Ethereum mines an account's transactions in nonce order, so one unused nonce stops
everything behind it.

```sql
SELECT transaction_nonce, status, tx_hash
FROM ethereum_transactions
WHERE signer_address = lower('0x<signer>')
ORDER BY transaction_nonce DESC LIMIT 20;
```

Compare the lowest non-terminal nonce against `cast nonce`. A `dropped` nonce with
later work in flight is filled automatically: recovery re-broadcasts the original
expired intent, whose revert consumes the nonce — that is the intent, not a
malfunction. A `dropped` nonce with nothing behind it is left alone deliberately;
filling it would spend gas for no reason.

If the gap is *not* being filled, check the recovery worker is ticking, and that
`ETH402_SIGNER_ADDRESS`-derived signer matches the `signer_address` on those rows —
a signer change mid-flight orphans the sequence.

## Gap filler succeeded — accounting divergence

Logged as `gap filler succeeded on an expired authorization`. The chain accepted an
authorization the facilitator judged expired, so **USDC moved while the payment is
recorded `expired`**, and the payment is escalated to `manual_review`.

This is a real divergence between the ledger and the record. It needs a person:

1. Confirm the transfer on chain (`cast receipt`) and read the `Transfer` log for
   the actual amount and recipient.
2. Decide whether the merchant was paid correctly. The authorization was valid when
   the buyer signed it; only the facilitator's clock or margin judged it expired.
3. Settle the record to match the chain. Never leave the divergence unresolved
   because the payment looks terminal.

Recurrence suggests clock skew between this service and the chain, or an
`ETH402_SETTLEMENT_EXPIRY_MARGIN` too tight for observed block times.

## Signer balance depleted

Broadcasts fail while intents remain committed, so nonces are allocated and unused
— the gap case above, at scale.

1. Top up from the funding source. Do not change the signer address: the nonce
   sequence in `signer_accounts` belongs to that address, and switching orphans
   every in-flight transaction.
2. The broadcast worker retries committed intents on its own once gas is affordable.
3. Check whether depletion was legitimate volume or a drain: compare
   `deriv(eth402_signer_balance_wei[15m])` against settlement volume over the same
   window. A compromised process cannot sign arbitrary transactions — the allowlist
   restricts it to `transferWithAuthorization` on USDC, structurally so under
   `ETH402_SIGNER_MODE=policy` — but it can settle payments of its choosing until
   the balance is gone. Refusals logged by the boundary as `refused to sign` are
   the signal to look at: they are either a caller bug or the first visible sign
   of a compromised caller.

## Worker stalled

`eth402_worker_last_tick_timestamp_seconds` stops advancing for one worker. The
heartbeat is written *after* a tick completes, so a wedged tick stops beating while
a panicking one keeps beating — the loop survived that.

1. Check logs for `settlement worker panic recovered`, which names the worker and
   stage.
2. Look for leases that outlived their holder:

   ```sql
   SELECT payment_identity, claimed_by, claimed_until
   FROM payment_records
   WHERE claimed_by IS NOT NULL AND claimed_until < now();
   ```

   Expired leases are reclaimed automatically; a pile of them means workers are
   dying mid-task rather than merely slow.
3. Restarting is safe. Every settlement step commits before acting, so a restart
   resumes from durable state rather than losing or repeating work.

## Payment in `broadcasting` with no signed transaction

The intent committed but signing never completed — a signer timeout or outage. The
nonce is reserved and nothing was sent.

Nothing needs doing: the broadcast worker retries, and because the nonce is already
fixed and stored, re-signing is safe rather than a fresh spend. If the signer stays
unavailable, the authorization eventually falls inside the expiry margin and the
intent is retired as `expired` with its transaction `dropped` — which then becomes
the nonce-gap case, resolved automatically only if later nonces are queued.

## Halting settlement

There is no runtime kill switch. To stop settling while leaving verification up,
set `ETH402_SIGNER_MODE=disabled` and restart: `/settle` then reports
`settlement_unavailable` and no worker starts. Committed intents stay durable and
resume when the signer is re-enabled.
