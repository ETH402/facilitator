# Contributing

Read `AGENTS.md`, open an issue for substantial scope changes, and keep changes
inside ETH402's mainnet-USDC-exact boundary.

Before submitting:

```sh
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

Add migrations and rollback paths for schema changes. Update OpenAPI and
versioned docs for public-contract changes. Developer Certificate of Origin
sign-off may be required in a future contribution policy.
