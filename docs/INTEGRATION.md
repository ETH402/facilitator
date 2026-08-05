# Public integration guide

ETH402 implements one deliberately narrow payment lane: x402 v2, the `exact`
scheme, Ethereum mainnet (`eip155:1`), and native USDC using EIP-3009
`transferWithAuthorization`. Buyer USDC moves directly to the merchant. The
facilitator never takes custody of it; the facilitator operator pays the
Ethereum gas.

The current build is suitable for local and controlled test environments. It
is **not yet approved for public mainnet payment processing** because the
independent review and controlled funded dry run remain open. See
[Deployment](DEPLOYMENT.md) before self-hosting.

The versioned wire contract is
[`openapi/eth402.yaml`](../openapi/eth402.yaml). Treat it, rather than examples
in prose, as the API source of truth.

## Discover the supported lane

Set the facilitator URL once:

```sh
export FACILITATOR_URL=http://localhost
curl --fail-with-body "$FACILITATOR_URL/supported"
```

Before constructing a payment, confirm that the response advertises:

- x402 version `2`
- scheme `exact`
- network `eip155:1`
- asset `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`
- asset transfer method `eip3009`
- no extensions

Do not silently fall back to another chain, token, scheme, or transfer method.

## Register a merchant recipient

Settlement is admitted only when `payTo` belongs to an active merchant. The
merchant API is separate from x402 protocol authorization.

Start registration:

```sh
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Example Merchant",
    "business_email": "payments@example.com",
    "recipient_address": "0x1111111111111111111111111111111111111111",
    "accept_terms": true
  }' \
  "$FACILITATOR_URL/v1/merchants/register"
```

The `202` response is deliberately the same for a new and an existing valid
registration. Obtain the one-time token from the verification email. In
development, the `log` backend records delivery without the secret body; the default
`file` backend writes it to `ETH402_EMAIL_FILE_DIR`. A frontend receiving the emailed
link must extract its `token` query parameter and submit it to the API:

```sh
export EMAIL_TOKEN='token-from-the-email'
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$EMAIL_TOKEN\"}" \
  "$FACILITATOR_URL/v1/merchants/verify-email"
```

Save the returned `merchant_id`, then request a wallet challenge:

```sh
export MERCHANT_ID='merchant-uuid'
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d "{\"merchant_id\":\"$MERCHANT_ID\"}" \
  "$FACILITATOR_URL/v1/merchants/wallet-challenge"
```

Sign the returned `message` exactly as supplied with the registered recipient
EOA. It is an EIP-4361 message using an EIP-191 signature. Do not reconstruct,
normalize, or reformat it. Submit the unchanged message, challenge ID, and
signature:

```sh
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d @wallet-verification.json \
  "$FACILITATOR_URL/v1/merchants/verify-wallet"
```

`wallet-verification.json` has this shape:

```json
{
  "merchant_id": "merchant-uuid",
  "challenge_id": "challenge-uuid",
  "message": "the exact returned message",
  "signature": "0x65-byte-EIP-191-signature"
}
```

The success response returns the merchant API key exactly once. Store it in a
secret manager. It authenticates merchant account operations:

```sh
export MERCHANT_API_KEY='key-returned-once'
curl --fail-with-body \
  -H "Authorization: Bearer $MERCHANT_API_KEY" \
  "$FACILITATOR_URL/v1/me"
```

It does not authorize `/verify` or `/settle`. Those endpoints follow the
facilitator protocol shape and do not accept merchant credentials.

The same flow is available in the first-party merchant panel at
`/merchant`. The panel does not put integration keys in local or session
storage. Email sign-in creates an HttpOnly browser session, and sensitive panel
operations require a fresh signature by the registered recipient wallet.
Private merchant statistics are disabled by default and begin only when the
merchant explicitly opts in. Public discovery at `/` and `/explore` is a
separate opt-in; it exposes only the merchant's name, declared HTTPS website,
post-consent confirmed-payment count, and last activity date.

Only EOA recipient proof is supported. ERC-1271 contract-wallet recipient
proof is not implemented.

## Verify and settle a payment

Use an x402 v2 SDK to construct and sign the EIP-3009 authorization. Do not
hand-roll EIP-712 signing or money conversion. ETH402 pins and tests the
official x402 Go implementation; an integrator should likewise pin an SDK
version and contract-test its serialized request.

The same outer object is sent to both endpoints:

```json
{
  "x402Version": 2,
  "paymentPayload": {
    "x402Version": 2,
    "accepted": {},
    "payload": {
      "signature": "0x65-byte-EIP-712-signature",
      "authorization": {
        "from": "0xpayer",
        "to": "0xmerchant",
        "value": "1000000",
        "validAfter": "0",
        "validBefore": "unix-seconds",
        "nonce": "0x32-byte-random-nonce"
      }
    },
    "extensions": {}
  },
  "paymentRequirements": {}
}
```

The two `{}` requirement objects above are abbreviations, not a valid payment:
`paymentPayload.accepted` and `paymentRequirements` must be identical and must
contain `scheme`, `network`, `asset`, `amount`, `payTo`,
`maxTimeoutSeconds`, and the USDC EIP-712 domain fields defined in OpenAPI.
The authorization must name the same recipient and exact amount.

