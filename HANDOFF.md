# Handoff

Last updated: 2026-08-05.

## Read first

1. `AGENTS.md`, `VISION.md`, and `PLAN.md`.
2. `docs/ARCHITECTURE.md`, `docs/SETTLEMENT_FLOW.md`, and `docs/DATA_MODEL.md`.
3. ADRs in `docs/decisions/`; dated amendments supersede original text.
4. `docs/OPERATIONS.md`, `docs/RUNBOOKS.md`, `docs/RELEASES.md`, and
   `docs/SSH_DOCKER_DEPLOYMENT.md` before any production change.

## Current status

Milestones 0–4 are complete and the production service is live. Milestone 5's
review gate is closed; the funded mainnet execution evidence is still pending:

1. **Independent security review — done.** All findings were dispositioned and
   the review updates are applied.
2. **Controlled funded mainnet dry run — ready, not yet executed.** Signing is
   enabled in production, but no real USDC settlement has reached the required
   confirmation depth yet. Enabling the signer is a prerequisite, not evidence
   that the dry run completed.

Production is live and healthy:

- Application host: `toufik@35.232.99.172`, Compose project `eth402prod`.
- Running containers: app (`ghcr.io/eth402/facilitator:v0.1.0-rc.2`),
  Caddy, PostgreSQL 17, Prometheus, Alertmanager. The policy signer is a
  separate workload reached at `https://signer.eth402.org`.
- `ETH402_SIGNER_MODE=policy`; `ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER=false`.
  Signing goes through the KMS-fronted policy-signer boundary; the
  facilitator holds no KMS grant.

## Open follow-ups

- **Deploy by digest, not tag.** Production currently runs the mutable tag
  `v0.1.0-rc.2`; the release process (`docs/RELEASES.md`) requires
  digest-pinned deployment tied to the reviewed source. Repin at the next
  release.
- **Stray foundry container on the production host** (`zealous_rubin`,
  `ghcr.io/foundry-rs/foundry:stable`). A development tool that does not
  belong in production; remove it unless it is serving a documented purpose.

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

1. Complete exactly one controlled funded mainnet settlement and retain the
   evidence required by `docs/MAINNET_DRY_RUN.md`.
2. Repin the production deployment to an immutable digest at the next release
   (see Open follow-ups).
3. Remove the stray foundry container from the production host.
4. Operate: watch signer balance/burn-rate alerts, worker liveness, and RPC
   agreement in Prometheus/Alertmanager; follow `docs/RUNBOOKS.md` for
   settlement states.
