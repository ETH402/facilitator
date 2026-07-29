# Threat model

Trust assumptions: PostgreSQL enforces constraints; operator hosts and CI are
administered securely; canonical USDC behaves as observed; at least one RPC can
provide honest chain data; external signer policy works. Residual risk remains.

| Threat | Asset / attacker / path | Impact | Preventive control | Detective control | Residual risk |
|---|---|---|---|---|---|
| Gas draining | facilitator ETH; malicious buyer floods valid/reverting authorizations | ETH loss | verification simulates, settlement re-simulates the exact calldata before signing, per-merchant quotas, fee caps, bounded hot balance, calldata allowlist | gas/revert alerts | a nonce consumed between simulation and mining still reverts; an unbroadcast nonce gap costs one revert if a later nonce is blocked |
| Settlement replay | buyer nonce; merchant/observer resubmits | duplicate work/gas | EIP-3009 state, durable unique nonce and identity | duplicate metrics/audit | conflicting facilitators race |
| Duplicate broadcast | signer nonce; retries/concurrency | gas/replacement confusion | DB intent/locks, signed hash, no blind retry, exact gap-filler bytes persisted before send | nonce reconciliation | RPC ambiguity |
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
| Signer compromise | facilitator ETH/reputation | arbitrary tx/gas loss | KMS-held key material, bounded hot balance with external top-up, and the allowlist enforcing mainnet, the canonical USDC address, the `transferWithAuthorization` selector, and zero ether value — in process, and behind the signing boundary under `ETH402_SIGNER_MODE=policy`, which builds the transaction from authorization fields so nothing else is expressible | chain monitoring, balance and burn-rate alerts | with the boundary deployed, a compromised facilitator can still settle real payments of its choosing and burn gas up to the boundary's ceilings; loss stays capped at the hot balance. Without it (`external` mode) KMS signs opaque digests and the allowlist is in-process only, so a compromise can have any transaction signed ([ADR-0004](decisions/0004-settlement-execution-model.md)) |
| Signing-boundary compromise | facilitator ETH/reputation | substituted transactions recorded as real payments | the boundary holds no database access and no merchant data; the client verifies every behaviour-determining field of the returned transaction against what it asked for, checks the recovered sender against the identity resolved at startup, and derives the sighash itself rather than trusting the response | signing-failure logs, chain monitoring | a boundary that refuses to sign halts settlement, which is a liveness failure rather than a loss; both processes are operated by the same team, so this is defence in depth, not a trust boundary between parties |
| Gas draining via valid authorization | facilitator ETH; attacker settles a genuine self-to-self payment | bounded gas loss | `/settle` re-checks that the recipient belongs to an active merchant and serializes the positive per-merchant quota under that merchant's row lock; suspension revokes later admission | quota rejections and per-merchant spend dashboards | registration is not Sybil-resistant, so one actor can obtain multiple merchant quotas |
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
