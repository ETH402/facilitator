# ETH402 vision

ETH402 makes Ethereum-native machine-to-machine payments accessible without
creating a new payment protocol or taking custody of funds.

The project implements x402 v2 conservatively for one interoperable lane:
exact native-USDC payments on Ethereum mainnet. Narrow scope is a security
feature. Protocol behavior remains separate from merchant identity,
authentication, rate limits, and any future commercial policy.

Success means a developer or autonomous agent can rely on a standards-shaped,
self-hostable facilitator whose behavior is inspectable, reproducible, and
operable without proprietary dependencies. Buyer authorization must constrain
amount, recipient, asset, network, time, and replay nonce. The resulting USDC
transfer must go directly to the merchant.

ETH402 does not claim global merchant uniqueness, business legitimacy, or
Sybil resistance. It supplies practical verification and operator controls
while documenting their limits.
