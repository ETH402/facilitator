ALTER TABLE merchants
    ADD COLUMN public_profile_opted_in_at timestamptz;

CREATE INDEX merchants_public_profile_idx
    ON merchants (public_profile_opted_in_at DESC)
    WHERE status = 'active' AND public_profile_opted_in_at IS NOT NULL;
