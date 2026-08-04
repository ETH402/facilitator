ALTER TABLE merchants
    ADD COLUMN stats_opted_in_at timestamptz;

ALTER TABLE wallet_verification_challenges
    DROP CONSTRAINT wallet_verification_challenges_action_check,
    ADD CONSTRAINT wallet_verification_challenges_action_check
        CHECK (action IN ('verify_recipient','change_recipient','authenticate_admin'));

CREATE TABLE merchant_admin_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    wallet_verified_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    CHECK (wallet_verified_at IS NULL OR wallet_verified_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX merchant_admin_sessions_active_idx
    ON merchant_admin_sessions (merchant_id, expires_at DESC)
    WHERE revoked_at IS NULL;
