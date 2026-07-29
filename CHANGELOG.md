# Changelog

All notable changes use semantic versioning. Public schemas are separately
versioned where noted.

## [Unreleased]

### Added

- An independent security-review packet with an immutable-target handoff,
  payment-critical invariants, required adversarial paths, reproducible
  evidence commands, finding expectations, and objective exit criteria. The
  external review itself remains a hard gate before funded mainnet use.

- Fail-closed production startup preflight: the database must have exactly the
  migrations embedded in the binary; primary and fallback RPCs are checked
  independently for chain ID 1; SMTP TLS/authentication is probed without
  sending mail; and production configuration requires distinct HTTPS RPCs,
  PostgreSQL `sslmode=verify-full`, metrics, and either the policy signer or
  disabled settlement. Direct KMS mode, raw keys, and unsafe overrides are now
  rejected in production.

- Configurable privacy retention (migration `000009` and a bounded worker).
  Stale verified payments expire automatically; terminal records older than 30
  days lose merchant linkage, payer/recipient addresses, authorization
  nonce/times/hash/signature, and stored raw transaction bytes. Irreversible
  identity, integer amount, state, and public transaction hash remain so
  lifetime statistics and duplicate `/settle` responses stay correct.
  Failed/expired authorizations that may still repair a signer nonce gap are
  excluded until their transactions are resolved. Expired email tokens,
  unreferenced wallet challenges, and revoked API keys are pruned on their own
  configurable schedules.

- Provider-neutral production SMTP delivery with mandatory certificate-verified
  STARTTLS or implicit TLS, bounded connection/session deadlines, optional
  paired authentication for private relays, RFC-compatible text messages,
  header-injection rejection, and delivery errors that do not echo merchant
  addresses into logs. Production configuration now requires this backend;
  development log/file delivery remains forbidden.

- An operator-controlled first-mainnet-transaction procedure with a one-intent
  global quota, explicit gas budget, two-RPC preflight, policy-signer/IAM
  checks, idempotency proof, abort rules, chain/database reconciliation, and a
  redacted evidence checklist. Execution remains blocked on independent review,
  funded infrastructure, and operator approval.

- Public integration and self-hosting documentation covering merchant
  activation, strict x402 request handling, verification and settlement
  outcomes, retry/idempotency rules, and the controls that must remain closed
  before mainnet use. The README now reflects implemented settlement and the
  KMS-fronted policy signer instead of describing them as future work. OpenAPI
  version 0.6.1 now marks the USDC EIP-712 `extra` object as required, matching
  the verifier's existing behavior.

- Facilitator-wide quota refusals now return the documented
  `facilitator_quota_exceeded` settlement result instead of surfacing as an
  internal dependency failure.

- Repeatable incident simulations for RPC and signer outages, ambiguous
  broadcasts, reorgs, worker crashes, lease loss, and nonce-gap recovery, with a
  destructive-test-database guard and documented evidence/response criteria.

- Fair-use enforcement (`docs/FAIR_USE.md`), all of it commercial policy kept out of
  the protocol path:

  - A **facilitator-wide settlement ceiling** (`ETH402_GLOBAL_SETTLEMENT_QUOTA`).
    The per-merchant quota alone left total exposure at merchants × quota, so the
    operator's worst-case gas spend was set by the size of the merchant list rather
    than by a decision. Counted under an advisory lock spanning every merchant,
    because the merchant row lock serialises one merchant and says nothing about two
    settling at once — without it, two concurrent callers commit two intents against
    a ceiling of one, which the new race test demonstrates.
  - **Per-merchant fair use** on authenticated endpoints, keyed on the merchant
    rather than the API key, since minting keys is self-service and would otherwise
    multiply the allowance. Fails open, exempts key revocation, and reports
    `X-RateLimit-*` plus `Retry-After`.
  - Migration `000008` with a pruning worker, so counters do not become an
    indefinite per-merchant activity log.

  Deliberately **not** added: a per-merchant request limit on `/verify` and
  `/settle`. Those are unauthenticated and the merchant comes from the
  caller-supplied `payTo`, so such a limit would let anyone exhaust an innocent
  merchant's allowance — a weapon rather than a control. Reasoning recorded in
  `docs/FAIR_USE.md`.

- An abuse test suite (`-tags=abuse`) covering bucket-map memory under 250,000
  distinct client addresses, flood behaviour in both directions, hostile JSON
  against `/verify` and `/settle`, and concurrent load across the public surface.

