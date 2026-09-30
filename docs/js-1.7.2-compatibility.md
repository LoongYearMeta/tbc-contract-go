# JS 1.7.2 alignment and testnet verification

Local release candidates: Rust `tbc-sdk 0.4.0`, Go `tbc-contract-go 0.2.0`.
Reference: npm `tbc-contract 1.7.2`, `tbc-lib-js 1.0.31`, pinned in
`scripts/reference-172/package-lock.json`. No package or Git tag was published.

## Public surfaces

| Contract | Rust module | Go package/surface |
| --- | --- | --- |
| New fungible token | `contracts::tbc20_standard` | `contract.TBC20Standard` |
| New NFT | `contracts::tbc721_standard` | `contract` TBC721Standard functions |
| New stablecoin and LP codecs | `contracts::modern_tokens` | `contract.TBC20Stablecoin`, `ModernTokenCode` |
| New AMM | `contracts::tbc_amm` | `contract.TBCAMM` |
| Limit orders | `contracts::tbc_lop` | `contract.TBCLOP` |
| HTLC | `contracts::tbc_htlc` | `contract.TBCHTLC` |
| Native timelock | `contracts::tbc_timelock` | `contract.GetTBCTimelockCode` and existing freeze/unfreeze builders |
| Full-template detection | `contracts::detect::detect_generation` | `contract.DetectGeneration` |
| Authenticated queries | `api::modern` | `contractquery.Client` |

Amounts are integer satoshis or raw token units. LOP price/rate denominator is
1,000,000; Pool fee rates use 10,000. Native and token outputs are derived from
actual parent transactions. New scripts and witnesses are separate from the
existing FT, TBC20, NFT, StableCoin, OrderBook and Pool v2 modules.

Stablecoin creation/additional mint/admin freeze/unfreeze support native BIP340
signing and external administrator prepare/finalize signing in both ports.
The external interface accepts an aggregate x-only key and verifies each BIP340
signature; the MuSig2 signing ceremony itself remains external, as in JS.
Go uses btcec/v2 2.3.4 and retains the module's Go 1.17 language target.
New-generation NFT batch minting, Stablecoin batch transfer, recursive merge,
attached TBC and additional-information outputs are implemented. The
`completion_172` fixtures cover these additions; the older limits below must
not be interpreted as missing these now-implemented features.

Pool v2 plan 6 uses 330 bp total / 200 bp LP, matching JS 1.7.2.
Historical 130 bp / 80 bp support is intentionally excluded: the user confirmed
that configuration has not been used. Creation rejects the old rate.
Stablecoin pools are intentionally unsupported in both native ports, per the
user's 2026-09-30 instruction. Legacy Pool v2 rejects stablecoin creation,
initialization, liquidity/swap operations and LP lifecycle builders. New AMM
accepts Standard genesis only. Historical low-level decoding is retained for
inspection; it does not authorize stablecoin-pool transaction construction.
Standalone Stablecoin operations are unaffected. Go additionally
fixes the plan-6 empty witness field and FT v4 change-output detection. The
latter fixes are tested against the published JS witness and a real existing
130 bp pool.

## Initial verified testnet operations

Every accepted transaction below passed the pinned JS script interpreter and
was broadcast through the testnet API, then fetched with matching raw bytes.
This is acceptance/raw-refetch evidence, not a claim of block confirmation.

| Report | Accepted transactions | Miner fees (sat) |
| --- | ---: | ---: |
| [Standard + TBC721](verification/2026-09-29-js-172-interop.json) | 36 | 8,706 |
| [Stablecoin](verification/2026-09-29-js-172-stablecoin.json) | 27 | 10,041 |
| [AMM + LP](verification/2026-09-29-js-172-amm.json) | 72 | 73,025 |
| [HTLC + Timelock](verification/2026-09-29-js-172-locks.json) | 54 | 14,342 |
| [LOP](verification/2026-09-29-js-172-lop.json) | 265 | 92,617 |
| [Additional stablecoin cases](verification/2026-09-29-js-172-stablecoin-advanced.json) | 36 | 11,862 |
| **New-generation total** | **490** | **210,593** |
| [Legacy regression](verification/2026-09-29-legacy-regression.json) | 129 | 77,249 |
| **Initial campaign accepted transactions** | **619** | **287,842** |