Money and timestamps are decimal integer strings. USDC has six decimals, so
`"1000000"` is 1 USDC. Never use a floating-point number for an amount.
Generate a fresh, cryptographically random 32-byte nonce for every
authorization. `validBefore` must leave enough time for the facilitator's
settlement expiry margin and on-chain inclusion.

Write the fully signed request to `payment.json`, then verify it:

```sh
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d @payment.json \
  "$FACILITATOR_URL/verify"
```

HTTP `200` does not itself mean the authorization is valid. Continue only when
the JSON response contains `"isValid": true`. An invalid protocol result also
uses HTTP `200` and includes a stable `invalidReason`. HTTP `400` means the
outer JSON/request shape is invalid; HTTP `503` means verification could not
safely complete.

After a successful verification, submit the **identical serialized payment**
to settlement:

```sh
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d @payment.json \
  "$FACILITATOR_URL/settle"
```

Continue only when the response has `"success": true` and a non-empty
`transaction`. `/settle` waits up to `ETH402_SETTLE_RESPONSE_WAIT` (default 3m)
for the transaction to reach full confirmation depth (12 confirmations by
default). `success: true` therefore means the payment is final at that depth.
If the window elapses first, `success: false` carries
`confirmation_timed_out` and the durable transaction hash while confirmation
continues asynchronously; a transaction confirmed as reverted returns
`success: false` with `transaction_reverted` and the hash.
Repeating the same verified payment is idempotent and returns the recorded
transaction hash; never create a replacement payment merely because the HTTP
client timed out.

A policy refusal uses HTTP `200`, `"success": false`, and a stable
`errorReason`. In particular:

| Reason | Caller action |
|---|---|
| `payment_not_found` or `payment_not_verified` | Send the exact payment to `/verify`; settle only after `isValid=true`. |
| `recipient_not_registered` | Register and activate the `payTo` merchant; do not redirect an existing signed authorization. |
| `authorization_expiring` | Create a new authorization with a fresh nonce and sufficient validity. |
| `merchant_quota_exceeded` | Wait for the merchant quota window; do not retry in a loop. |
| `facilitator_quota_exceeded` | Wait for operator capacity to reset or use another explicitly trusted facilitator. |
| `simulation_reverted` | Treat the authorization as unsafe to broadcast and investigate its on-chain state. |
| `transaction_reverted` | The broadcast transaction reverted on chain; inspect the returned hash. Do not retry the same authorization — its nonce may be consumed. |
| `confirmation_timed_out` | Retry the identical payment to observe its durable outcome. Do not create a new authorization while the returned transaction remains unresolved. |
| `broadcast_failed` | Query/retry the same payment; do not generate a second authorization until its state is known. |
| `settlement_unavailable` | Stop automatic settlement and alert the operator. |

HTTP `503` is also a dependency or signer availability failure. Preserve the
response `X-Request-ID` in support and incident reports.

## Integration invariants

- The resource server decides the exact atomic amount and active merchant
  recipient. A facilitator's successful verification is not a merchant
  legitimacy endorsement.
- `paymentPayload.accepted` and `paymentRequirements` must describe the same
  requirements. ETH402 rejects semantic mismatches.
- The payer must be an EOA. ERC-1271 and ERC-6492 payer signatures, Permit2,
  extensions, and non-USDC assets are rejected.
- Never log raw API keys, email tokens, wallet signatures, payer
  authorizations, private keys, or funded test keys.
- Cache neither a successful `/verify` result nor `/supported` indefinitely.
  Nonce state and service availability change.
- A client timeout is ambiguous. Retry the same idempotent request and reconcile
  the transaction hash before taking any action that might authorize a second
  payment.

## Self-hosting checklist

1. Follow [Local development](LOCAL_DEVELOPMENT.md) for a non-funded
   environment and [Deployment](DEPLOYMENT.md) for the production design.
2. Apply every SQL migration before the application rollout:
   `migrate up` for the built migration binary, `go run ./cmd/migrate up` from
   source, or `make migrate-up` in Compose. Migrations
   `000008_merchant_fair_use` and
   `000009_payment_retention` are required by this build.
3. Configure two authenticated Ethereum RPCs, managed PostgreSQL with TLS and
   recovery, secrets, monitoring, and the policy signer. Never use a funded raw
   private key in configuration, tests, fixtures, documentation, or logs.
4. Set a positive `ETH402_MERCHANT_SETTLEMENT_QUOTA` and an
   `ETH402_GLOBAL_SETTLEMENT_QUOTA` greater than or equal to it. These bound
   settlement intent admission; signer fee and gas ceilings bound the ETH
   exposure per intent.
5. Configure the provider-neutral SMTP sender with mandatory STARTTLS or
   implicit TLS before selecting `ETH402_ENV=production`. The bundled `log`
   and `file` adapters remain development-only, and configuration validation
   rejects them in production.
6. Keep settlement disabled until the signer boundary, gas ceilings, alert
   delivery, incident drills, limited funded mainnet dry runs, and an
   independent security review are complete.

Operational behavior and recovery procedures are documented in
[Operations](OPERATIONS.md), [Runbooks](RUNBOOKS.md), and
[Incident simulations](INCIDENT_SIMULATIONS.md). The first funded transaction
must follow the operator-controlled
[limited mainnet dry-run procedure](MAINNET_DRY_RUN.md).
