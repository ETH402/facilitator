# Deployment

The current build is not approved for mainnet payment processing.

A future production deployment should run the immutable application image
behind Caddy or a managed TLS load balancer, use managed PostgreSQL with TLS
and point-in-time recovery, two authenticated Ethereum RPCs, a real email
provider, centralized secret management, and an external KMS/HSM/Vault signer.

This build intentionally refuses to start in production because its only
email adapters are development `log` and `file` implementations. Add and
review a provider adapter behind `email.Sender` before deployment, then extend
the configuration allowlist explicitly; never make an unknown backend fall
back to logging. Production also requires `ETH402_ENV=production`, an HTTPS
public URL, canonical chain/asset constants, and managed secrets. The process
rejects raw private keys unless a conspicuous dangerous override is set;
production policy should prohibit that override.

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
