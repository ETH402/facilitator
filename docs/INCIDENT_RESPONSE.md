# Incident response

1. Triage severity and preserve logs, database snapshots, signed image digest,
   configuration metadata, and RPC evidence without copying secrets.
2. For signer suspicion, stop settlement workers/broadcast immediately,
   isolate the signer, rotate authority, and inspect account nonce/transactions.
3. For database integrity suspicion, make the service read-only/unready,
   preserve WAL/backups, compare append-only histories, and reconcile on-chain.
4. For API-key or email compromise, revoke affected credentials, suspend the
   merchant if needed, and require recipient re-verification.
5. For RPC manipulation, quarantine the provider and cross-check independent
   nodes before changing payment state.
6. Communicate scoped facts; never publish victim secrets or exploitable detail.
7. Recover from known-good artifacts, monitor, write a blameless postmortem,
   and update threat controls/tests.

Never “fix” ambiguous broadcast by sending another transaction without
hash-and-nonce reconciliation.
