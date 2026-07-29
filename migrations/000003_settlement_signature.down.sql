ALTER TABLE payment_records
    DROP CONSTRAINT payment_records_payer_signature_shape,
    DROP CONSTRAINT payment_records_payer_signature_normalized,
    DROP COLUMN payer_signature;
