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

The logging email backend records only delivery metadata. The file backend
writes mode-0600 JSON under `email-outbox/` and is forbidden in production.
No development private key is supplied by default and signing is disabled.

Use `docker compose down -v` only when intentionally discarding local database
state.
