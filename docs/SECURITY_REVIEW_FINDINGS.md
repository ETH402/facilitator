# AI-assisted review findings — delta on top of `d431603`

This documents an AI-assisted security pass and the fixes it produced, for
whoever performs the [independent security review](SECURITY_REVIEW.md) that
gates the [mainnet dry run](MAINNET_DRY_RUN.md). It is a handover, not the
review itself: [SECURITY_REVIEW.md](SECURITY_REVIEW.md) is explicit that the
reviewer must be independent of the implementation, and this pass was done by
the same assistant that has been working on the codebase. Treat everything
below as input that narrows what the human reviewer needs to re-check, not as
a substitute for their sign-off.

## Baseline

- Frozen commit named in the review packet: `d431603ab45d67bb071cb71841e1cce8978f27f5`
- This document describes changes carried as an **uncommitted working-tree
  diff on top of that commit** at the time of writing. Before engaging a
  reviewer, commit these changes and update the frozen-commit record in
  [SECURITY_REVIEW.md](SECURITY_REVIEW.md) to the new commit hash — the
  packet requires review of an exact, clean revision.

```
 internal/merchant/service.go                   | 21 ++++++++-
 internal/merchant/service_integration_test.go  | 61 ++++++++++++++++++++++++
 migrations/000004_settlement_recovery.down.sql | 12 +++--
 migrations/000004_settlement_recovery.up.sql   |  5 ++-
 internal/store/settlement_recovery_migration_integration_test.go | new file
```

## What changed

### 1. Merchant suspension did not bind to the recipient wallet (Critical)

**Root cause.** Settlement attribution picks the oldest merchant row with
`status='active'` for a given `recipient_address`
(`internal/store/store.go:157-161`). Suspending a merchant only flips that
merchant's own row to `status='suspended'` — nothing marks the wallet address
itself as blocked, and `recipient_address` carries no uniqueness constraint
(`migrations/000001_initial.up.sql:24` is a plain index). A suspended
operator, who still controls the private key for that address, could
register a new merchant account under a different email with the same
`recipient_address`, complete SIWE + email verification, reach
`status='active'` again, and have new payments attribute and settle to the
same wallet — suspension had no lasting effect on the address it targeted.

**Fix** (`internal/merchant/service.go`, `VerifyWallet`): both places that can
move a row toward `status='active'` for a given `recipient_address` —
first-time activation (`verify_recipient`) and an existing merchant's
`change_recipient` — now require
`NOT EXISTS (... WHERE recipient_address = <target> AND status = 'suspended' ...)`
in the same `UPDATE`, inside the existing transaction. No schema change; this
is a pure admission-check tightening, so it carries no migration risk against
existing data.

**Scope note:** this closes the suspension bypass specifically. It
deliberately does not add a general uniqueness constraint on
`recipient_address` — that would touch the codebase's existing, documented
tolerance for multiple *active* merchants sharing an address (see the comment
at `internal/store/store.go:157-159` and the accepted Sybil-quota risk in
[THREAT_MODEL.md](THREAT_MODEL.md) row "Sybil registration"). A reviewer
should decide whether that broader tolerance is still acceptable; it is
unchanged by this fix.

**Regression test:**
`TestSuspendedMerchantCannotReactivateSameAddressUnderNewRegistration` in
`internal/merchant/service_integration_test.go`. Verified to fail against the
pre-fix code (reactivation silently succeeded) and pass against the fix.

### 2. `000004_settlement_recovery` could not be rolled back after any replacement (Low)

**Root cause.** The up-migration deliberately drops the
`(payment_id, transaction_nonce)` uniqueness constraint, because a replaced
or gap-filled transaction legitimately reuses its predecessor's nonce
(ADR-0004 decision 1) — two `ethereum_transactions` rows for one payment can
correctly share a nonce. The down-migration unconditionally re-added that
same constraint, so rolling back after any real replacement had occurred
would fail with a `23505` unique-violation, silently breaking the assumption
that this migration is reversible during an incident.

