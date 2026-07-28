-- Payer EIP-3009 signature for settlement calldata (ADR-0004).
--
-- transferWithAuthorization calldata needs every authorization field plus the
-- 65-byte ECDSA signature. payment_records already carried the fields but only
-- hashed the payload, so the signature was unrecoverable after /verify
-- returned. It is written atomically with the settlement intent (never at
-- verification time): the payment identity hash binds the exact signature, so
-- the value stored here is the only one that can ever match this row, and the
-- broadcast worker can rebuild calldata after a crash without trusting a
-- caller twice.
ALTER TABLE payment_records
    ADD COLUMN payer_signature char(132),
    ADD CONSTRAINT payment_records_payer_signature_normalized
        CHECK (payer_signature IS NULL OR payer_signature = lower(payer_signature)),
    ADD CONSTRAINT payment_records_payer_signature_shape
        CHECK (payer_signature IS NULL OR payer_signature ~ '^0x[0-9a-f]{130}$');
