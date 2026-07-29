-- Fair-use accounting for authenticated merchant endpoints.
--
-- Keyed by merchant, deliberately not by API key: a merchant that could multiply
-- its allowance by minting keys would not be limited at all.
--
-- Tumbling windows rather than sliding: one row per merchant per window, so
-- enforcement is a single upsert on an already database-bound request, and old
-- rows are trivially prunable. The cost is that a merchant can spend its full
-- allowance at the end of one window and again at the start of the next, so a
-- burst of up to 2× the limit is possible across a boundary. That is acceptable
-- for a fair-use control and is documented in docs/FAIR_USE.md; it would not be
-- acceptable for anything protecting funds, which is why settlement quotas are
-- counted over a true trailing interval instead.
CREATE TABLE merchant_usage (
    merchant_id  uuid        NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    window_start timestamptz NOT NULL,
    requests     bigint      NOT NULL DEFAULT 0 CHECK (requests >= 0),
    PRIMARY KEY (merchant_id, window_start)
);

-- Supports pruning old windows without scanning the merchant dimension.
CREATE INDEX merchant_usage_window_idx ON merchant_usage (window_start);