**Fix:**
- `migrations/000004_settlement_recovery.down.sql` no longer restores the
  constraint (with a comment explaining why); it still drops the two
  `max_fee_per_gas`/`max_priority_fee_per_gas` columns, which is always safe.
- `migrations/000004_settlement_recovery.up.sql` changed
  `DROP CONSTRAINT ...` to `DROP CONSTRAINT IF EXISTS ...`, so re-applying
  this migration after a rollback doesn't assume the (now not-restored)
  constraint is still present.

**Regression test:**
`TestSettlementRecoveryMigrationRollsBackAfterReplacement` in
`internal/store/settlement_recovery_migration_integration_test.go`. Inserts
two `ethereum_transactions` rows sharing a nonce (a replaced + its
replacement), rolls back through `000004`, and re-applies. Verified to fail
against the original down-migration with the exact `23505` error described
above, and pass against the fix. The pre-existing
`TestFairUseMigrationRollsBack` (covering `000008`–`000013`) still passes
unmodified.

## What was checked and found clean

Six independent code-reading passes against the packet's own
"[required adversarial paths](SECURITY_REVIEW.md#required-adversarial-paths)"
list, each cross-checked against exact file:line citations rather than
re-asserting the docs, plus one adversarial refutation pass on the finding
above before it was treated as confirmed:

- EIP-712 parsing, signature normalization/recovery, replay protection
- Merchant/API-key/session cross-tenant isolation, recipient-change auth
- Concurrency: quota TOCTOU, nonce ordering, lease takeover, reorg handling
- The policy-signer/KMS boundary: transaction construction, response
  verification, SSRF, secret logging, timeout handling
- HTTP layer: proxy-derived client identity, public-stats leakage,
  registration enumeration, email-token handling
- Migrations, retention races, metrics exposure, startup validation (this is
  where finding 2 above surfaced)

Also checked and clean: CI workflow supply-chain posture (every action
SHA-pinned, default `contents: read`, elevated permissions scoped only to the
jobs that need them) and the `Dockerfile` (digest-pinned base images,
distroless nonroot, the policy-signer built as a separate image from the
facilitator by design).

## Reproducible evidence executed

From [SECURITY_REVIEW.md](SECURITY_REVIEW.md#reproducible-evidence), run
against this diff:

| Check | Result |
|---|---|
| `git diff --exit-code` | clean before this diff was introduced |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l .` | clean |
| `golangci-lint run` | 0 issues |
| `govulncheck ./...` | 0 vulnerabilities reachable by this code |
| `go test -race ./...` (unit) | clean |
| `go test -tags=integration -race -p 1 ./internal/...` | clean, includes both new regression tests |
| `go test -tags=abuse -race -timeout 20m ./internal/httpapi` | clean |
| `FuzzUnsigned` (45s, ~1.26M execs) | no crashes |
| `FuzzSplitSignature` (30s, ~7.5M execs) | no crashes |
| `make incident-simulations` | all pass |
| `docker build` (facilitator + policysigner targets) | both succeed |

**Not run:** the mainnet-forked E2E procedure in
[LOCAL_DEVELOPMENT.md](LOCAL_DEVELOPMENT.md) — it requires forking a live
mainnet RPC, which is an infrastructure decision left to the operator rather
than something to stand up unilaterally during a code review.

## For the reviewer

- Re-verify the two fixes above independently; both are small enough to read
  in full against the exploit descriptions given.
- The suspension-bypass fix only covers the two `UPDATE` sites in
  `VerifyWallet`. Confirm there is no third path that can move a merchant row
  to `status='active'` (e.g. an operator/admin endpoint) that would need the
  same guard.
- Confirm the load-bearing invariants in
  [SECURITY_REVIEW.md](SECURITY_REVIEW.md#load-bearing-invariants) against
  whatever commit these changes are committed under, not against `d431603`
  directly.
