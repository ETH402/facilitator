\set ON_ERROR_STOP on

-- Run once as the PostgreSQL cluster administrator while connected to the
-- eth402 database. These are deliberately NOLOGIN roles: enable LOGIN (or grant
-- them to provider-managed login identities) and attach credentials outside of
-- this repository. Keeping passwords out of psql arguments also keeps them out
-- of shell history and process listings.
BEGIN;

DO $$
BEGIN
    IF current_database() <> 'eth402' THEN
        RAISE EXCEPTION 'role bootstrap must run in database eth402, found %', current_database();
    END IF;
END
$$;

SELECT format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', role_name)
FROM (VALUES ('eth402_owner'), ('eth402_migration'), ('eth402_runtime')) AS roles(role_name)
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = role_name)
\gexec

ALTER ROLE eth402_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE eth402_migration NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE eth402_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;

ALTER DATABASE eth402 OWNER TO eth402_owner;
REVOKE ALL ON DATABASE eth402 FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE eth402 TO eth402_migration;
GRANT CONNECT ON DATABASE eth402 TO eth402_runtime;

ALTER SCHEMA public OWNER TO eth402_owner;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM eth402_runtime;
GRANT USAGE, CREATE ON SCHEMA public TO eth402_migration;
GRANT USAGE ON SCHEMA public TO eth402_runtime;

-- Do not add runtime default privileges here. Every new table must receive an
-- explicit, reviewed grant in grant-runtime.sql; its test fails when a migration
-- adds a table without doing so.
COMMIT;
