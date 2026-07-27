CREATE TABLE merchants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    business_email text NOT NULL,
    email_domain text NOT NULL,
    website text,
    description text,
    recipient_address char(42) NOT NULL,
    terms_version text NOT NULL,
    terms_accepted_at timestamptz NOT NULL,
    email_verified_at timestamptz,
    wallet_verified_at timestamptz,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','manual_review','active','suspended','rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT merchants_email_normalized CHECK (business_email = lower(business_email)),
    CONSTRAINT merchants_domain_normalized CHECK (email_domain = lower(email_domain)),
    CONSTRAINT merchants_recipient_normalized CHECK (recipient_address = lower(recipient_address))
);
CREATE UNIQUE INDEX merchants_business_email_active_unique
    ON merchants (business_email) WHERE status <> 'rejected';
CREATE INDEX merchants_status_idx ON merchants (status);
CREATE INDEX merchants_recipient_idx ON merchants (recipient_address);

CREATE TABLE email_verification_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    sent_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX email_tokens_merchant_created_idx
    ON email_verification_tokens (merchant_id, created_at DESC);

CREATE TABLE wallet_verification_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    address char(42) NOT NULL,
    nonce char(64) NOT NULL UNIQUE,
    message_hash char(64) NOT NULL UNIQUE,
    action text NOT NULL CHECK (action IN ('verify_recipient','change_recipient')),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (address = lower(address)),
    CHECK (expires_at > issued_at),
    CHECK (consumed_at IS NULL OR consumed_at >= issued_at)
);
CREATE INDEX wallet_challenges_merchant_created_idx
    ON wallet_verification_challenges (merchant_id, created_at DESC);

CREATE TABLE api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    key_prefix text NOT NULL UNIQUE,
    key_hash char(64) NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);
CREATE INDEX api_keys_merchant_active_idx
    ON api_keys (merchant_id, created_at DESC) WHERE revoked_at IS NULL;

CREATE TABLE recipient_address_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    previous_address char(42),
    new_address char(42) NOT NULL,
    requested_at timestamptz NOT NULL,
    verified_at timestamptz NOT NULL,
    actor_type text NOT NULL CHECK (actor_type IN ('merchant','operator','system')),
    actor_id text NOT NULL,
    wallet_challenge_id uuid REFERENCES wallet_verification_challenges(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (previous_address IS NULL OR previous_address = lower(previous_address)),
    CHECK (new_address = lower(new_address)),
    CHECK (verified_at >= requested_at)
);
CREATE INDEX recipient_history_merchant_idx
    ON recipient_address_history (merchant_id, created_at DESC);

CREATE TABLE payment_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_identity char(68) NOT NULL UNIQUE,
    merchant_id uuid REFERENCES merchants(id) ON DELETE RESTRICT,
    x402_version smallint NOT NULL CHECK (x402_version = 2),
    scheme text NOT NULL CHECK (scheme = 'exact'),
    network text NOT NULL CHECK (network = 'eip155:1'),
    asset char(42) NOT NULL,
    payer_address char(42) NOT NULL,
    recipient_address char(42) NOT NULL,
    amount_atomic numeric(78,0) NOT NULL CHECK (amount_atomic > 0),
    authorization_nonce char(66) NOT NULL,
    valid_after timestamptz NOT NULL,
    valid_before timestamptz NOT NULL,
    payload_hash char(64) NOT NULL,
    verification_status text NOT NULL DEFAULT 'pending'
        CHECK (verification_status IN ('pending','verified','failed')),
    state text NOT NULL DEFAULT 'received'
        CHECK (state IN ('received','verification_failed','verified','broadcasting','broadcast','confirming','confirmed','failed','reverted','replaced','expired','manual_review')),
    settlement_requested_at timestamptz,
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (asset = lower(asset)),
    CHECK (payer_address = lower(payer_address)),
    CHECK (recipient_address = lower(recipient_address)),
    CHECK (valid_before > valid_after),
    CHECK (confirmed_at IS NULL OR state = 'confirmed'),
    UNIQUE (network, asset, payer_address, authorization_nonce)
);
CREATE INDEX payment_records_state_idx ON payment_records (state, updated_at);
CREATE INDEX payment_records_merchant_idx ON payment_records (merchant_id, created_at DESC);
CREATE INDEX payment_records_confirmed_idx
    ON payment_records (confirmed_at DESC) WHERE state = 'confirmed';

