# Handoff

Working state and hard-won operational knowledge for whoever picks this up next.
Standing rules live in [AGENTS.md](AGENTS.md); scope and status in
[PLAN.md](PLAN.md); settlement design in
[ADR-0004](docs/decisions/0004-settlement-execution-model.md). This file holds
what those do not: where things stand, what is blocked on a human, and the traps
that already cost time.

## Where things stand

- `main` carries Milestone 2 (verification). CI green.
- **PR #2** (`milestone-3-settlement` → `main`) carries all of Milestone 3:
  `/settle`, durable nonce allocation, broadcast/confirmation/recovery workers,
  the Cloud KMS signer, migrations `000002`–`000005`. All four CI jobs green.
- Post-review maintenance on the milestone branch fixes merchant-wide quota
  serialization, re-checks active merchant status at settlement time, aligns
  the local integration target with CI, and removes stale simulation/quota
  documentation. Inspect `git status` to determine whether those follow-ups
  have been committed and pushed.
- Milestone branches are kept, not deleted. History is linear per milestone; PR #1
  was merged with a merge commit specifically so `milestone-3-settlement` stayed a
  clean descendant of m2's exact SHAs.

**PR #2 has not been reviewed by a human.** Every commit in it was written and
checked by an agent. That is the single largest outstanding risk in the project,
larger than any individual open item below.

## Blocked on the human — do not attempt these

1. **The 12-confirmation finality cut** (ADR-0004 decision 5). `confirmed` is
   terminal, so a reorg deeper than 12 blocks silently leaves a payment marked
   confirmed that no longer exists on chain. Recorded as accepted residual risk
   because it matches the existing default, but never explicitly agreed to.
2. **Signer balance and burn-rate alerting.** Decision 8 makes the bounded hot
   balance *the* operative signer-compromise control, so until the alert exists
   the bound is a convention. Infrastructure, not code.
3. **Whether to lease the three unleased recovery passes.** Currently documented
   as a single-instance constraint in `OPERATIONS.md` instead.

Resolved since this handoff was written: Cloud KMS signing determinism. It is
**not** deterministic (verified live: three signatures of one transaction, three
raw hashes), which made decision 4's raw-hash identity check unreachable in
production. Recovery now persists the deterministic sighash at signing time
(migration `000006`) and proves identity by it; a legitimately differing
re-signature is recorded replacement-shaped (`manual_review → replaced`).
`TestCloudKMSSigHashStableAcrossSignatures` pins the property live.

## How to verify

The integration suite needs PostgreSQL and Anvil from `compose.yaml`:

```sh
docker compose up -d postgres anvil
```

Tests run against a **separate** database, `eth402_test`, created deliberately:
the suite does `TRUNCATE merchants CASCADE`, and pointing it at the `eth402` dev
database destroys local state.

```sh
export ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable'

gofmt -l .                     # must print nothing
go build ./... && go vet ./...
golangci-lint run              # must report 0 issues
go test -race -count=1 ./...
go test -tags=integration -race -p 1 -count=1 ./internal/...
govulncheck ./...              # see trap 3
```

`-p 1` matters: the integration packages share one database and coordinate
through a Postgres advisory lock.

## Traps that already cost time

**1. Database clock versus process clock.** `settlement_requested_at`,
`updated_at`, and friends are stamped by PostgreSQL's `now()`, while test
fixtures and policy inputs use Go's `time.Now()`. Comparing them at a hairline
threshold is flaky — sub-millisecond container skew flips it. Two tests were
written wrong this way. Age rows **in SQL** (`... - interval '48 hours'`) rather
than advancing the Go clock. Advancing the Go clock far enough to matter also
trips the expiry guard, which looks like an unrelated failure.

**2. A green concurrency test proves nothing.** Every concurrency test here was
first written in a form that passed against a deliberately broken
implementation. Before trusting one, break the code it guards and confirm the
test fails. Two examples that mattered: a naive read-then-write nonce allocator,
and a lease claim without `SKIP LOCKED` — the first version of that test used 40
payments and passed, because each worker finished before the next began; it only
discriminates with many workers contending for **one** row.

**3. `govulncheck` was CI-only.** It caught a *reachable* grpc vulnerability
(GO-2026-6061, through the Cloud KMS signing path) that many local verification
passes had missed. Run it locally. One advisory remains and needs no action:
GO-2026-5932, `x/crypto/openpgp` unmaintained, no fix version, no call path.

**4. Writing an audit row on a second connection self-blocks.** `settlement_attempts`
references `payment_records`, so its foreign-key check needs `FOR KEY SHARE` on a
row the caller may hold `FOR UPDATE`. Doing that on a separate pool connection
blocks forever, and PostgreSQL will **not** report it as a deadlock, because only
one side waits on a lock while the other waits on the client. It hung a test for
455 seconds. Rejection paths must write inside the caller's transaction — see the
comment on `rejectSettlement`.

**5. A duplicate payment violates two unique indexes at once.**
`payment_identity` and `(network, asset, payer, nonce)`. `ON CONFLICT` resolves
only its arbiter index speculatively while the other takes an ordinary uniqueness
wait, so concurrent inserts deadlock (40P01). Fixed with an advisory transaction
lock keyed on payer+nonce in `RecordVerification`; retrying alone "fixes" the test
while leaving a real deadlock on every concurrent duplicate, paying PostgreSQL's
1s `deadlock_timeout` each time. Settlement inherits the lock because it reaches
those rows through the same function.

**6. A transaction-local quota count is not automatically serialized.**
Locking one `payment_records` row protects duplicate settlement of that payment,
not different payments attributed to the same merchant. Quota admission must
lock the active `merchants` row before counting and hold it through commit. The
regression test deliberately holds that row so a broken implementation completes
both requests while the correct one queues both at the merchant boundary.

## Highest-risk code

In rough order of intricacy against test coverage:

1. `internal/settlement/recovery.go` — `replaceStuck` and `fillNonceGaps`. The
   most subtle code in the repo, and the hardest to exercise: both need
   adversarial chain conditions (a stuck mempool, a mined original beating its
   replacement, a nonce gap with traffic queued behind it). Unit tests use fakes.
2. `signIdentical` — correct only if signing is reproducible. See blocked item 1.
3. `internal/signer/kms.go` — `ethereumSignature` calls `big.Int.FillBytes`, which
   panics on a negative value, and `asn1` parses INTEGER as signed with no range
   check on r or s. Workers are now panic-guarded, so this degrades rather than
   crashing, but the range check is still missing.

## Conventions that are enforced, not aspirational

- **No configuration key without code that reads it.** `ETH402_METRICS_ENABLED`
  was parsed and validated but never read for an entire milestone; several keys
  were deliberately withheld until their consumer existed.
- **Zero is not "unlimited"** for a security control. The gas ceiling and the
  merchant quota both reject zero rather than treating it as disabled.
- **A public schema change bumps the OpenAPI version.** It is at 0.6; the last two
  bumps were single new `errorReason` enum values.
- **Every schema change gets a migration**, and the up/down/up round trip is part
  of CI.
- Commit messages explain *why*, including what was rejected and what a test would
  have missed. Match that.

## Do not relitigate

ADR-0004 is accepted and records its rejected alternatives with reasons:
`eth_getTransactionCount` and `max(transaction_nonce)+1` for nonce allocation,
Cloud HSM, a Safe with a transaction guard, and managed custody providers for
signing. If you want to revisit one, argue against the recorded reason rather than
rediscovering the option.
