# Task claims

One line per task. Claim before writing code; clear the claim when the PR merges.
See [COLLABORATION.md](COLLABORATION.md) rule 3.

Status is one of `open`, `claimed by <agent>`, `in review (PR #n)`, or `blocked on
human`. A task claimed but untouched for a day should be released rather than
held.

## In progress

| Task | Status | Notes |
|---|---|---|
| PR #2 settlement review fixes | in review (PR #2) | Implemented by Codex: terminal idempotency, canonical finality, lease renewal, and KMS scalar validation. |
| Gap-filler raw transaction persistence (migration `000007`) | in review (PR #2) | Implemented by Codex; exact signed bytes are persisted before broadcast and retried identically. |
| Range-check `r`/`s` in `ethereumSignature` | in review (PR #2) | Implemented by Codex with malformed DER regression coverage. |

## Open — no human decision needed

| Task | Status | Notes |
|---|---|---|
| Lease the three unleased recovery passes | open | `observeReplacements`, `fillNonceGaps`, `observeGapFillers` run without a lease, which constrains deployment to one application instance. Two instances would each re-estimate fees and broadcast different transactions for one nonce gap. Documented in `OPERATIONS.md` as a constraint. |
| Exercise the differing-signature re-broadcast against a real node | open | The branch is correct by inspection and covered by fake signers, but no test has watched a real mempool reject a same-nonce same-fee re-broadcast as underpriced — which is what happens if the original reappears between the on-chain check and the send. |

## Blocked on a human decision

Do not attempt these; they need the operator, not an agent.

| Task | Status | Notes |
|---|---|---|
| Sign off on the 12-confirmation finality cut | blocked on human | ADR-0004 decision 5. `confirmed` is terminal, so a reorg deeper than 12 blocks leaves a payment marked confirmed that no longer exists on chain. Recorded as accepted residual risk because it matches the existing default, never explicitly agreed. |
| Signer balance and burn-rate alerting | blocked on human | Infrastructure. ADR-0004 decision 8 makes the bounded hot balance *the* operative signer-compromise control, so without the alert the bound is a convention. |
| Human review of PR #2 | blocked on human | Thirty-odd commits of settlement — the code that signs transactions and spends ETH — none read by a person. Cross-review between agents (rule 5) reduces this risk but does not remove it. |
