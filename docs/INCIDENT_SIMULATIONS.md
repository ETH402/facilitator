# Incident simulations

These drills prove that documented failure signals and recovery invariants are
exercised together, rather than assuming individually green tests form an
incident response. They are deterministic and use only local fakes plus the
destructive `eth402_test` database. They never use funded keys or broadcast to
mainnet.

Run the complete set:

```sh
export ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable'
make incident-simulations
```

The runner refuses any database URL that does not name `eth402_test`. The store
suite truncates test data and applies all migrations, including
`000008_merchant_fair_use`.

## Acceptance criteria

### RPC outage and provider degradation

- Failed provider attempts increment the RPC error observation even when the
  fallback succeeds.
- An unavailable or wrong-chain RPC makes readiness fail.
- Public health becomes degraded or outage from real observations; absence of
  evidence is never reported as operational.
- A failed signer-balance read preserves the last value and its old timestamp,
  allowing the stale and unreadable alerts to fire.

Operator response: quarantine the provider, compare an independent mainnet
node, and do not alter payment state from one provider's evidence.

### Signer outage

- A signing failure leaves the committed intent and allocated nonce unchanged.
- Nothing is marked ambiguous because no signed transaction reached the
  broadcast boundary.
- The next broadcast-worker tick can retry the same durable intent.

Operator response: inspect policy-signer or KMS availability and credentials.
Do not change signer address; doing so orphans its durable nonce sequence.

### Ambiguous broadcast and reorg

- An unknown send outcome becomes an ambiguous transaction and moves the
  payment to `manual_review`.
- Recovery re-attaches a transaction found on chain or re-signs only the exact
  stored transaction, proven by sighash.
- A sighash mismatch refuses recovery.
- A non-canonical receipt cannot finalize, and a reorged non-final transaction
  returns to broadcast observation.

Operator response: reconcile by transaction hash and signer nonce before any
manual send. Follow [the concrete runbook](RUNBOOKS.md#payment-in-manual_review).

### Worker crash and lease loss

- A panic is contained to one payment and does not terminate the worker loop.
- The payment lease is released after a panicking step.
- A worker whose batch lease expired skips the payment rather than acting after
  another worker may have reclaimed it.

Operator response: inspect panic logs and expired leases, then restart from the
same deployment artifact. Durable state makes restart safe.

### Durable recovery and nonce gaps

- Ambiguous and reorged database records return to the ordinary pipeline through
  guarded transitions.
- A dropped nonce is not filled while its EIP-3009 authorization can still
  succeed.
- Expired and simulation-failed authorizations become safe fillers only after
  the expiry safety window.
- Concurrent recovery workers cannot both own the same payment.

Operator response: let recovery consume only safe gaps. Never reallocate a
durably reserved signer nonce to another payment.

## Evidence to retain

For a beta rehearsal, retain:

- date, operator, source commit, and configuration digest;
- command output and pass/fail result for every drill;
- relevant alert and status-page screenshots from a staging deployment;
- database queries and RPC evidence with secrets and payer signatures redacted;
- deviations, response time, and a named owner for every follow-up.

## Deliberate limits

These simulations validate application behavior, durable transitions, and the
signals feeding alerts. They do not prove Prometheus/notification delivery,
cloud IAM revocation, live Cloud KMS availability, a real provider partition, or
mainnet transaction behavior. Those require a staging tabletop and the limited
mainnet dry run; the latter needs funded keys and explicit operator approval.
