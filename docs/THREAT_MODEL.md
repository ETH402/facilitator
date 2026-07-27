# Threat model

Trust assumptions: PostgreSQL enforces constraints; operator hosts and CI are
administered securely; canonical USDC behaves as observed; at least one RPC can
provide honest chain data; external signer policy works. Residual risk remains.

| Threat | Asset / attacker / path | Impact | Preventive control | Detective control | Residual risk |
|---|---|---|---|---|---|
| Gas draining | facilitator ETH; malicious buyer floods valid/reverting authorizations | ETH loss | verify, simulate, quotas, fee caps, signer policy | gas/revert alerts | simulation/state race |
| Settlement replay | buyer nonce; merchant/observer resubmits | duplicate work/gas | EIP-3009 state, durable unique nonce and identity | duplicate metrics/audit | conflicting facilitators race |
| Duplicate broadcast | signer nonce; retries/concurrency | gas/replacement confusion | DB intent/locks, signed hash, no blind retry | nonce reconciliation | RPC ambiguity |
| Malicious merchant | buyers; deceptive recipient/resource | buyer loss/reputation | email/wallet proof, suspension, clear requirements | complaints/audit | verification is not legitimacy |
| Malicious buyer | merchant/gas; malformed or expiring signatures | DoS/gas loss | strict parse, expiry margin, simulation, limits | failure metrics | adaptive abuse |
| Sybil registration | service capacity; determined actor | quota evasion | domain controls, throttles, review | linked-abuse analysis | no global Sybil proof |
| API-key theft | merchant account | recipient/key abuse | keyed hashes, one-time display, scoped auth, rotation | last-use/audit alerts | endpoint compromise |
| Email takeover | onboarding/recovery | account takeover | wallet proof, cooldown, no email-only recipient change | audit review | both factors compromised |
| Recipient takeover | merchant revenue | redirected payments | fresh SIWE proof, history, cooldown | append-only history/audit | compromised wallet |
| RPC manipulation | payment truth; RPC provider | false verify/receipt/head | fallback providers, chain check, confirmations | cross-provider mismatch | correlated providers |
| Chain reorg | finality | reversed payment | canonical hash checks, confirmations | reorg worker/lag alert | deep reorg |
| Transaction replacement | pending tx | stuck/replaced settlement | signer nonce ownership, linked replacements | mempool/receipt checks | provider mempool gaps |
| DB race | idempotency | duplicate intent | unique constraints, transactions, row locks | constraint/audit alerts | application bug |
| Record corruption | financial/audit state | incorrect stats/recovery | least privilege, checks, backups, append-only triggers | reconciliation/integrity checks | privileged DBA |
| Insider abuse | all | theft/suppression/data leak | separation of duties, signer policy, immutable audit | access/audit review | collusion |
| Signer compromise | facilitator ETH/reputation | arbitrary tx/gas loss | KMS-held key material, in-process zero-value/USDC selector allowlist | chain monitoring | Cloud KMS cannot enforce a calldata allowlist, so a compromised process can have any transaction signed ([ADR-0004](decisions/0004-settlement-execution-model.md)) |
| Gas draining via valid authorization | facilitator ETH; attacker settles a genuine self-to-self payment | unbounded gas loss | `/settle` admits only payments whose recipient is an active registered merchant, plus per-merchant quotas and suspension | per-merchant spend dashboards | registration is not Sybil-resistant |
| Denial of service | availability | outage | limits, timeouts, cache, circuit breaker, capacity | saturation/SLO alerts | volumetric attack |
| Gas-price spike | ETH/availability | high cost or delayed settle | max-fee and admission policy | fee/pending alerts | payments expire |
| Dependency compromise | build/runtime | code execution | minimal deps, checksums, pinned CI, review | vuln/SBOM scans | upstream compromise |
| Supply-chain attack | release image | fleet compromise | minimal CI permissions, SHA actions, signed artifacts | provenance verification | maintainer account loss |
| Secret leakage | keys/tokens | account/fund loss | redaction, secret manager, no raw logs/tests | secret scanning | memory/host forensics |
| Metrics leakage | merchant/privacy | correlation | no high-cardinality labels, bounded route labels, `ETH402_METRICS_ENABLED` gate, proxy refuses `/metrics` | schema review | traffic inference |
| Public-stats abuse | availability/privacy | scraping/DoS/inference | aggregate-only, cache, rate limits | request alerts | coarse business inference |
| Rate-limit evasion | availability; client forges `X-Forwarded-For` or rotates addresses | limit bypass, shared-bucket DoS | trusted-proxy allowlist, rightmost-untrusted selection, header ignored from untrusted peers, IPv6 grouped by `/64` | limit-rejection alerts | misconfigured proxy list; large address pools |
| Verification-record growth | storage/availability; unauthenticated `/verify` flood | disk exhaustion, stats noise | per-client rate limits, aggregate-only rows, no payment row before success | table-size and attempt-rate alerts | append-only table cannot be pruned by the runtime role |
| Gas policy manipulation | operator funds | economic DoS | protocol-independent limits/manual halt | spend dashboards | undecided business policy |