- Fuzz targets for the signing boundary's request parser and its recovery-id
  handling. The boundary target asserts the invariant rather than absence of
  panics: every accepted input must produce a zero-value
  `transferWithAuthorization` call to canonical mainnet USDC within the configured
  ceilings. 6.6M executions found no violation.

- A public status page at `/status`, self-contained HTML with no external
  stylesheet, script, font, or image — it keeps working during the network failures
  it exists to report, and discloses nothing to a third party about who is reading
  it. It renders the same cached snapshot as `/stats`, so a public endpoint cannot
  drive database or RPC load, and it cannot disclose more than the JSON does.

- An explicit privacy posture in `docs/PRIVACY.md`, including the retention gap it
  does *not* close.

- The KMS-fronted policy signer, closing the Milestone 4 item ADR-0004 decision 8
  deferred. `ETH402_SIGNER_MODE=policy` signs through `cmd/policysigner`, a
  separate workload holding the only grant on the KMS key, so the calldata
  allowlist survives a compromise of the facilitator.

  What crosses the boundary is the design: **authorization fields, never calldata
  and never a digest.** The boundary builds the transaction itself — chain 1,
  canonical USDC, zero ether value, `transferWithAuthorization` — so a caller
  cannot express an ether transfer, another contract, or another function, because
  there is no field for any of them. That makes the restriction structural rather
  than a validation that must be kept complete. Spend ceilings are the boundary's
  own configuration and unset ceilings mean sign nothing.

  Distrust runs both ways: settlement records the returned transaction's hash as a
  real payment's identity and re-signs against its sighash during recovery, so the
  client checks every behaviour-determining field against what it asked for,
  verifies the recovered sender, and derives the sighash itself rather than reading
  it from the response.

  The bounded hot balance is not superseded — a compromised facilitator can still
  settle payments of its choosing, which the boundary does nothing about.

- GCP deployment documentation, including the trap that Cloud Run's default CPU
  throttling and scale-to-zero stop the settlement workers entirely — committed
  intents are never broadcast — while `/verify` and `/settle` keep answering, so the
  service looks healthy.

- `docs/RUNBOOKS.md`: what to do about the states settlement actually produces —
  ambiguous broadcasts, nonce gaps, a gap filler the chain accepted, a depleted
  signer, a stalled worker, an intent that was never signed, and how to halt
  settlement without stopping verification. Every query in it was run against the
  schema before publishing.

- Deployment hardening: base images pinned by digest, request body and header caps
  plus reverse-proxy timeouts in Caddy, security headers on the proxy's own error
  responses, memory and CPU limits on the application container, and
  `eth402 -health-check` so the distroless image can be probed without a shell in it.

- Real RPC and worker-liveness metrics: `eth402_rpc_requests_total`,
  `eth402_rpc_errors_total`, and `eth402_worker_last_tick_timestamp_seconds` per
  worker, with alert rules in `deploy/alerts.yml`. RPC attempts are counted at the
  client's single choke point, so each retry registers — a read that only succeeds
  on its fallback still reports the failing provider. Worker health derives from the
  last *completed* tick rather than a flag the worker sets, because a wedged worker
  never gets around to clearing its own flag.

### Removed

- Placeholder metrics published as literal zeros: `eth402_confirmation_lag_blocks`,
  `eth402_settlement_latency_seconds`, `eth402_database_errors_total`,
  `eth402_settlements_confirmed_total`, `eth402_settlements_failed_total`, and
  `eth402_worker_healthy`. A metric that never moves is worse than an absent one: an
  alert on `rpc_errors_total` would never have fired however broken the RPC was, and
  one on `worker_healthy == 0` would have fired constantly while everything was
  fine. `OPERATIONS.md` told operators to alert on all of them. Confirmed and failed
  settlement counts remain in `/stats`, derived from the database.

- Milestone 0 foundation and validated Ethereum-mainnet-only architecture.
- Version 1 public statistics schema.
- Initial PostgreSQL schema and append-only security history.
- Merchant registration, one-time email verification, EIP-4361 recipient
  proof, API-key lifecycle, recipient changes, and operator suspension.
- Version 0.2 ETH402 merchant OpenAPI contract.
- Standard x402 v2 `/supported` and `/verify` endpoints, pinned official Go
  v2.19.0 types/verifier, durable verification attempts, and version 0.3
  OpenAPI contract.
