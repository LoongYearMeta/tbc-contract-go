# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository purpose

Go implementation of TBC (TuringBitChain) on-chain contracts and the indexer/API client. **Strict 1:1 parity with the TypeScript reference repo `tbc-contract`** (sibling at `../tbc-contract`). Whenever behavior is ambiguous, the TS source (`../tbc-contract/lib/...`) is authoritative — new Go code must reproduce JS wire output byte-for-byte (sizes, sighash preimages, push encodings, fees, change).

This is a **library-first** repo: `lib/` is the actual library; there are no `*_test.go` files. The repo also ships runnable smoke programs under `test/<contract>/main.go` (one `package main` per contract, configured via top-of-file `const ( … )`) — these mirror the templates in `docs/test-cases/` and broadcast real txns when their `do*` switches are toggled. Library users can ignore `test/` entirely; downstream business repos still verify behavior using their own copies of the `docs/test-cases/` templates.

## Build & develop

```bash
go build ./...        # the only check this repo runs
go vet ./...
```

Note: `go build ./...` (without `-o`) compiles each `package main` under `test/<contract>/` and drops a binary by that name into the **current working directory** (i.e. the repo root). The .gitignore lists those eight names (`/ft`, `/nft`, …, `/stablecoin`) so they don't get tracked, but for a clean check prefer `go build -o /dev/null ./test/...` or just `go vet ./test/...`.

**Module dependency:** `go.mod` requires `github.com/LoongYearMeta/tbc-lib-go v1.0.0` (no `replace` directive — pulled from the public GitHub tag). `tbc-lib-go` itself is a single self-contained module — `libsv/go-bk` and `sCrypt-Inc/go-bt/v2` are NOT dependencies and must not be re-added. If proxy.golang.org / sum.golang.org return 404 right after a fresh tag is pushed (their caches lag a few hours), bypass with `GOPROXY=direct GOSUMDB=off go mod download` once and the populated `go.sum` keeps subsequent builds green.

Go toolchain: **`go 1.17`** (see `go.mod`). Don't bump without coordinating with callers.

## Architecture

Three flat packages under `lib/`, each a single Go package; structure mirrors `tbc-contract/lib/` 1:1:

- **`lib/contract`** — one file per contract family: `ft.go`, `nft.go`, `orderbook.go`, `multisig.go`, `htlc.go`, `piggybank.go`, `poolnft2.go`, `stablecoin.go`. All embedded ASM scripts/templates live in **`lib/contract/asm/*.asm`** and are pulled in via `//go:embed asm/<name>.asm` — edit those raw files to change scripts, don't inline the bytes. `poolnft2.go` has five (`poolnft2_code.asm`, `poolnft2_ftlp_code.asm`, `poolnft2_ftlp_locktime_code.asm`, `poolnft2_lock_code_pre.asm`, `poolnft2_lock_code_last.asm`) — the inline ASM strings these replaced were severely truncated relative to TS and should not be reintroduced. `stablecoin.go` has two: `stablecoin_mint.asm` (FT mint code with `${adminPubHash} / ${codeHash} / ${tapeSizeHex} / ${hash}` placeholders) and `stablecoin_coinnft_code.asm` (coin-NFT code with `${txIDVout}`). `ft.go` and `nft.go` use `ft_mint.asm`, `nft_code.asm`, `nft_code_v0.asm`. Both pure-ASM and `${placeholder}`-templated files share the `.asm` extension; the templated ones go through `strings.ReplaceAll` then `strip0xHexPushesInASM` before `bscript.NewFromASM`.

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
- **Change/fee adjustment is pre-signature, not post — except for FT-heavy paths.** For P2PKH-only paths sign once after `tx.ChangeToAddress(addr, newFeeQuote80())`; do NOT compute `len(tx.Bytes())` after signing and re-adjust — the actual signed P2PKH unlock is ~110 B but TS estimates 180 B per input, and a post-sign re-adjust would produce hex that diverges from JS by 2-6 sat per input. **However**, `JSEstimateSize` only assigns 41 B per non-P2PKH input, which catastrophically underestimates FT inputs (actual unlock ~1200 B); a 5-FT merge estimated as ~580 B underpays the relay by ~470 sat → "66: insufficient priority". For paths with multiple FT inputs (`mergeFTSingle` is the canonical example) we therefore do a deliberate two-pass: provisional sign → recompute fee from `len(tx.Bytes())` → adjust the change output → re-sign every input (sighash committed to outputs, but unlock script *size* is deterministic so the second pass converges in one redo). When adding a new path with ≥2 FT inputs, follow the same pattern.
- **Set `tx.LockTime` and per-input `SequenceNumber` BEFORE calling `ft.GetFTunlock` / `signP2PKHAtIdx`.** The sighash preimage embeds both fields; setting them after the unlocker would invalidate every signature.
- **`bt.UTXO.TxID` is forward (display-order) bytes** throughout this repo. `hex.DecodeString(tx.TxID())` gives the right bytes; do NOT reverse them. Three pool sites previously had this bug.
- `BuildFTtransferCode` returns `(*bscript.Script, error)`. There is no byte-offset fallback for "v1 layout" — the chunks-walk path is the only correct path; a fallback that patches `[1537:1558]` would silently corrupt FT-LP scripts (which have different padding offsets).
- **Any ASM emitted from an embedded template (or `fmt.Sprintf` with `0x<hex>` tokens) must go through `strip0xHexPushesInASM` before `bscript.NewFromASM`.** tbc-lib-js's `Script.fromString` accepts `0x<hex>` push-data syntax; tbc-lib-go's `NewFromASM` does not (it returns `invalid opcode data`). FT/NFT/stablecoin mint paths and all four pool template paths (`getPoolNftCode`, `getPoolNftCodeWithLock`, `getFtlpCode`, `getFtlpCodeWithLockTime`) route through this strip helper. When porting a new template-based contract from TS, do the same.

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

