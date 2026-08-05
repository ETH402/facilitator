# Registration flow

```mermaid
sequenceDiagram
  participant M as Merchant
  participant E as ETH402
  participant Mail as Email adapter
  participant DB as PostgreSQL
  M->>E: registration + terms acceptance
  E->>DB: pending merchant + hashed token + encrypted delivery outbox + audit
  E->>Mail: claim outbox item and submit one-time verification link
  Mail-->>E: accepted or transient failure
  E->>DB: accepted: sent_at + erase ciphertext; failed: schedule retry
  M->>E: raw token
  E->>DB: hash raw token, match stored hash, and consume once
  M->>E: connect desired recipient wallet
  E->>DB: optionally replace pending recipient + create hashed SIWE challenge
  M->>E: signed SIWE message
  E->>E: parse, bind domain/merchant/address/action/chain/time
  E->>DB: consume once, activate, create API key hash + audit
  E-->>M: full API key once
```

The browser flow continues at `/merchant`. Consuming the emailed token also
creates an unprivileged admin-session cookie. The initial recipient signature
activates the merchant and elevates that session. Later email sign-ins require a
fresh `authenticate-admin` recipient signature before the panel may show private
statistics or manage API keys. See [ADR-0005](decisions/0005-merchant-admin-sessions-and-private-stats.md).

Before activation, the email-authenticated merchant may replace the unverified
recipient by connecting a different account and explicitly confirming the old
and new addresses. Replacement does not activate the account: the new wallet
must sign, and all challenges issued for prior pending addresses become stale.

Email responses are enumeration-resistant. Resends and registrations are
throttled. A live pending delivery suppresses duplicates, while the normal
resend cooldown starts only after the mail adapter accepts the message. SMTP
failure never leaves a misleading `sent_at`: the same one-time token is retried
from a leased durable outbox with bounded exponential backoff. The raw token is
AEAD-encrypted under the dedicated email-outbox key only while delivery is
pending, bound to its merchant/hash/message kind, and erased after delivery or
expiry; verification stores and compares only its SHA-256 hash.
Disposable/free-provider and domain lists are operator controls,
not proof of legitimacy. A determined actor can create multiple accounts.

After activation, recipient changes are available through either API-key
authentication or a wallet-elevated panel session. Both require a fresh
challenge signed by the new address, the policy cooldown, append-only history,
and an audit event. A panel change atomically revalidates the initiating session
and elevates only that session for the new recipient. The
cooldown is measured from the most recent verified recipient proof in
`recipient_address_history`, so unrelated merchant writes such as operator
suspension and reinstatement neither extend nor reset it.

See the [public integration guide](INTEGRATION.md) for request examples and the
versioned [OpenAPI contract](../openapi/eth402.yaml) for exact schemas.
