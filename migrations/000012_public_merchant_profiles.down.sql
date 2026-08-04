DROP INDEX IF EXISTS merchants_public_profile_idx;

ALTER TABLE merchants
    DROP COLUMN IF EXISTS public_profile_opted_in_at;
