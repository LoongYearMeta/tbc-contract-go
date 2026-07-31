# Fee Safety Testnet Verification

Date: 2026-07-30, updated 2026-07-31
Network: TBC testnet

The runtime WIF was injected only into an interactive process, was not written
to this repository or report, and was unset after broadcasting.

## Fee policy

Contract transactions must pay:

```text
max(80, ceil(actual_signed_bytes * 80 / 1000)) satoshis
```

Metrics below were independently reconstructed after broadcast by fetching
each transaction and every parent output from the testnet API.

| Scenario | Txid | Bytes | Paid sat | Target sat | sat/kB | Status |
|---|---|---:|---:|---:|---:|---|
| Token A mint source | `f1a6f8bc0c67aae304694769f61f09a238b15dd72f17876401999b6403e13e26` | 320 | 80 | 80 | 250.00 | accepted |
| Token A mint | `0a600c8b0f6cf4a396c4cebd237096014eefc915918a81df1cc36aea990d1280` | 2168 | 174 | 174 | 80.26 | accepted |
| Token B mint source | `5248df854316cc893ef914e812ec631bd3c788de140dfd0a1f4867fcb002c4ca` | 321 | 80 | 80 | 249.22 | accepted |
| Token B mint | `f572760f4737e17b2c2c810da27aae3950d783e13d0118a338263862dd8a99de` | 2169 | 174 | 174 | 80.22 | accepted |
| FT v3 self-transfer | `329f25407a77d402ec46ba191a50f7d5b2b2cf6c9c1b6ca37ec74fcd00e9abcd` | 5225 | 419 | 418 | 80.19 | accepted |
| Token HTLC withdraw deploy | `628744d66c55a70f575bb1e8b3c73afd9da922e5a8dd7eaa338409d7eff01d4f` | 5548 | 510 | 444 | 91.93 | accepted |
| Token HTLC withdraw | `0081aa6e288a2577c5c6f386823dbfdf90793dbedd02bcc57f6f2174cd7977fd` | 3987 | 423 | 319 | 106.09 | accepted |
| Token HTLC refund deploy | `df86b4c706bf80570e886e47a6801b923f9a006a726b1b5358aa6c202d5119a9` | 5668 | 510 | 454 | 89.98 | accepted |
| Token HTLC refund | `ddcd85179015156ce803dc665699679a826be15b84d257e38e8356b33fd8e5c9` | 3957 | 423 | 317 | 106.90 | accepted |

Token A contract:
`0a600c8b0f6cf4a396c4cebd237096014eefc915918a81df1cc36aea990d1280`

Token B contract:
`f572760f4737e17b2c2c810da27aae3950d783e13d0118a338263862dd8a99de`

## Runtime regression found

The first FT self-transfer build exposed a convergence edge case before any
transaction was broadcast: DER signature length alternated as fee change was
updated, causing the exact-fee target to oscillate by one satoshi.

The finalizer now keeps a monotonic high-water mark of every observed target
fee. A deterministic regression test reproduces the alternating 80/81 satoshi
case and verifies that the final paid fee remains greater than or equal to the
target.

## Expanded real-broadcast matrix

The following 25 additional transactions were signed locally and accepted by
the TBC testnet node. Together with the nine transactions above, this report
contains 34 accepted broadcasts.

