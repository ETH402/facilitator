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

The fork must be mainnet: the verifier requires USDC's real EIP-712 domain
(`USD Coin` / `2`) at the canonical address, so a bare Anvil cannot substitute. Not
every public endpoint supports forking — Cloudflare's rejects the methods Anvil
needs with `-32046`; `ethereum-rpc.publicnode.com` works.

The buyer is funded through USDC's own `configureMinter`/`mint` path via an
impersonated master minter, so the balance is state the contract agrees with rather
than a written storage slot. Each run generates a fresh buyer and recipient because
the fork keeps its state between runs.

The `e2e` build tag keeps it out of CI, which has no fork to point at.