- Worker lease primitive over the `claimed_by`/`claimed_until` columns: claim by
  state with a bounded limit, renew only while still held, release, and automatic
  reclaim of leases left behind by a dead worker. Claiming is one atomic statement
  that re-checks ownership under the row lock, so two workers can never hold the
  same payment. `ErrLeaseLost` tells an overrunning worker to stop rather than
  risk duplicating work.
- Settlement batches renew each payment lease immediately before acting and
  skip work whose ownership has lapsed.
- Canonical receipt finality checks block hash as well as depth for successful
  and reverted transactions.
- Terminal `/settle` retries return the original durable transaction hash.
- Nonce-gap fillers wait through authorization expiry plus the safety margin and
  persist exact signed bytes before broadcast (migration `000007`).
- Durable settlement intent. One transaction locks the payment, applies admission
  (verified state, recipient is an active registered merchant, authorization not
  inside the expiry margin), allocates the nonce, moves the payment to
  `broadcasting`, and writes the `intent` transaction row. Idempotent per payment:
  a second request returns the existing intent and its nonce rather than
  allocating another. Refusals are committed as `settlement_attempts` rows with a
  stable reason code and consume no nonce. Nothing is signed or broadcast.
- Durable Ethereum nonce allocation (`signer_accounts`) and worker lease columns
  in migration `000002`. Allocation joins the caller's transaction, so a nonce is
  never consumed without a durable record and a rolled-back settlement reissues
  rather than gaps the sequence.
- `signer.Transaction` now carries nonce and EIP-1559 fields with fail-closed
  validation rejecting non-mainnet chains, non-zero ether value, absent gas
  ceilings, and a priority fee above the total fee.
- Milestone 3 settlement execution model as ADR-0004 (accepted), covering durable
  nonce allocation, a single signer address on GCP Cloud KMS, recipient-gated
  `/settle` admission, lease-based workers, ambiguous-broadcast handling, an
  expiry margin, and the finality cut. A bounded signer hot balance is the
  operative signer-compromise control; a KMS-fronted policy signer carrying the
  calldata allowlist is deferred to Milestone 4.
- `POST /settle` (version 0.4 OpenAPI contract). Admission reuses the strict
  `/verify` parser and requires a prior verified payment record — the durable
  row is what binds the recipient to an active registered merchant (ADR-0004
  decision 9). The endpoint claims the payment lease and runs the broadcast
  pipeline inline: EIP-3009 `transferWithAuthorization` calldata is built from
  the durable record, signed under `ETH402_SIGNING_TIMEOUT`, broadcast once
  against the primary RPC, and the transaction hash is returned in the official
  `SettleResponse`. Calls are idempotent per payment; policy rejections return
  HTTP 200 with `success: false` and a stable `errorReason`.
- `payment_records.payer_signature` (migration `000003`): the EIP-3009
  signature the payment identity binds, persisted atomically with the
  settlement intent so calldata can be rebuilt after a crash.
- Broadcast and confirmation workers over the worker lease. The broadcast
  worker retries committed intents on `ETH402_WORKER_INTERVAL`; a signing
  failure leaves the intent untouched, an unknown broadcast outcome marks the
  transaction `ambiguous` and moves the payment to `manual_review` (never a
  re-sign, never a fresh nonce), and an authorization expiring before broadcast
  retires the intent as `expired` with the transaction `dropped`. The
  confirmation worker advances receipts to `confirming`, `confirmed` at
  `ETH402_REQUIRED_CONFIRMATIONS`, or `reverted`.
- Development signer backend (raw key, EIP-1559) behind the existing signer
  interface for local use against Anvil; production rejects it without
  `ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER`. The `external` mode is reserved for
  the Cloud KMS backend and now rejected at startup.
- `ETH402_SETTLEMENT_EXPIRY_MARGIN` (60s), `ETH402_SIGNING_TIMEOUT` (10s), and
  `ETH402_SETTLEMENT_LEASE_DURATION` (2m).
