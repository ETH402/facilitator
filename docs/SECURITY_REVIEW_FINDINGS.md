# AI-assisted review handover

This file is input to, not a substitute for, the independent security review.
The implementation team and its AI assistants cannot approve their own work.
The reviewer must freeze the current full `origin/main` SHA and independently
verify every claim below.

## Merchant suspension and shared recipient addresses

An AI-assisted pass observed that recipient addresses are intentionally not
unique across merchants. Suspending one merchant therefore does not prevent a
different active merchant controlled by the same wallet from receiving
attribution, and a wallet controller can attempt a new registration after a
merchant-scoped suspension.

An initial patch tried to reject future activation onto any address held by a
suspended merchant. It was withdrawn because it was incomplete and changed
policy semantics:

- `READ COMMITTED` `NOT EXISTS` checks did not serialize concurrent suspension
  and activation;
- already-active shared-address merchants remained active; and
- merchant suspension silently became an address-wide sanction that could
  affect unrelated merchants using the same custody wallet.

The current code retains the pre-existing merchant-scoped behavior. Before
public settlement, the independent reviewer and operator must explicitly
disposition this model. If address-wide blocking is required, implement it as a
separate reviewed control with durable blocked-address state, common
address-level serialization across block/unblock, activation, recipient change,
and settlement, defined behavior for existing shared claimants, an ADR, a new
migration, and concurrency tests. Do not reintroduce the withdrawn `NOT EXISTS`
patch.

## Downgrade across settlement recovery migration

Migration `000004_settlement_recovery` removes the historical uniqueness
constraint on `(payment_id, transaction_nonce)` because replacement transaction
rows legitimately reuse the original nonce. Once such history exists, the
pre-`000004` schema cannot represent it without deleting or rewriting
payment-critical facts.

An initial patch altered the already-applied `000004` up/down SQL to omit the
old constraint. It was withdrawn because applied migrations are immutable and
the resulting database would be labeled pre-`000004` without actually having
the pre-`000004` invariant.

The compliant remediation leaves both historical SQL files byte-for-byte
unchanged. `internal/migrate.Down` now takes an exclusive table lock before
reverting `000004`, detects duplicate non-null `(payment_id,
transaction_nonce)` pairs, and refuses the downgrade with instructions to
restore a pre-replacement backup or recover forward. The refusal and unchanged
schema/version are covered by an integration test. The reviewer should verify
both the lock/check ordering and the no-partial-change assertion.

## Evidence already produced

The repository's full hardening suite, database-role upgrade harness, image
builds/scans, no-mining Anvil replacement test, abuse tests, bounded fuzzing,
and backup restore drill have passed during implementation. These results must
be rerun or independently assessed on the final frozen commit. Kimi's read-only
reviews found no remaining payment-correctness defect before this handover, but
Kimi is an implementation assistant and does not satisfy the independence gate.
