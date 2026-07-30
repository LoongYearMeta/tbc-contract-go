# Fee Safety Testnet Verification

Date: 2026-07-30  
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
