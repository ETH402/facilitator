# Handoff

Last updated: 2026-08-04. Do not treat a hash written in a handoff as the review
target: freeze and record the full `origin/main` SHA after the final push and
require CI and the independent reviewer to approve that exact revision.

## Read first

1. `AGENTS.md`, `VISION.md`, and `PLAN.md`.
2. `docs/SECURITY_REVIEW.md` and `docs/SECURITY_REVIEW_FINDINGS.md`.
3. `docs/ARCHITECTURE.md`, `docs/SETTLEMENT_FLOW.md`, and `docs/DATA_MODEL.md`.
4. ADRs in `docs/decisions/`; dated amendments supersede original text.
5. `docs/RELEASES.md`, `docs/SSH_DOCKER_DEPLOYMENT.md`, and
   `docs/MAINNET_DRY_RUN.md` before any production change.

## Current status

Milestones 0–4 are complete. Milestone 5 still has two external gates:

1. **Independent security review.** AI-assisted review is useful input but is
   not independent approval. The reviewer must approve the exact final commit,
   production/IAM architecture, and eventual immutable image digests.
2. **Controlled funded mainnet dry run.** The procedure is complete but has not
   been executed. It remains blocked until every prerequisite in
   `docs/MAINNET_DRY_RUN.md` is evidenced.

The production site and API are healthy on the existing signer-disabled
deployment. The application host is `toufik@35.232.99.172`; the policy signer
remains a separate workload. Do not enable signing merely because source CI is
green. The newer hardening commits are not deployed until the reviewed,
attested digest and database transition are approved.

Repository description/topics are configured. On the current private GitHub
Free repository, branch protection, native secret scanning/push protection,
release attestations, and the checked-in public-release workflow cannot all be
enabled. The intended transition is: private source review, public visibility
with signer disabled, immediately enable repository controls, publish an
immutable release, obtain the reviewer's digest addendum, then deploy by digest.

## 2026-08-04 hardening

- Dual-provider fail-closed RPC agreement, request/response identity binding,
  and local signed-transaction hash verification.
- Strict JSON parsing, including duplicate-key and invalid-UTF-8 rejection.
- Durable encrypted email outbox with leased/fenced delivery and operational
  metrics/alerts.
- PostgreSQL owner/migration/runtime role separation and a real disposable
  privilege-upgrade integration gate in CI and release.
- Immutable release workflow, pinned build/scanning inputs, SBOM/provenance
  evidence, and digest-only deployment documentation.
- Production backup copied off the VM and successfully restored through schema
  adoption, migration `000012` to `000013`, runtime grants, and ownership audit.
- AI-assisted follow-up identified two candidate fixes. The initial versions
  were withdrawn because one changed suspension semantics without a race-safe
  address-block design and the other edited an applied migration. The compliant
  migration preflight now refuses a lossy downgrade across `000004` while
  leaving historical migration files unchanged. See
  `docs/SECURITY_REVIEW_FINDINGS.md`.

## Required validation

Run from the final frozen revision:

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
- Production signer mode remains `disabled` until the controlled procedure
  explicitly enables `policy`. The facilitator must never receive the KMS
  signing grant.
- Different RPC hostnames are only a syntax guard. Deployment evidence must
  prove different operators and authenticated accounts.
- Commits and release tags are SSH-signed through the configured 1Password
  agent. GitHub's verification result is authoritative if the local Git client
  lacks an `allowedSignersFile`.

## Next actions

1. Push the final signed revision and require exact-commit CI success.
2. Commission the independent review using `docs/SECURITY_REVIEW.md`; include
   the AI-assisted findings as untrusted handover material.
3. Address findings and obtain explicit disposition for every later delta.
4. Make the repository public with signing still disabled, enable repository
   controls, and publish a signed immutable prerelease.
5. Have the reviewer verify both OCI digests and attestations and sign an
   addendum binding them to the reviewed source.
6. Deploy the facilitator digest signer-disabled, complete the production/IAM
   preflight, then execute exactly one controlled funded mainnet payment with
   the required independent observers.
