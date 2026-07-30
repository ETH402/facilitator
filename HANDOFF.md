# Handoff

Last updated: 2026-07-30 (main @ `a910fc5`, CI green). Written for the next
agent or operator picking up this repository cold.

## Read first

1. `AGENTS.md` — binding repo conventions (scope guards, migrations, testing).
2. `VISION.md`, `PLAN.md` — roadmap and milestone status.
3. `docs/ARCHITECTURE.md`, `docs/SETTLEMENT_FLOW.md`, `docs/DATA_MODEL.md`.
4. ADRs in `docs/decisions/` (0001–0004; 0002–0004 carry dated amendments that
   supersede parts of the original text).

## Where the project stands

Milestones 0–4 are complete. Milestone 5 has exactly two open boxes, both
explicitly non-delegable:

1. **Independent security review** — cannot be self-performed.
   `docs/SECURITY_REVIEW.md` is the frozen-target handoff for the reviewer.
2. **Limited mainnet dry runs** — procedure is complete in
   `docs/MAINNET_DRY_RUN.md`; blocked on the security review, funded keys,
   live infrastructure, and an operator decision.

Everything else on the roadmap is shipped, including the review backlog from
the Milestone 3 settlement review (fixed in `9bbb50a`, `fff193e`, `27f352e`)
and a full hardening pass (`846d801`, `fca4b69`, `45a4989`, `a910fc5`).

## Recent work (2026-07-30)

- Settlement recovery: exponential backoff on ambiguous-resolution re-signs
  (migration `000010`, stops continuous KMS spend); reverted originals in
  `observeReplacements` now wait for full finality depth; stuck gap fillers
  get the same ≥110% fee-bump path as other broadcasts; settlement intervals
  measured against the DB clock.
- Signer/config/RPC: KMS startup fetch bounded at 30s;
  `ETH402_MAX_GAS_LIMIT` floored at `MinGasLimit = 100_000`; JSON-RPC
  response ids verified; calldata length pinned to 292 bytes (test);
  merchant quota comment corrected (counts committed intents, enforcement
  unchanged).
- `TestReplacementAgainstRealMempool` exercises the stuck-tx replacement
  lifecycle against a real `--no-mining` Anvil mempool (compose service
  `anvil-nomine`, profile `testing`, port 8547, env
  `ETH402_TEST_ANVIL_NOMINE_URL`). Note: Anvil has no price-bump rule
  (verified on anvil 1.5.1 — it accepts any strictly greater fee), so the
  geth underpriced-rejection branch cannot be exercised locally; the ≥110%
  floors are pinned by the unit suite.
- Docs: PLAN M4 policy-signer wording corrected; ADR-0002/0003 amendments;
  ADR-0004 signer-balance alerts moved to resolved.

## Build, test, validate

Run the whole list before declaring anything done (see Makefile):

```sh
gofmt -l .                      # must be empty
go vet ./... && go vet -tags integration ./...
go test ./...
go test -race ./...
golangci-lint run               # 0 issues; binary at ~/go/bin/golangci-lint
govulncheck ./...
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=integration -p 1 ./internal/...
```

Slow but required before declaring anything done — it caught the
rate-limiter DoS, so do not skip it:

```sh
go test -tags=abuse -race -timeout 20m ./internal/httpapi
```

Real-mempool replacement test (needs the extra container):

```sh
docker compose --profile testing up -d anvil-nomine
ETH402_TEST_ANVIL_NOMINE_URL=http://localhost:8547 \
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=integration -run TestReplacementAgainstRealMempool -v ./internal/settlement/
```

Mainnet-fork e2e (optional, slow): see `docs/LOCAL_DEVELOPMENT.md`
(`ETH402_TEST_FORK_RPC_URL`, restart the fork before each run).

## Gotchas

- Integration tests self-migrate via `migrate.Up`. Never hand-apply SQL to
  the `eth402_test` database.
- Schema changes require a migration pair (`migrations/NNNNNN_name.{up,down}.sql`,
  next number `000011`). Never auto-create schema; never edit applied
  migrations' SQL (comment-only fixes have precedent in `000005`).
- Scope guards are load-bearing: Ethereum mainnet only, x402 v2 only,
  exact-only, native USDC only. Local Anvil uses chain id 1 solely to
  exercise the chain-id guard.
- Never commit secrets or use funded mainnet keys in tests, fixtures, docs,
  or logs. Tests use the development signer (`internal/signer/development.go`)
  and Anvil's well-known keys.
- Commits are SSH-signed via the 1Password agent; if signing fails with
  "failed to fill whole buffer", unlock 1Password and retry.
- Keep business logic (auth, metering, quota, billing) separate from x402
  protocol logic; small reviewable commits; never weaken tests.

## Known accepted deviations (do not "fix" without an ADR)

- Anvil mempool accepts any strictly-higher-fee replacement (no geth 10%
  rule); production-side answer is the ≥110% floor in `BumpFees`.
- 12-confirmation finality cut accepted 2026-07-29 (PLAN M3, ADR-0004).
- Gap-filler reorg hygiene: a reorged-out resolved filler is not re-filled
  (payment already terminal; nonce-hygiene edge only).
- `internal/settlement/recovery.go` has not had a fully independent read;
  it is in scope for the security review.

## Environment

- GCP: project `eth402`, KMS key
  `projects/eth402/locations/europe-west1/keyRings/eth402-settlement/cryptoKeys/eth402-settlement-signer/cryptoKeyVersions/1`
  (signer `0xc6927a70468bd4ea24ca4beb7ff433122b877383`). Live KMS tests:
  `ETH402_TEST_KMS_KEY_NAME` + `ETH402_TEST_ANVIL_URL`.
  These identifiers are published deliberately: neither is a secret (the
  KMS resource is IAM-gated; the signer address becomes public on-chain
  the moment it transacts), and a public link to the settlement wallet
  makes the bounded hot balance externally watchable.
- Local stack: `docker compose up -d postgres anvil` (postgres :5432,
  anvil :8545, chain id 1).

## Next actions

1. Commission the independent security review (`docs/SECURITY_REVIEW.md`).
2. Address findings.
3. Provision production per `docs/DEPLOYMENT.md` and `docs/KEY_MANAGEMENT.md`.
4. Execute `docs/MAINNET_DRY_RUN.md` with funded keys and an operator call.
