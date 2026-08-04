# ADR 0005: Merchant admin sessions and opt-in private statistics

Status: accepted — 2026-08-04.

ETH402 serves a self-contained merchant panel from the facilitator origin. An
emailed one-time token creates a random, hashed, expiring admin session carried
in an `HttpOnly`, `Secure`, `SameSite=Strict` cookie. The cookie
is not an integration credential and is never exposed to JavaScript.

Email proves mailbox control only. A new admin session is therefore
**unprivileged** until the registered recipient wallet signs a fresh EIP-4361 /
EIP-191 challenge with the `authenticate-admin` action. Initial merchant
activation satisfies both recipient verification and session elevation in one
signature. API-key management, private statistics, and statistics consent all
require an elevated session. A later recipient change invalidates prior session
elevation because its timestamp predates the new recipient proof. This preserves
the existing email-plus-wallet
security boundary and prevents a mailbox compromise from minting an API key.

Merchant statistics are first-party, private, and disabled by default. Opt-in
records its timestamp. Queries include only payments created after that time and
still attributable inside the configured payment-retention window; opting out
immediately removes access and stores no separate aggregate. Required payment,
idempotency, audit, and retention records are protocol/operational data and are
not represented as optional analytics. There are no third-party scripts,
trackers, fonts, or analytics services.

The admin surface remains in the merchant/authentication module. It does not
alter `/verify`, `/settle`, x402 schemas, settlement admission, signer policy,
or the Ethereum-mainnet/native-USDC scope.
