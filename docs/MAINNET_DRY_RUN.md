# Limited mainnet dry run

This is the procedure for the first funded ETH402 transaction on Ethereum
mainnet. It is a change-controlled validation, not permission to open public
settlement. Stop after one authorization and one settlement unless a separate
operator approval explicitly extends the run.

The procedure has not been executed. No funded key or mainnet credential is
stored in this repository.

## Hard prerequisites

Do not begin while any item is false:

- The exact commit and immutable image digest are recorded and have passed
  formatting, build, tests, race tests, vet, static analysis, vulnerability
  scanning, integration tests, incident simulations, and image scanning.
- The provider-neutral SMTP sender is configured against the selected relay,
  mandatory TLS and certificate verification have been exercised in staging,
  and a real verification email has been received without leaking its token
  into production logs.
- All migrations, including `000009_payment_retention`, have been applied by
  the migration role and verified before the application starts.
- Two independently operated, authenticated mainnet RPCs agree on chain ID,
  head, USDC code, payer balance, and authorization nonce state.
- PostgreSQL uses certificate-verified TLS (`sslmode=verify-full`), every
  embedded migration is applied, and the production startup preflight passes
  without exceptions.
- `ETH402_SIGNER_MODE=policy` is deployed as described in
  [Deployment](DEPLOYMENT.md). The policy-signer identity is the only identity
  with the KMS signing grant; the facilitator cannot call KMS directly.
- The KMS-derived signer address has been verified through an independent
  channel. It has a fresh or fully reconciled nonce sequence and no unknown
  pending transactions.
- The signer has only the explicitly approved bounded ETH balance. The funding
  source is outside ETH402's control, and signer-balance alerts are live,
  delivered, and tested.
- The merchant is active and controls the intended recipient EOA. The payer is
  a dedicated test EOA holding no more native USDC than the approved test
  amount. The payer does not need ETH.
- The operator has approved the exact USDC amount, maximum gas exposure,
  addresses, time window, observers, rollback owner, and evidence location.
  The approval records the configured primary-database and backup retention
  periods for the test authorization and addresses.
- An independent security review is complete. If it is not, the dry run
  remains blocked; this checklist is not a substitute for that review.

Never use a personal wallet, a production treasury, a customer authorization,
or a key copied from a development fixture.

## Freeze the budget

Record the chosen values in the change ticket before deployment:

```text
merchant quota                 = 1
facilitator-wide quota         = 1
quota window                   = <approved duration>
gas limit                      = <reviewed decimal integer>
maximum fee per gas (wei)      = <reviewed decimal integer>
maximum priority fee (wei)     = <reviewed decimal integer>
maximum admitted gas exposure  = gas limit × maximum fee per gas
signer starting balance (wei)  = <approved bounded amount>
payment amount (USDC atomic)   = <approved positive integer>
```

Set `ETH402_MERCHANT_SETTLEMENT_QUOTA=1` and
`ETH402_GLOBAL_SETTLEMENT_QUOTA=1`. If an earlier intent already occupies the
window, wait for the window to expire; do not raise a ceiling merely to make
the test pass. Keep `ETH402_REQUIRED_CONFIRMATIONS=12` unless a separately
reviewed decision changes the finality policy.

The policy signer's fee and gas ceilings must equal the facilitator ceilings.
A lower boundary ceiling causes a liveness failure; a higher boundary ceiling
weakens the protection if the facilitator is compromised.

Fund the signer only after verifying the address and budget out of band. Its
balance must stay within the approved loss bound while covering the intended
transaction under the configured ceiling. Never paste a funding key into an
ETH402 host or shell history.

## Preflight without signing

Two people independently record the result of each check:

1. Confirm the deployed image digest, commit, configuration checksum, migration
   level, KMS key version, policy-signer identity, and facilitator identity.
   Preserve redacted configuration; never preserve secret values.
2. Prove through IAM inspection that only the policy-signer identity can sign
   with the selected KMS key version.
3. Query both RPCs for `eth_chainId`, latest block, canonical USDC bytecode, the
   payer's USDC balance and authorization state, the recipient balance, the
   signer balance, and the signer's pending nonce. Stop on any disagreement.
4. Confirm `/health/ready`, `/supported`, `/status`, Prometheus scrape health,
   worker heartbeats, signer-balance freshness, and alert delivery. `/supported`
   must advertise only v2/exact/`eip155:1`/native USDC/EIP-3009 and the expected
   facilitator transaction signer under `signers["eip155:*"]`.
