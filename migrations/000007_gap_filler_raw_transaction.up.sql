-- Gap fillers are signed after the original intent has been retired. Persist
-- the exact signed bytes before broadcast so an ambiguous RPC outcome can be
-- retried byte-for-byte even when Cloud KMS produces randomized ECDSA
-- signatures.
ALTER TABLE ethereum_transactions
    ADD COLUMN raw_transaction bytea;
