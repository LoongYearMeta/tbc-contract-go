# StableCoin OrderBook v3 compatibility verification

Date: 2026-07-31 UTC

Reference implementation: `tbc-contract` 1.6.5.

Network/API: TBC testnet, `https://api.tbcdev.org/api/tbc/`.

## Fix

The 2012-byte StableCoin FT code contains the same fill marker used to select the FT v3 swap-unlock format. Go previously returned v2 immediately for every StableCoin script, so OrderBook match omitted the v3 contract-input-index `OP_0`. `MatchOrder` also left the StableCoin FT input sequence at `0xffffffff` instead of the JavaScript value `0xfffffffe`.

The fix:

- inspects the fifth script chunk from the end for both 1884-byte FT and 2012-byte StableCoin code;
- returns `{Version: 3, IsCoin: true}` for the JavaScript 1.6.5 StableCoin fixture;
- sets input 1 sequence to `4294967294` before signatures and contract txdata are built;
- produces the v3-index and coin-mode suffix `OP_0 OP_1` before `preTxData`.

No token-token OrderBook behavior was added or changed.

## Real accepted transaction equivalence

The opt-in read-only test `TestLiveGoStableCoinOrderBookMatchesAcceptedJS165Transaction` refetches the real testnet parents, reconstructs the transaction with Go, executes all four inputs with the Go script interpreter, and requires the exact txid of the already accepted JavaScript/Rust-compatible transaction:

`72c25b7bca31b67d20efc7e50d7276df95c65b84c73681f349c7efee1a5eb804`

The accepted transaction is 8496 bytes, pays 1791 satoshis, and has a 680-satoshi target at 80 sat/KB. Go reconstructs the same raw transaction and txid, so a second broadcast is neither necessary nor possible after the inputs were spent.

The first pre-fix diagnostic transaction `b21d1c85be9c6d78f3b8191afeb6cc187afd63f49f3cf35943f3b683fe8ca7ab` was rejected once by the node with `Invalid OP_SPLIT range` and was not retried.

## Verification commands

```text
TBC_LIVE_STABLE_ORDERBOOK_FIXTURE=1 go test ./test/testnet-parity -run TestLiveGoStableCoinOrderBookMatchesAcceptedJS165Transaction -count=1 -v
go test ./...
go vet ./...
```

All commands exited successfully. The private testnet key was neither printed nor stored in tracked files; this fixture uses only public txids and a deterministic temporary matcher key.
