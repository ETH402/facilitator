-- Not restoring ethereum_transactions_payment_id_transaction_nonce_key: once
-- this migration has been live, a replaced or gap-filled transaction
-- legitimately shares its nonce with the row it replaced (see the up
-- migration and ADR-0004 decision 1), so re-adding that constraint fails
-- against real data. The invariant that matters -- at most one in-flight
-- transaction per payment -- is unaffected by this migration and stays
-- enforced by the ethereum_transactions_active_payment_unique partial index
-- from migration 000001.
ALTER TABLE ethereum_transactions
    DROP COLUMN max_priority_fee_per_gas,
    DROP COLUMN max_fee_per_gas;
