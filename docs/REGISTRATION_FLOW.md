# Registration flow

```mermaid
sequenceDiagram
  participant M as Merchant
  participant E as ETH402
  participant Mail as Email adapter
  participant DB as PostgreSQL
  M->>E: registration + terms acceptance
  E->>DB: pending merchant + hashed email token + audit
  E->>Mail: one-time verification link
  M->>E: raw token
  E->>DB: hash raw token, match stored hash, and consume once
  M->>E: request recipient challenge
  E->>DB: hashed SIWE challenge, nonce, expiry
  M->>E: signed SIWE message
  E->>E: parse, bind domain/merchant/address/action/chain/time
  E->>DB: consume once, activate, create API key hash + audit
  E-->>M: full API key once
```

Email responses are enumeration-resistant. Resends and registrations are
throttled. Disposable/free-provider and domain lists are operator controls,
not proof of legitimacy. A determined actor can create multiple accounts.

Recipient changes require API-key authentication, a fresh challenge for the
new address, policy cooldown, append-only history, and an audit event.