- Settlement recovery worker and migration `000004`. Ambiguous broadcasts are
  reconciled on chain by their signed-transaction hash and, after
  `ETH402_SETTLEMENT_RECOVERY_GRACE` (2m) without a sighting, re-broadcast as
  the byte-identical transaction — same nonce, gas, and fees, proven by hash —
  never a fresh nonce. Broadcasts pending beyond
  `ETH402_SETTLEMENT_REPLACEMENT_AFTER` (5m) are replaced by fee-bumped
  transactions on the same nonce (tip ×1.125, capped by the configured
  ceiling); an original the network mines anyway becomes the recorded truth
  and its replacement is dropped. Dropped nonces blocking a later in-flight
  nonce are filled by re-broadcasting the original expired intent, and
  reorged-out transactions return to `broadcast`. Migration `000004` persists
  the signing gas limit and fee pair at signing time and drops the
  `(payment_id, transaction_nonce)` uniqueness that replacement chains
  violate.
- EIP-1559 fee estimation: the initial max fee is `min(2·baseFee + tip,
  ETH402_MAX_FEE_PER_GAS_WEI)` from the latest block instead of the configured
  ceiling verbatim, so settlement no longer overpays beneath its own cap and
  replacements have bump headroom. The ceiling remains the hard spend bound.
- Cloud KMS signer backend (`ETH402_SIGNER_MODE=external`), completing
  Milestone 3. Signing runs against a GCP Cloud KMS `EC_SIGN_SECP256K1_SHA256`
  key version named by `ETH402_KMS_KEY_NAME`; key material never leaves KMS.
  The Ethereum sighash travels in the KMS digest field, DER signatures are
  normalized to low-s (EIP-2), and the recovery id is found against the
  address resolved from the key's public key at startup. Credentials come from
  Application Default Credentials. Verified end-to-end: a KMS-signed
  transaction was broadcast and mined on the local Anvil chain.
- Real `eth402_settlement_requests_total` and `eth402_settlement_failures_total`
  metrics replacing the zero placeholders.
- `ethereum_transactions.sighash` (migration `000006`): the deterministic digest
  a settlement signature commits to, persisted at signing time so recovery can
  prove a re-signed transaction identical under any signer backend.
- Per-merchant settlement quota, version 0.5 OpenAPI contract adding the
  `merchant_quota_exceeded` settle `errorReason`
  (`ETH402_MERCHANT_SETTLEMENT_QUOTA`, default 1000,
  per `ETH402_MERCHANT_QUOTA_WINDOW`, default 24h) with migration `000005`. This is
  the bound ADR-0004 decision 9 rests on: the recipient gate ensures gas is only
  spent for a party that accepted terms and can be suspended, but registration is
  not Sybil-resistant, so without a quota one registration bought unbounded gas.
  Counted while holding the active merchant row lock inside the transaction that
  commits the intent, so different payments for one merchant decide in commit
  order and cannot collectively slip beneath the limit; suspension also revokes
  settlement of previously verified payments. A refusal consumes no nonce. Zero
  is rejected rather than treated as unlimited.
- Pre-broadcast simulation, version 0.6 OpenAPI contract adding the
  `simulation_reverted` settle `errorReason`. Settlement runs the exact calldata
  it would send through `eth_call` from the signer address before signing.
  `/verify` already read `authorizationState`, but a nonce consumed between
  `/verify` and `/settle` was previously discovered by spending gas on a certain
  revert while the caller received a hash for a doomed transfer. A revert retires
  the intent as `failed`, unsigned and unbroadcast; a simulation that cannot run
  is transient and leaves the intent for the next tick, so a rate-limited RPC
  never abandons a payment that could have settled.

### Added

- Settlement signer balance monitoring: `eth402_signer_balance_wei`,
  `eth402_signer_balance_updated_timestamp_seconds`, and
  `eth402_signer_balance_read_errors_total`, with example alert rules in
  `deploy/alerts.yml`. ADR-0004 decision 8 makes the bounded hot balance the
  operative signer-compromise control, and a bound nobody observes is a convention
  rather than a control. Freshness is published alongside the value because a
  failing read leaves the last figure looking healthy; burn rate is left to
  Prometheus, which derives it correctly across restarts.

### Added

- End-to-end test against real USDC (`internal/e2e`, `e2e` build tag). Drives
  `/verify` and `/settle` on a mainnet-forked Anvil and asserts the money moved:
  the merchant's balance rises, the authorization nonce is spent on chain, a
  duplicate settle converges on the recorded hash, and confirmation reaches
  `confirmed`. Every other test stubs the chain, so nothing had previously executed
  a real `transferWithAuthorization`.

