# tbc-contract 1.7.3 alignment

Version candidates: Rust `tbc-sdk 0.5.0` and Go `tbc-contract-go 0.3.0`.
The reference is the published npm `tbc-contract@1.7.3` with `tbc-lib-js@1.0.31`.
The exact dependencies and npm integrity hashes are pinned in the Rust repository's
`scripts/reference-173/package-lock.json`; the artifact hashes are in
`tests/fixtures/js-173/reference-manifest.json` (Go: `lib/contract/testdata/js-1.7.3/`).
This update does not publish packages or Git tags.

## Implementation changes

- New AMM uses the published 1.7.3 public/controller compiler templates and the
  168-byte Pool Tape (`006a4c9b`). The 1.7.2 templates and 143-byte Tape
  (`006a4c82`) remain supported in the same native SDKs.
- Four independent uint16 basis-point rates replace the on-chain plan field:
  LP, tax, service provider and protocol. Operational policies require their sum
  to be below 10,000. The Tape codec itself preserves the entire uint16 range.
- Tape carries the 20-byte P2PKH fee recipient; Code commits to SHA256 of that
  recipient's full P2PKH script. State restoration verifies both bindings.
  The three non-LP fee shares use one recipient. Rounding remains
  `floor(total rate × amount) - floor(LP rate × amount)`, with a 10-sat payout floor.
- Service-provider labels are immutable raw bytes in Code. Default constructors
  use UTF-8 `NULL`. An explicitly empty label remains empty. Parsing walks script
  instructions, so embedded marker bytes, truncated pushes and noncanonical tails
  cannot impersonate another template. Public and 1–5-controller pools are covered.
- Pool queries obtain the fee rates, recipient and label from authenticated raw
  Code/Tape rather than legacy index `service_fee_rate`, `lp_plan` or
  `service_provider` summaries.
- Shared UTXO readers follow cursor pages, deduplicate overlapping pages, reject
  duplicate outpoints within a page and cursor cycles, and preserve integer
  precision. Target queries and bounded merge batches stop after one response.
  LP `utxos: null` is accepted as an empty list.
- Modern token selection supports index `target`/`limit`, verifies raw parents,
  and separates height locks from timestamp locks. Incompatible Stablecoin/LP
  target results can fall back to all pages and select compatible inputs.
  Index error code, HTTP status and request ID are retained.
- The JS Stablecoin public ID now denotes the original issuance-certificate
  transaction. Native creation already returns `[certificate, mint]` without a
  mutable JS-style `contractTxid` property. Use the first transaction's ID for
  Stablecoin index queries; the real cross-language query tests verify this.

Rust entry points: `TbcAmm::with_fees`, `TbcAmm::from_pool`, `PoolFeeRates`,
`ApiClient::fetch_utxo_records`, `fetch_token_utxos_for_amount`,
`fetch_ftlp_utxos_batch`, and `api::modern::select_token_utxos`.

Go entry points: `NewTBCAMMWithFees`, `TBCAMMFromPool`, `TBCAMMFeeRates`,
`api.FetchUTXORecords`, `contractquery.Client.FetchTokenUTXOsForAmount`,
`contractquery.SelectTokenUTXOs`, and optional batch sizes on the FT, Stablecoin
and legacy LP list methods. Go nil labels mean `NULL`; an empty non-nil byte slice
means an empty label. Rust's explicit constructor accepts the exact byte vector.

The existing plan-based constructors (`TbcAmm::new` / `NewTBCAMM`) retain their
1.7.2 behavior and create the original 143-byte format. Use `with_fees` /
`NewTBCAMMWithFees` to create 1.7.3 pools. Use `from_pool` / `TBCAMMFromPool`
for deployed pools of either version: they identify the full compiler template,
restore the matching immutable configuration, and verify Code/Tape/FT/LP bindings.
Callers do not need to supply a version flag.

## Compatibility boundary