| Contract family | Operation | Txid | Bytes | Paid sat | Target sat | sat/kB | Status |
|---|---|---|---:|---:|---:|---:|---|
| P2PKH | Self-transfer | `e78c864be4eaba5bbe5888cbee5e1e3e97c0b8ee7921aaa6e4fd6e6c2c99703a` | 226 | 80 | 80 | 353.98 | accepted |
| NFT | Create collection | `9302cfef436717ac15d80ca79110c9eed182596bdca92d2d55871de116a05846` | 358 | 80 | 80 | 223.46 | accepted |
| NFT | Mint | `0b808f74e72d0575b1830a5a47a298d67d8c4d69e5a2f9216b1405087aa8c7ab` | 756 | 80 | 80 | 105.82 | accepted |
| NFT | Self-transfer | `979632b18a2cdd1f1bba563b4b832d32a336a54d0a6b0659d1355fc6da55a0dc` | 1583 | 127 | 127 | 80.23 | accepted |
| MultiSig | Create 2-of-3 wallet | `ce5d3e92c9005a6d6df5dd39d9b2283795952982a1a0bc05e020a581f4e1290d` | 663 | 80 | 80 | 120.66 | accepted |
| MultiSig | Spend to testnet P2PKH | `44b037b401543914940f96a9e7d7b4584aaa51744a80cda8fa173816107d6544` | 382 | 1000 | 80 | 2617.80 | accepted |
| PiggyBank | Freeze | `3961fe3e044a2c4dcbff354b38fc6f7c7171bb1f76be9a9cc58258b142d2beec` | 254 | 80 | 80 | 314.96 | accepted |
| PiggyBank | Unfreeze | `1e05283fbeb35abffba4fa188d685c6ab26b0b72e0dd45c6d32b8d5170126d79` | 191 | 80 | 80 | 418.85 | accepted |
| Base HTLC | Withdraw deploy | `d4494da93ad05e9a90441228cb8530f98911747984c8d46b915038cbde0a4823` | 338 | 80 | 80 | 236.69 | accepted |
| Base HTLC | Withdraw | `b602028bf8bb0bc6402fe679231ec2733f5efd0286fd2ba2e48ffbcb9caec0db` | 225 | 80 | 80 | 355.56 | accepted |
| Base HTLC | Refund deploy | `1553d1e54274f992218dc91c7417647449d68d1274f1563eef0279dfbfb352fd` | 338 | 80 | 80 | 236.69 | accepted |
| Base HTLC | Refund | `c10c860098f1d2cd5e64657bfd797d3b356b86f999576231ca063ec5c8947680` | 193 | 80 | 80 | 414.51 | accepted |
| Token OrderBook | Create order for cancellation | `d96b2d6acc50b4ca5b0630868bb523a804241552548f00557683b059c774f521` | 6790 | 605 | 544 | 89.10 | accepted |
| Token OrderBook | Cancel | `861993121d94734ae2e7bda41f3e45adccf5dfe469e037a8c53acc753b8e8d72` | 3956 | 343 | 317 | 86.70 | accepted |
| Token OrderBook | Create sell order | `0f2c990cf302f2659c21fc9d04bea324cdab4ad6bb80231984806f4b090d4aa7` | 4693 | 447 | 376 | 95.25 | accepted |
| Token OrderBook | Create buy order | `fc8de7be6f8b6cda1a0a1195dc4c283daeeacc5c59e96c4d2deaafb792176c2d` | 6788 | 605 | 544 | 89.13 | accepted |
| Pool v2 | Foundation source | `d7201e18fa656aa2910642c42f3e350f4f8a9e63c4091ec23ea4546e28613a8c` | 243 | 80 | 80 | 329.22 | accepted |
| Pool v2 | Foundation mint | `96d3fb5f8ba8fa7abaa8adbc37f9cab33b3be242530117068dc6b67da4e8a282` | 3644 | 320 | 292 | 87.82 | accepted |
| Pool v2 | Initialize FTLP | `9c5f40f00de2105dd72558bd55a35a7c5697299324aa43f61f1d223e297d3187` | 13072 | 1046 | 1046 | 80.02 | accepted |
| Pool v2 | Consume FTLP | `5049f157f1c99ab06911c08b96e4c79af5748af107b672d9aefcce401aabd887` | 19087 | 1528 | 1527 | 80.05 | accepted |
| StableCoin | Create coin NFT | `7e1687bcfa0cced4613cfe7d7a1981d0d767bd1ec49df7e25aa2bc61e180a4d4` | 723 | 80 | 80 | 110.65 | accepted |
| StableCoin | Mint | `dd9179753d18568b88b497466379d7f5cd25e2fe7c6c7cf8887af7a9d2dc1a65` | 3912 | 314 | 313 | 80.27 | accepted |
| StableCoin | Owner transfer | `7e09400238efa8877fe8bd5976043544f5cc04ad84865a78f627b93c4204fdc2` | 5745 | 460 | 460 | 80.07 | accepted |
| StableCoin | Freeze | `9ee2297bb4cc1bb97a7e3687fed17c19466700b1e0f390ec1f746b30528921a6` | 3483 | 279 | 279 | 80.10 | accepted |
| StableCoin | Unfreeze | `a5caadc09fc552ac27828c971be4e1343317ac5403750c5bcf76b4da80c5031e` | 3320 | 266 | 266 | 80.12 | accepted |

All 25 accepted transactions satisfy the signed-size fee target. The MultiSig
spend retains its legacy fixed 1000 satoshi fee, which is safe but materially
overpays relative to the current target.

## Defects exposed by node broadcast

Real node submission found three implementation defects that construction-only
tests had missed:

1. A small P2PKH transaction initially paid only its proportional 23 satoshi
   fee and was rejected with `insufficient priority`. `tbc-lib-go` now applies
   the node minimum of 80 satoshis to change calculation. The P2PKH row above is
   the accepted retest.
2. MultiSig originally pushed the three public keys separately. The contract
   expects one concatenated public-key blob and rejected the spend with
   `Invalid OP_SPLIT range`. The unlocking script and testnet P2PKH recipient
   detection were fixed; the create/spend rows above are the accepted retest.
3. StableCoin freeze/unfreeze double-encoded the Schnorr signature push length,
   causing `Non-canonical DER signature`. The admin path now passes raw 65-byte
   signature data to the script builder; the freeze/unfreeze rows above are the
   accepted retest.

## Token-to-token OrderBook resolution

The earlier `Invalid OP_SPLIT range` blocker was resolved. Two independent
defects had been hidden behind the same live-node rejection:

1. The JavaScript 1.6.5 TokenOrder template discarded the computed settlement
   output hash before comparing it. Repaired buy/sell templates now keep all 12
   serializer slots and compare the computed hash.
