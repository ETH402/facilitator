# PostgreSQL production roles

Production uses three PostgreSQL roles with different trust boundaries:

- `eth402_owner` owns the database and `public` schema, cannot log in, and is
  used only by an administrator for provisioning and recovery.
- `eth402_migration` owns tables created by `cmd/migrate`, may create objects in
  the application schema, and is used only during a deployment migration step.
- `eth402_runtime` is the application's role. It has no DDL, role-management,
  trigger, truncate, or broad default privileges.

[`bootstrap-roles.sql`](../deploy/postgres/bootstrap-roles.sql) creates the
roles as `NOLOGIN`, removes the default public database/schema privileges, and
establishes the schema boundary in one transaction. Run it once as the cluster
administrator while connected to a newly created `eth402` database:

```sh
psql --set=ON_ERROR_STOP=1 --dbname=eth402 \
  --file=deploy/postgres/bootstrap-roles.sql
```

Then enable login and attach passwords through the database provider's secret
channel, or grant the roles to provider-managed identities. Do not pass a
password on a command line. The migration connection must act as
`eth402_migration` itself so newly created objects are owned by that role.

For every release, run the immutable image's migration binary with the migration
connection, then run
[`grant-runtime.sql`](../deploy/postgres/grant-runtime.sql) with the same role:

```sh
docker compose --env-file /opt/eth402/prod/compose.env \
  -f deploy/compose.production.yaml run --rm migrate up
psql "$ETH402_MIGRATION_DATABASE_URL" \
  --set=ON_ERROR_STOP=1 --file=deploy/postgres/grant-runtime.sql
```

The transactional grants file starts by revoking every runtime privilege on the
explicit ETH402 table and sequence list, then restores only the reviewed DML.
It does not use schema-wide `ALL TABLES` or `ALL SEQUENCES`, so an unrelated
object in `public` cannot make the rollout fail or be modified. This makes it
authoritative and removes stale ETH402 grants from older releases. It grants
deletion only for bounded-retention tables; payment, transaction, outbox,
merchant, and append-only records cannot be deleted by the service. Append-only
facts also receive no update privilege.

The migration environment file contains only `ETH402_ENV=production` and the
TLS-verified `ETH402_DATABASE_URL` for `eth402_migration`. The migration command
deliberately does not load normal application configuration, so it never needs
RPC, SMTP, policy-signer, operator, API-key-pepper, or outbox-encryption secrets.

For an existing installation where the former all-purpose application role owns
the tables, use this order:

1. Stop the application and all workers, take an off-host backup, and record the
   current schema version.
2. Run `bootstrap-roles.sql` as the database administrator.
3. Run
   [`adopt-existing-schema.sql`](../deploy/postgres/adopt-existing-schema.sql)
   as the database administrator. It is one transaction and accepts either the
   live `000012` schema or the post-upgrade `000013` schema. At `000012`, the
   not-yet-created outbox is intentionally skipped.
4. Run the new image's `migrate up` as `eth402_migration`; migration `000013`
   then creates the outbox under the correct owner.
5. Apply `grant-runtime.sql` as `eth402_migration`, audit privileges and object
   ownership, then start the application with the runtime URL.

The adoption is an explicit list of ETH402 tables, sequences, and the
append-only trigger function; it deliberately avoids `REASSIGN OWNED`, which
could capture unrelated objects. Any schema version other than `000012` or
`000013`, or any missing expected object, aborts and rolls back the adoption.
All three provisioning scripts assert `current_database() = 'eth402'`; a
connection pointed at another database fails before changing it.

Run the disposable upgrade-path integration test with:

```sh
deploy/postgres/integration-test.sh
```

It provisions PostgreSQL 17 through `000012` under a legacy owner, bootstraps
and adopts the roles, runs `000013` as migration, applies runtime grants, and
checks representative allowed and denied operations plus final ownership.

There are intentionally no runtime default privileges. A schema migration that
adds a table must update `grant-runtime.sql`; the repository test fails if it
does not. Apply the grants after migration and before starting the new binary.

To audit the live role, connect as the database administrator and inspect:

```sql
SELECT table_name, privilege_type
FROM information_schema.role_table_grants
WHERE grantee = 'eth402_runtime'
ORDER BY table_name, privilege_type;

SELECT sequence_name, privilege_type
FROM information_schema.role_usage_grants
WHERE grantee = 'eth402_runtime'
ORDER BY sequence_name, privilege_type;
```

Backups and restore drills use a separate infrastructure identity. Neither the
runtime nor migration role should have replication, database creation, role
creation, superuser, or row-security-bypass attributes.
