DROP INDEX IF EXISTS payment_records_lease_idx;
ALTER TABLE payment_records
    DROP CONSTRAINT IF EXISTS payment_records_lease_paired,
    DROP COLUMN IF EXISTS claimed_until,
    DROP COLUMN IF EXISTS claimed_by;
DROP TABLE IF EXISTS signer_accounts;
