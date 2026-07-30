# Local development

Docker Compose starts PostgreSQL, Anvil with local chain ID 1, the Go service,
and Caddy. Anvil is a disposable development network; no mainnet RPC or funds
are required. Never reuse its generated accounts on mainnet.

```sh
cp .env.example .env
docker compose up -d postgres anvil
make migrate-up
make test
make test-race
docker compose up --build app caddy
curl http://localhost/health/ready
curl http://localhost/stats
```

The logging email backend emits the verification body so the development link
can be used; the file backend writes mode-0600 JSON under `email-outbox/`.
Both expose raw email tokens by design and are forbidden in production. Never
copy these logs or files to shared systems. No development private key is
supplied by default and signing is disabled.

Use `docker compose down -v` only when intentionally discarding local database
state.

## End-to-end against real USDC

Every other test stubs the chain. `internal/e2e` drives `/verify` and `/settle`
against genuine USDC on a mainnet-forked Anvil, then asserts the balances actually
moved — which is the only test that proves the facilitator moves money rather than
satisfying its own fakes.

```sh
docker run -d --name eth402-fork -p 8546:8545 --entrypoint anvil \
  ghcr.io/foundry-rs/foundry:stable \
  --host 0.0.0.0 --fork-url https://ethereum-rpc.publicnode.com --chain-id 1

ETH402_TEST_FORK_RPC_URL=http://localhost:8546 \
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=e2e -count=1 -v ./internal/e2e
```

## Stuck-transaction replacement against a real mempool

The unit suite stubs the chain, so nothing real exercises the transaction
pool's replacement semantics. The `anvil-nomine` compose service runs Anvil
with `--no-mining`: broadcasts sit pending until `evm_mine` mines them on
demand, which lets the integration test prove the full replacement path — a
foreign same-nonce transaction that outbids the original by only 1 wei takes
over the nonce in the pool while the durable record stays untouched, the
recovery worker's proper ≥110% bump is computed from the stored fees, outbids
that squatter, and is accepted, and after `evm_mine` the payment finalizes
from the replacement's receipt rather than the original broadcast hash.

Note that Anvil's pool replaces a pending transaction on any strictly greater
fee; it has no 10% price-bump rule, so the "underpriced" rejection a geth
mempool would produce cannot be reproduced against it. The production-side
answer to that rule is `BumpFees`'s ≥110% floors, which the unit suite pins.

```sh
docker compose --profile testing up -d anvil-nomine postgres

ETH402_TEST_ANVIL_NOMINE_URL=http://localhost:8547 \
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  go test -tags=integration -p 1 -run TestReplacementAgainstRealMempool -v ./internal/settlement/
```

The test signs with Anvil's well-known genesis-funded account #0, which must
never be used anywhere else. Plain Anvil has no USDC contract, so the
settlement call returns empty success and the transaction mines with status 1;
the test exercises mempool, replacement, and confirmation mechanics, not USDC
semantics (that is the forked e2e test above). The `testing` compose profile
keeps the service out of the default stack.

## Abuse and fuzz tests

```sh
# Slow and allocation-heavy, so tagged out of the default run.
go test -tags=abuse -race -timeout 20m ./internal/httpapi

# Payment-critical parsers. The boundary target asserts far more than "no panic":
# every accepted input must yield a zero-value transferWithAuthorization call to
# canonical mainnet USDC inside the configured ceilings.
go test -run=x -fuzz=FuzzUnsigned -fuzztime=5m ./internal/policy
go test -run=x -fuzz=FuzzSplitSignature -fuzztime=2m ./internal/policy
```

The abuse suite characterises behaviour under deliberate abuse rather than gating
commits: bucket-map memory under 250k distinct client addresses, whether a flood
can deny service to everyone else, whether the limiter still applies during one,
and hostile JSON against `/verify` and `/settle`. It found a real denial of service
— see the note in [operations](OPERATIONS.md) on rate-limit eviction.

The fork must be mainnet: the verifier requires USDC's real EIP-712 domain
(`USD Coin` / `2`) at the canonical address, so a bare Anvil cannot substitute. Not
every public endpoint supports forking — Cloudflare's rejects the methods Anvil
needs with `-32046`; `ethereum-rpc.publicnode.com` works.

The buyer is funded through USDC's own `configureMinter`/`mint` path via an
impersonated master minter, so the balance is state the contract agrees with rather
than a written storage slot. Each run generates a fresh buyer and recipient because
the fork keeps its state between runs.

**Restart the fork before a run if it has been up for a while.** Anvil pins the
fork to the block it started at, and public endpoints serve state for only the most
recent blocks — so once that block ages out, Anvil's state reads become archive
requests and the provider rejects them:

```
Archive requests require a personal token
```

That is the fork expiring, not a facilitator failure. `docker rm -f eth402-fork`
and start it again to re-pin to current head, or use an archive-capable endpoint.

Each run generates a fresh buyer, recipient, and facilitator signer, so runs do not
interfere — but the fork accumulates their state until it is restarted.

The `e2e` build tag keeps it out of CI, which has no fork to point at.

## Incident simulations

The deterministic incident drills exercise failure detection and durable
recovery without funded keys or mainnet writes:

```sh
ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable' \
  make incident-simulations
```

The command refuses any database other than `eth402_test`. See
[incident simulations](INCIDENT_SIMULATIONS.md) for acceptance criteria,
operator actions, evidence to retain, and what still requires staging or
mainnet.
