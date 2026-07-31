# Token Order Match Script Repair Implementation Plan

> **For Codex:** Use `superpowers:executing-plans` to execute this plan task by task in the existing isolated worktree.

**Goal:** Make equal-fill and partial-fill Token A ↔ Token B matches satisfy all four contract inputs, then prove the fix with a real testnet broadcast.

**Architecture:** Preserve the existing TokenOrder locking scripts. Add TokenOrder-specific serializers that model the locking script's fixed layout: twelve parent-output slots and twelve current physical-output slots, four stack fields per slot. Keep the legacy TBC ↔ FT OrderBook serializers unchanged.

**Tech Stack:** Go, `tbc-lib-go` transaction/script interpreter, TBC testnet API.

---

### Task 1: Add the interpreter regression test

**Files:**
- Modify: `lib/contract/orderbook_token_test.go`

**Step 1: Add the interpreter imports**

```go
import (
    // existing imports
    "github.com/LoongYearMeta/tbc-lib-go/interpreter"
)
```

**Step 2: Add a helper that executes the four contract inputs**

Build a match from `tokenOrderMatchFixture`, parse it, construct the previous
outputs for buy order, buy FT, sell order, and sell FT, and call:

```go
err := interpreter.NewEngine().Execute(
    interpreter.WithTx(tx, inputIndex, previousOutput),
    interpreter.WithAfterGenesis(),
    interpreter.WithForkID(),
)
```

Fail with the case name, input index, and interpreter error.

**Step 3: Cover equal and partial fills**

```go
func TestMatchTokenOrderScriptsValidate(t *testing.T) {
    tests := []struct {
        name       string
        buyVolume  int64
        sellVolume int64
    }{
        {name: "equal fill", buyVolume: 400, sellVolume: 400},
        {name: "partial fill", buyVolume: 600, sellVolume: 400},
    }
    // Build and execute all four contract inputs for each case.
}
```

**Step 4: Run the regression test and verify RED**

Run:

```bash
go test ./lib/contract -run TestMatchTokenOrderScriptsValidate -count=1 -v
```

Expected: the current implementation fails in the TokenOrder locking script
with `Invalid OP_SPLIT range`.

**Step 5: Commit the failing test**

```bash
git add lib/contract/orderbook_token_test.go
git commit -m "test: reproduce token order match script failure"
```

### Task 2: Repair the parent and current-output layouts

**Files:**
- Modify: `lib/util/orderbookunlock.go`
- Modify: `lib/contract/orderbook_token.go`

**Step 1: Parameterize parent-output padding without changing legacy callers**

Extract the existing body behind a private helper:

```go
func getPreTxdataOBFixed(
    tx *bt.Tx,
    vout int,
    contractOutputNumber int,
    fixedOutputCount int,
) (string, error)
```

Keep `GetPreTxdataOB` passing `10`, and add:

```go
func GetTokenOrderPreTxdataOB(
    tx *bt.Tx,
    vout int,
    contractOutputNumber int,
) (string, error) {
    return getPreTxdataOBFixed(tx, vout, contractOutputNumber, 12)
}
```

Validate that `fixedOutputCount` is non-negative and the transaction does not
have more physical outputs than the fixed layout.

**Step 2: Add a TokenOrder-specific current-output serializer**

Add:

```go
func GetTokenOrderCurrentTxOutputsDataOB(tx *bt.Tx) (string, error)
```

It must:

- reject more than twelve outputs;
- encode every physical output independently as exactly four stack fields:
  amount, suffix, partial hash, and complete size;
- push a 32-byte partial hash with `0x20`, including ordinary long scripts such
  as FT Tape outputs;
- not combine FT Code and Tape outputs;
- append four `OP_0` bytes per missing physical output until twelve slots exist.

Retain `GetCurrentTxOutputsDataOBFixed` byte-for-byte behavior for legacy
OrderBook callers.

**Step 3: Route TokenOrder unlock construction through both new helpers**