Legacy regression covers Rust and Go FT, NFT, StableCoin, MultiSig and
unlocked Pool v2 lifecycles, plus the existing Go token HTLC. The 129 unique
accepted transactions include setup transactions and repeated pool diagnostics;
they are not 129 different test cases. The Go historical 130 bp pool also has
[a focused initialization record](verification/2026-09-29-go-legacy-pool-plan6.json).
Rust `cargo test --all-targets --quiet` and Go `go test ./...` passed after the
implementation and compatibility fixes.

The LOP count includes funding movements among the owner, seller and matcher
roles. One funding shortfall was caught before broadcasting; the runner
resumed from accepted txids and added one top-up. Six continuation records
were reconstructed from the resume log and independently reverified after
fixing report-array persistence; they are labeled `recoveredFromResumeLog`.

AMM cases include public/controller pools, ordinary/timelocked LP, initial and
additional liquidity, both swaps, LP transfer/merge, partial and complete LP
removal. LOP covers Standard/TBC and Stablecoin/TBC, both order sides, cancel,
partial buy/sell continuation and mixed Standard/Stablecoin token pairs.
HTLC covers TBC and both token families, preimage redemption and matured refund.
Stablecoin advanced cases include additional issuance, equal-owner freeze
aggregation, freezing another owner's tokens, admin unfreeze and ordinary-owner
transfer. No intentionally premature transaction was broadcast.

Native Rust/Go transactions are built by their own binaries. The JS process
supplies data, runs the reference comparison/interpreter, and broadcasts. The
reference counterpart in `jsComparison.referenceTxid` is **not** separately
broadcast: it is a same-input comparison transaction. Different valid
signatures and miner-fee change can produce different txids. Contract output
scripts/values, ordering, input outpoints, sequences and lock time are compared.

[Read-only query evidence](verification/2026-09-29-js-172-queries.json) compares
18 JS/Rust/Go reader checks against assets created by every language, including raw
Code/Tape checks, integer balances, lock times and ancestor txids. The stablecoin
index uses the **issuance certificate source txid**, whereas the JS instance's
`contractTxid` records the first mint. Pass the certificate ID to stablecoin
index routes. Passing the mint ID causes `STABLECOIN_NOT_FOUND` even for assets
created with the JS reference itself.

## Expanded Pool verification

The follow-up [Pool parameter coverage report](pool-parameter-coverage-172.md)
separately records the full finite AMM configuration matrix, expanded LP
input/controller cases, boundary checks and cross-language index queries.
It also records legacy Pool v2 plans 1–6 across JS/Rust/Go, public-key counts
1–10 and precision-6 fractional amounts. In that historical campaign, legacy time-locked LP initialization
was rejected by the testnet gateway with A3, including the published JS
builder; those historical cases remain recorded as rejected. The latest
indexer recheck below now passes the previously failing scenario.
The initial 72-transaction AMM campaign above only exercised plan 6 and two
authorization/LP combinations, with a zero LP lock value.

## Scope and remaining operational limits

- Stablecoin pools and deprecated PoolNFT v1 are deliberately excluded in both
  SDKs. Standalone StableCoin / TBC20Stablecoin remains supported.
- Existing FT/NFT/Pool v2 and new Standard/TBC721/AMM are separate generations.
  Updating an SDK does not rewrite deployed scripts.
- Native APIs use Rust/Go types, integer raw units and explicit authenticated
  parents. They are functional ports, not literal copies of every JS overload,
  async callback, online convenience method or `fillSig*` name.
- Full-template detection is separate from the FT source-graph validator.
- Local AMM validation executes every input and checks value conservation. It
  does not establish UTXO availability, finality, index availability or node
  acceptance. Rust bounds execution resources; Go retains the internal VM's
  execution limits. These APIs are not full consensus-node replacements.
