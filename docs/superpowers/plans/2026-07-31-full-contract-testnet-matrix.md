# Full Contract Testnet Matrix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and execute a resumable testnet harness that validates every economically distinct transaction path supported by `tbc-contract-go`, checks deterministic compatibility with released JavaScript 1.6.5, and records fresh broadcast/refetch/fee evidence without persisting secrets.

**Architecture:** Keep live integration code under `test/testnet-parity`, split by contract family, and route every signed transaction through one evidence collector that validates the local txid, performs exactly one broadcast POST, refetches the transaction and all parents with bounded read-only retries, calculates the actual fee, and emits one JSON result line. Family stages consume only runtime WIF material and public resume parameters; deterministic JavaScript comparisons remain ordinary unit tests backed by fixtures generated from the sibling `tbc-contract` 1.6.5 checkout.

**Tech Stack:** Go 1.17, `tbc-contract-go`, `tbc-lib-go`, TBC testnet REST API, Node.js, released `tbc-contract` 1.6.5, Go `testing`, shell verification commands.

---

## File Structure

- Create `test/testnet-parity/evidence.go`: broadcast/refetch/fee evidence and machine-readable output.
- Create `test/testnet-parity/evidence_test.go`: pure tests for fee targets, overflow, txid matching, and JSON secret exclusion.
- Create `test/testnet-parity/stage_state.go`: public-only resume state and stage dispatch.
- Create `test/testnet-parity/stage_state_test.go`: config/stage/state validation tests.
- Create `test/testnet-parity/ft_stage.go`: fresh FT v3 foundation, transfer, additional-info transfer, batch, merge, and Token HTLC.
- Create `test/testnet-parity/nft_stage.go`: collection, single/batch mint, transfer, and transfer-with-TBC.
- Create `test/testnet-parity/multisig_stage.go`: ephemeral 2-of-3 wallet, TBC spend, FT deposit, and FT spend.
- Create `test/testnet-parity/htlc_piggybank_stage.go`: base HTLC withdraw/refund and PiggyBank freeze/unfreeze.
- Modify `test/testnet-parity/stablecoin_stage.go`: add admin mint, batch, merge, and explicit state checks.
- Create `test/testnet-parity/pool_stage.go`: standard PoolNFT2 and lock-variant lifecycles.
- Create `test/testnet-parity/orderbook_stage.go`: ordinary FT/TBC sell, buy, cancel, full match, and partial match.
- Modify `test/testnet-parity/core_contracts.go`: retain shared transaction/UTXO helpers and remove family logic moved to focused files.
- Modify `test/testnet-parity/main.go`: load safe runtime configuration and dispatch resumable stages.
- Modify `test/testnet-parity/main_test.go`: cover the complete testnet-only configuration surface.
- Modify `scripts/generate-js-parity-fixtures.js`: export deterministic code/tape/order fixtures from JavaScript 1.6.5.
- Modify `lib/contract/testdata/js-1.6.5/script-hashes.json`: commit only public deterministic fixture hashes and decoded fields.
- Modify `lib/contract/script_parity_test.go`: compare all deterministic Go protocol artifacts with the JavaScript fixture.
- Create `docs/verification/2026-07-31-full-contract-testnet-matrix.md`: final coverage table with public txids and fee/state results.

### Task 1: Evidence Collector and Secret-Safe Result Format

**Files:**
- Create: `test/testnet-parity/evidence.go`
- Create: `test/testnet-parity/evidence_test.go`
- Modify: `test/testnet-parity/main.go`
- Modify: `test/testnet-fee-report/main.go`

- [x] **Step 1: Write failing unit tests for the fee formula and public result**

```go
func TestTargetFee80PerKBWithFloor(t *testing.T) {
	tests := []struct {
		size int
		want uint64
	}{{1, 80}, {999, 80}, {1000, 80}, {1001, 81}, {2500, 200}}
	for _, test := range tests {
		if got := targetFee80(test.size); got != test.want {
			t.Fatalf("size=%d got=%d want=%d", test.size, got, test.want)
		}
	}
}

func TestEvidenceJSONContainsOnlyPublicFields(t *testing.T) {
	result := txEvidence{
		Stage: "ft-transfer", TxID: strings.Repeat("1", 64),
		Bytes: 250, PaidFee: 80, TargetFee: 80, Refetched: true,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"wif", "private", "secret", "raw", "signature"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("public evidence leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}
```

