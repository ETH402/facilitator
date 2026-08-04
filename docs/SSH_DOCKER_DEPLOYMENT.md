# SSH and Docker production deployment

This is the checked-in deployment shape for the current non-GCP application
host: Caddy is the only public container, the facilitator runs continuously on
an internal Docker network, PostgreSQL is external and TLS-verified, and the
policy signer is reached through its private HTTPS endpoint. The policy signer
and KMS identity remain a separate workload; this manifest does not collapse
that security boundary onto the application host.

The topology is defined by
[`compose.production.yaml`](../deploy/compose.production.yaml) and
[`Caddyfile.production`](../deploy/Caddyfile.production). It exposes
`eth402.org`, `www.eth402.org`, and `api.eth402.org`, refuses public metrics,
and preserves the product/API origin split. No database, signer, or Prometheus
port is published.
Caddy explicitly overwrites `X-Forwarded-For` with the peer it observed before
proxying. This is required because the application trusts Caddy's private Docker
address; appending an attacker-supplied value would cross that trust boundary.

## Host state

Keep operator state outside the Git checkout, for example under
`/opt/eth402/prod`:

- `compose.env`: only the immutable application image digest and path to the
  application environment file;
- `app.env`: mode `0600`, populated from
  [`production.env.example`](../deploy/production.env.example), containing
  runtime database, RPC, SMTP, policy-signer, and application secrets;
- `migration.env`: mode `0600`, containing only `ETH402_ENV=production` and the
  TLS-verified `ETH402_DATABASE_URL` for the migration role;
- release evidence recording the Git tag, commit, verified image digest, SBOM,
  attestation, reviewed deploy-file checksums, migration result, grant audit,
  and health checks.

The repository and release artifacts contain no populated secret file. Never
let the long-running app read the migration credential, and never give the
migration container the application's unrelated secrets.

## Release sequence

1. Verify the release attestation and image digest as described in
   [Immutable releases](RELEASES.md). Put the exact
   `repository@sha256:<64 hex>` value in `compose.env`; never deploy a tag. Check
   out the exact verified release commit in detached-HEAD state on the host—do
   not download deploy files from a moving branch—and require a clean
   `git status --porcelain` plus `git diff --exit-code`. Record
   `git rev-parse HEAD` and a checksum manifest for every reviewed deployment
   file with `git ls-files -z deploy | sort -z | xargs -0 sha256sum`. Those
   checked-in files, the image digest, and release evidence must all identify
   the same reviewed commit.
2. Validate the manifest, Caddy policy, and redacted application configuration:

   ```sh
   set -a
   . /opt/eth402/prod/compose.env
   set +a
   deploy/validate-production.sh
   ```

3. Stop the old app and let its 60-second grace period drain all workers before
   changing ownership, schema, or grants. Back up PostgreSQL and verify that the
   latest restore drill is current. For an existing all-purpose database, follow
   the exact bootstrap/adopt/migrate/grant order in
   [PostgreSQL production roles](POSTGRESQL_ROLES.md). Run the migration profile
   with the migration-only environment file and apply `grant-runtime.sql` before
   the new application starts. Never overlap an old runtime process with this
   privilege transition.
4. Pull the exact digests, recreate the app, then inspect readiness before
   switching or retaining traffic:

   ```sh
   docker compose --env-file /opt/eth402/prod/compose.env \
     -f deploy/compose.production.yaml pull app caddy
   docker compose --env-file /opt/eth402/prod/compose.env \
     -f deploy/compose.production.yaml up -d app caddy
   docker compose --env-file /opt/eth402/prod/compose.env \
     -f deploy/compose.production.yaml ps
   curl --fail --silent --show-error https://api.eth402.org/health/ready
   ```

5. Record the running image ID from `docker inspect`, the schema version, and
   the runtime grant audit. Keep the previous digest available for rollback,
   but remember that an older binary refuses a newer schema; schema rollback is
   a separate, reviewed operation.

The app has a 60-second container stop grace period, exceeding its 45-second
shutdown window. Caddy persists certificate state in named volumes. Prometheus
is an optional profile and scrapes `app:8080` only on the internal network.

Host firewall policy should allow public TCP 80/443 and UDP 443 only. Restrict
SSH by operator source and key authentication. Database egress, both
authenticated RPC providers, SMTP, and the policy-signer endpoint are the only
application dependencies. Backups must leave this host and use a credential
that neither the app nor migration container can read.

## Restore drill

Test every custom-format logical backup on an isolated PostgreSQL 17 instance.
For archives compressed after `pg_dump --format=custom --no-owner`, omit stored
ACLs during the data restore and reapply the reviewed role bootstrap, ownership,
and runtime-grant scripts separately. This prevents a legacy production role
name in an archive from making the recovery test environment-dependent:

```sh
createdb eth402
gzip -dc eth402-YYYYMMDDTHHMMSSZ.dump.gz \
  | pg_restore --dbname=eth402 --no-owner --no-acl --exit-on-error
```

The `eth402` database name is required because every role script fails closed on
any other name. Run this only in a fresh isolated test cluster; never overwrite a
live database. After the restore, verify every migration row and critical table
count, then run the role procedure in
[PostgreSQL production roles](POSTGRESQL_ROLES.md). Record the backup checksum,
restore time, PostgreSQL image digest, schema version, and test result in
deployment evidence. A successful dump command without this restore drill is
not a usable rollback gate.
