# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository purpose

Go implementation of TBC (TuringBitChain) on-chain contracts and the indexer/API client, kept in **strict functional parity with the TypeScript reference repo `tbc-contract`** (expected as a sibling at `../tbc-contract`). Whenever behavior is ambiguous, the TS source (`../tbc-contract/lib/...`) and specs (`../tbc-contract/docs/*.md`) are authoritative — new Go code must reproduce JS wire output byte-for-byte (sizes, sighash preimages, push encodings, fees, change).

This is a **library-only** repo: no `cmd/`, no `*_test.go`, no scripts. Behavior is verified in downstream business repos using the scenarios under `docs/test-cases/`.

## Build & develop

```bash
go build ./...        # the only check this repo runs; there are no tests here
go vet ./...
```

**Sibling dependency (required):** `go.mod` contains `replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go`. The build will fail unless a team fork of tbc-lib-go exists at `../tbc-lib-go` (not a public `go get` — it's the internal fork that carries `JSEstimateSize`, `FtUTXO`, and the signing-getter shape this code relies on). If that path is missing, fix the `replace` before attempting a build rather than removing it.

Go toolchain: **`go 1.17`** (see `go.mod`). Don't bump without coordinating with callers.

## Architecture

Three flat packages under `lib/`, each a single Go package:

- **`lib/contract`** — one package `contract`, one file per contract family: `ft.go`, `stablecoin.go` (embeds `*FT`), `nft.go`, `orderbook.go`, `multisig.go`, `htlc.go`, `piggybank.go`, `poolnft2.go`. Script templates live next to them as `.asm` / `.txt` files and are pulled in via `//go:embed` — edit those raw files to change scripts, don't inline the bytes.
- **`lib/api`** — HTTP client for the TBC indexer. Base URLs are selected by a `network` string:
  - `"mainnet"` / `""` → `https://api.turingbitchain.io/api/tbc/`
  - `"testnet"` → `https://api.tbcdev.org/api/tbc/`
  - anything ending in `/` is treated as a custom base URL.
  Transient GETs auto-retry on a small set of network errors (`getBaseURL`, `isRetryableHTTPGetErr` in `api.go`). Keep that list conservative.
- **`lib/util`** — parity helpers for the TS `lib/util`: `ParseDecimalToBigInt`, `BuildUTXO`/`FtUnspentOutput`, and (critically) `nftunlock.go`'s `NftEncodeMinimalPushData`, which exists because TBC enforces `SCRIPT_VERIFY_MINIMALDATA` on unlock scripts. Any new push-data emission for NFT / coin-NFT unlocks must route through this, not through ad-hoc `bscript.AppendPushData`.

### The parity discipline

This is the one thing most likely to trip up edits:

- **Size estimation goes through `(*bt.Tx).JSEstimateSize()`** (the fork's JS-aligned estimator), not `tx.Size()`. See `tbcJSEstimateTxBytes` and `ftInputUnlockWireDeltaFromEmpty` in `ft.go` — the comments there call out a specific class of off-by-varint bugs (empty scriptSig is 41B incl. 1-byte length prefix; a ≥253-byte unlock needs a 3-byte varint, so the delta is `L + varint(L) − 1`, not `L − 41`). Reuse the existing helpers rather than re-deriving sizes.
- **Change/fee adjustment happens post-outputs, pre-signatures** via `adjustFTTransferChangeFee` — the same sequence `tbc-lib-js` uses. Signing before the change is correct, or using `tx.Size()` on a partly-signed tx, will diverge in the last few sats.
- `ft_transfer_steps.go` exposes `FTTransferStepReport` for comparing signing digests and wire hashes step-by-step against the JS implementation when parity breaks.
- FT amounts: prefer **`TransferDecimalString`** over the `float64` `Transfer` — it mirrors `parseDecimalToBigInt(ft_amount, decimal)` in TS and avoids `float64` drift on high-decimal or large-integer amounts.
- Pool NFT: only **2.0 (linear)** is implemented in `poolnft2.go`. The legacy non-linear `poolNFT` is intentionally not ported — don't add it without checking.

### Typical call flow

`api.FetchUTXO` / `api.FetchFtUTXO...` → construct tx through a `contract` type (e.g. `FT.Transfer*`, `NFT.Transfer`, `PoolNFT2.Swap*`, `StableCoin.TransferCoin`) → fill signatures with `bec.PrivateKey` + an `unlocker` (note `newFTTransferUnlockerGetter` in `ft.go` mixes FT custom unlocks with standard P2PKH on the same tx) → `api.BroadcastTXRaw` / `BroadcastTXsRaw`.

## Tuning knobs (env vars)

Read from within `lib/contract` (mostly `ft.go`). Only set these when debugging a parity break against JS — defaults match current node policy:

- `FT_FEE_SAT_PER_KB`, `NFT_FEE_SAT_PER_KB` — override sat/KB fee rate for estimation.
- `FT_SIGNED_UNLOCK_BYTES` — force a specific signed-unlock length (overrides per-input estimation).
- `FT_RELAY_FEE_SIGNED_ESTIMATE=1` plus `FT_RELAY_SIGNED_UNLOCK_BYTES` — switch relay-fee estimation to the signed-unlock path and set its length.

`piggybank.go` uses a hardcoded 80 sat/KB for freeze fees (matches TS); that is not env-configurable.

## Documentation layout

- `README.md`, `docs/合约库说明.md`, `docs/quick-start-go.md` — user-facing overview, library surface, and a minimal `go run` example for balance / UTXO.
- `docs/test-cases/*.md` — one per contract family, each starting with an env-var table followed by a single-file `package main` you can drop into a downstream module. These are the canonical integration-test templates; mirror the structure when adding a new scenario.
- Spec source-of-truth stays at `../tbc-contract/docs/` (TS repo). `docs/README.md` has the TS↔Go mapping table — update it when adding or renaming a Go counterpart.
