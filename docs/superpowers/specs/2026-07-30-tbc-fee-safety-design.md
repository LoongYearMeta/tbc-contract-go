# TBC Go Fee Safety Design

## Goal

Make `tbc-lib-go` and `tbc-contract-go` construct transactions whose actual
signed fee is sufficient, whose change outputs follow current SDK and node
dust policy, and whose amount arithmetic cannot silently underflow.

The implementation must preserve JavaScript 1.6.5 contract and script
compatibility. Fee safety may intentionally change a transaction's change
amount and txid.

## Confirmed root causes

1. `tbc-lib-go` v1.0.0 uses a one-satoshi `DustLimit`, while the current node
   rejects ordinary outputs below 10 satoshis and `tbc-lib-js` uses 42
   satoshis for change.
2. `ChangeToAddress` silently succeeds when the available value is below the
   estimated fee.
3. Contract builders commonly estimate custom inputs before their large
   unlocking scripts are inserted. Existing accepted testnet FT transactions
   paid 65.06-76.11 sat/KB instead of the intended 80 sat/KB.
4. Several direct subtraction paths can create zero, dust, or wrapped
   `uint64` outputs.
5. The Go API accepts `float64` TBC amounts and can return the first UTXO when
   no single UTXO satisfies the requested minimum.

## Compatibility policy

- TBC values are integers in satoshis in all new internal and public APIs.
- Node policy dust is documented as 10 satoshis.
- Generic SDK change uses 42 satoshis to match the installed
  `tbc-lib-js` behavior. Zero-satoshi data outputs remain valid.
- The contract fee schedule is:
  `max(80, ceil(actualSignedBytes * 80 / 1000))`.
- Existing `float64` API methods remain as compatibility wrappers, but new
  satoshi methods are canonical.
- Existing transaction-building method signatures remain compatible unless
  returning a previously hidden insufficiency error is required for safety.

## `tbc-lib-go` responsibilities

### Strict change calculation

`ChangeToAddress` and `ChangeWithScript` calculate the target fee with checked
arithmetic.

- Available value below the target fee returns `ErrInsufficientInputs`.
- Available value equal to the fee succeeds without a change output.
- A positive remainder below 42 satoshis is added to the paid fee and no
  change output is created.
- A remainder of at least 42 satoshis creates the requested change output.

The library exports distinct node and SDK dust constants so callers do not
confuse consensus/policy acceptance with the SDK's safer change threshold.

### Fee helpers

The library exposes checked helpers for:

- proportional fee calculation with ceiling;
- a minimum absolute fee;
- checked input/output summation where an error-returning API is possible.

Existing fee quote behavior remains available for callers that need pure
fee-per-kilobyte JavaScript parity.

## `tbc-contract-go` responsibilities

### Signed-size finalization

Contract transactions with private-key signing use a shared fee finalizer:

1. build outputs and a provisional change output;
2. sign all inputs;
3. calculate the target from the real serialized length;
4. update the designated change output using checked arithmetic;
5. re-sign all sighash-committing inputs;
6. verify the final paid fee is at least the target.

The helper accepts an explicit change-output index because OrderBook matching
can place continuation outputs after fee change. If a contract requires a
fixed output shape and cannot retain non-dust change, construction fails with
an actionable “larger fee UTXO required” error.

Unsigned builders use conservative documented unlocking-script size
estimates. Their signing/fill methods perform the final actual-size
verification whenever signatures are available.

### Contract boundary fixes

- FT mint, transfer, batch transfer, and merge use actual signed size.
- StableCoin transfer and batch paths finalize all input counts, including a
  single FT input.
- NFT fee-adjustment errors are returned rather than ignored.
- Pool keeps its existing actual-size two-pass process but uses shared checked
  arithmetic where practical.
- OrderBook keeps contract-specific output placement and uses the shared fee
  formula/verification.
- MultiSig validates `totalIn >= sendAmount + fee` before subtraction and
  rejects dust ordinary outputs.
- HTLC withdraw/refund and PiggyBank unfreeze require the post-fee ordinary
  output to meet SDK dust.

### Exact UTXO selection

Add satoshi-native API methods. Single-UTXO selection returns a clear error
when no individual UTXO satisfies the minimum; it never silently returns an
insufficient first entry. Compatibility wrappers convert legacy TBC amounts
at the boundary and delegate to the satoshi methods.

## Testing

Development follows red-green-refactor.

### Library boundaries

- target fee minus one, exactly target fee, and target fee plus remainder;
- change values 1, 9, 10, 23, 24, 41, and 42 satoshis;
- 999, 1000, and 1001-byte fee calculations;
- overflow paths.

### Contract boundaries

- every signed transaction satisfies
  `inputSum - outputSum >= max(80, ceil(actualBytes*80/1000))`;
- no ordinary output is between 1 and 41 satoshis;
- insufficient MultiSig, HTLC, PiggyBank, NFT, FT, and StableCoin funding
  returns an error;
- UTXO selection never returns less than the requested satoshi amount.

Both repositories run their complete unit suites, race/static checks where
available, and clean-tree checks.

## Testnet verification

After offline verification, representative FT mint/transfer and at least one
large-contract transaction are broadcast to TBC testnet. The WIF is supplied
only through `TBC_TESTNET_WIF`; it is never written to source, reports, shell
history, or command output. For each accepted transaction the report records
txid, byte size, paid fee, and actual sat/KB.

## Delivery order

1. Implement and commit `tbc-lib-go` safety primitives.
2. Point `tbc-contract-go` at the tested local library during development.
3. Implement contract and API fixes.
4. Verify both repositories together.
5. Release/tag the library or update to an approved commit, then update
   `tbc-contract-go` dependency metadata.
6. Perform testnet verification and write the fee report.
