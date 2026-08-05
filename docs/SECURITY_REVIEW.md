# Independent security review packet

This packet defines the minimum handoff for the independent review that gates
funded mainnet use. It is not a self-attestation: the reviewer must be
independent of the implementation and must approve an exact immutable revision.

## Freeze the review target

Before review begins, record:

```text
source commit:
release tag:
container digest:
Go version:
migration set:
reviewer:
review start/end:
```

The reviewed source must have a clean working tree. Generate the migration set
with `find migrations -type f -print | sort`; production startup
will reject any database whose applied migration names differ from those
embedded in that binary. Record any code or configuration change after review
as a delta requiring explicit reviewer disposition.

Do not give the reviewer production secrets, funded authorizations, private
keys, raw signed transactions, or a production database copy. The local
mainnet-fork procedure exercises canonical USDC with unfunded test material.

## Load-bearing invariants

Review these as security properties, not merely code quality:

1. Only x402 v2, `exact`, `eip155:1`, canonical native USDC, and EIP-3009
   `transferWithAuthorization` are admitted.
2. The signed authorization, requirements, stored identity, simulation
   calldata, policy-signer request, signed transaction, and public result agree
   on payer, recipient, atomic integer amount, nonce, validity interval,
   network, and asset.
3. One payment identity allocates at most one active signer nonce and repeated
   `/settle` calls return the original result. Concurrent requests, process
   death, RPC ambiguity, replacements, and reorgs cannot create a second
   transfer.
4. The policy signer constructs rather than accepts calldata and can express
   only a chain-1, zero-value call to canonical USDC within fee and gas
   ceilings. The facilitator workload has no direct KMS signing grant.
5. Simulation is repeated immediately before signing. Expired or already-used
   authorizations, predictable reverts, quota exhaustion, lease loss, and
   unsafe fee values fail closed.
6. Per-merchant and facilitator-wide quotas are serialized correctly under
   concurrency and cannot be multiplied by API-key creation or merchant count.
7. Authentication tokens, API keys, SMTP credentials, signer material,
   authorizations, and transaction bytes are absent from public responses and
   logs. Retention does not erase material still needed for nonce recovery.
8. Both RPCs are independently pinned to chain ID 1, production dependencies
   require authenticated/encrypted boundaries, and startup rejects schema or
   configuration drift.

The rationale and residual risks are in
[ADR-0004](decisions/0004-settlement-execution-model.md),
[Threat model](THREAT_MODEL.md), [Settlement flow](SETTLEMENT_FLOW.md),
[Key management](KEY_MANAGEMENT.md), and [Privacy](PRIVACY.md).

## Required adversarial paths

At minimum, independently inspect and attempt to break:

- EIP-712 domain/message parsing, signature normalization and recovery,
  malformed/duplicate JSON fields, integer boundaries, and authorization replay
- merchant ownership proof, recipient changes, API-key lookup/rotation, and
  cross-merchant authorization
- concurrent intent creation, global quota enforcement, signer nonce
  allocation, lease takeover, dropped/replaced transactions, and reorg handling
- every route from an untrusted request to the policy signer and Cloud KMS,
  including SSRF, header injection, secret logging, and timeout/cancellation
- unauthenticated endpoint privacy, proxy-derived client identity, rate-limit
  memory bounds, registration enumeration, and email-token handling
- migration rollback assumptions, retention races, backup implications,
  operator endpoints, metrics exposure, and production startup validation

Findings should identify severity, exploit preconditions, affected revision,
reproduction, violated invariant, remediation, and retest result. Any accepted
risk needs a named owner and explicit operator approval.

## Reproducible evidence

Run from the frozen revision:

```sh
make fmt
git diff --exit-code
make build
make vet
make lint
make security
make test-race

ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=integration -race -p 1 ./internal/...
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  make incident-simulations

go test -tags=abuse -race -timeout 20m ./internal/httpapi
go test -run=^$ -fuzz=FuzzUnsigned -fuzztime=5m ./internal/policy
go test -run=^$ -fuzz=FuzzSplitSignature -fuzztime=2m ./internal/policy
make docker-build
```

Use the mainnet-forked E2E procedure in [Local development](LOCAL_DEVELOPMENT.md)
after restarting Anvil from a fresh public-RPC fork. Preserve command output,
tool versions, fuzz seeds/corpus, image scan results, and the immutable image
digest. A green automated suite is evidence, not a substitute for design and
manual review.

## Recorded review result

John Doo of Pentest Company, with no implementation or operations role,
reported no unresolved Critical/High findings and dispositioned all lower
findings for commit `5c30b799c6c5fa1c4f72f31c1a72823d038bae67`. The signed
report reference supplied by the operator is `082026`, dated 2026-08-04.

That result is scoped to the named commit. The later confirmation-wait change
(`2ab9526e70398f2dd1c4ef91a425046875d214ac`) and `/supported` signer
advertisement (`4a60841615d1864ed517e1f93aacdb20e70f2db5`) passed the full
automated release gates and adversarial agent review, but no separate human
delta disposition is claimed.

## Exit criteria

The review is complete only when:

- every in-scope critical/high finding is fixed and independently retested;
- every lower-severity finding is fixed or explicitly accepted by its owner;
- the reviewer confirms the load-bearing invariants against the final commit
  and image digest;
- production configuration and IAM architecture are reviewed separately from
  repository defaults; and
- a signed report or equivalent tamper-evident approval is stored outside this
  repository.

Only then may the operator begin the separately controlled
[limited mainnet dry run](MAINNET_DRY_RUN.md). Neither review completion nor a
successful dry run automatically authorizes public traffic.