**Existing AMM 1.7.2 pools remain tradable with both updated native SDKs.**
All six original fee plans are recovered from the old Tape; total fees and the
Code's fee-script hash must match that plan. Pool updates retain the exact old
Code and 143-byte Tape, preserving the Pool Code hash and LP identity. They never
silently convert an old pool into a 1.7.3 pool. Mixed-generation Code/Tape pairs,
modified compiler templates and mismatched recipients are rejected.
Public/controller pools, ordinary/timelocked LP, prepared signing, swaps,
liquidity operations and LP transfers share the compatible builders.
Historical fixtures and reports remain available. This does not change the
separate legacy PoolNFT v2 family. Legacy FT, NFT, StableCoin, MultiSig and other
existing modules remain available. Stablecoin pools and deprecated PoolNFT v1
remain outside the agreed scope.

The native SDKs use typed inputs, integer raw amounts and explicit authenticated
parents. They are functional ports; JS overload names, async callbacks and HTTP
request counts are not a one-to-one native API guarantee.

## Real testnet verification — 2026-10-12

The supplied key was used only in process memory for testnet signing, then its
runner was terminated. It is not included in source, fixtures or reports.

Every transaction below passed the pinned JS script interpreter, was accepted by
`https://api.tbcdev.org/api/tbc/`, and was fetched again with identical raw bytes.
This is acceptance/raw-refetch evidence, not a block-confirmation claim.

| Campaign | Transactions | Miner fees (sat) |
| --- | ---: | ---: |
| [AMM and LP](verification/2026-10-12-js-173-live-amm.json) | 72 | 73,707 |
| [Standard and NFT](verification/2026-10-12-js-173-live-interop.json) | 36 | 8,706 |
| [Stablecoin](verification/2026-10-12-js-173-live-stable.json) | 27 | 10,038 |
| [Stablecoin admin/additional mint](verification/2026-10-12-js-173-live-stable-advanced.json) | 36 | 11,869 |
| [HTLC and Timelock](verification/2026-10-12-js-173-live-locks.json) | 54 | 14,343 |
| [Limit orders](verification/2026-10-12-js-173-live-lop.json) | 264 | 92,537 |
| [Legacy Stablecoin](verification/2026-10-12-js-173-live-legacy-stable.json) | 24 | 7,040 |
| **Total unique broadcasts** | **513** | **218,240** |

[The manifest](verification/2026-10-12-js-173-live-manifest.json) records report
hashes and builder counts: Rust 123, Go 123, JS 267. There are 180 same-input JS
comparisons for native transactions; contract outputs, input fields and order
match while valid signatures and miner-fee change may differ. Comparison-only
JS transactions are not counted as broadcasts. Fee totals exclude value retained
in test assets and contract outputs.

AMM coverage includes public/controller pools, ordinary/timelocked LP, initial
and additional liquidity, both swap directions, LP transfers, partial and full
liquidity removal. Rates are `[200, 30, 70, 30]`, the fee recipient is the test
account and the label is `Interop173`. This is not an exhaustive live parameter
matrix. Offline vectors cover other rates, rounding boundaries, Unicode/raw/empty
labels, larger push encodings and 1–5 controllers.

- [AMM query evidence](verification/2026-10-12-js-173-amm-queries.json): 18 reads,
  three languages × six pools, including recovered fees, recipient and raw tag.
- [Token query evidence](verification/2026-10-12-js-173-token-queries.json): 18
  reads of Standard/Stablecoin metadata, UTXOs, balances and ancestors.
- [External signing and native validation](verification/2026-10-12-js-173-prepared-amm-offline.json):
  72 offline AMM/LP lifecycle transactions, checked by JS and both native validators.
- [Validator differential](verification/2026-10-12-js-173-native-validator-differential.json):
  63 valid/invalid cases, including altered outputs, unlocking scripts and duplicate
  inputs. The previously recorded JS `checkSequence`/`BN.and(number)` reference
  defect remains explicitly identified.

## Backward compatibility verification — 2026-10-12

After restoring 1.7.2 support, two additional live campaigns exercised the same
updated Rust/Go workers with automatic `from_pool` restoration:

