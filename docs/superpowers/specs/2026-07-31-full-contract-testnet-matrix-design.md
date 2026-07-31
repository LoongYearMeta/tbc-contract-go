# Full Contract Testnet Matrix Design

## Objective

Validate the final Go SDK against the released JavaScript 1.6.5 protocol
surface with a fresh, auditable TBC testnet run. Every economically distinct
transaction path supported by `tbc-contract-go` must be constructed by the
current Go code, broadcast, fetched back, checked for asset flow and script
shape, and independently checked for its actual signed-size fee.

This replaces the earlier family-level report, which proved representative
paths but did not rerun every supported lifecycle branch after the FT v3
restoration.

## Definition of Full Coverage

“Full” means every supported operation that produces a distinct on-chain
state transition. Alternative signing surfaces that produce the same
transaction semantics (`build`, `fillSigs`, and direct `WithSign`) are checked
for local equivalence but are not each broadcast, because broadcasting them
would double-spend the same test input.

The matrix excludes:

- Pool NFT v1, which is intentionally absent from the Go SDK.
- Unreleased dual-token matching and its retired FT extension.
- Read-only helpers that do not construct transactions.
- Mainnet activity.

## Broadcast Matrix

### Native TBC

- P2PKH self-transfer with current minimum-fee handling.

### FT v1-v3 Surface

- Fresh FT v3 source and mint.
- Normal transfer.
- Transfer with an additional `OP_FALSE OP_RETURN` information output.
- Batch transfer to at least two logical recipients.
- Merge of at least two FT UTXOs.

The run checks the 1,884-byte released FT v3 code, the 1,856-byte partial
boundary, Code/Tape pairing, amount conservation, ancestry data, and TBC
change placement.

### NFT

- Collection creation with enough mint slots for all scenarios.
- Single NFT mint.
- Batch NFT mint.
- Normal NFT transfer.
- NFT transfer with an atomic TBC payment.

The run checks the Code/Hold/Tape triple, collection/index continuity,
ownership changes, metadata preservation, and TBC payment output.

### Pool NFT 2.0

Standard pool:

- Foundation source and mint.
- Initial liquidity and FTLP mint.
- Liquidity increase.
- TBC-to-FT swap.
- FT-to-TBC swap.
- Liquidity consumption.
- FTLP merge.
- FTLP burn.
- Pool-held FT merge.

Lock variants:

- Pool creation with multisig lock.
- Pool creation with FTLP lock time.
- Lock-time FTLP unlock and subsequent consumption.

Every transition must spend the previous Pool NFT state UTXO, recreate the
Pool NFT and tape, preserve the underlying FT contract, and satisfy the
integer AMM/state deltas.

### Ordinary TBC-to-FT OrderBook

- Sell-order creation and cancellation.
- Buy-order creation and cancellation.
- Full match.
- Partial match with a residual order.

The matrix uses only the released ordinary OrderBook. It checks fixed script
lengths, decoded order data, locked asset amounts, fee outputs, and residual
sale volume. The StableCoin `isCoin` script branch is covered by deterministic
parity tests; live StableCoin lifecycle testing remains in the StableCoin
section so the matrix does not duplicate the same order semantics.

### MultiSig

- Create a 2-of-3 wallet.
- Spend TBC using build/sign/finalize.
- Deposit FT into the multisig contract.
- Spend FT using build/sign/finalize.

Two signer keys are generated in memory for this test run; the approved
testnet account provides the third key and funding. Temporary private keys are
never written to the repository, report, command output, or checkpoint data.
Any recoverable TBC/FT result is returned to the approved test address.

### HTLC

- Base-TBC deploy and preimage withdrawal.
- Base-TBC deploy and timeout refund.
- FT deploy and preimage withdrawal.
- FT deploy and timeout refund.

Secrets stay in memory. The report records only hashlocks and public
transaction identifiers when useful.

### StableCoin

- Coin NFT creation and initial mint.
- Additional administrator mint.
- Owner transfer.
- Batch transfer.
- Merge of at least two Coin UTXOs.
- Administrator freeze.
- Administrator unfreeze.

The existing external MuSig2 signer is used for administrator operations.
The run checks the 2,012-byte Coin script, Coin NFT supply updates, Coin
amount conservation, lock-time tape updates, and 64-byte administrator
signature finalization.

### PiggyBank

- Freeze TBC.
- Spend an already-unlocked PiggyBank output.

The test uses a lock height that is already spendable, while still verifying
the script’s encoded lock value and transaction lock-time behavior.

## JavaScript Consistency Checks

The sibling `tbc-contract` checkout must report version 1.6.5. Deterministic
protocol components are compared directly between Go and JavaScript:

- Contract code scripts and their lengths/hashes.
- Tape/data serialization.
- Output ordering and fixed contract satoshi values.
- Decoded asset amounts and state fields.
- Transaction version, lock time, sequence, and sighash-sensitive layout.

Whole signed raw transactions and txids are not required to be identical when
UTXO selection, current-node dust adaptation, or signature encoding differs.
Every such difference must be classified and documented; unexplained
structural differences fail the matrix.

## Runner Architecture

The current `test/testnet-parity` program becomes a resumable stage runner.
Each contract family has a focused file and returns a public result structure
containing txids, contract IDs, and the next public state needed by dependent
stages.

The runner:

1. Requires `TBC_TESTNET_NETWORK=testnet` and refuses every other network.
2. Loads the approved WIF only from the runtime environment.
3. Builds and validates a complete transaction before broadcasting.
4. Broadcasts chained transactions strictly in parent-first order.
5. Confirms the returned txid equals the locally computed txid.
6. Fetches the transaction back through the testnet API.
7. Validates outputs, contract markers, asset deltas, and relevant state.
8. Reconstructs inputs and calculates paid versus required fee.
9. Prints one machine-readable result line per transaction.

Public checkpoint data may contain txids, output indexes, contract IDs,
addresses, and amounts. It must never contain WIFs, temporary private keys,
raw transactions, MuSig secret material, HTLC preimages, or unbroadcast
signatures.

## Failure and Resume Behavior

The runner stops at the first failed invariant or rejected broadcast and
prints the exact stage and last accepted txid. A failure is classified as one
of:

- Builder/API defect in Go.
- JavaScript parity mismatch.
- Node policy/fee rejection.
- Test-state issue such as an already-spent UTXO.
- Transient API/index delay.

Transient read-after-write delays may use bounded read-only retries.
Broadcast POST requests are never retried automatically. After fixing a real
SDK defect, the affected lifecycle is rebuilt from a fresh spendable state
and rebroadcast; an old accepted txid is not reused as proof of the fix.

## Evidence and Success Criteria

The final report contains:

- A row for every matrix operation.
- Fresh txid(s), byte size, paid fee, target fee, and API-refetch status.
- Contract IDs and pool/order/NFT identifiers needed to independently inspect
  state.
- Go-versus-JavaScript deterministic parity results.
- Asset-flow assertions and any documented current-node compatibility
  differences.
- Explicit unsupported-scope statements.

The matrix passes only if:

- Unit tests, race tests, vet, build, and parity fixtures pass.
- Every required live operation is accepted and refetched.
- Every fee meets `max(80, ceil(actual_signed_bytes * 80 / 1000))`.
- Every asset/state invariant passes.
- No private key or HTLC secret is stored in git.