### Fixed

- A denial of service in the public rate limiter, found by the new abuse suite. The
  bucket map is capped at 100,000 entries, and past the cap every new client was
  collapsed into one shared per-minute bucket — so an attacker who filled the map
  denied service to every legitimate client arriving afterwards. Measured, not
  theorised: a client sending 20 requests was denied all 20. IPv6 makes filling it
  cheap, since a single /32 allocation yields 2^32 distinct /64 buckets. The limiter
  now evicts an entry to make room instead, which fails the other way at a cost
  that makes the trade uninteresting — resetting one bucket costs ~100,000 requests.

- `SECURITY.md` stated "The current implementation does not process x402 payments",
  which stopped being true when settlement landed. A security policy that
  understates its own scope tells researchers there is nothing worth attacking. It
  now states that real mainnet USDC moves, and enumerates what is in and out of
  scope.

- `/stats` reported `"status": "operational"` unconditionally — a hardcoded
  constant, so the field reported health during an outage, which is the one moment
  anybody reads it. Status is now derived from a database ping, an RPC chain-id
  check, and settlement-worker heartbeats, published alongside the per-component
  observations backing it. An unobservable status reports `unknown` rather than
  defaulting to healthy, which would have restated the same bug.

- Settled-volume figures were published unauthenticated, which is not the anonymous
  disclosure it appears to be: polling a cumulative total yields its own deltas, and
  a delta spanning one settlement is that payment's exact amount, correlatable with
  on-chain `Transfer` events to identify payer and merchant. Volume now requires
  `ETH402_PUBLISH_STATS_VOLUME=true`. This changes the public response shape, so
  `schema_version` is `2` and the OpenAPI schema is `StatsV2`.

- A deploy could strand a payment in `manual_review`. Shutdown cancels the worker
  context, and a cancellation landing between sending a transaction and recording its
  hash left it on the network with nothing recording it — the ambiguous case, which
  costs a human to resolve. The send-and-record pair is now detached from
  cancellation and bounded, and shutdown waits up to 45 seconds for an in-flight
  tick.

### Fixed

- A payment signed with a recovery id of 0 or 1 verified successfully and then
  could not settle. The official verifier applies `v - 27` before recovery so it
  accepts either encoding, while calldata construction required 27/28 —
  and `crypto.Sign`, along with many wallet libraries, emits the 0/1 form.
  Calldata now normalizes 0/1 upward; genuinely invalid ids are still rejected.
  The verification tests happened to use 0/1 and the settlement tests 0x1b, so
  neither side observed the disagreement.

### Changed

- Policy-signer success and refusal logs no longer contain payer, recipient,
  amount, authorization nonce, or signature material. Validation responses now
  expose only a stable error category, preventing malformed authorization data
  from crossing into facilitator logs.

- The facilitator's policy-signer client now accepts only an HTTP(S) origin,
  refuses redirects, and never includes boundary-controlled response bodies in
  errors, and rejects a malformed signer identity, closing bearer-token
  downgrade and authorization-log injection paths.

- The policy-signer HTTP boundary now rejects trailing JSON, compares fixed-size
  bearer-token digests, and bounds headers plus read/write/idle connection time.

- Container validation now builds both production workloads: the facilitator
  image and the separately targeted policy-signer image.

- CI now pins the current official Node 24 `checkout` and `setup-go` action
  revisions, removing the runner's Node 20 deprecation fallback.

- `ETH402_LOG_LEVEL` now controls the runtime structured logger and rejects
  unknown values instead of being silently ignored.

- The recovery worker's replacement, nonce-gap, and gap-filler passes now claim the
  payment lease before acting, removing the single-instance deployment constraint.
  They iterate over query results rather than a leased batch, so two instances would
  otherwise each re-estimate fees for one nonce gap and broadcast different signed
  transactions — the deduplicating hash lookup misses and both send, one replacing
  the other.

- A nonce-gap filler the chain accepts is escalated to `manual_review` once
  instead of being re-reported on every worker tick forever. Success means USDC
  moved on an authorization believed expired, so the record disagreed with the
  ledger and nothing resolved it; the receipt is now persisted and a human
  reconciles. `expired` gains a single outgoing edge to `manual_review` for this
  case and no other — recovery still never finalizes a payment.
