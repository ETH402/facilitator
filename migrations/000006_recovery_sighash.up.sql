-- Deterministic sighash for settlement recovery (ADR-0004 decision 4).
--
-- raw_transaction_hash identifies one particular signing of a transaction, but
-- Cloud KMS randomizes the ECDSA nonce, so re-signing the identical transaction
-- produces different bytes and a different hash. The sighash — keccak of the
-- unsigned EIP-1559 payload — is fully determined by the stored nonce, gas,
-- fees, and calldata, so it is stable across signers and across signatures.
-- Recovery proves a re-signed transaction is the recorded one by comparing
-- sighashes, then records the fresh signature's hash. Nullable because rows
-- written before this migration lack it; those rows are recovered by raw-hash
-- comparison (deterministic signers) or on-chain lookup only.
ALTER TABLE ethereum_transactions
    ADD COLUMN sighash char(64),
    ADD CONSTRAINT ethereum_transactions_sighash_shape
        CHECK (sighash IS NULL OR sighash ~ '^[0-9a-f]{64}$');
