# Key management

The facilitator settlement key pays gas and authorizes only outer Ethereum
transactions. Buyer EIP-3009 authorization constrains the USDC movement, but a
compromised signer can still drain its ETH and call arbitrary contracts.

Production must use an external signer with policy enforcement for chain ID 1,
canonical USDC destination, allowed calldata selector, zero ETH value, gas
limits, rate limits, and audited access. Separate staging and production keys.
Use dual control for policy changes and emergency disable.

Raw private keys are permitted only in explicit development/test mode and are
never printed, stored in database, embedded in images, or committed. No funded
key appears in this repository. Rotation must coordinate pending Ethereum
nonces; never abandon transactions silently.
