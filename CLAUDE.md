# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository purpose

Go implementation of TBC (TuringBitChain) on-chain contracts and the indexer/API client. **Strict 1:1 parity with the TypeScript reference repo `tbc-contract`** (sibling at `../tbc-contract`). Whenever behavior is ambiguous, the TS source (`../tbc-contract/lib/...`) is authoritative — new Go code must reproduce JS wire output byte-for-byte (sizes, sighash preimages, push encodings, fees, change).

This is a **library-only** repo: no `cmd/`, no `*_test.go`. Behavior is verified in downstream business repos using the `package main` templates under `docs/test-cases/`.

## Build & develop

```bash
go build ./...        # the only check this repo runs
go vet ./...
```

`go mod tidy` will fail because the build-tagged `stablecoin.go` / `api_stablecoin.go` reference symbols that no longer exist (see "stablecoin" below). Don't run tidy unless you've also re-enabled stablecoin; the current `go.mod` is hand-tuned and correct.

**Sibling dependency:** `go.mod` has `replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go`. The build will fail without that sibling. `tbc-lib-go` itself is a single self-contained module — `libsv/go-bk` and `sCrypt-Inc/go-bt/v2` are NOT dependencies and must not be re-added.

Go toolchain: **`go 1.17`** (see `go.mod`). Don't bump without coordinating with callers.

## Architecture

Three flat packages under `lib/`, each a single Go package; structure mirrors `tbc-contract/lib/` 1:1:

- **`lib/contract`** — one file per contract family: `ft.go`, `nft.go`, `orderbook.go`, `multisig.go`, `htlc.go`, `piggybank.go`, `poolnft2.go`, `stablecoin.go`. Script templates live next to them as `.asm` / `.txt` / `.tmpl` files and are pulled in via `//go:embed` — edit those raw files to change scripts, don't inline the bytes. `poolnft2.go` has five embedded ASM templates (`poolnft2_code.tmpl`, `poolnft2_ftlp_code.tmpl`, `poolnft2_ftlp_locktime_code.tmpl`, `poolnft2_lock_code_pre.tmpl`, `poolnft2_lock_code_last.tmpl`) — the inline ASM strings these replaced were severely truncated relative to TS and should not be reintroduced.

- **`lib/api`** — HTTP client for the TBC indexer, split by topic: `api.go` (general), `api_ft.go`, `api_nft.go`, `api_pool.go`, `api_frozen.go`, `api_umtxo.go`, `api_stablecoin.go`. Base URLs are selected by a `network` string:
  - `"mainnet"` / `""` → `https://api.turingbitchain.io/api/tbc/`
  - `"testnet"` → `https://api.tbcdev.org/api/tbc/`
  - anything ending in `/` is treated as a custom base URL.
  Transient GETs auto-retry on a small allowlist of network errors (`isRetryableHTTPGetErr` in `api.go`). POST broadcasts MUST NOT retry. Every successful `json.Unmarshal` of an indexer response is followed by `apiCodeError(body)` to surface 200-with-non-200-code envelopes.

- **`lib/util`** — six files mirroring `tbc-contract/lib/util/`:
  - `util.go` — `FtUTXO` type, `BuildUTXO`, `BuildFtPrePreTxData`, `ParseDecimalToBigInt`, `NFTInfo`/`NFTBrief` (defined here, not in `lib/contract` or `lib/api`, to avoid an api↔contract import cycle).
  - `ftunlock.go` — six FT preimage helpers (`GetPreTxdata`, `GetPrePreTxdata`, `GetCurrentTxdata`, `GetCurrentInputsdata`, `GetContractTxdata`, `GetSize`).
  - `nftunlock.go` — NFT-side preimage helpers + Go-only `NftEncodeMinimalPushData`. **Critical:** any push-data emission for NFT / coin-NFT unlocks must route through `NftEncodeMinimalPushData`, not raw `bscript.AppendPushData` — TBC enforces `SCRIPT_VERIFY_MINIMALDATA` on unlock scripts and the SDK's append doesn't enforce minimal encoding for small integers.
  - `orderbookunlock.go` — orderbook preimage helpers.
  - `poolnftunlock.go` — pool preimage helpers (~1100 lines, includes the byte-for-byte port of TS `getCurrentTxOutputsDataforPool2` covering case 1/2/3-option1/3-option2/4 with `IsServiceFeePkh`-aware trailing-output discrimination). Constants `ServiceFeeAddress[1..5]` for the five service-fee destinations.
  - `utxoselect.go` — `FindMinFiveSum` selection helper.

