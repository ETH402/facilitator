-- Index supporting the per-merchant settlement quota (ADR-0004 decision 9).
--
-- Admission counts a merchant's settlement intents inside a rolling window on
-- every /settle call, so the count must not scan the merchant's whole payment
-- history. settlement_requested_at is set exactly when an intent commits, which
-- makes it the count of broadcasts attempted on that merchant's behalf —
-- replacements and gap fillers reuse existing rows and correctly do not count.
CREATE INDEX payment_records_merchant_settlement_idx
    ON payment_records (merchant_id, settlement_requested_at DESC)
    WHERE settlement_requested_at IS NOT NULL;
