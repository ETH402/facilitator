DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_records WHERE redacted_at IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back payment retention after rows have been redacted';
    END IF;
END;
$$;

DROP INDEX IF EXISTS payment_records_retention_idx;
ALTER TABLE payment_records
    DROP CONSTRAINT IF EXISTS payment_records_redaction_complete,
    DROP COLUMN IF EXISTS redacted_at,
    ALTER COLUMN payer_address SET NOT NULL,
    ALTER COLUMN recipient_address SET NOT NULL,
    ALTER COLUMN authorization_nonce SET NOT NULL,
    ALTER COLUMN valid_after SET NOT NULL,
    ALTER COLUMN valid_before SET NOT NULL,
    ALTER COLUMN payload_hash SET NOT NULL;