5. Confirm there are no non-terminal or manual-review payments and no
   unreconciled signer nonces:

   ```sql
   SELECT p.payment_identity, p.state, t.status, t.transaction_nonce, t.tx_hash
   FROM payment_records p
   LEFT JOIN ethereum_transactions t ON t.payment_id = p.id
   WHERE p.state NOT IN
     ('confirmed', 'failed', 'reverted', 'verification_failed', 'expired');
   ```

6. Confirm the active merchant record maps to the approved recipient and that
   the quota counters are empty for the selected window.
7. Rehearse the stop command: set `ETH402_SIGNER_MODE=disabled`, deploy/restart,
   and verify `/settle` returns `settlement_unavailable`. Restore policy mode
   only after the rehearsal is recorded and the preflight is repeated.

Any unexpected row, address, nonce, balance, status, permission, or alert
failure aborts the run.

## Execute exactly one payment

Keep the signed payment body out of chat, tickets, shell history, and logs.
Record only its redacted identity, request IDs, expected amount and recipient,
and eventual transaction hash.

1. The resource server creates requirements for the approved atomic amount and
   recipient. The payer SDK generates one fresh random 32-byte nonce and signs
   the EIP-3009 authorization with enough validity for the configured settlement
   margin and observation window.
2. Submit the payment once to `/verify`. Require HTTP `200` and
   `"isValid": true`. Compare the reported payer and all locally held
   requirements with the approved values.
3. Recheck the payer nonce as unused and the recipient and signer balances on
   both RPCs.
4. Submit the identical serialized payment once to `/settle`. A successful
   response must contain `"success": true`, network `eip155:1`, the exact atomic
   amount, and one transaction hash.
5. If the request times out, returns `broadcast_failed`, or produces any
   ambiguous result, **do not create another authorization and do not send a
   replacement transaction**. Retry only the same idempotent request and follow
   [Runbooks](RUNBOOKS.md), reconciling the signed hash and signer nonce first.
6. Observe the hash independently through both RPCs. Verify chain ID, canonical
   USDC destination, zero ETH value, sender, nonce, fee fields, gas limit,
   `transferWithAuthorization` selector, payer, recipient, amount,
   authorization times, and authorization nonce.
7. Wait for the configured canonical confirmation depth. Require the database
   payment state to become `confirmed`, the authorization nonce to be used,
   the recipient balance to increase by the exact amount, and the payer balance
   to decrease by the same amount.
8. Repeat `/settle` with the identical payment only after the first response is
   known. It must return the same transaction hash and must not allocate a new
   signer nonce or broadcast another transaction.

Stop after that idempotency check. A second payment needs a new change approval
and a fresh preflight; do not raise either quota during the run.

## Abort conditions

Immediately halt settlement by switching to disabled mode and restarting if:

- either RPC disagrees or reports a reorg, unexpected nonce, bytecode, balance,
  transaction field, or receipt;
- the policy signer accepts anything outside its structural USDC call or
  configured ceilings;
- the signer balance, burn rate, or KMS audit trail is unexpected;
- a worker heartbeat stalls, readiness degrades, an alert does not deliver, or
  any payment enters `manual_review`;
- the observed database state differs from the chain;
- any secret or full authorization is disclosed.

Disabling the signer does not erase committed intents. Preserve the database,
logs, RPC responses, KMS audit records, and image/configuration metadata, then
use [Incident response](INCIDENT_RESPONSE.md). Do not rotate the signer while a
nonce is unresolved, do not mutate payment rows by hand, and never resolve an
ambiguous broadcast by guessing.

## Close the run

1. Disable settlement and verify the disabled status before ending the change
   window.
2. Reconcile payer, recipient, and signer balances; the signer pending/latest
   nonce; transaction receipt and canonical block hash; USDC authorization
   state; and every payment/transaction state in PostgreSQL.
3. Account for actual gas against the approved maximum. Investigate every
   difference rather than rounding it away.
4. Preserve a redacted evidence bundle containing timestamps, reviewers,
   commit and image digest, migration level, configuration checksum, addresses,
   approved ceilings, request IDs, transaction hash, confirmations, balance
   deltas, database query results, alerts, and KMS audit references. Do not
   include secrets, signatures, raw authorizations, or signed transaction bytes.
5. Record a go/no-go decision. Re-enabling settlement, increasing quotas,
   topping up the signer, or admitting public traffic is a separate decision,
   not an automatic consequence of a successful dry run.

Execution remains an operator-controlled Milestone 5 item because it requires
funded mainnet accounts, external review, live infrastructure, and an explicit
risk decision.
