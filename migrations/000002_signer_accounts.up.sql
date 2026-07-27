-- Durable Ethereum nonce allocation for the settlement signer (ADR-0004).
--
-- One row per signer address. next_nonce is the value the next settlement will
-- claim; allocation increments it inside the caller's transaction so that a
-- nonce cannot be consumed without durably recording the transaction that owns
-- it. The chain is consulted only to seed this row and to reconcile during
-- recovery, never as the live allocator: two concurrent reads of
-- eth_getTransactionCount return the same value and produce two transactions
-- sharing a nonce, one of which is silently dropped.
CREATE TABLE signer_accounts (
    signer_address char(42) PRIMARY KEY,
    next_nonce numeric(78,0) NOT NULL CHECK (next_nonce >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT signer_accounts_address_normalized
        CHECK (signer_address = lower(signer_address))
);

-- Confirmation and recovery workers claim payments with an explicit lease rather
-- than holding a transaction open across RPC calls, which would let a slow
-- provider starve the connection pool (ADR-0004 decision 10).
ALTER TABLE payment_records
    ADD COLUMN claimed_by text,
    ADD COLUMN claimed_until timestamptz,
    ADD CONSTRAINT payment_records_lease_paired
        CHECK ((claimed_by IS NULL) = (claimed_until IS NULL));

-- Partial index so the worker's claim scan only walks currently-leased rows.
CREATE INDEX payment_records_lease_idx
    ON payment_records (claimed_until)
    WHERE claimed_by IS NOT NULL;
