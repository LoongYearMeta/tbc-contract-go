# TBC Go Fee Safety Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go base library and contract SDK reject unsafe funding, avoid dust/overflow outputs, and pay at least 80 sat/KB against the final signed transaction size.

**Architecture:** `tbc-lib-go` owns checked generic fee and change behavior. `tbc-contract-go` owns a reusable signed-size finalizer plus contract-specific sign callbacks and output-index rules. The contract SDK remains source-compatible with v1.0.0 while the library fix is developed and tested locally, so the final dependency update can follow the library release without committing a non-portable `replace`.

**Tech Stack:** Go 1.17, `tbc-lib-go`, `tbc-contract-go`, TBC transaction version 10, table-driven Go tests, TBC testnet API.

## Global Constraints

- TBC values use integer satoshis internally.
- Node ordinary-output dust is 10 satoshis; generic SDK change dust is 42 satoshis for installed JavaScript parity.
- Contract target fee is `max(80, ceil(actualSignedBytes * 80 / 1000))`.
- Zero-satoshi data outputs remain valid.
- Never write or print the testnet WIF.
- Do not commit a local filesystem `replace` directive.
- Every production behavior change follows red-green-refactor.

---

### Task 1: Strict `tbc-lib-go` change and fee primitives

**Files:**
- Modify: `/home/ubuntu/projects/tbc-lib-go/transaction/change.go`
- Modify: `/home/ubuntu/projects/tbc-lib-go/transaction/fees.go`
- Modify: `/home/ubuntu/projects/tbc-lib-go/transaction/errors.go`
- Test: `/home/ubuntu/projects/tbc-lib-go/transaction/change_test.go`
- Test: `/home/ubuntu/projects/tbc-lib-go/transaction/fees_test.go`

**Interfaces:**
- Produces: `NodeDustLimit uint64`, `DustLimit uint64`
- Produces: `CeilFeeForBytes(sizeBytes int, satPerKB uint64, minimum uint64) (uint64, error)`
- Preserves: `(*Tx).ChangeToAddress(string, *FeeQuote) error`

- [ ] **Step 1: Write failing strict-change tests**

Add table-driven cases that construct real transactions and assert:

```go
tests := []struct {
    name          string
    availableSat  uint64
    wantErr       error
    wantChangeSat uint64
}{
    {"below fee", 79, ErrInsufficientInputs, 0},
    {"exact fee", 80, nil, 0},
    {"fee plus 41 is donated", 121, nil, 0},
    {"fee plus 42 creates change", 122, nil, 42},
}
```

Use a fee quote whose estimated fee is a hand-derived 80 satoshis so the
expected values do not reuse the production fee helper.

- [ ] **Step 2: Verify the strict-change tests fail**

Run:

```bash
go test ./transaction -run 'TestTx_Change(ToAddress)?_StrictFunding|TestTx_ChangeSDKDust' -count=1
```

Expected: the below-fee case returns nil and/or the 2-41 satoshi cases create
change because v1.0.0 uses `DustLimit = 1`.

- [ ] **Step 3: Write failing fee-boundary tests**

Add literal expectations:

```go
tests := []struct {
    size, rate, minimum int
    want                uint64
}{
    {999, 80, 80, 80},
    {1000, 80, 80, 80},
    {1001, 80, 80, 81},
}
```

Also assert negative sizes and multiplication overflow return errors.

- [ ] **Step 4: Verify the fee helper test fails because the API is missing**

Run:

```bash
go test ./transaction -run TestCeilFeeForBytesBoundaries -count=1
```

Expected: compile failure for undefined `CeilFeeForBytes`.

- [ ] **Step 5: Implement the minimum fee and strict change behavior**

Implement:

```go
const (
    NodeDustLimit uint64 = 10
    DustLimit     uint64 = 42
)

func CeilFeeForBytes(sizeBytes int, satPerKB, minimum uint64) (uint64, error) {
    if sizeBytes < 0 {
        return 0, ErrInvalidFee
    }
    hi, lo := bits.Mul64(uint64(sizeBytes), satPerKB)
    if hi != 0 || lo > ^uint64(0)-999 {
        return 0, ErrFeeOverflow
    }
    fee := (lo + 999) / 1000
    if fee < minimum {
        fee = minimum
    }
    return fee, nil
}
```

Use checked addition/subtraction in `change`. Return
`ErrInsufficientInputs` only when `available < txFees`; permit exact fee and
donate a remainder below `DustLimit`.

- [ ] **Step 6: Run library tests and refactor**

Run:

```bash
go test -count=1 ./transaction
go test -count=1 ./...
go test -race ./transaction
go vet ./...
```

Expected: all pass.

- [ ] **Step 7: Commit the library change**

```bash
git add transaction/change.go transaction/fees.go transaction/errors.go transaction/change_test.go transaction/fees_test.go
git commit -m "fix: make fee and change handling strict"
```

---

### Task 2: Shared contract signed-fee finalizer

**Files:**
- Create: `lib/contract/fee.go`
- Create: `lib/contract/fee_test.go`

**Interfaces:**
- Produces: `contractTargetFee(sizeBytes int) (uint64, error)`
- Produces: `finalizeSignedFee(tx *bt.Tx, changeIndex int, sign func() error) error`
- Produces: `requireOrdinaryOutput(valueSat uint64, context string) error`

- [ ] **Step 1: Write failing fee-formula and funding tests**

Use literal boundary cases:

```go
func TestContractTargetFeeBoundaries(t *testing.T) {
    cases := map[int]uint64{0: 80, 999: 80, 1000: 80, 1001: 81}
    // Assert each literal result.
}
```

Construct a real `bt.Tx` with known input/output satoshis and assert the
finalizer:

- returns an error when inputs cannot cover non-change outputs plus target;
- rejects a final change between 1 and 41 satoshis;
- re-signs after changing the output;
- leaves `inputSum-outputSum >= contractTargetFee(len(tx.Bytes()))`.

- [ ] **Step 2: Verify the new tests fail to compile**

Run:

```bash
go test ./lib/contract -run 'TestContractTargetFee|TestFinalizeSignedFee' -count=1
```

Expected: undefined helper failures.

- [ ] **Step 3: Implement checked shared helpers**

Implement checked sums with `math/bits`, an explicit change index, and a
bounded convergence loop:

```go
func finalizeSignedFee(tx *bt.Tx, changeIndex int, sign func() error) error {
    for attempt := 0; attempt < 8; attempt++ {
        if err := sign(); err != nil { return err }
        target, err := contractTargetFee(len(tx.Bytes()))
        if err != nil { return err }
        changed, err := setChangeForTarget(tx, changeIndex, target)
        if err != nil { return err }
        if !changed {
            return verifyPaidFee(tx, target)
        }
    }
    return ErrFeeDidNotConverge
}
```

The final verification recomputes size after the last signature and rejects
underpayment.

- [ ] **Step 4: Verify helper tests and full contract baseline**

Run:

```bash
go test ./lib/contract -run 'TestContractTargetFee|TestFinalizeSignedFee' -count=1
go test -count=1 ./lib/contract
```

Expected: all pass.

- [ ] **Step 5: Commit the shared helper**

```bash
git add lib/contract/fee.go lib/contract/fee_test.go
git commit -m "feat: centralize signed transaction fees"
```

---

### Task 3: Finalize FT and StableCoin fees from actual signed bytes

**Files:**
- Modify: `lib/contract/ft.go`
- Modify: `lib/contract/stablecoin.go`
- Create: `lib/contract/fee_regression_test.go`

**Interfaces:**
- Consumes: `finalizeSignedFee`
- Preserves: all exported FT and StableCoin method signatures

- [ ] **Step 1: Write failing actual-fee regression tests**

Reuse deterministic FT fixtures and assert:

```go
paid := tx.TotalInputSatoshis() - tx.TotalOutputSatoshis()
want, _ := contractTargetFee(len(tx.Bytes()))
if paid < want {
    t.Fatalf("paid fee %d below target %d for %d bytes", paid, want, len(tx.Bytes()))
}
```

Cover FT mint, one-input transfer, batch transfer, merge, StableCoin
one-input transfer, and StableCoin batch transfer.

- [ ] **Step 2: Verify existing builders fail the new assertions**

Run:

```bash
go test ./lib/contract -run 'TestFTActualSignedFee|TestStableCoinActualSignedFee' -count=1
```

Expected: FT mint/transfer and single-input StableCoin paths report paid fee
below the target.

- [ ] **Step 3: Migrate FT private-key builders**

Replace ad hoc final adjustments with `finalizeSignedFee`. Each sign callback
must sign fee inputs and rebuild every FT unlock after the change amount is
updated. Require a retained 42-satoshi-or-greater fee change output; otherwise
return an error asking for a larger fee UTXO.

- [ ] **Step 4: Verify FT regressions pass**

Run:

```bash
go test ./lib/contract -run 'TestFTActualSignedFee|TestMintFT|TestFT' -count=1
```

Expected: all pass.

- [ ] **Step 5: Migrate StableCoin private-key builders**

Use the shared finalizer for all FT input counts. Preserve locktime and
StableCoin sequence values before every signing pass.

- [ ] **Step 6: Verify StableCoin and complete contract tests**

Run:

```bash
go test ./lib/contract -run 'TestStableCoinActualSignedFee|TestStableCoin' -count=1
go test -count=1 ./...
```

Expected: all pass.

- [ ] **Step 7: Commit FT and StableCoin fixes**

```bash
git add lib/contract/ft.go lib/contract/stablecoin.go lib/contract/fee_regression_test.go
git commit -m "fix: price FT transactions from signed size"
```

---

### Task 4: Fix NFT and direct-output boundary failures

**Files:**
- Modify: `lib/contract/nft.go`
- Modify: `lib/contract/multisig.go`
- Modify: `lib/contract/htlc.go`
- Modify: `lib/contract/piggybank.go`
- Create: `lib/contract/fee_boundaries_test.go`

**Interfaces:**
- Consumes: `finalizeSignedFee`, `requireOrdinaryOutput`
- Preserves: existing exported method signatures

- [ ] **Step 1: Write failing boundary tests**

Assert:

```go
// MultiSig: totalIn < amount + fee returns an error, never wraps.
// HTLC: 80 sat input cannot create a zero-satoshi withdrawal/refund.
// PiggyBank: sum equal to fee cannot create a zero-satoshi output.
// NFT: an actual-size adjustment failure is returned to the caller.
```

Use real scripts and transactions; no mocks.

- [ ] **Step 2: Verify boundary tests fail for the expected branches**

Run:

```bash
go test ./lib/contract -run 'TestMultiSigRejectsFeeUnderflow|TestHTLCRejectsDustResult|TestPiggyBankRejectsDustResult|TestNFTReturnsFeeAdjustmentError' -count=1
```

Expected: MultiSig wraps, HTLC/PiggyBank return raw transactions, and NFT
returns nil despite the fee adjustment failure.

- [ ] **Step 3: Implement checked boundary validation**

Check additions before subtraction, validate every ordinary recipient/refund
output against 42 satoshis, and return NFT fee-finalization errors. Use the
shared finalizer for signed NFT transfers.

- [ ] **Step 4: Run focused and full tests**

Run:

```bash
go test ./lib/contract -run 'TestMultiSigRejectsFeeUnderflow|TestHTLCRejectsDustResult|TestPiggyBankRejectsDustResult|TestNFTReturnsFeeAdjustmentError' -count=1
go test -count=1 ./...
```

Expected: all pass.

- [ ] **Step 5: Commit contract boundary fixes**