- [x] **Step 2: Run the focused tests and confirm they fail before implementation**

Run: `go test ./test/testnet-parity -run 'TestTargetFee80PerKBWithFloor|TestEvidenceJSONContainsOnlyPublicFields' -count=1`

Expected: FAIL because `targetFee80` and `txEvidence` do not exist.

- [x] **Step 3: Implement the evidence collector**

```go
type txEvidence struct {
	Stage      string `json:"stage"`
	TxID       string `json:"txid"`
	Bytes      int    `json:"bytes"`
	PaidFee    uint64 `json:"paid_fee_sat"`
	TargetFee  uint64 `json:"target_fee_sat"`
	Refetched  bool   `json:"refetched"`
	Invariant  string `json:"invariant"`
}

func targetFee80(size int) uint64 {
	target := (uint64(size)*80 + 999) / 1000
	if target < 80 {
		return 80
	}
	return target
}

func checkedAddSatoshis(total, value uint64) (uint64, error) {
	next, carry := bits.Add64(total, value, 0)
	if carry != 0 {
		return 0, fmt.Errorf("satoshi sum overflow")
	}
	return next, nil
}

func feeFromParents(tx *bt.Tx, fetch func(string) (*bt.Tx, error)) (uint64, error) {
	var inputs uint64
	for i, input := range tx.Inputs {
		parent, err := fetch(input.PreviousTxIDStr())
		if err != nil {
			return 0, fmt.Errorf("input %d parent: %w", i, err)
		}
		vout := int(input.PreviousTxOutIndex)
		if vout < 0 || vout >= len(parent.Outputs) {
			return 0, fmt.Errorf("input %d parent vout %d out of range", i, vout)
		}
		inputs, err = checkedAddSatoshis(inputs, parent.Outputs[vout].Satoshis)
		if err != nil {
			return 0, err
		}
	}
	var outputs uint64
	for _, output := range tx.Outputs {
		var err error
		outputs, err = checkedAddSatoshis(outputs, output.Satoshis)
		if err != nil {
			return 0, err
		}
	}
	if inputs < outputs {
		return 0, fmt.Errorf("outputs %d exceed inputs %d", outputs, inputs)
	}
	return inputs - outputs, nil
}
```

Implement `broadcastAndVerify` so it parses and validates the transaction before the POST, calls `api.BroadcastTXRaw` exactly once, compares the returned txid to `tx.TxID()`, refetches with at most ten GET attempts, checks byte-for-byte raw equality, calculates the fee from refetched parents, rejects underpayment, runs the family invariant callback, and prints `RESULT <json>`.

- [x] **Step 4: Make the evidence unit tests pass**

Run: `go test ./test/testnet-parity ./test/testnet-fee-report -count=1`

Expected: PASS.

- [x] **Step 5: Route existing broadcasts through the collector**

Replace `broadcastOne` calls with:

```go
accepted, evidence, err := broadcastAndVerify(
	cfg, label, raw,
	func(tx *bt.Tx) error { return validate(tx) },
)
```

Retain a compatibility wrapper only until every family file is migrated, then remove it.

- [x] **Step 6: Commit the evidence foundation**

```bash
git add test/testnet-parity/evidence.go test/testnet-parity/evidence_test.go \
  test/testnet-parity/main.go test/testnet-fee-report/main.go
git commit -m "test: add testnet transaction evidence collector"
```

### Task 2: Resumable Stage State and Hard Testnet Guard

**Files:**
- Create: `test/testnet-parity/stage_state.go`
- Create: `test/testnet-parity/stage_state_test.go`
- Modify: `test/testnet-parity/main.go`
- Modify: `test/testnet-parity/main_test.go`

- [x] **Step 1: Write failing config and public-state tests**

```go
func TestParseStageRejectsUnknownStage(t *testing.T) {
	_, err := parseStage("mainnet-send")
	if err == nil || !strings.Contains(err.Error(), "unknown testnet stage") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPublicStateRejectsSecretFields(t *testing.T) {
	typ := reflect.TypeOf(publicState{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, forbidden := range []string{"wif", "private", "secret", "raw", "signature"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("public state field %q is forbidden", typ.Field(i).Name)
			}
		}
	}
}
```

