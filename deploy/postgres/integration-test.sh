#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
container_name="eth402-postgres-roles-$$"
database_password=legacy-test-only
migration_password=migration-test-only
runtime_password=runtime-test-only

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker run -d --name "$container_name" \
  -e POSTGRES_DB=eth402 \
  -e POSTGRES_USER=eth402_legacy \
  -e POSTGRES_PASSWORD="$database_password" \
  -p 127.0.0.1::5432 \
  -v "$repository_root:/workspace:ro" \
  postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193 \
  >/dev/null

ready=false
attempt=0
while [ "$attempt" -lt 30 ]; do
  if docker exec "$container_name" pg_isready -U eth402_legacy -d eth402 >/dev/null 2>&1; then
    ready=true
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
if [ "$ready" != true ]; then
  echo "test PostgreSQL did not become ready" >&2
  exit 1
fi

# A database-name mistake must fail before creating any cluster roles, and the
# surrounding transaction must leave no partial bootstrap behind.
docker exec "$container_name" createdb -U eth402_legacy not_eth402
if docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d not_eth402 \
  -f /workspace/deploy/postgres/bootstrap-roles.sql >/dev/null 2>&1; then
  echo "role bootstrap unexpectedly accepted the wrong database" >&2
  exit 1
fi
bootstrap_role_count=$(docker exec "$container_name" psql -U eth402_legacy -d eth402 -Atc \
  "SELECT count(*) FROM pg_roles WHERE rolname IN ('eth402_owner','eth402_migration','eth402_runtime')")
if [ "$bootstrap_role_count" -ne 0 ]; then
  echo "failed bootstrap left partial roles behind" >&2
  exit 1
fi

# Reproduce the live upgrade boundary: the legacy all-purpose role owns a schema
# migrated only through 000012.
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -c "CREATE TABLE schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())" \
  >/dev/null
for migration_path in "$repository_root"/migrations/*.up.sql; do
  migration_version=${migration_path##*/}
  migration_version=${migration_version%.up.sql}
  if [ "$migration_version" = "000013_email_delivery_outbox" ]; then
    break
  fi
  docker exec "$container_name" psql -v ON_ERROR_STOP=1 -1 \
    -U eth402_legacy -d eth402 -f "/workspace/migrations/${migration_version}.up.sql" >/dev/null
  docker exec "$container_name" psql -v ON_ERROR_STOP=1 \
    -U eth402_legacy -d eth402 \
    -c "INSERT INTO schema_migrations(version) VALUES ('$migration_version')" >/dev/null
done

docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -f /workspace/deploy/postgres/bootstrap-roles.sql >/dev/null

# Force a late missing-object failure after earlier ALTER OWNER statements and
# prove the whole adoption rolls back before trying the valid path.
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -c "ALTER TABLE merchant_admin_sessions RENAME TO merchant_admin_sessions_temporarily_missing" >/dev/null
if docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -f /workspace/deploy/postgres/adopt-existing-schema.sql >/dev/null 2>&1; then
  echo "schema adoption unexpectedly succeeded with a missing expected table" >&2
  exit 1
fi
schema_owner_after_failure=$(docker exec "$container_name" psql -U eth402_legacy -d eth402 -Atc \
  "SELECT tableowner FROM pg_tables WHERE schemaname='public' AND tablename='schema_migrations'")
if [ "$schema_owner_after_failure" != "eth402_legacy" ]; then
  echo "failed schema adoption did not roll back ownership changes" >&2
  exit 1
fi
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -c "ALTER TABLE merchant_admin_sessions_temporarily_missing RENAME TO merchant_admin_sessions" >/dev/null
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -f /workspace/deploy/postgres/adopt-existing-schema.sql >/dev/null
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -c "ALTER ROLE eth402_migration LOGIN PASSWORD '$migration_password'; ALTER ROLE eth402_runtime LOGIN PASSWORD '$runtime_password';" \
  >/dev/null

published_port=$(docker port "$container_name" 5432/tcp)
published_port=${published_port##*:}
(
  cd "$repository_root"
  ETH402_ENV=development \
  ETH402_DATABASE_URL="postgres://eth402_migration:${migration_password}@127.0.0.1:${published_port}/eth402?sslmode=disable" \
    go run ./cmd/migrate up
)

# The adoption script is also safe/idempotent at the post-migration 000013
# version, which supports operators who perform role separation after upgrade.
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 \
  -f /workspace/deploy/postgres/adopt-existing-schema.sql >/dev/null

# An unrelated public object and its existing runtime grant must be untouched by
# the explicit ETH402 revocation list.
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 -c \
  "CREATE TABLE unrelated_operator_table(value integer); GRANT SELECT ON unrelated_operator_table TO eth402_runtime;" \
  >/dev/null

docker exec -e PGPASSWORD="$migration_password" "$container_name" \
  psql -v ON_ERROR_STOP=1 -U eth402_migration -d eth402 \
  -f /workspace/deploy/postgres/grant-runtime.sql >/dev/null

# Seed one row as the administrator, then exercise representative allowed DML
# as runtime, including an identity sequence and a retention DELETE.
docker exec "$container_name" psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 -c \
  "INSERT INTO merchants(id,name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at)
   VALUES ('00000000-0000-0000-0000-000000000001','role test','role-test@example.com','example.com','0x0000000000000000000000000000000000000001','test',now())" \
  >/dev/null
docker exec -e PGPASSWORD="$runtime_password" "$container_name" \
  psql -v ON_ERROR_STOP=1 -U eth402_runtime -d eth402 -c \
  "SELECT version FROM schema_migrations WHERE version='000013_email_delivery_outbox';
   SELECT count(*) FROM unrelated_operator_table;
   UPDATE merchants SET description='runtime update' WHERE id='00000000-0000-0000-0000-000000000001';
   INSERT INTO audit_events(event_type,merchant_id,actor_type) VALUES ('role_test','00000000-0000-0000-0000-000000000001','system');
   INSERT INTO merchant_usage(merchant_id,window_start,requests) VALUES ('00000000-0000-0000-0000-000000000001',date_trunc('hour',now()),1);
   DELETE FROM merchant_usage WHERE merchant_id='00000000-0000-0000-0000-000000000001';" \
  >/dev/null

expect_runtime_denied() {
  denied_sql=$1
  if docker exec -e PGPASSWORD="$runtime_password" "$container_name" \
    psql -v ON_ERROR_STOP=1 -U eth402_runtime -d eth402 -c "$denied_sql" \
    >/dev/null 2>&1; then
    echo "runtime unexpectedly executed: $denied_sql" >&2
    exit 1
  fi
}
expect_runtime_denied "CREATE TABLE runtime_must_not_create(id integer)"
expect_runtime_denied "DELETE FROM payment_records"
expect_runtime_denied "UPDATE audit_events SET event_type='mutated'"

wrong_owner_count=$(docker exec "$container_name" \
  psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 -Atc \
  "SELECT count(*) FROM pg_tables
   WHERE schemaname='public' AND tablename <> 'unrelated_operator_table'
     AND tableowner <> 'eth402_migration'")
if [ "$wrong_owner_count" -ne 0 ]; then
  echo "found $wrong_owner_count application tables not owned by migration role" >&2
  exit 1
fi
final_version=$(docker exec "$container_name" \
  psql -v ON_ERROR_STOP=1 -U eth402_legacy -d eth402 -Atc \
  "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
if [ "$final_version" != "000013_email_delivery_outbox" ]; then
  echo "unexpected final schema version: $final_version" >&2
  exit 1
fi

echo "PostgreSQL role upgrade integration test passed"
