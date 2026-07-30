# Token Order Match Script Repair

## Goal

Make Token A ↔ Token B OrderBook matching pass both the local TBC script
interpreter and a real testnet node broadcast. Preserve compatibility with
orders already created by `tbc-contract` 1.6.5 whenever the existing locking
script permits it.

## Current evidence

- Token sell/buy creation and cancellation transactions are accepted by the
  testnet node.
- Both Go and JavaScript 1.6.5 construct the same TokenOrder match shape and
  fail with `Invalid OP_SPLIT range`.
- Replaying the existing testnet orders locally fails in input 0 at byte 27 of
  the 1,332-byte TokenOrder locking script.
- The failing `OP_SPLIT` expects a 21-byte field selected through a fixed stack
  offset, but the selected stack item is shorter. The failure occurs before
  price, fee, or settlement arithmetic.
- JavaScript 1.6.5 (`19acaa0`) is the current upstream version; no newer
  published fix exists.

## Chosen approach

First repair the TokenOrder unlocking-data layout. The current locking script
uses fixed stack offsets, so `GetCurrentTxOutputsDataOBFixed` and
`GetPreTxdataOB` must provide exactly the logical slots expected by the
contract. This approach can make already-created TokenOrder outputs spendable.

The implementation must not retain JavaScript parity when JavaScript parity is
the defect. It must instead preserve the locking script's verified stack
contract and document any intentional divergence.

If interpreter tracing proves that no unlocking-data encoding can satisfy the
existing script without omitting or falsifying committed transaction data, the
fallback is a versioned TokenOrder locking script. New orders will use the
corrected script; existing orders remain cancellable through their existing
cancel branch.

Rejected alternatives:

- Copy the Rust OrderBook implementation: it implements the older TBC ↔ FT
  order model rather than Token A ↔ Token B matching.
- Remove local validation and broadcast anyway: the testnet node independently
  rejects the same script.
- Patch only the interpreter: the node is the authority and exhibits the same
  failure.

## Components and data flow

1. `MatchTokenOrder` builds the five-input atomic settlement transaction.
2. `GetCurrentTxOutputsDataOBFixed` serializes current outputs into stack
   elements consumed by both TokenOrder inputs.
3. `GetPreTxdataOB` serializes each order's parent transaction and contract
   output.
4. `GetTokenOrderUnlock` concatenates current-output data, parent data, and the
   match selector (`OP_1`).
5. The TokenOrder locking script validates output ownership, amounts, fees,
   residual orders, and transaction commitments.
6. FT swap unlocks independently validate the two locked token inputs.

The repair stays in steps 2–4 unless the fallback locking-script version is
proven necessary.

## Error handling and compatibility

- Reject unsupported output counts and malformed FT Code/Tape adjacency before
  generating an unlocking script.
- Reject layouts whose logical slot count differs from the locking script's
  fixed expectation.
- Keep cancel-order behavior unchanged.
- Keep ordinary TBC ↔ FT OrderBook helpers unchanged.
- Cover both equal fills (nine physical outputs) and partial fills (twelve
  physical outputs).

## Test strategy

1. Add interpreter regression tests using deterministic TokenOrder fixtures.
   The current implementation must fail at `OP_SPLIT` before the fix.
2. Assert both order inputs and both FT inputs execute successfully after the
   fix; validating only transaction shape is insufficient.
3. Retain JavaScript fixture comparisons for locking scripts and explicitly
   update only unlocking-layout assertions that represent the upstream defect.
4. Run `go test ./...`, `go test -race ./...`, and `go vet ./...`.
5. Reuse the existing unspent testnet orders when compatible; otherwise create
   new versioned orders.
6. Broadcast the match to testnet, verify the returned txid, fetch the raw
   transaction and every parent output, and confirm:
   `paid_fee >= max(80, ceil(signed_bytes * 80 / 1000))`.

## Completion criteria

- A regression test is observed failing before production changes and passing
  afterward.
- Full Go verification passes.
- The real TokenOrder match transaction is accepted by the testnet node.
- Its txid and independently reconstructed fee metrics are recorded in the
  testnet verification report.
- No private key is stored in source, tests, shell history, or reports.