- One pinned reference defect is recorded explicitly: `tbc-lib-js@1.0.31`
  `checkSequence` passes a number to `BN.and`, so an otherwise satisfied CSV
  example throws `num.clone is not a function`. Native validators perform the
  sequence check correctly. Current AMM artifacts do not use this branch.
- The historical Pool v2 time-locked LP `index-gate/A3` rejection no longer
  reproduces in the fresh indexer recheck: JS/Rust/Go passed 12 complete flows,
  168 broadcasts/raw refetches and zero rejections. Coverage is ordinary FT
  v4, decimal 6, plans 1/6, public/three-controller pools and matured height/
  timestamp LP locks. This targeted rerun does not relabel the historical 63
  rejected cases as passed or claim to rerun every old parameter combination.
  See [the indexer recheck](verification/2026-09-30-082958-old-pool-locked-recheck.json).


## External signing and validation completion (2026-09-30)

The implementation gaps identified by the recheck have been filled:

| Capability | Rust | Go |
| --- | --- | --- |
| AMM prepare / finalize | `prepare_mint_tbc_amm`, `prepare_add_liquidity`, `prepare_remove_liquidity`, `prepare_swap_ft`, `prepare_swap_tbc`, `prepare_transfer_lp`, `prepare_unlock_lp` | `PrepareMintTbcAmm`, `PrepareAddLP`, `PrepareRemoveLP`, `PrepareSwapFT`, `PrepareSwapTBC`, `PrepareTransferLP`, `PrepareUnlockLP` |
| HTLC external signing | `prepare_deploy`, `prepare_spend`, `prepare_deploy_token`, `prepare_spend_token` | `PrepareDeploy`, `PrepareSpend`, `PrepareDeployToken`, `PrepareSpendToken` |
| LOP external signing | `prepare_make`, `prepare_cancel`, `prepare_match_orders` | `PrepareMake`, `PrepareCancel`, `PrepareMatchLOPOrders` |
| Legacy StableCoin admin | `prepare_freeze_coin_utxos`, `prepare_unfreeze_coin_utxos` | Existing `PrepareFreezeCoinUTXO`, `PrepareUnfreezeCoinUTXO` |
| Native AMM input validation | `validate_tbc_amm_transaction` | `ValidateTBCAMMTransaction` |
| AMM optional fee change | Mint from root, liquidity, swaps, LP transfer/unlock | Same |

AMM/HTLC/LOP preparation only requires public keys. A prepared object owns the
transaction snapshot, returns the signing digest/public key/input index, and
verifies canonical low-S DER + `0x41` signatures before finalization. Outputs
and fees remain fixed; shorter actual signatures can leave slightly more fee
than strictly necessary. Private-key convenience methods remain available.
Legacy StableCoin administrators use externally supplied BIP340 signatures;
its fee input is locally signed, matching JS's administrator workflow.

AMM prepared mint uses an explicitly selected root. Its caller must finalize
and obtain the txid of a Source before preparing a dependent mint; the ordinary
`mint` / `Mint` convenience method still returns Source + mint. Rust's prepared
funding type is `Utxo<PublicKey>`: the existing field name `private_key` stores
the public key for that generic type. Go prepare methods take public keys and
ordinary `bt.UTXO` funding references.

All seven expanded offline matrices passed: **513 transactions**, each checked
by the pinned JS interpreter and both native validators. Coverage includes
multi-input legacy StableCoin freeze/unfreeze and administrator actions on
another owner's coins. Additional boundary tests cover wrong keys/signatures,
stale digests, immutable prepared snapshots, insufficient funding, duplicate
inputs and missing ancestry. See `prepared_signing_172` tests in both projects.

The [native-validator differential report](verification/2026-09-30-native-validator-differential.json)
contains 103 cases (102 agree with JS; one records the CSV reference defect).
The [no-change report](verification/2026-09-30-amm-no-change-offline.json) contains
20 native AMM cases, each accepted by all three validators, covering five
operations in public/plain and controlled/timelocked pools. Mint no-change and
exact funding boundaries additionally have native unit tests.

