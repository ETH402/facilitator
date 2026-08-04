DROP TABLE merchant_admin_sessions;

DELETE FROM wallet_verification_challenges
WHERE action = 'authenticate_admin';

ALTER TABLE wallet_verification_challenges
    DROP CONSTRAINT wallet_verification_challenges_action_check,
    ADD CONSTRAINT wallet_verification_challenges_action_check
        CHECK (action IN ('verify_recipient','change_recipient'));

ALTER TABLE merchants
    DROP COLUMN stats_opted_in_at;
