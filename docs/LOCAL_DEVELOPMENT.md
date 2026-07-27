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
