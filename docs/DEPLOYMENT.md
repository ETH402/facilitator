# Deployment

The current build is not approved for mainnet payment processing.

Before any first funded transaction, complete the
[limited mainnet dry-run procedure](MAINNET_DRY_RUN.md). The procedure is
currently blocked by the required independent security review, funded
infrastructure, and operator approval.

A production deployment should run the immutable application image
behind Caddy or a managed TLS load balancer, use managed PostgreSQL with TLS
and point-in-time recovery, two authenticated Ethereum RPCs, a real email
provider, centralized secret management, and the KMS-fronted policy signer.

Production email uses the provider-neutral `smtp` backend with mandatory
certificate-verified TLS. Set `ETH402_SMTP_TLS_MODE=starttls` for explicit
upgrade (typically port 587) or `tls` for implicit TLS (typically port 465);
STARTTLS absence or negotiation failure aborts delivery rather than
downgrading to plaintext. Configure `ETH402_SMTP_ADDRESS`,
`ETH402_SMTP_FROM`, and—when the relay requires them—the username/password
pair through managed secrets. A private identity-authenticated relay may leave
both credential fields empty. The development `log` and `file` backends remain
forbidden in production.

Production also requires `ETH402_ENV=production`, an HTTPS public URL,
canonical chain/asset constants, and managed secrets. The process rejects raw
private keys, direct KMS mode, and unsafe signer overrides. Settlement must use
`ETH402_SIGNER_MODE=policy` or remain disabled.

Configuration validation additionally requires two distinct HTTPS RPC URLs,
`sslmode=verify-full` on PostgreSQL, and enabled metrics. Startup queries both
RPCs independently for chain ID 1, probes SMTP TLS/authentication without
sending a message, and requires the database migration set to match the binary
exactly. A missing migration cannot become a runtime SQL error, and an older
binary cannot silently start against a newer unreviewed schema.

Start from [`deploy/production.env.example`](../deploy/production.env.example).
Before touching dependencies, validate a populated environment with
`eth402 -check-config`; output is redacted and the command performs no network
or database calls. A normal start then performs the dependency preflight above.

Choose and record the retention values before rollout. The defaults tombstone
terminal payment authorization data after 30 days, prune expired onboarding
credentials after 24 hours, and prune revoked API keys after 30 days.
`ETH402_PAYMENT_RETENTION` must cover
`ETH402_MERCHANT_QUOTA_WINDOW`; startup rejects a shorter value because it
would erase merchant attribution while that payment still counts toward the
gas quota. See [Privacy](PRIVACY.md).

Do not expose PostgreSQL, signer, or RPC management endpoints publicly.
Restrict `/metrics`, use least-privilege service identities, scan images, sign
releases, and deploy migrations before application rollout. Kubernetes and Helm are
intentionally absent.

## What the bundled configuration already hardens

- **Base images pinned by digest** in `Dockerfile` and `compose.yaml`, so the image
  that ships is the image that was reviewed. Updating is a deliberate edit.
- **Distroless nonroot runtime**, static `CGO_ENABLED=0` build, `-trimpath`. The
  container has no shell, so `eth402 -health-check` probes readiness from inside the
  binary rather than requiring `curl` in the image.
- **`read_only` root filesystem**, `cap_drop: ALL`, `no-new-privileges`, and memory
  and CPU limits on the application container.
- **Caddy** refuses `/metrics*` on the public listener, caps request bodies at 1 MB
  and headers at 32 KB before anything reaches the application, bounds every
  reverse-proxy stage with timeouts so a wedged upstream cannot pin its workers, and
  sets the security headers on its own error responses as well as proxied ones.

## GCP

ADR-0004 decision 8 already commits to Cloud KMS, so GCP is the assumed target. The
sub-choice that matters is where the process runs, and it is not free.

### The settlement workers need a continuously running process

Broadcast, confirmation, recovery, and the balance poller are background goroutines
ticking on `ETH402_WORKER_INTERVAL` (default 15s). They are not driven by requests.

**Cloud Run throttles CPU to zero outside request handling by default, and scales to
zero.** Under those defaults the workers simply do not run: committed settlement
intents are never broadcast, receipts are never observed, and ambiguous transactions
are never reconciled — while `/verify` and `/settle` keep answering, so the service
looks healthy. If you deploy to Cloud Run:

```sh
gcloud run deploy eth402 \
  --no-cpu-throttling \
  --min-instances=1 \
  --max-instances=4 \
  --port=8080
```

`--no-cpu-throttling` keeps the goroutines scheduled between requests, and
`--min-instances=1` stops the only instance being reclaimed. Without both, settlement
silently stops.

GCE or GKE has no such caveat and is the simpler choice if you are not already on
Cloud Run.

### More than one instance is safe

Every settlement path claims a payment lease before acting, and nonce allocation
serialises on the `signer_accounts` row inside the transaction that commits the
intent. `--max-instances` above 1 is therefore fine. See
[operations](OPERATIONS.md).

### Components

