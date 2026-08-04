ALTER TABLE email_verification_tokens
    ALTER COLUMN sent_at DROP NOT NULL,
    ALTER COLUMN sent_at DROP DEFAULT;

CREATE TABLE email_delivery_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    token_id uuid NOT NULL UNIQUE REFERENCES email_verification_tokens(id) ON DELETE CASCADE,
    message_kind text NOT NULL CHECK (message_kind IN ('registration','admin_login')),
    token_ciphertext bytea,
    request_id text,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    claimed_until timestamptz,
    claim_token uuid,
    delivered_at timestamptz,
    abandoned_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (token_ciphertext IS NOT NULL OR delivered_at IS NOT NULL OR abandoned_at IS NOT NULL),
    CHECK ((claimed_until IS NULL) = (claim_token IS NULL)),
    CHECK (delivered_at IS NULL OR abandoned_at IS NULL),
    CHECK ((delivered_at IS NULL AND abandoned_at IS NULL) OR claim_token IS NULL),
    CHECK (delivered_at IS NULL OR delivered_at >= created_at),
    CHECK (abandoned_at IS NULL OR abandoned_at >= created_at)
);

CREATE INDEX email_delivery_outbox_pending_idx
    ON email_delivery_outbox (next_attempt_at, created_at)
    WHERE delivered_at IS NULL AND abandoned_at IS NULL;

CREATE INDEX email_tokens_merchant_sent_idx
    ON email_verification_tokens (merchant_id, sent_at DESC)
    WHERE sent_at IS NOT NULL;