## Stablecoin: admin MuSig2 split

`StableCoin` extends `*FT` (composition). Two execution flavors:

- **Owner-signed** (ECDSA, isCoin=true variant of `getFTunlock`): `Transfer`, `BatchTransfer`, `MergeCoin`. Identical surface to FT but with per-input `SequenceNumber = 0xfffffffe` and `tx.LockTime = max(per-input tape lockTime)` so frozen UTXOs only spend after their lockTime expires. Two-pass fee adjust kicks in for ≥2 FT inputs (same rule as FT mergeFTSingle / orderbook cancel-buy).
- **Admin MuSig2** (BIP340 Schnorr): `PrepareCreateCoin`, `PrepareMintCoin`, `PrepareFreezeCoinUTXO`, `PrepareUnfreezeCoinUTXO`. tbc-lib-go does NOT include Schnorr/MuSig2 primitives, so these methods return `*AdminPrepared{ Tx, Sighashes, Finalize }`: pre-seed admin inputs with 64-byte zero placeholders, lock the byte layout via a two-pass ECDSA fee-input sign, expose 32-byte SHA256d sighashes for an external MuSig ceremony, and `Finalize(sigs)` swaps the placeholders for real 64-byte BIP340 sigs (same byte length → no re-fee). Caller is responsible for the BIP327 `keyAgg` / `nonceGen` / `partialSign` ceremony — typically run on `tbc-lib-js` cross-language or whatever Schnorr stack ships with their wallet.

Admin-input unlock layout:
- coin-NFT-code (input 0 of mint / createCoin): `<sigPush 0x41 sig64 SIGHASH_ALL_FORKID> <pubPush 0x20 xonly32> <currTxData> <prepre> <pre>`. Built via `BuildCoinNftUnlockScriptSchnorr`.
- coin-NFT-hold (input 1): `<sigPush 0x41 sig64 SIGHASH_ALL_FORKID> <pubPush 0x20 xonly32>`. Built via `buildSchnorrP2PKHLikeUnlock`. The locking script is `OP_DUP OP_HASH160 <admin pkh20> OP_EQUALVERIFY OP_CHECKSIG OP_RETURN ...`; on-chain `OP_CHECKSIG` dispatches Schnorr by sig-length 64 and pubkey-length 32, so HASH160(xonly32) must match the embedded admin pkh.
- coin FT inputs (freeze / unfreeze): `StaticGetFTunlock(sigPushHex, xonlyHex, …, isCoin=true)`.

`PrepareCreateCoin.Finalize(sigs)` returns `[coinNftRaw, mintRaw]`; the other three return one-element slices. `coinNftRaw` is the upstream coin-NFT-creation tx (broadcast first); `mintRaw` is the mint that consumes its outputs 0/1/3.

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
- `test/<contract>/main.go` — in-repo runnable counterpart of each `docs/test-cases/*.md`. Configuration is via constants at the top (`wifA`, `ftContractTxid`, `do*` switches, etc.); no env vars. Build with `go build -o /dev/null ./test/...` to avoid littering the repo root with binaries. `test/stablecoin/main.go` exercises only the owner-signed paths (`Transfer` / `MergeCoin`), since the admin paths require an external Schnorr/MuSig2 ceremony — see `docs/test-cases/stablecoin.md`.
- Spec source-of-truth stays at `../tbc-contract/docs/` (TS repo). `docs/README.md` has the TS↔Go mapping table — update it when adding or renaming a Go counterpart.
- `docs/superpowers/specs/` and `docs/superpowers/plans/` hold design / planning artifacts (gitignored, do not commit).