- [x] **Step 2: Run the focused config tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'TestParseStage|TestPublicState|TestLoadConfig' -count=1`

Expected: FAIL because the new state parser is absent.

- [x] **Step 3: Implement explicit stage names and public resume state**

```go
type stageName string

const (
	stageFoundation  stageName = "foundation"
	stageFT          stageName = "ft"
	stageNFT         stageName = "nft"
	stageMultiSig    stageName = "multisig"
	stageBaseHTLC    stageName = "base-htlc"
	stagePiggyBank   stageName = "piggybank"
	stageStableCoin  stageName = "stablecoin"
	stagePoolCreate  stageName = "pool-create"
	stagePoolTrade   stageName = "pool-trade"
	stagePoolLock    stageName = "pool-lock"
	stageOrderBook   stageName = "orderbook"
)

type publicState struct {
	TokenID      string `json:"token_id,omitempty"`
	TokenCode    string `json:"token_code_hash,omitempty"`
	PoolID       string `json:"pool_id,omitempty"`
	CollectionID string `json:"collection_id,omitempty"`
	CoinID       string `json:"coin_id,omitempty"`
	LastTxID     string `json:"last_txid,omitempty"`
	LastVout     uint32 `json:"last_vout,omitempty"`
}
```

Use environment variables for public resume fields. Do not create a state file automatically. Keep `TBC_TESTNET_WIF` runtime-only and reject every network string except exact `testnet`.

- [ ] **Step 4: Add dispatch that stops at the first failed stage**

```go
func runStage(ctx *stageContext) error {
	switch ctx.Config.Stage {
	case stageFoundation:
		return runFoundationStage(ctx)
	case stageFT:
		return runFTStage(ctx)
	case stageNFT:
		return runNFTStage(ctx)
	case stageMultiSig:
		return runMultiSigStage(ctx)
	case stageBaseHTLC:
		return runBaseHTLCStage(ctx)
	case stagePiggyBank:
		return runPiggyBankStage(ctx)
	case stageStableCoin:
		return runStableCoinStage(ctx)
	case stagePoolCreate:
		return runPoolCreateStage(ctx)
	case stagePoolTrade:
		return runPoolTradeStage(ctx)
	case stagePoolLock:
		return runPoolLockStage(ctx)
	case stageOrderBook:
		return runOrderBookStage(ctx)
	default:
		return fmt.Errorf("unknown testnet stage %q", ctx.Config.Stage)
	}
}
```

- [ ] **Step 5: Run tests and commit**

Run: `go test ./test/testnet-parity -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/stage_state.go test/testnet-parity/stage_state_test.go \
  test/testnet-parity/main.go test/testnet-parity/main_test.go
