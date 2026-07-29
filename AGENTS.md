# Instructions for coding agents

Before changing this repository:

1. Read `VISION.md`, `PLAN.md`, `HANDOFF.md`, and relevant ADRs in `docs/decisions/`.
2. Research current official specifications before protocol changes.
3. Preserve Ethereum-mainnet-only, x402-v2-only, exact-only, native-USDC-only scope.
4. Prefer official libraries where appropriate and keep dependencies minimal.
5. Never commit secrets or use funded mainnet keys in tests, fixtures, docs, or logs.
6. Use integer arithmetic for money.
7. Treat verification, idempotency, signing, and settlement as security-critical.
8. Run formatting, tests, race tests, vet, static analysis, and relevant integration tests.
9. Document assumptions and unresolved discrepancies.
10. Do not leave payment-critical TODOs.
11. Do not silently change public schemas; version them and update OpenAPI.
12. Create SQL migrations for every schema change; never auto-create application schema.
13. Update architecture, threat model, operations docs, and tests when behavior changes.
14. Prefer small, reviewable commits. Never weaken tests to hide bugs.
15. Keep business-model, authentication, metering, quota, and billing logic separate
    from x402 protocol logic.
