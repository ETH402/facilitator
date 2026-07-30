-- Backoff state for ambiguous-broadcast resolution (ADR-0004 decision 4).
--
-- Resolving an ambiguous broadcast past the grace window re-signs the
-- identical transaction, and every re-sign is a paid Cloud KMS operation. A
-- transaction whose re-broadcast keeps failing would otherwise be re-signed on
-- every worker tick. The counter drives an exponential backoff measured from
-- updated_at — both stamped by the database clock — so retries slow down
-- instead of spending continuously.
ALTER TABLE ethereum_transactions
    ADD COLUMN ambiguous_attempts integer NOT NULL DEFAULT 0
        CHECK (ambiguous_attempts >= 0);