```bash
git add lib/contract/nft.go lib/contract/multisig.go lib/contract/htlc.go lib/contract/piggybank.go lib/contract/fee_boundaries_test.go
git commit -m "fix: reject dust and fee underflow outputs"
```

---

### Task 5: Add exact satoshi UTXO selection

**Files:**
- Modify: `lib/api/api.go`
- Modify: `lib/api/api_umtxo.go`
- Create: `lib/api/api_utxo_test.go`

**Interfaces:**
- Produces: `FetchUTXOSat(address string, minimumSat uint64, network string) (*bt.UTXO, error)`
- Produces: `FetchUTXOWithAPITxIDSat(address string, minimumSat uint64, network string) (*bt.UTXO, string, error)`
- Preserves: legacy `float64` wrappers

- [ ] **Step 1: Extract and test deterministic selection**

Write table-driven tests for an unexported selector:

```go
cases := []struct {
    minimum uint64
    values  []uint64
    want    uint64
    wantErr bool
}{
    {100, []uint64{99, 100, 101}, 100, false},
    {102, []uint64{99, 100, 101}, 0, true},
}
```

- [ ] **Step 2: Verify the selection test fails because the selector is missing**

Run:

```bash
go test ./lib/api -run TestSelectUTXOAtLeast -count=1
```

Expected: undefined selector.

- [ ] **Step 3: Implement satoshi-native methods and wrappers**

The selector returns the first sufficient UTXO, matching existing order, and
returns a descriptive error otherwise. Legacy wrappers validate finite,
non-negative, six-decimal-compatible values before delegating.

- [ ] **Step 4: Run API and full tests**

Run:

```bash
go test ./lib/api -count=1
go test -count=1 ./...
```

Expected: all pass.

- [ ] **Step 5: Commit API changes**

```bash
git add lib/api/api.go lib/api/api_umtxo.go lib/api/api_utxo_test.go
git commit -m "feat: select fee UTXOs in satoshis"
```

---

### Task 6: Cross-repository integration and verification

**Files:**
- Modify temporarily, then restore: `go.mod`
- Create: `docs/fee-safety-testnet-report.md`

**Interfaces:**
- Consumes: local `tbc-lib-go` fee-safety branch during verification
- Produces: clean commits in both repositories and a reproducible report

- [ ] **Step 1: Test `tbc-contract-go` against the local fixed library**

Temporarily run:

```bash
go mod edit -replace github.com/LoongYearMeta/tbc-lib-go=/home/ubuntu/projects/tbc-lib-go/.worktrees/fee-safety
go test -count=1 ./...
go test -race ./...
go vet ./...
```

Restore `go.mod`/`go.sum` afterward and verify no `replace` remains.

- [ ] **Step 2: Verify both repositories independently**

Run:

```bash
# tbc-lib-go worktree
go test -count=1 ./...
go test -race ./...
go vet ./...

# tbc-contract-go worktree
go test -count=1 ./...
go test -race ./...
go vet ./...
git diff --check
```

Expected: all commands exit zero.

- [ ] **Step 3: Perform testnet fee verification**

With `TBC_TESTNET_WIF` already set outside command history, broadcast a fresh
FT mint/transfer and one large contract transaction. Record only txids,
serialized byte sizes, paid fees, and sat/KB. Never record raw transactions
or secrets.

- [ ] **Step 4: Write and verify the report**

The report includes:

```text
scenario | txid | bytes | paid_sat | target_sat | actual_sat_per_kb | accepted
```

Every accepted transaction must have `paid_sat >= target_sat`.

- [ ] **Step 5: Commit the report**

```bash
git add docs/fee-safety-testnet-report.md
git commit -m "test: verify fee safety on TBC testnet"
```

- [ ] **Step 6: Confirm clean worktrees and dependency handoff**

Run:

```bash
git status --short
git log -5 --oneline
```

Document that `tbc-contract-go` should update from v1.0.0 only after the
`tbc-lib-go` commit is published or tagged; do not invent an unavailable
module version.