git commit -m "test: make contract testnet runner resumable"
```

### Task 3: Full FT and Token HTLC Stage

**Files:**
- Create: `test/testnet-parity/ft_stage.go`
- Create: `test/testnet-parity/ft_stage_test.go`
- Modify: `test/testnet-parity/main.go`

- [x] **Step 1: Write failing pure assertions for released FT v3**

```go
func TestValidateFTV3Pair(t *testing.T) {
	code, err := contract.NewFT(&contract.FtParams{
		Name: "Matrix", Symbol: "MFT", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	raws, err := code.MintFT(testPrivateKey(t), testAddress(t), testFundingUTXO(t))
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(raws[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := validateFTV3Outputs(mint, 0); err != nil {
		t.Fatal(err)
	}
}
```

`validateFTV3Outputs` must require Code and Tape outputs, a 1,884-byte code script, the 1,856-byte partial boundary, the expected amount, and matching contract ancestry.

- [x] **Step 2: Run the FT assertion test and confirm failure**

Run: `go test ./test/testnet-parity -run TestValidateFTV3Pair -count=1`

Expected: FAIL because `validateFTV3Outputs` is absent.

- [x] **Step 3: Implement the FT lifecycle in strict parent-first order**

Use one fresh FT v3 with enough supply, then broadcast:

```text
ft-source
ft-mint
ft-transfer
ft-transfer-additional-info
ft-batch-transfer
ft-merge
token-htlc-withdraw-deploy
token-htlc-withdraw
token-htlc-refund-deploy
token-htlc-refund
```

Construct the batch with two independently generated recipient addresses and return both resulting FT branches to the approved address in the merge. Generate HTLC preimages with `crypto/rand`, keep them in memory, and emit only public txids.

- [x] **Step 4: Assert amounts, output layout, and alternative signing equivalence**

For every FT transaction, decode each Tape amount with `contractutil.GetFtBalanceFromTape`, sum inputs and outputs as `big.Int`, and require equality. Locally build the token-HTLC direct-sign and build/fill variants from cloned state and require equal version, lock time, sequences, input outpoints, output scripts, and output values; broadcast only the direct-sign variant.

- [x] **Step 5: Run local tests and commit**

Run: `go test ./lib/contract ./lib/util ./test/testnet-parity -run 'FT|TokenHTLC|ValidateFT' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/ft_stage.go test/testnet-parity/ft_stage_test.go \
  test/testnet-parity/main.go
git commit -m "test: cover full FT testnet lifecycle"
```

### Task 4: Full NFT Stage

**Files:**
- Create: `test/testnet-parity/nft_stage.go`
- Create: `test/testnet-parity/nft_stage_test.go`
- Modify: `test/testnet-parity/core_contracts.go`

- [x] **Step 1: Write failing Code/Hold/Tape and payment-output tests**

```go
func TestValidateNFTOutputsRequiresCodeHoldTape(t *testing.T) {
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 200, LockingScript: mustScript(t, "006a")})
	if err := validateNFTOutputs(tx, 0); err == nil {
		t.Fatal("expected incomplete NFT output triple to fail")
	}
}

func TestValidateNFTPaymentOutput(t *testing.T) {
	tx := bt.NewTx()
	tx.AddP2PKHOutput(testAddress(t), 10_000)
	if err := validatePaymentOutput(tx, testAddress(t), 10_000); err != nil {
		t.Fatal(err)
	}
}
```

- [x] **Step 2: Run the NFT tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'TestValidateNFT' -count=1`

Expected: FAIL because the validators do not exist.

- [x] **Step 3: Implement and broadcast the NFT lifecycle**

Create a collection with supply at least four, then broadcast:

```text
nft-collection-create
nft-single-mint
nft-batch-mint-1
nft-batch-mint-2
nft-transfer
nft-transfer-with-tbc
```

Use the generated temporary recipient for the normal transfer and return ownership to the approved address with `TransferNFTWithTBC`. Refetch `/nft/nftinfo` where the indexer is available and also validate raw Code/Hold/Tape scripts so an index delay cannot mask a structural failure.

- [x] **Step 4: Assert collection/index continuity and metadata**

Decode Tape data before and after transfer and require identical name, symbol, attributes, and description. Require the collection ID and index to remain constant, the Hold script to change to the intended owner, and the TBC payment output to equal the requested satoshi amount.

- [x] **Step 5: Run tests and commit**

Run: `go test ./lib/contract ./test/testnet-parity -run 'NFT|ValidateNFT' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/nft_stage.go test/testnet-parity/nft_stage_test.go \
  test/testnet-parity/core_contracts.go
git commit -m "test: cover full NFT testnet lifecycle"
```

### Task 5: MultiSig, Base HTLC, PiggyBank, and P2PKH Stages

**Files:**
- Create: `test/testnet-parity/multisig_stage.go`
- Create: `test/testnet-parity/multisig_stage_test.go`
- Create: `test/testnet-parity/htlc_piggybank_stage.go`
- Modify: `test/testnet-parity/core_contracts.go`

- [x] **Step 1: Write failing tests for ephemeral signer creation and secret-safe output**

```go
func TestNewEphemeralMultiSigSetIsTwoOfThree(t *testing.T) {
	set, err := newEphemeralMultiSigSet(testPrivateKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if set.Required != 2 || len(set.Signers) != 3 || len(set.PublicKeys) != 3 {
		t.Fatalf("unexpected signer set: %#v", set)
	}
	if !sort.StringsAreSorted(set.PublicKeys) {
		t.Fatal("public keys must use deterministic order")
	}
}
```

- [x] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'MultiSig|HTLC|PiggyBank' -count=1`

Expected: FAIL because the split stage helpers do not exist.

- [x] **Step 3: Implement P2PKH and 2-of-3 MultiSig lifecycles**

Generate two `bec.NewPrivateKey` values in memory and combine them with the approved key. Broadcast:

```text
plain-p2pkh-self-transfer
multisig-wallet-create
multisig-tbc-spend
multisig-ft-deposit
multisig-ft-spend
```

For both TBC and FT spends, call the exported build, per-signer sign, and finish methods. Require two signatures per multisig input, validate the multisig address against the sorted public keys, and return FT/TBC to the approved test address.

- [x] **Step 4: Implement base HTLC and PiggyBank lifecycles**

Broadcast:

```text
base-htlc-withdraw-deploy
base-htlc-withdraw
base-htlc-refund-deploy
base-htlc-refund
piggybank-freeze
piggybank-unfreeze
```

Use a cryptographically random preimage in memory. Use an already-spendable block height for refund/unfreeze, and verify encoded lock time, input sequence, and transaction lock time. Locally compare direct-sign and build/fill transaction structure; broadcast one variant.

- [x] **Step 5: Run tests and commit**

Run: `go test ./lib/contract ./test/testnet-parity -run 'MultiSig|HTLC|PiggyBank|Plain' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/multisig_stage.go \
  test/testnet-parity/multisig_stage_test.go \
  test/testnet-parity/htlc_piggybank_stage.go \
  test/testnet-parity/core_contracts.go
git commit -m "test: cover multisig HTLC and piggybank lifecycles"
```

### Task 6: Complete StableCoin Lifecycle

**Files:**
- Modify: `test/testnet-parity/stablecoin_stage.go`
- Create: `test/testnet-parity/stablecoin_stage_test.go`
- Modify: `test/testnet-parity/js-musig-sign.js`

- [x] **Step 1: Write failing tests for 64-byte admin signatures and lock-time Tape**

```go
func TestValidateAdminSignatures(t *testing.T) {
	if err := validateAdminSignatures([][]byte{make([]byte, 63)}); err == nil {
		t.Fatal("expected 63-byte signature rejection")
	}
	if err := validateAdminSignatures([][]byte{make([]byte, 64)}); err != nil {
		t.Fatal(err)
	}
}

func TestStableCoinLockTimeRoundTrip(t *testing.T) {
	tape := testCoinTape(t)
	locked, err := contract.SetLockTimeInTape(tape, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := contract.GetLockTimeFromTape(locked)
	if err != nil || got != 1800000000 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}
```

- [x] **Step 2: Run the StableCoin tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'StableCoin|AdminSignatures' -count=1`

Expected: FAIL because `validateAdminSignatures` and the full stage are absent.

- [x] **Step 3: Extend the existing external MuSig2 flow**

Keep the JavaScript signer process isolated and return only aggregate public key or 64-byte signatures over supplied sighashes. The Go stage must reject any count mismatch or non-64-byte signature before calling `AdminPrepared.Finalize`.

- [x] **Step 4: Build the complete StableCoin chain and wire strict broadcast/indexer validation**

Broadcast:

```text
stablecoin-coin-nft-create
stablecoin-initial-mint
stablecoin-admin-mint
stablecoin-owner-transfer
stablecoin-batch-transfer
stablecoin-merge
stablecoin-freeze
stablecoin-unfreeze
```

Use at least two Coin UTXOs in `MergeCoin`. Check the 2,012-byte Coin code, supply increase in the Coin NFT Tape, Coin amount conservation, two logical batch recipients, merge consolidation, and freeze/unfreeze lock-time changes. Query the stablecoin info/UTXO/decode endpoints after the raw transaction checks.

- [x] **Step 5: Run tests and commit**

Run: `go test ./lib/contract ./test/testnet-parity -run 'StableCoin|Coin|Admin' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/stablecoin_stage.go \
  test/testnet-parity/stablecoin_stage_test.go \
  test/testnet-parity/js-musig-sign.js
git commit -m "test: cover complete stablecoin lifecycle"
```

### Task 7: Full PoolNFT2 Standard and Lock Variants

**Files:**
- Create: `test/testnet-parity/pool_stage.go`
- Create: `test/testnet-parity/pool_stage_test.go`
- Modify: `test/testnet-parity/main.go`

- [x] **Step 1: Write failing state-transition validation tests**

```go
func TestValidatePoolTransitionRequiresPreviousStateInput(t *testing.T) {
	previous := strings.Repeat("1", 64)
	tx := bt.NewTx()
	if err := validatePoolStateInput(tx, previous, 0); err == nil {
		t.Fatal("expected missing pool-state input to fail")
	}
}

func TestPoolAmountDeltaUsesIntegers(t *testing.T) {
	before := poolAmounts{TBC: 1_000_000, FT: big.NewInt(1_000_000)}
	after := poolAmounts{TBC: 1_100_000, FT: big.NewInt(900_000)}
	if err := validatePoolSwapDelta(before, after, 100_000, big.NewInt(100_000)); err != nil {
		t.Fatal(err)
	}
}
```

- [x] **Step 2: Run the Pool tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'Pool' -count=1`

Expected: FAIL because the state validators do not exist.

- [x] **Step 3: Implement standard pool stages**

Use a fresh released FT v3 and broadcast:

```text
pool-source
pool-mint
pool-init
pool-increase-lp
pool-swap-tbc-to-ft
pool-swap-ft-to-tbc
pool-consume-lp
pool-merge-ftlp
pool-burn-ftlp
pool-merge-held-ft
```

Create enough FTLP branches before merge/burn. Before each builder call, refetch the current Pool NFT state, FT/FTLP UTXOs, and ancestry; after broadcast, require the previous state outpoint is spent exactly once and a new Code/Hold/Tape Pool NFT state is produced.

- [x] **Step 4: Implement lock-variant stages**

Broadcast:

```text
pool-create-multisig-lock
pool-create-ftlp-locktime
pool-unlock-ftlp
pool-consume-unlocked-ftlp
```

Use an already-reached lock time to keep the suite deterministic. Require the locked pool script hash to match the configured public keys, require the FTLP Tape lock field before unlock, and require the standard unlocked FTLP code afterward.

- [x] **Step 5: Assert AMM and asset invariants**

All pool calculations use `big.Int` or checked `uint64`; no floating-point equality is permitted. Assert expected reserve direction, LP supply direction, foundation/service outputs, FT contract identity, and no unexplained asset loss.

- [x] **Step 6: Run tests and commit**

Run: `go test ./lib/contract ./lib/util ./test/testnet-parity -run 'Pool|FTLP|Swap' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/pool_stage.go test/testnet-parity/pool_stage_test.go \
  test/testnet-parity/main.go
git commit -m "test: cover complete poolnft2 testnet matrix"
```

### Task 8: Ordinary FT/TBC OrderBook Lifecycle

**Files:**
- Create: `test/testnet-parity/orderbook_stage.go`
- Create: `test/testnet-parity/orderbook_stage_test.go`
- Modify: `test/testnet-parity/main.go`

- [x] **Step 1: Write failing decoded-order and residual-volume tests**

```go
func TestValidateResidualSaleVolume(t *testing.T) {
	before := &contract.OrderData{SaleVolume: 100_000}
	after := &contract.OrderData{SaleVolume: 40_000}
	if err := validateResidualSaleVolume(before, after, 60_000); err != nil {
		t.Fatal(err)
	}
}

func TestOrderBookUsesOrdinaryFTBranch(t *testing.T) {
	if contract.IsCoinScript(testFTV3Code(t)) {
		t.Fatal("ordinary orderbook stage must not use stablecoin isCoin branch")
	}
}
```

- [x] **Step 2: Run the OrderBook tests and confirm failure**

Run: `go test ./test/testnet-parity -run 'OrderBook|ResidualSale' -count=1`

Expected: FAIL because `validateResidualSaleVolume` is absent.

- [x] **Step 3: Build and broadcast create/cancel cases**

Use fresh, independent FT branches and TBC UTXOs for:

```text
orderbook-sell-create
orderbook-sell-cancel
orderbook-buy-create
orderbook-buy-cancel
```

Exercise build/fill signing for one side and direct `WithSign` for the other, compare their local structural model, and broadcast only one transaction per outpoint.

- [x] **Step 4: Build and broadcast full and partial matches**

Broadcast:

```text
orderbook-full-sell-create
orderbook-full-buy-create
orderbook-full-match
orderbook-partial-sell-create
orderbook-partial-buy-create
orderbook-partial-match
```

Decode order scripts with `contract.GetOrderData`. Check fixed script lengths, token ID, owner, price, fee rate, locked FT/TBC amount, fee outputs, full-order closure, and partial-match residual sale volume. Query `/dex/txinfo/txid/{txid}` and require PLACE/CANCEL/TRADE classification when indexed.

- [x] **Step 5: Run tests and commit**

Run: `go test ./lib/contract ./lib/util ./test/testnet-parity -run 'Order|DEX' -count=1`

Expected: PASS.

```bash
git add test/testnet-parity/orderbook_stage.go \
  test/testnet-parity/orderbook_stage_test.go \
  test/testnet-parity/main.go
git commit -m "test: cover ordinary orderbook testnet lifecycle"
```

### Task 9: Expand JavaScript 1.6.5 Deterministic Parity

**Files:**
- Modify: `scripts/generate-js-parity-fixtures.js`
- Modify: `lib/contract/testdata/js-1.6.5/script-hashes.json`
- Modify: `lib/contract/script_parity_test.go`

- [ ] **Step 1: Write failing Go parity assertions for missing fixture keys**

```go
func TestRenderedTransactionArtifactsMatchJS165(t *testing.T) {
	fixtures := loadJSFixture(t)
	for _, name := range []string{
		"ftV3Mint", "ftTransferTape", "nftTape", "poolV3",
		"poolV3Locked", "ftlpV3", "ftlpV3LockTime",
		"sellOrder", "buyOrder", "stableCoinMint", "stableCoinTape",
	} {
		if _, ok := fixtures[name]; !ok {
			t.Fatalf("missing JavaScript 1.6.5 fixture %q", name)
		}
	}
}
```

- [ ] **Step 2: Run parity tests and confirm failure**

Run: `go test ./lib/contract -run 'TestRendered.*JS165' -count=1`

Expected: FAIL for fixture keys not yet generated.

- [ ] **Step 3: Generate public deterministic fixtures from the sibling checkout**

Extend the Node script to emit, for each artifact:

```js
fixtures[name] = {
  length: bytes.length,
  sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
  decoded: decodedPublicFields,
};
```

The script must first require `package.json` version exact `1.6.5`. Use fixed public addresses, txids, amounts, lock times, and metadata. Do not use any private key or signature fixture.

Run:

```bash
node scripts/generate-js-parity-fixtures.js \
  lib/contract/testdata/js-1.6.5/script-hashes.json
```

Expected: fixture JSON is updated successfully and contains no WIF, raw signed transaction, or secret.

- [ ] **Step 4: Implement corresponding Go artifact comparisons**

Render the same Code/Tape/order artifacts in Go and compare length, SHA-256, decoded amount/state fields, output ordering, fixed satoshi values, version, lock time, and sequence. Classify deliberate signed-raw differences in the final report rather than comparing signatures or txids.

- [ ] **Step 5: Run tests and commit**

Run: `go test ./lib/contract -run 'JS165|Parity' -count=1`

Expected: PASS.

```bash
git add scripts/generate-js-parity-fixtures.js \
  lib/contract/testdata/js-1.6.5/script-hashes.json \
  lib/contract/script_parity_test.go
git commit -m "test: expand javascript 1.6.5 protocol parity"
```

### Task 10: Static Verification Before Live Broadcast

**Files:**
- Modify only files implicated by a failing command.

- [ ] **Step 1: Scan tracked files for test secrets**

Run:

```bash
git grep -nE '(^|[^A-Za-z0-9])[KL][1-9A-HJ-NP-Za-km-z]{50,51}([^A-Za-z0-9]|$)' -- . \
  ':!docs/superpowers/specs/*' ':!docs/superpowers/plans/*'
```

Expected: no matches.

- [ ] **Step 2: Format and run the full unit suite**

Run:

```bash
gofmt -w test/testnet-parity/*.go test/testnet-fee-report/*.go \
  lib/contract/*.go lib/util/*.go
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run race, vet, build, and regenerated fixture diff**

Run:

```bash
go test -race ./... -count=1
go vet ./...
go build ./...
fixture_tmp="$(mktemp)"
node scripts/generate-js-parity-fixtures.js "$fixture_tmp"
diff -u lib/contract/testdata/js-1.6.5/script-hashes.json "$fixture_tmp"
rm "$fixture_tmp"
```

Expected: every command succeeds and the fixture diff is empty.

- [ ] **Step 4: Commit any verification-only corrections**

```bash
git add test lib scripts
git commit -m "test: harden full contract matrix verification"
```

If there are no corrections, do not create an empty commit.

### Task 11: Execute Fresh Testnet Broadcast Matrix

**Files:**
- No source edits during a successful run.
- Create after all broadcasts: `docs/verification/2026-07-31-full-contract-testnet-matrix.md`

- [ ] **Step 1: Preflight the runtime account without printing the WIF**

Set `TBC_TESTNET_WIF` only in the process environment, set `TBC_TESTNET_NETWORK=testnet`, and run the harness with broadcasting disabled. Confirm the derived address, public TBC balance, spendable UTXO count, and dry-run builders. Never use `set -x`.

- [ ] **Step 2: Run JavaScript parity and foundation stages**

Run the ordinary Go tests first, then execute the fresh FT foundation stage with `TBC_TESTNET_BROADCAST=1`. Capture only `RESULT` and `STATE` lines. Confirm each returned txid equals the locally computed txid and is refetched.

- [ ] **Step 3: Run independent contract-family stages sequentially**

Execute in this order:

```text
ft
nft
multisig
base-htlc
piggybank
stablecoin
pool-create
pool-trade
pool-lock
orderbook
```

Use the prior stage’s public `STATE` values only where required. Never automatically retry a broadcast POST. On failure, stop, preserve the last public accepted txid, classify the failure, and rebuild from a fresh spendable state after any code fix.

- [ ] **Step 4: Independently recalculate fees from the API**

Pass every accepted txid to:

```bash
go run ./test/testnet-fee-report <txid> <txid> ...
```

Expected: every line reports `status=ok`, with `paid_sat >= max(80, ceil(bytes*80/1000))`.

- [ ] **Step 5: Refetch public contract state**

Use raw transaction refetch for every txid and the specialized FT/NFT/Pool/StableCoin/DEX APIs for contract state. Bounded GET retries are allowed for index lag. Record `indexed=pending` only when raw refetch and raw state checks pass but a non-consensus index remains delayed; do not mark a rejected or unrefetchable transaction as passed.

### Task 12: Final Report, Review, and Branch Completion

**Files:**
- Create: `docs/verification/2026-07-31-full-contract-testnet-matrix.md`
- Modify: `docs/README.md`

- [ ] **Step 1: Write one evidence row for every required operation**

Use this exact public schema:

```markdown
| Family | Operation | Txid | Bytes | Paid fee | Target fee | API refetch | Asset/state invariant | JS 1.6.5 |
|---|---|---|---:|---:|---:|---|---|---|
```

Include explicit exclusions for Pool v1, unreleased token-token matching, retired FT extensions, read-only helpers, and mainnet.

- [ ] **Step 2: Document classified compatibility differences**

State that different UTXO selection, node dust adaptation, and signature encoding may change complete signed raw/txid while deterministic script hashes, Tape data, output order/value, version, lock time, and sequence remain equal. List only differences actually observed.

- [ ] **Step 3: Run final verification from a clean worktree**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./...
git status --short
```

Expected: tests, race, vet, and build pass; status shows only the intended report/index changes before commit.

- [ ] **Step 4: Commit the public report**

```bash
git add -f docs/verification/2026-07-31-full-contract-testnet-matrix.md
git add docs/README.md
git commit -m "docs: record full contract testnet matrix"
```

- [ ] **Step 5: Review the complete branch**

Review `git diff 44d571d...HEAD`, verify every design-spec row maps to a fresh evidence row, scan again for secrets, and inspect all public transaction identifiers for 64-character lowercase hex format.

- [ ] **Step 6: Rebase/merge only after verification and push with explicit user authorization**

Do not alter `main` or push until the completed branch has passed final review and the user has authorized the remote update.
