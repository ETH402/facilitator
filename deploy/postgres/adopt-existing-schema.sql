\set ON_ERROR_STOP on

-- Run as the database administrator only when converting an existing ETH402
-- database whose application role owns the schema objects. Fresh databases
-- migrated as eth402_migration do not need this step. Explicit objects avoid a
-- broad REASSIGN OWNED operation that might capture unrelated database state.
-- Versions 000012 and 000013 are accepted so the live 000012 schema can be
-- adopted before migration 000013 creates the outbox under the migration role.
BEGIN;

DO $$
DECLARE
    current_version text;
BEGIN
    IF current_database() <> 'eth402' THEN
        RAISE EXCEPTION 'schema adoption must run in database eth402, found %', current_database();
    END IF;
    SELECT max(version) INTO current_version FROM schema_migrations;
    IF current_version NOT IN ('000012_public_merchant_profiles', '000013_email_delivery_outbox') THEN
        RAISE EXCEPTION 'schema adoption requires version 000012 or 000013, found %', current_version;
    END IF;
END
$$;

ALTER TABLE schema_migrations OWNER TO eth402_migration;
ALTER TABLE merchants OWNER TO eth402_migration;
ALTER TABLE email_verification_tokens OWNER TO eth402_migration;
ALTER TABLE wallet_verification_challenges OWNER TO eth402_migration;
ALTER TABLE api_keys OWNER TO eth402_migration;
ALTER TABLE recipient_address_history OWNER TO eth402_migration;
ALTER TABLE payment_records OWNER TO eth402_migration;
ALTER TABLE verification_attempts OWNER TO eth402_migration;
ALTER TABLE settlement_attempts OWNER TO eth402_migration;
ALTER TABLE payment_transitions OWNER TO eth402_migration;
ALTER TABLE ethereum_transactions OWNER TO eth402_migration;
ALTER TABLE audit_events OWNER TO eth402_migration;
ALTER TABLE merchant_suspensions OWNER TO eth402_migration;
ALTER TABLE signer_accounts OWNER TO eth402_migration;
ALTER TABLE merchant_usage OWNER TO eth402_migration;
ALTER TABLE merchant_admin_sessions OWNER TO eth402_migration;

-- This table exists only after 000013. \gexec leaves the pre-000013 adoption
-- path valid without hiding any other missing-object failure.
SELECT 'ALTER TABLE email_delivery_outbox OWNER TO eth402_migration'
WHERE to_regclass('public.email_delivery_outbox') IS NOT NULL
\gexec

ALTER SEQUENCE recipient_address_history_id_seq OWNER TO eth402_migration;
ALTER SEQUENCE verification_attempts_id_seq OWNER TO eth402_migration;
ALTER SEQUENCE settlement_attempts_id_seq OWNER TO eth402_migration;
ALTER SEQUENCE payment_transitions_id_seq OWNER TO eth402_migration;
ALTER SEQUENCE audit_events_id_seq OWNER TO eth402_migration;

ALTER FUNCTION reject_append_only_mutation() OWNER TO eth402_migration;

COMMIT;
