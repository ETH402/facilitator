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
Restrict `/metrics`, use least-privilege service identities, scan images, pin
digests, sign releases, and deploy migrations before application rollout.
Kubernetes and Helm are intentionally absent.