CREATE TABLE verification_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id uuid REFERENCES payment_records(id) ON DELETE RESTRICT,
    payment_identity char(68),
    result text NOT NULL CHECK (result IN ('verified','failed')),
    reason_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX verification_attempts_created_idx
    ON verification_attempts (created_at DESC);
CREATE INDEX verification_attempts_payment_idx
    ON verification_attempts (payment_id, created_at DESC);

CREATE TABLE settlement_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id uuid REFERENCES payment_records(id) ON DELETE RESTRICT,
    payment_identity char(68),
    result text NOT NULL CHECK (result IN ('accepted','duplicate','rejected')),
    reason_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX settlement_attempts_created_idx
    ON settlement_attempts (created_at DESC);
CREATE INDEX settlement_attempts_payment_idx
    ON settlement_attempts (payment_id, created_at DESC);

CREATE TABLE payment_transitions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id uuid NOT NULL REFERENCES payment_records(id) ON DELETE RESTRICT,
    from_state text,
    to_state text NOT NULL,
    reason_code text,
    actor_type text NOT NULL CHECK (actor_type IN ('http','worker','operator','system')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX payment_transitions_payment_idx
    ON payment_transitions (payment_id, id);

CREATE TABLE ethereum_transactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id uuid NOT NULL REFERENCES payment_records(id) ON DELETE RESTRICT,
    tx_hash char(66) UNIQUE,
    signer_address char(42) NOT NULL,
    transaction_nonce numeric(78,0),
    raw_transaction_hash char(64),
    status text NOT NULL
        CHECK (status IN ('intent','broadcasting','broadcast','confirming','confirmed','reverted','replaced','dropped','ambiguous')),
    block_number bigint CHECK (block_number IS NULL OR block_number >= 0),
    block_hash char(66),
    gas_limit numeric(78,0),
    gas_used numeric(78,0),
    effective_gas_price numeric(78,0),
    replaced_by_id uuid REFERENCES ethereum_transactions(id) ON DELETE RESTRICT,
    broadcast_attempted_at timestamptz,
    first_seen_at timestamptz,
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (payment_id, transaction_nonce)
);
CREATE INDEX ethereum_transactions_status_idx
    ON ethereum_transactions (status, updated_at);
CREATE UNIQUE INDEX ethereum_transactions_active_payment_unique
    ON ethereum_transactions (payment_id)
    WHERE status IN ('intent','broadcasting','broadcast','confirming','ambiguous');

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type text NOT NULL,
    merchant_id uuid REFERENCES merchants(id) ON DELETE RESTRICT,
    payment_id uuid REFERENCES payment_records(id) ON DELETE RESTRICT,
    actor_type text NOT NULL CHECK (actor_type IN ('merchant','operator','system','anonymous')),
    actor_id text,
    request_id text,
    source_ip_hash char(64),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_merchant_idx ON audit_events (merchant_id, id);
CREATE INDEX audit_events_payment_idx ON audit_events (payment_id, id);
CREATE INDEX audit_events_type_created_idx ON audit_events (event_type, created_at DESC);

CREATE TABLE merchant_suspensions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
    reason_code text NOT NULL,
    operator_id text NOT NULL,
    suspended_at timestamptz NOT NULL DEFAULT now(),
    reinstated_at timestamptz,
    reinstated_by text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    CHECK (reinstated_at IS NULL OR reinstated_at >= suspended_at),
    CHECK ((reinstated_at IS NULL) = (reinstated_by IS NULL))
);
CREATE UNIQUE INDEX merchant_suspensions_one_active
    ON merchant_suspensions (merchant_id) WHERE reinstated_at IS NULL;

CREATE OR REPLACE FUNCTION reject_append_only_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER recipient_history_append_only
BEFORE UPDATE OR DELETE ON recipient_address_history
FOR EACH ROW EXECUTE FUNCTION reject_append_only_mutation();

CREATE TRIGGER payment_transitions_append_only
BEFORE UPDATE OR DELETE ON payment_transitions
FOR EACH ROW EXECUTE FUNCTION reject_append_only_mutation();

CREATE TRIGGER verification_attempts_append_only
BEFORE UPDATE OR DELETE ON verification_attempts
FOR EACH ROW EXECUTE FUNCTION reject_append_only_mutation();

CREATE TRIGGER settlement_attempts_append_only
BEFORE UPDATE OR DELETE ON settlement_attempts
FOR EACH ROW EXECUTE FUNCTION reject_append_only_mutation();

CREATE TRIGGER audit_events_append_only
BEFORE UPDATE OR DELETE ON audit_events
FOR EACH ROW EXECUTE FUNCTION reject_append_only_mutation();
