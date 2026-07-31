# FT v3 Restoration Testnet Verification

Date: 2026-07-31  
Network: TBC testnet  
Test address: `miZGyNMbgfWNZdfQFYHKiL4ytJymC3nizd`

The approved testnet key was supplied only through `TBC_TESTNET_WIF` at
runtime. It is not stored in this repository or report.

## Protocol checks

- Both newly minted FT code outputs were exactly 1884 bytes, matching the
  released FT v3 template and its 1856-byte partial-hash boundary.
- The FT v3 mint template matched the JavaScript 1.6.5 fixture in unit tests:
  1884 bytes and SHA-256
  `93a0613a9eb50f7c42059ebb517b8ad023fde2e3587cd86fe34ce3f5cb214f62`
  for the fixed parity inputs.
- Every transaction below was broadcast to testnet and immediately fetched
  back through `FetchTXRaw`; every returned and refetched txid matched the
  locally serialized transaction.
- The freshly minted FT v3 output was then spent in a self-transfer and both
  HTLC withdraw and refund paths, proving that the restored output is
  spendable rather than construction-only.

Token A contract:
`a62f87ec5e4f8cb031c0b00e638ce2112cbde2dff3efacd0476acb9dd315caef`

Token B contract:
`3a771a2596bcb91f6e84736d681bffbd475f130b734580ea6a434ce979e530ef`

## Broadcast and fee evidence

The target fee is `max(80, ceil(actual_signed_bytes * 80 / 1000))`
satoshis. Fees were independently reconstructed after broadcast by fetching
each transaction and every parent output.

| Operation | Txid | Bytes | Paid sat | Target sat | Status |
|---|---|---:|---:|---:|---|
| Token A mint source | `2afd4cd010863024e98904f0d0d3a612a1e36e2b59eae78bbf58823c0907af31` | 321 | 80 | 80 | accepted/refetched |
| Token A FT v3 mint | `a62f87ec5e4f8cb031c0b00e638ce2112cbde2dff3efacd0476acb9dd315caef` | 2168 | 174 | 174 | accepted/refetched |
| Token B mint source | `e6188742d57185265c4ba6d003812cce610eb1f1e570f19b095b41c41f19e11f` | 320 | 80 | 80 | accepted/refetched |
| Token B FT v3 mint | `3a771a2596bcb91f6e84736d681bffbd475f130b734580ea6a434ce979e530ef` | 2168 | 174 | 174 | accepted/refetched |
| FT v3 self-transfer | `da6c1786e9ce35c492fbfe1ebc5cf86e566752baa6e666086c400caf9e57b624` | 5226 | 419 | 419 | accepted/refetched |
| Token HTLC withdraw deploy | `08cda4fab134453c3c5ca00778fc2a4ef5aea618a38597e98dfa2f1259c742e5` | 5547 | 510 | 444 | accepted/refetched |
| Token HTLC withdraw | `39fea98ddc9d728d8239b87fd80415f7c36c129641118ddc94e9f296b42983f2` | 3989 | 423 | 320 | accepted/refetched |
| Token HTLC refund deploy | `89179d6463a3d52f0e06a7de1761b8f6ea728c431eaf0ce31bfc5e83cc414c69` | 5667 | 510 | 454 | accepted/refetched |
| Token HTLC refund | `637661e47f9b73b0ebd8517bd122fd9923ecf0f04e12d645d45c4af05b5336af` | 3956 | 423 | 317 | accepted/refetched |

All nine transactions meet or exceed the current testnet fee requirement.