Offline reports are `verification/2026-09-30-prepared-*-offline.json`.
The fresh external-signing testnet campaign accepted and raw-refetched **414
unique transactions**: AMM 72, HTLC/Timelock 54, LOP 264, legacy StableCoin 24.
All **771 inputs** passed JS and both native validators; **136 native
transactions** also matched JS contract outputs using the same inputs. Miner
fees were **186,951 sat (0.186951 TBC)**. These counts include setup/funding
transactions; they do not assert block confirmation. See
[the completion manifest](verification/2026-09-30-080902-completion-live-manifest.json)
for per-phase reports and txids. Rust all-target tests passed 454 tests and Go
`go test ./...` passed. The runtime key file was removed after testing.

## 2026-09-30 source recheck and fresh testnet run

The installed npm references were checked against registry tarballs and SHA-512
integrity: all 57 `tbc-contract@1.7.2` files and all 87 `tbc-lib-js@1.0.31`
files matched. Rust `cargo test --all-targets --quiet` passed 450 tests; Go
`go test ./...` passed. All six offline matrices passed (489 transactions).
See [offline results](verification/2026-09-30-js-172-recheck-offline.json).

A fresh user-authorized testnet run from
`143KgKGcse57nXBnXyJwtQrf2KP4KWto59` accepted and raw-refetched **489 unique
transactions**, with **210,522 sat** in miner fees. Standard/NFT: 36;
Stablecoin: 27; additional Stablecoin cases: 36; AMM/LP: 72;
HTLC/Timelock: 54; LOP: 264. All inputs passed the pinned JS interpreter;
172 native transactions additionally passed same-input JS comparison.
Counts include setup and funding transactions, not just distinct test cases.
Stablecoin pools were excluded. No block-confirmation claim is made.
See the [fresh live manifest](verification/2026-09-30-073440-live-audit-manifest.json) for individual reports,
transaction IDs and fees. Earlier evidence files were preserved.

This earlier campaign verifies the core transaction flows before external
signing completion. The follow-up section above records the newly implemented
interfaces and their additional evidence. The old Pool v2 campaign was not
rerun here; its stablecoin rejection guards passed native regression tests.

## Reproduce

The cross-language runner and pinned reference live in the Rust `tbc-sdk`
repository; run the commands below from that repository, with the Go checkout
at the sibling `../tbc-contract-go` path.

Install the pinned reference with `npm ci --prefix scripts/reference-172`.
Set `TBC_INTEROP_REPORT_PATH` to save an offline or live run separately from the historical
reports; `--resume` uses the same selected report path.
Build Rust `interop_172` and Go `test/interop-172` (the latter binary defaults to
`/tmp/tbc-go-interop-172`, overridable with `TBC_GO_INTEROP_BIN`). Run:

```sh
node scripts/interop-172.js
node scripts/interop-172.js --stable
node scripts/interop-172.js --stable-advanced
node scripts/interop-172.js --amm
node scripts/interop-172.js --locks
node scripts/interop-172.js --lop
node scripts/interop-172.js --legacy-stable
```

Set `TBC_INTEROP_PREPARED=1` to exercise public-key-only native preparation and
external finalization, and `TBC_INTEROP_NATIVE_VALIDATE=1` to execute all inputs
in both native validators as well as JS. Optional corpus/request paths are
for offline differential testing; they omit signing keys.

These commands are offline unless `--broadcast` is supplied. Live mode reads
`TBC_TESTNET_WIF` from the environment and always selects `testnet`; never
commit that variable or a key file. `--resume` replays a failed report's accepted
transactions by txid, then continues. Keep separate copies of prior reports
when intentionally starting another live run with the same phase filename.

LOP auxiliary keys are runtime SHA256 derivations of the supplied private key
bytes followed by `tbc-interop-172-lop-seller`, `tbc-interop-172-lop-matcher`, or
`tbc-interop-172-lop-tax` UTF-8 bytes, interpreted as compressed keys. Test
payouts and token outputs remain at these reproducible addresses. AMM service
fees are protocol outputs and are not included in miner-fee totals. Full LP
removal retains the contract's 1,500-sat pool value.
