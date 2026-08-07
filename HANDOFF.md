# Handoff

Last updated: 2026-08-07.

## Read first

1. `AGENTS.md`, `VISION.md`, and `PLAN.md`.
2. `docs/ARCHITECTURE.md`, `docs/SETTLEMENT_FLOW.md`, and `docs/DATA_MODEL.md`.
3. ADRs in `docs/decisions/`; dated amendments supersede original text.
4. `docs/OPERATIONS.md`, `docs/RUNBOOKS.md`, `docs/RELEASES.md`, and
   `docs/SSH_DOCKER_DEPLOYMENT.md` before any production change.

## Current status

Milestones 0–4 are complete and the production service is live. Milestone 5's
funded mainnet execution evidence is still pending:

1. **Independent security review — recorded.** John Doo of Pentest Company,
   with no implementation or operations role, reported no unresolved
   Critical/High findings and dispositioned all lower findings for commit
   `5c30b799c6c5fa1c4f72f31c1a72823d038bae67` under signed report reference
   `082026` on 2026-08-04. The later confirmation-wait, `/supported`, and
   merchant recipient self-service deltas passed the full release gates and
   adversarial agent review, but no separate human delta disposition is claimed.
2. **Controlled funded mainnet dry run — ready, not yet executed.** Signing is
   enabled in production, but no real USDC settlement has reached the required
   confirmation depth yet. Enabling the signer is a prerequisite, not evidence
   that the dry run completed.

Production is live and healthy:

- Application host: `toufik@35.232.99.172`, Compose project `eth402prod`.
- Facilitator release `v0.1.0-rc.6`, source commit
  `03eaeb900f73776dea735b08d0f6c57989bff483`, is signed and GitHub-verified.
- App image:
  `ghcr.io/eth402/facilitator@sha256:71d7f88289b19ec5393657a84d1509e58ebc18f4ab88d75879ae468f28384abe`.
- Policy-signer image:
  `ghcr.io/eth402/policysigner@sha256:859af4e82401a65896c3621f98ff198d5f24b66be1c28bd041e9a6eabc39f3c5`.
- Running services are app, Caddy, PostgreSQL 17, Prometheus, and Alertmanager.
  The policy signer is a separate GCE workload reached at
  `https://signer.eth402.org`.
- `ETH402_SIGNER_MODE=policy`; `ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER=false`.
  Signing goes through the KMS-fronted policy-signer boundary; the
  facilitator holds no KMS grant.

The app-only rc6 rollout completed on 2026-08-07. It transactionally revalidates
wallet sessions and API keys at every protected mutation, closes recipient and
suspension races, preserves one-time keys until acknowledgement, and hardens the
public and merchant UI's mobile, accessibility, session-expiry, outage, and CSP
behavior. The unchanged policy signer remains on its rc4 digest. `/supported`
advertises the signer under `signers["eip155:*"]`, schema remains
`000013_email_delivery_outbox`, and payment and transaction counts remain zero.
Caddy serves the reviewed verification-page CSP, Prometheus loaded all 16 rules,
and the recipient-change counters are being scraped. Release and deployment
evidence is stored read-only at `/opt/eth402/releases/v0.1.0-rc.6`. The verified
pre-rc6 backup is `/opt/eth402/backups/eth402-20260806T221207Z.dump.gz` with
SHA-256 `65635c626857fb084c04443220da50c91dba4abf0335ae8bd23515e94a799275`.
The daily backup entry was moved from the unprivileged `toufik` crontab to root
after preflight proved that the former could not write the root-only backup
directory.

## Open follow-ups

- **Fund and execute exactly one controlled mainnet dry run.** The KMS signer
  address is `0xc6927a70468bd4ea24ca4beb7ff433122b877383`. At deployment verification it
  held `600000000000000` wei (`0.0006 ETH`), below the `0.01 ETH`
  `SignerBalanceLow` threshold. The payer must create the EIP-3009
  authorization in its own wallet; never copy its private key to an ETH402
  host, repository, log, or chat.
- **Record the post-review delta decision.** The operator accepted the solo
  maintainer model for the rc4 rollout. Before representing rc4 itself as
  independently reviewed, obtain an explicit human disposition of commits
  `2ab9526e70398f2dd1c4ef91a425046875d214ac` and
  `4a60841615d1864ed517e1f93aacdb20e70f2db5`.

The policy-signer bearer token exposed during inspection was rotated on
2026-08-05. Secret Manager version 2 is active, version 1 is disabled, the old
token is rejected, and both the signer and facilitator restarted healthy with
the unchanged KMS signer identity.

## Required validation

Run from the final frozen revision before any release:

```sh
test -z "$(gofmt -l .)"
git diff --check
go vet ./... && go vet -tags=integration ./...
go test ./...
go test -race ./...
golangci-lint run ./...
govulncheck ./...
gitleaks git --redact --verbose .
deploy/postgres/integration-test.sh

ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=integration -race -p 1 ./internal/...
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  make incident-simulations

go test -tags=abuse -race -timeout 20m ./internal/httpapi
```

Run the bounded fuzzing, real no-mining Anvil replacement test, image builds,
image scans, Compose validation, and Caddy validation described in the review
and release documents. The ordinary CI workflow must be green on the exact
commit; a previous green run is not transferable.

## Load-bearing constraints and gotchas

- Ethereum mainnet only, x402 v2 only, exact only, canonical native USDC only.
- Never store funded keys, authorizations, raw signed transactions, credentials,
  or production database contents in the repository, fixtures, logs, or review
  packet.
- Money and nonce values use integer arithmetic.
- Applied migration SQL is immutable. The next migration number is `000014`.
  Migration `000004` cannot be downgraded after replacement rows share a
  payment nonce; the migration command now fails before changing schema and
  instructs the operator to restore a pre-replacement backup or recover forward.
- Integration tests self-migrate and share a destructive database; run packages
  serially with `-p 1`.
- Production signer mode is `policy`. Direct KMS mode (`external`), raw keys,
  and unsafe overrides are rejected at startup. The facilitator must never
  receive the KMS signing grant; the policy signer holds it alone.
- Different RPC hostnames are only a syntax guard. Deployment evidence must
  prove different operators and authenticated accounts.
- Commits and release tags are SSH-signed through the configured 1Password
  agent. GitHub's verification result is authoritative if the local Git client
  lacks an `allowedSignersFile`.

## Next actions

1. Raise the bounded signer balance above the configured operational threshold,
   then complete exactly one controlled funded mainnet settlement and retain
   the evidence required by `docs/MAINNET_DRY_RUN.md`.
2. Operate: watch signer balance/burn-rate alerts, worker liveness, and RPC
   agreement in Prometheus/Alertmanager; follow `docs/RUNBOOKS.md` for
   settlement states.
