-- Settlement recovery prerequisites (ADR-0004).
--
-- Fee values are persisted so recovery can re-sign the *identical*
-- transaction after an ambiguous broadcast: decision 4 forbids a fresh nonce,
-- so the only safe reconstruction is byte-for-byte equality with what may
-- already be on chain, verified by comparing the recomputed
-- raw_transaction_hash. Nullable because rows written before this migration
-- lack them; those rows are recovered by on-chain lookup only.
ALTER TABLE ethereum_transactions
    ADD COLUMN max_fee_per_gas numeric(78,0) CHECK (max_fee_per_gas IS NULL OR max_fee_per_gas > 0),
    ADD COLUMN max_priority_fee_per_gas numeric(78,0) CHECK (max_priority_fee_per_gas IS NULL OR max_priority_fee_per_gas >= 0);

-- Replacements reuse the original nonce by design (decision 1 forbids
-- allocating a fresh one) and link through replaced_by_id, so two rows for one
-- payment legitimately share a nonce. The invariant that actually matters —
-- at most one in-flight transaction per payment — stays enforced by the
-- ethereum_transactions_active_payment_unique partial index.
ALTER TABLE ethereum_transactions
    DROP CONSTRAINT ethereum_transactions_payment_id_transaction_nonce_key;
