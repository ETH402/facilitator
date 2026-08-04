\set ON_ERROR_STOP on

-- Run as eth402_migration after every successful `migrate up`. Explicit
-- revocation makes this authoritative without touching unrelated objects that
-- an operator may have placed in public.
BEGIN;

DO $$
DECLARE
    current_version text;
BEGIN
    IF current_database() <> 'eth402' THEN
        RAISE EXCEPTION 'runtime grants must run in database eth402, found %', current_database();
    END IF;
    SELECT max(version) INTO current_version FROM schema_migrations;
    IF current_version <> '000013_email_delivery_outbox' THEN
        RAISE EXCEPTION 'runtime grants require version 000013, found %', current_version;
    END IF;
END
$$;

REVOKE ALL PRIVILEGES ON TABLE
    schema_migrations,
    merchants,
    email_verification_tokens,
    wallet_verification_challenges,
    api_keys,
    recipient_address_history,
    payment_records,
    verification_attempts,
    settlement_attempts,
    payment_transitions,
    ethereum_transactions,
    audit_events,
    merchant_suspensions,
    signer_accounts,
    merchant_usage,
    merchant_admin_sessions,
    email_delivery_outbox
FROM eth402_runtime;

REVOKE ALL PRIVILEGES ON SEQUENCE
    recipient_address_history_id_seq,
    verification_attempts_id_seq,
    settlement_attempts_id_seq,
    payment_transitions_id_seq,
    audit_events_id_seq
FROM eth402_runtime;

GRANT SELECT ON TABLE schema_migrations TO eth402_runtime;

GRANT SELECT, INSERT, UPDATE ON TABLE
    merchants,
    email_verification_tokens,
    wallet_verification_challenges,
    api_keys,
    payment_records,
    ethereum_transactions,
    merchant_suspensions,
    signer_accounts,
    merchant_usage,
    merchant_admin_sessions,
    email_delivery_outbox
TO eth402_runtime;

GRANT DELETE ON TABLE
    email_verification_tokens,
    wallet_verification_challenges,
    api_keys,
    merchant_usage,
    merchant_admin_sessions
TO eth402_runtime;

-- These are append-only application records. Runtime can read the records used
-- by normal queries and insert new facts, but cannot update or delete any of
-- them. Database triggers remain a second line of defence.
GRANT SELECT, INSERT ON TABLE
    recipient_address_history,
    verification_attempts,
    settlement_attempts,
    payment_transitions
TO eth402_runtime;

GRANT INSERT ON TABLE audit_events TO eth402_runtime;

GRANT USAGE ON SEQUENCE
    recipient_address_history_id_seq,
    verification_attempts_id_seq,
    settlement_attempts_id_seq,
    payment_transitions_id_seq,
    audit_events_id_seq
TO eth402_runtime;

COMMIT;