2. FT v3 hard-coded the contract parent at current input 0. In an atomic
   Token A/Token B match, the second independently created order uses contract
   input 2, so its FT covenant could never authenticate the correct parent.
   FT v4 selects contract input 0 or 2 from the current FT input index while
   preserving the partial-hash boundary used by dependent contracts.

Existing FT v3 token IDs are immutable and cannot use the independent-order
match path. The successful retest therefore minted two FT v4 test tokens and
created fresh orders:

Token A contract:
`fdc2954f91e1ab7c052cbebc15b9277f7ece84a6ffec8e0420fd2061b3a6b79c`

Token B contract:
`68e0da0429e1747a5cf41812f0951b4daef9c2e68b6cb5c97448d1fb0d52463f`

| Operation | Txid | Bytes | Paid sat | Target sat | Status |
|---|---|---:|---:|---:|---|
| Token A v4 mint source | `4276b84eb8140081bebd3ec72c693140fc6be5fbe06d5770799a48cdeac56c73` | 321 | 80 | 80 | accepted |
| Token A v4 mint | `fdc2954f91e1ab7c052cbebc15b9277f7ece84a6ffec8e0420fd2061b3a6b79c` | 2233 | 179 | 179 | accepted |
| Token B v4 mint source | `3016d6ddd0efa57de5245b90b291628948cc14260cce8d7298998ab5fb71a755` | 320 | 80 | 80 | accepted |
| Token B v4 mint | `68e0da0429e1747a5cf41812f0951b4daef9c2e68b6cb5c97448d1fb0d52463f` | 2232 | 179 | 179 | accepted |
| Buyer/seller/matcher role funding | `15999011b3f941e258bdbc5bbdfe7d420f6f5eb60a6fcad22d2195142386737d` | 260 | 80 | 80 | accepted |
| Split Token A to seller | `d797b61e75ee1af9c2c6ab4890bfc136633b8500fdb69f64f56f78f9f087324b` | 5289 | 424 | 424 | accepted |
| Split Token B to buyer | `164787d482d1c29bc68b92969a2bf128cbda6516e87cb94bf597d011adef46b9` | 5289 | 424 | 424 | accepted |
| Create sell order | `fe73195de02618c9245ae084cba44f7452f49ebdb0c82280e2dd4a285939422e` | 4677 | 452 | 375 | accepted |
| Create buy order | `a765471144735a3541a5d84174cb57e6811f31c078efe3bd47b03a2598f928cd` | 4678 | 452 | 375 | accepted |
| Fresh matcher funding | `b626822bf1264eb1866fd23bf6afd7bea6836738ea07540ff0542b388f342a60` | 225 | 80 | 80 | accepted |
| Atomic Token A/Token B match | `f2d303b2c1f600132e7eae9b672d54183c9737ff55b6a30f1d402c3eef72050a` | 14909 | 1193 | 1193 | accepted |

Before the accepted match, the exact same raw transaction passed the script
interpreter independently for all five inputs. After broadcast, it was fetched
back from the testnet API and its five parent references, nine outputs, asset
amounts, and fee were reconstructed. Outputs paid 990,000 Token B to the
seller, 990,000 Token A to the buyer, and 10,000 of each token to the fee
address.

The first v4 match build was intentionally not broadcast because final signed
size was 14,908 bytes but it paid only 1,165 satoshis instead of the required
1,193. `MatchTokenOrder` now iteratively adjusts the fee change and rebuilds
both TokenOrder unlocks, both FT unlocks, and all ordinary signatures until the
actual serialized-size target is met. A regression test reproduces the
underpayment on a 19,085-byte fixture.

Post-broadcast review also added construction guards for two adjacent cases:
ordinary FT v1-v3 is rejected before creating a new TokenOrder, and a
fractional-price partial fill is rejected when per-fill integer rounding would
leave more Token B than the residual order represents. Compatible fractional
full and partial fills pass the same five-input interpreter suite. PoolNFT2's
standard and lock-time FTLP templates were also extended to the FT v4
1948-byte/1920-byte boundary so newly minted v4 tokens do not enter a legacy
FTLP branch.

## Rust SDK comparison

The local Rust SDK uses `DEFAULT_FEE_RATE_SAT_PER_KB = 80` and computes:

```text
max(fee_rate_sat_per_kb, ceil(actual_signed_bytes * fee_rate_sat_per_kb / 1000))
```

This matches the 80-satoshi absolute minimum now enforced in `tbc-lib-go`.
Rust FT/NFT/StableCoin/OrderBook builders also iterate using the actual signed
serialized transaction size and checked change. Its SDK dust constant is 24
satoshis, while the JavaScript-compatible Go library dust constant is 42
satoshis and the node dust limit is tracked separately as 10 satoshis.

The Rust OrderBook implementation represents the older TBC-to-FT order family,
not the JavaScript Token A/Token B matcher. It therefore confirms the fee
strategy but cannot validate the TokenOrder template or FT v4 covenant changes.
