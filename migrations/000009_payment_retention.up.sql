-- Privacy-preserving payment tombstones.
--
-- Terminal payments retain their irreversible identity, state, integer amount,
-- and public transaction hash so aggregate statistics and /settle idempotency
-- survive retention. The fields that reproduce or directly associate the payer
-- authorization become nullable and are cleared by the retention worker.
ALTER TABLE payment_records
    ALTER COLUMN payer_address DROP NOT NULL,
    ALTER COLUMN recipient_address DROP NOT NULL,
    ALTER COLUMN authorization_nonce DROP NOT NULL,
    ALTER COLUMN valid_after DROP NOT NULL,
    ALTER COLUMN valid_before DROP NOT NULL,
    ALTER COLUMN payload_hash DROP NOT NULL,
    ADD COLUMN redacted_at timestamptz,
    ADD CONSTRAINT payment_records_redaction_complete CHECK (
        redacted_at IS NULL OR (
            merchant_id IS NULL
            AND payer_address IS NULL
            AND recipient_address IS NULL
            AND authorization_nonce IS NULL
            AND valid_after IS NULL
            AND valid_before IS NULL
            AND payload_hash IS NULL
            AND payer_signature IS NULL
            AND claimed_by IS NULL
            AND claimed_until IS NULL
        )
    );

CREATE INDEX payment_records_retention_idx
    ON payment_records (updated_at, valid_before)
    WHERE redacted_at IS NULL
      AND state IN ('confirmed','reverted','failed','expired','verification_failed');