## Parity discipline

These rules collectively prevent regressions of bugs that were already found and fixed once.

### Imports & types

- The package alias for the SDK is always `bt "github.com/LoongYearMeta/tbc-lib-go"` (root facade). Do not import `tbc-lib-go/transaction` directly except where you specifically need a type that the facade doesn't re-export.
- `bt.FtUTXO` and `bt.GetPreTxdata` and friends do **not** exist in `tbc-lib-go` — those types/functions live in `lib/util` here. Don't reach for them under `bt.`.
- All FT amounts are `*big.Int`. There is no `Transfer(float64)` / `TransferDecimalString(string)` overload pair. Decimal-string callers go through `util.ParseDecimalToBigInt`.

### Transactions

- Every contract tx must have **`tx.Version = 10`** (TBC node requirement; `bt.NewTx()` defaults to 1). Use `newFTTx()` (in `ft.go`) — every `bt.NewTx()` outside that one helper is a bug. ft.go / nft.go / orderbook.go / multisig.go / htlc.go / piggybank.go / poolnft2.go all route through `newFTTx()`.
- **Size estimation goes through `(*bt.Tx).JSEstimateSize()`** (the JS-aligned estimator in tbc-lib-go), not `tx.Size()`.
- **Change/fee adjustment is pre-signature, not post.** Sign once after `tx.ChangeToAddress(addr, newFeeQuote80())` (or after the manual placeholder-output + JSEstimateSize sequence used in `MintFT`). Do NOT compute `len(tx.Bytes())` after signing and re-adjust — the actual signed P2PKH unlock is ~110 B but TS estimates 180 B per input, and a post-sign re-adjustment produces hex that diverges from JS by 2-6 sat per input.
- **Set `tx.LockTime` and per-input `SequenceNumber` BEFORE calling `ft.GetFTunlock` / `signP2PKHAtIdx`.** The sighash preimage embeds both fields; setting them after the unlocker would invalidate every signature.
- **`bt.UTXO.TxID` is forward (display-order) bytes** throughout this repo. `hex.DecodeString(tx.TxID())` gives the right bytes; do NOT reverse them. Three pool sites previously had this bug.
- `BuildFTtransferCode` returns `(*bscript.Script, error)`. There is no byte-offset fallback for "v1 layout" — the chunks-walk path is the only correct path; a fallback that patches `[1537:1558]` would silently corrupt FT-LP scripts (which have different padding offsets).

### Pool-specific

