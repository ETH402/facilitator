ALTER TABLE ethereum_transactions
    ADD CONSTRAINT ethereum_transactions_payment_id_transaction_nonce_key
        UNIQUE (payment_id, transaction_nonce);

ALTER TABLE ethereum_transactions
    DROP COLUMN max_priority_fee_per_gas,
    DROP COLUMN max_fee_per_gas;
