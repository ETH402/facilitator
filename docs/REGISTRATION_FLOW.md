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
  M->>E: request recipient challenge
  E->>DB: hashed SIWE challenge, nonce, expiry
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

Recipient changes require API-key authentication, a fresh challenge for the
new address, policy cooldown, append-only history, and an audit event. The
cooldown is measured from the most recent verified recipient proof in
`recipient_address_history`, so unrelated merchant writes such as operator
suspension and reinstatement neither extend nor reset it.

See the [public integration guide](INTEGRATION.md) for request examples and the
versioned [OpenAPI contract](../openapi/eth402.yaml) for exact schemas.