- `calculatePartialHash` in `poolnft2.go` MUST call `partialsha256.CalculatePartialHash` (the JS-aligned mid-state hasher). A plain `crypto.Sha256` here makes every pool created via `CreatePoolNFT` unspendable — the pool's on-chain `OP_PARTIAL_HASH` check would fail.
- `lpPlan` precedence: **instance value wins over caller param.** TS: `lpPlan = (this.lp_plan in [1..5]) ? this.lp_plan : (lpPlan || 1)`. A pool initialized from chain with `LpPlan=2` must NOT be overridable by a caller passing `1`.
- `adjustFeeAndChange` uses fee schedule `size < 1000 ? 80 : ceil(size * 80 / 1000)` (TS `Math.ceil`) and **drops the trailing change output entirely if it would be below 42 sat** (tbc-lib-js `Transaction.DUST_AMOUNT = 42`, NOT tbc-lib-go's `DustLimit = 1`). Hex parity with JS depends on this dust threshold.
- `mergeFTinPoolSingle` uses `vout=4` for the chained-fee input from `poolnftPreTX` (TS hardcodes this). `len(Outputs)-1` would silently pick the merged FT tape if a previous iteration's adjust had dropped the change.
- `updatePoolNftTape` patches **only `chunks[3].Buf`** of the live tape (the 24-byte amount block) and re-serializes via `bscript.FromChunks`. It must NOT recompute the tape from instance state via `GetPoolNftTape` — instance fields are caches and can drift from the on-chain truth.

### Satoshi conversions

- Every `*big.Int → uint64` conversion that ends up in `bt.Output.Satoshis` (or compares against `utxo.Satoshis`) goes through `bigIntToSats(n, label)` (in `poolnft2.go`). Plain `n.Uint64()` silently truncates on overflow — catastrophic for misconfigured pool amounts.

### Errors

- Errors return as `(value, error)`. No `panic` in library code. `BuildFTtransferCode` and `NftEncodeMinimalPushData` (and the eight pool-side `bigIntToSats` callers) all surface errors rather than panicking on bad input.
- `signP2PKHInput` returns an error when the previous-tx P2PKH points at a different pubkey hash. A silent no-op there produces an unsigned input the mempool rejects with no caller-visible cause.

## Stablecoin (frozen)

`lib/contract/stablecoin.go` and `lib/api/api_stablecoin.go` carry `//go:build legacy_stablecoin` and are excluded from the default build. They reference the pre-refactor shape (e.g. `bt.FtUTXO`, `adjustFTTransferChangeFee`, deleted ft.go internals) and won't compile even with the build tag — they're parked as reference material for a future stablecoin rewrite.

When the rewrite happens: don't lift code 1:1, port from TS `tbc-contract/lib/contract/stableCoin.ts` and the matching api file, the same way the rest of the repo was done.

## Pool: with-lock-time and the unlock precursor

`PoolNFT2` supports `WithLockTime` mode. When that flag is set, FT-LP UTXOs carry a per-UTXO lock_time embedded in tape `chunks[3].Buf[24:28]` (LE uint32, < 500_000_000 = block height, ≥ 500_000_000 = unix time).

- `(*PoolNFT2).UnlockFTLP(privKey, utxo, lockTime *uint32)` produces a precursor tx that consolidates locked LP UTXOs and re-emits them with `lock_time = 0`. Returns `("", nil)` when only one already-unlocked LP UTXO matched (no precursor needed).
- `(*PoolNFT2).ConsumeLP(...)` returns `([]string, error)`. The slice is `[consumeRaw]` for non-with-lock-time pools or when no precursor was needed; `[unlockRaw, consumeRaw]` otherwise. Broadcast in order. The consume tx's fee input is automatically taken from `unlockTX.outputs[2]` when an unlock precursor is present, so the caller's `utxo` isn't double-spent.
- The TS methods `initPoolNFTWithLockTime`, `increaseLpWithLockTime`, `consumeLpWithLockTime` are deprecated upstream and intentionally NOT ported.
- Legacy `poolNFT` v1 (non-linear AMM) is intentionally NOT ported — TS keeps it for old pools but no Go consumer needs it.

## Typical call flow

`api.FetchUTXO` / `api.FetchFtUTXO...` → construct tx through a `contract` type (e.g. `FT.Transfer`, `NFT.TransferNFT`, `PoolNFT2.SwapToToken`, `PoolNFT2.ConsumeLP`) → `api.BroadcastTXRaw` / `BroadcastTXsRaw`. For with-lock-time pool consumes, broadcast the unlock tx first, wait for it to land, then broadcast the consume tx.

## Documentation layout

- `README.md`, `docs/合约库说明.md`, `docs/quick-start-go.md` — user-facing overview, library surface, and a minimal `go run` example.
- `docs/test-cases/*.md` — one per contract family. Each starts with a parameter table and ends with a single-file `package main` runnable from a downstream module after configuring `replace`. These are the canonical integration-test templates; mirror the structure when adding a new scenario.
- Spec source-of-truth stays at `../tbc-contract/docs/` (TS repo). `docs/README.md` has the TS↔Go mapping table — update it when adding or renaming a Go counterpart.
- `docs/superpowers/specs/` and `docs/superpowers/plans/` hold design / planning artifacts (gitignored, do not commit).
