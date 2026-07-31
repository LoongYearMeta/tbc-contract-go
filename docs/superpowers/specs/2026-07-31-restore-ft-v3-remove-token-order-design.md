# Restore FT v3 and Remove TokenOrder Design

## Goal

Return `tbc-contract-go` to the protocol scope shared by the officially
supported JavaScript and Rust SDKs. New Go FT mints must use FT v3, and the Go
package must not expose the unreleased Token-to-Token OrderBook or FT v4
experiment.

## Compatibility Baseline

The compatibility reference is:

- JavaScript `tbc-contract` released FT and ordinary TBC-to-FT OrderBook
  behavior.
- Rust `tbc-sdk` FT v3 implementation: 1,884-byte FT Code script with a
  1,856-byte partial-hash boundary.
- Existing on-chain FT v1-v3 and StableCoin scripts.

Token-to-Token OrderBook, FT v4, the 1,948-byte FT Code script, and the
1,920-byte partial-hash boundary are outside this baseline.

## Public Behavior

`FT.MintFT` will create FT v3 scripts again. Existing FT v1-v3 assets remain
supported by normal transfer, batch transfer, merge, MultiSig, HTLC, PoolNFT2,
StableCoin, and ordinary OrderBook paths where those paths already support the
asset version.

The following experimental public surfaces will be removed:

- TokenOrder data and script builders.
- TokenOrder create, fill-signature, cancellation, and matching methods.
- Online TokenOrder API composition helpers.
- FT v4 classification and unlock serialization.

Ordinary TBC-to-FT OrderBook creation, cancellation, and matching remain
available.

## Code Changes

The FT mint template will be restored to the JavaScript/Rust-compatible FT v3
template. Shared FT classification and serialization will recognize FT v1,
FT v2, FT v3, and StableCoin only.

All FT v4 branches will be removed from:

- FT pre-transaction and grandparent serialization.
- OrderBook FT partial-hash selection.
- PoolNFT2 FT and FTLP script selection.
- PoolNFT unlock serialization.

TokenOrder Go files, embedded buy/sell scripts, tests, and testnet runner code
will be removed. Existing fee convergence, dust-policy handling, MultiSig,
StableCoin, NFT, HTLC, PiggyBank, API, and PoolNFT2 fixes will not be reverted.

## On-Chain Compatibility

Existing FT v1-v3 token IDs do not change when moved by the Go SDK and remain
usable by JavaScript, Rust, and Go for supported operations.

The two FT v4 assets created during the testnet experiment remain on chain but
will no longer be recognized or spent by the supported Go API. They were
created with the approved testnet-only key and are not part of the released
protocol surface.

## Documentation

User-facing documentation will describe FT v1-v3 support and ordinary
OrderBook only. The active verification report will stop presenting FT v4 and
TokenOrder as supported behavior. Git history remains the audit trail for the
retired experiment.

## Verification

Implementation will follow test-driven development:

1. Change the mint compatibility test to require the 1,884-byte FT v3 script
   and its JavaScript/Rust-compatible hash, and observe it fail against the
   current FT v4 implementation.
2. Change classification tests to reject the 1,948-byte FT v4 script and
   observe the current classifier fail that expectation.
3. Restore the FT v3 template and remove FT v4 branches until focused tests
   pass.
4. Remove TokenOrder sources and update remaining tests to compile against the
   supported API.
5. Run `go test ./... -count=1`, `go test -race ./... -count=1`, and
   `go vet ./...`.
6. On testnet, mint a fresh FT v3 asset, broadcast its source and mint
   transactions in order, broadcast a normal FT transfer, then fetch and
   validate all accepted transactions and their fees.

## Non-Goals

- Adding Token-to-Token support to JavaScript or Rust.
- Migrating experimental FT v4 assets to FT v3.
- Reverting unrelated correctness or fee fixes.
- Restoring the old PoolNFT v1 implementation.