- The signer calldata allowlist is now enforced. `signer.Transaction.Validate`
  requires the canonical Ethereum-mainnet USDC recipient and the
  `transferWithAuthorization` selector, alongside the existing mainnet, zero-value
  and fee checks. ADR-0004, the threat model, and a comment in the KMS backend all
  claimed this allowlist existed; only the zero-value and mainnet halves did.
  Because Cloud KMS signs opaque digests, this is the only barrier between a
  compromised process and an arbitrary signed transaction.
- Settlement workers recover from panics. They run as bare goroutines, so an
  unrecovered panic terminated the whole process and took HTTP serving with it,
  while the HTTP path had recovery middleware of its own. Every worker tick and
  per-payment step is now guarded, and a panicking step still releases its lease
  instead of stranding the payment.
- Enabling a settlement signer now requires non-zero max fee per gas and max gas
  limit, and a priority fee may not exceed the total fee ceiling.
- Rate limits key on the client address resolved through `ETH402_TRUSTED_PROXIES`
  instead of the direct peer, which collapsed all traffic behind a reverse proxy
  into a single bucket. IPv6 clients are grouped by `/64`.
- `ETH402_METRICS_ENABLED` now actually gates the `/metrics` route; it was parsed
  and validated but never read.
- The recipient-change cooldown is measured from the last verified recipient
  proof rather than `merchants.updated_at`, which unrelated writes such as
  operator reinstatement silently pushed forward.
- Malformed numeric, duration, and boolean environment values are reported by
  variable name instead of collapsing to a sentinel that validation either
  misattributed or accepted.
- Per-route metrics report identifier-bearing paths as their registered pattern
  rather than `unknown`.
- Payment-to-merchant attribution orders active claimants deterministically when
  more than one merchant shares a recipient address.
- Registration surfaces unexpected database errors instead of reporting success,
  while keeping duplicate registrations enumeration-resistant.
- Concurrent duplicate verification now converges instead of deadlocking. Writers
  for one authorization serialise on an advisory transaction lock, and transient
  deadlock or serialization aborts are retried, so two simultaneous `/verify`
  requests no longer risk one caller receiving `503`.

### Fixed

- Ambiguous-broadcast recovery no longer depends on reproducible signatures.
  Cloud KMS randomizes the ECDSA nonce, so a re-signed transaction never hashed
  to the stored raw hash and the re-broadcast path of ADR-0004 decision 4 was
  unreachable with the production signer — verified live against the real key.
  Recovery now proves identity by the persisted deterministic sighash; a
  re-signed transaction whose hash legitimately differs is recorded as the
  replacement of the ambiguous original (new `manual_review → replaced` edge),
  so the network mining either signature resolves the payment instead of it
  sitting in `manual_review` forever. Rows predating migration `000006` keep
  the raw-hash comparison, which deterministic signers still satisfy.
- Per-merchant settlement quota admission now locks the merchant row before
  counting. Locking only the individual payment allowed simultaneous settlements
  for different payments to observe the same pre-limit count and exceed the gas
  bound. A forced-contention integration test distinguishes the fixed
  implementation from the broken read-count-write race.
- The local `make integration` target now serializes internal packages with
  `-p 1`, matching CI and preventing packages that share and truncate one test
  database from corrupting each other's fixtures.
- Worker-driven payment transitions no longer fail the
  `payment_transitions.actor_type` check: workers audited transitions with
  their full lease identity (for example `confirmation/host/pid`), which the
  schema rejects, so every worker-side transition would have rolled back in
  production. Transitions are now audited with the coarse `worker` actor type;
  the full identity stays on the lease.

### Security

- `google.golang.org/grpc` bumped to v1.82.1 for GO-2026-6061, which
  `govulncheck` found reachable from the Cloud KMS signing path through the gRPC
  HTTP/2 transport.
- Live settlement is intentionally absent and transaction signing defaults to disabled.
- `X-Forwarded-For` is honoured only from configured trusted proxies, using the
  rightmost untrusted entry, so a forged header cannot select another client's
  rate-limit bucket. The bundled Caddy configuration replaces the header and
  refuses `/metrics` on the public listener.
- API keys are stored as keyed hashes; email tokens and wallet messages are
  stored only as hashes. Wallet challenges are single-use and time-bound.
- Verification rejects non-mainnet networks, non-native-USDC assets, Permit2,
  extensions, contract-wallet payers, used EIP-3009 nonces, and non-exact
  requirement echoes before any settlement action.