| Piece | Service | Notes |
|---|---|---|
| Application | Cloud Run or GCE | see the CPU caveat above |
| Database | Cloud SQL for PostgreSQL | private IP; separate owner, migration, and runtime roles |
| Signer | Cloud KMS, `EC_SIGN_SECP256K1_SHA256` | `ETH402_SIGNER_MODE=policy` via the boundary below; direct `external` mode is rejected in production |
| Signing boundary | Cloud Run or GCE, own service identity | `cmd/policysigner`; the only identity granted the KMS key |
| Secrets | Secret Manager | `ETH402_API_KEY_PEPPER`, `ETH402_OPERATOR_TOKEN`, database credentials |
| Metrics | Managed Prometheus | scrape `/metrics` on the internal port; rules from `deploy/alerts.yml` |
| TLS | Cloud Load Balancing, or Caddy | if terminating at the balancer, add its egress range to `ETH402_TRUSTED_PROXIES` |

Grant the runtime identity only `roles/cloudkms.signerVerifier` on the one key
version, and nothing on the key ring. It never needs to create or destroy keys.

### The signing boundary

`ETH402_SIGNER_MODE=policy` routes signing through `cmd/policysigner`, which
receives authorization fields rather than a transaction or a digest and therefore
*builds* what it signs. A compromised facilitator cannot express an ether transfer
or a call to another contract, because there is no field for either. See
[ADR-0004](decisions/0004-settlement-execution-model.md) decision 8.

Two deployment facts carry the entire benefit:

1. **The boundary gets its own service identity**, and it is the *only* identity
   with `roles/cloudkms.signerVerifier` on the key version. Leaving that grant on
   the facilitator lets it bypass the boundary, which makes the whole arrangement
   decorative. This is the one thing to verify after deploying.
2. **The ceilings are configured on the boundary**, not sent by the facilitator, so
   set them at or above the facilitator's `ETH402_MAX_*` values — lower, and
   legitimate settlements pass the facilitator's checks only to be refused at the
   boundary.

```sh
docker build --target policysigner -t policysigner .
gcloud run deploy policysigner \
  --service-account=policysigner@PROJECT.iam.gserviceaccount.com \
  --ingress=internal --no-allow-unauthenticated --port=8081
```

`--ingress=internal` matters: the boundary's bearer token is its only
authentication, so it should not be reachable from the internet. The facilitator
needs `ETH402_POLICY_SIGNER_URL` (HTTPS, enforced in production) and
`ETH402_POLICY_SIGNER_TOKEN`; both processes read the same token from Secret
Manager. Configuration reference: `deploy/policysigner.env.example`.

The boundary needs no database, no migrations, and no merchant data. It does not
need `--no-cpu-throttling`, because unlike the facilitator it is purely
request-driven — but it does need `--min-instances=1` if a cold start would exceed
`ETH402_SIGNING_TIMEOUT`, since a signing timeout leaves a committed intent
unbroadcast for a tick.

A wrong token or an unreachable boundary fails at facilitator **startup**, not on
the first payment: the client resolves the signing identity during construction.

### Migrations run before rollout

`cmd/migrate` is a separate binary in the same image. Run it as a Cloud Run job or a
one-off container before switching traffic; the application never creates schema.
The application refuses startup unless the applied versions exactly equal those
embedded in its binary. Rollback therefore means rolling the schema down before
starting the older image; already-running old instances are not interrupted by
the migration, but a restarted old instance correctly refuses the newer schema.

The runtime database role needs `SELECT` on `schema_migrations`; table/sequence
permissions required by the application; and `DELETE` only on
`email_verification_tokens`, unreferenced
`wallet_verification_challenges`, revoked `api_keys`, and `merchant_usage`.
It needs `UPDATE` on payment and transaction rows for state transitions and
retention tombstones, but no DDL and no ability to disable the append-only
triggers. The migration role owns schema changes and is not used by the service.

### Deploys and in-flight settlement

A deploy sends `SIGTERM`. The process stops accepting requests, then waits up to 45
seconds for an in-flight worker tick, because a broadcast interrupted between
sending and recording its hash would become an `ambiguous` transaction — and
resolving one of those needs a human. The send-and-record pair is detached from
cancellation so it can finish.

Set the platform's termination grace period **above** that wait — Cloud Run's
`--timeout` and GKE's `terminationGracePeriodSeconds` — or the platform kills the
process during the window the wait exists to protect.

## TLS

The bundled `Caddyfile` listens on `:80` because the local stack has no domain. For
public deployment either replace the `:80` block with the hostname, which makes
Caddy obtain and renew certificates automatically, or terminate TLS at a managed
load balancer. **If you terminate elsewhere, add that balancer's egress range to
`ETH402_TRUSTED_PROXIES`** — otherwise every client collapses into one rate-limit
bucket, which is the failure this project has already had once.

Any deployment that terminates connections in front of the application must set
`ETH402_TRUSTED_PROXIES` to every intermediate hop. Left empty, rate limits key
on the proxy address and degrade to one shared bucket for all traffic. See
[operations](OPERATIONS.md) for the selection rules and the `/metrics` gate.