| Pool format / JS oracle | Transactions | Miner fees (sat) |
| --- | ---: | ---: |
| [1.7.2 existing-format pools](verification/2026-10-12-amm-172-compat-live.json) | 72 | 73,025 |
| [1.7.3 new-format regression](verification/2026-10-12-amm-173-compat-live.json) | 72 | 73,708 |
| **Additional broadcasts** | **144** | **146,733** |

Both Rust and Go spend pools created by JS 1.7.2 in the first campaign. Each
campaign has 24 JS, 24 Rust and 24 Go broadcasts, with 32 same-input comparisons
against its matching JS version. All pool updates preserve the exact initial
Code and the corresponding 143/168-byte Tape size. Coverage includes public and
controller pools, plain/timelocked LP, initial/additional liquidity, both swaps,
LP transfer/merge, partial removal and full removal. Live old-format fees use
plan 6 (330 total / 200 LP basis points); all six plans are also checked in local
restoration tests. Legacy PoolNFT v2 remains a separate contract family.

- [Old-pool queries](verification/2026-10-12-amm-172-compat-amm-queries.json)
  and [new-pool queries](verification/2026-10-12-amm-173-compat-amm-queries.json):
  18 reads each, with matching current state and recovered fee configuration.
- [Old-format prepared signing](verification/2026-10-12-amm-172-compat-prepared.json)
  and [new-format prepared signing](verification/2026-10-12-amm-173-compat-prepared.json):
  72 offline lifecycle transactions each, checked by all three script validators.
- [Compatibility manifest](verification/2026-10-12-amm-compat-manifest.json)
  records report hashes, counts, selected JS-created pools and native spends.

```rust,ignore
// The same recovery call accepts deployed 1.7.2 and 1.7.3 pools.
let amm = TbcAmm::from_pool(&pool_tx, ft_genesis_tx, None)?;
```

```go
amm, err := contract.TBCAMMFromPool(poolTx, ftGenesisTx, "")
```

No pool or LP migration is required. These changes update the repository sources;
no package release or Git tag has been published.

## Local validation and reproduction

Passed: Rust `cargo check --all-targets`, `cargo test --lib --tests`; Go
`go test ./...`. New tests cover both AMM generations, all six legacy fee plans, mixed-generation
rejection, fee/Tape/tag bindings, codec and rounding parity,
cursor overlap/cycles, duplicate outpoints, bounded/target queries, error metadata
and lock-domain selection.

`cargo test --all-targets` also attempted legacy Pool example tests. Two tests in
`examples/support/pool_preflight.rs` fail because their historical JS 1.6.5
harness requires the absent `/home/ubuntu/projects/tbc-contract` checkout.
Their version/authority pins were not weakened. Current library/integration
checks and the independent 1.7.3 interpreter campaigns above pass.

From the Rust checkout:

```sh
npm ci --prefix scripts/reference-173 --ignore-scripts
npm ci --prefix scripts/reference-172 --ignore-scripts
cargo build --example interop_173 --example query_amm_173 --example query_172
go -C ../tbc-contract-go build -o /tmp/tbc-go-interop-173 ./test/interop-173
go -C ../tbc-contract-go build -o /tmp/tbc-go-query-amm-173 ./test/query-amm-173
go -C ../tbc-contract-go build -o /tmp/tbc-go-query-172 ./test/query-172
node scripts/generate-amm-fixtures-173.js
TBC_INTEROP_REPORT_PATH=/tmp/amm-173-offline.json node scripts/interop-173.js --amm
TBC_INTEROP_REPORT_PATH=/tmp/amm-172-compat.json node scripts/interop-173.js --amm-172
```

For another real broadcast campaign, provide `TBC_TESTNET_WIF` through the process
environment, use a fresh `TBC_INTEROP_REPORT_PATH`, and add `--broadcast`. Do not
replace the checked-in historical evidence. The runner caps miner fees at
1,000,000 sat per campaign and serializes funding spends. It supports `--resume`
for a failed live report after reviewing its last accepted transaction.
