DROP INDEX IF EXISTS email_tokens_merchant_sent_idx;

-- The older application cannot decrypt or retry pending outbox rows. Delete
-- their unusable tokens instead of fabricating sent_at and imposing a false
-- cooldown for mail that may never have left the provider boundary.
DELETE FROM email_verification_tokens
WHERE sent_at IS NULL;

DROP TABLE IF EXISTS email_delivery_outbox;

ALTER TABLE email_verification_tokens
    ALTER COLUMN sent_at SET DEFAULT now(),
    ALTER COLUMN sent_at SET NOT NULL;