```go
preTxData, err := util.GetTokenOrderPreTxdataOB(preTX, preTxVout, 1)
// ...
currentTxData, err := util.GetTokenOrderCurrentTxOutputsDataOB(currentTX)
```

**Step 4: Run focused tests and verify GREEN**

Run:

```bash
go test ./lib/contract -run 'Test(MatchTokenOrderScriptsValidate|MatchTokenOrder)' -count=1 -v
go test ./lib/util -count=1
```

Expected: equal and partial fills validate all four contract inputs.

**Step 5: Commit the repair**

```bash
git add lib/util/orderbookunlock.go lib/contract/orderbook_token.go
git commit -m "fix: align token order match stack layout"
```

### Task 3: Verify the repository

**Files:**
- No production changes expected.

**Step 1: Format touched Go files**

Run:

```bash
gofmt -w lib/util/orderbookunlock.go lib/contract/orderbook_token.go lib/contract/orderbook_token_test.go
```

**Step 2: Run all normal tests**

```bash
go test ./...
```

**Step 3: Run race tests**

```bash
go test -race ./...
```

**Step 4: Run static analysis**

```bash
go vet ./...
```

### Task 4: Broadcast and independently verify the real testnet match

**Files:**
- Modify: `docs/verification/2026-07-30-fee-safety-testnet.md`

**Step 1: Confirm the known sell and buy order outputs remain unspent**

Query the testnet API for:

- sell: `0f2c990cf302f2659c21fc9d04bea324cdab4ad6bb80231984806f4b090d4aa7`
- buy: `fc8de7be6f8b6cda1a0a1195dc4c283daeeacc5c59e96c4d2deaafb792176c2d`

Stop if either order or its paired FT output has been spent.

**Step 2: Load the testnet-only WIF without echo or persistence**

Use an interactive PTY, disable echo while reading the value, export it only
for the process, and unset it immediately after the broadcast command. Never
write the WIF to a file, command argument, shell history, output, or report.

**Step 3: Build, locally validate, and broadcast**

Set:

```text
TBC_TESTNET_NETWORK=testnet
TBC_TESTNET_BROADCAST=1
TBC_TESTNET_STAGE=token-order-match
TBC_TESTNET_TOKEN_A=0a600c8b0f6cf4a396c4cebd237096014eefc915918a81df1cc36aea990d1280
TBC_TESTNET_TOKEN_B=f572760f4737e17b2c2c810da27aae3950d783e13d0118a338263862dd8a99de
TBC_TESTNET_ORDER_SELL_TXID=0f2c990cf302f2659c21fc9d04bea324cdab4ad6bb80231984806f4b090d4aa7
TBC_TESTNET_ORDER_BUY_TXID=fc8de7be6f8b6cda1a0a1195dc4c283daeeacc5c59e96c4d2deaafb792176c2d
```

Run:

```bash
go run ./test/testnet-parity
```

Expected: local validation passes inputs 0 through 4 and the node returns a
testnet transaction ID.

**Step 4: Fetch and independently verify the accepted transaction**

Fetch the raw transaction by txid, fetch every previous output, and calculate:

```text
paid_fee = sum(inputs) - sum(outputs)
required_fee = max(80, ceil(signed_bytes * 80 / 1000))
```

Confirm the fetched raw transaction matches the locally broadcast raw
transaction and `paid_fee >= required_fee`.

**Step 5: Update the verification report**

Record the txid, acceptance, byte size, paid fee, required fee, and the fact
that all four contract inputs passed local execution. Do not record secrets.

**Step 6: Commit the report**

```bash
git add docs/verification/2026-07-30-fee-safety-testnet.md
git commit -m "docs: record token order match testnet verification"
```

### Task 5: Final branch verification and integration

**Files:**
- No changes expected.

**Step 1: Run `superpowers:verification-before-completion`**

Repeat the focused regression test and full verification commands from Task 3.

**Step 2: Run `superpowers:finishing-a-development-branch`**

Use the previously selected local-merge workflow, verify the target branch,
merge without discarding unrelated user changes, and rerun the required tests
on the merged result.

