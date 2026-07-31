# Restore FT v3 and Remove TokenOrder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore JavaScript/Rust-compatible FT v3 minting and completely remove the unreleased FT v4 and Token-to-Token OrderBook surface from the Go SDK.

**Architecture:** Keep the current branch and all unrelated correctness fixes, but surgically restore the FT v3 covenant template and remove every v4 serializer branch. Delete TokenOrder sources and public fields, retain ordinary TBC-to-FT OrderBook, and validate the supported protocol with byte fixtures, full Go verification, and fresh testnet broadcasts.

**Tech Stack:** Go 1.x, `tbc-lib-go`, embedded ASM templates, Go tests, TBC testnet API.

---

## File Map

Protocol behavior and tests:

- Modify `lib/contract/ft_v3_test.go`: require FT v3 minting and the 1,856-byte partial boundary.
- Modify `lib/contract/script_parity_test.go`: require the JavaScript fixture for FT minting, then stop rendering TokenOrder when its sources are removed.
- Modify `lib/util/ftversion_test.go`: reject the retired 1,948-byte script.
- Modify `lib/contract/htlc_token_test.go`: expect FT v3 outputs.
- Delete `lib/contract/ft_version_test.go`: it exists only for FT v4 PoolNFT2 coverage.
- Modify `lib/contract/asm/ft_mint.asm`: restore the released FT v3 template.
- Modify `lib/contract/ft.go`: limit the contract-input marker to FT v3.
- Modify `lib/util/ftversion.go`: remove `FTVersion4` and 1,948-byte classification.
- Modify `lib/util/ftunlock.go`: remove the 1,948/1,920 serializer branch.
- Modify `lib/util/poolnftunlock.go`: remove FT v4 PoolNFT serialization.
- Modify `lib/contract/orderbook.go`: remove FT v4 hashing and TokenOrder fields.
- Modify `lib/contract/poolnft2.go`: remove FT v4 and FTLP v4 generation.

TokenOrder removal:

- Delete `lib/contract/orderbook_token.go`.
- Delete `lib/contract/orderbook_token_match.go`.
- Delete `lib/contract/orderbook_token_online.go`.
- Delete `lib/contract/orderbook_token_test.go`.
- Delete `lib/contract/asm/orderbook_token_buy.hex`.
- Delete `lib/contract/asm/orderbook_token_sell.hex`.
- Modify `lib/util/orderbookunlock.go`: retain the corrected ten-output ordinary OrderBook padding, remove twelve-output and TokenOrder handling.
- Modify `lib/contract/testdata/js-1.6.5/script-hashes.json`: remove TokenOrder fixtures.
- Modify `scripts/generate-js-parity-fixtures.js`: stop generating TokenOrder fixtures.
- Modify `scripts/sync-js-asm-templates.js`: stop syncing TokenOrder templates.

Testnet and documentation:

- Modify `test/testnet-parity/main.go`: remove TokenOrder config, stages, helpers, matching, and local interpreter imports.
- Modify `test/testnet-parity/main_test.go`: remove TokenOrder-only configuration tests.
- Modify `docs/合约库说明.md`: describe FT v1-v3 and ordinary OrderBook only.
- Modify `docs/testnet-parity-report.md`: remove TokenOrder from the active compatibility report.
- Modify `docs/verification/2026-07-30-fee-safety-testnet.md`: remove the retired FT v4/TokenOrder support section.
- Create `docs/verification/2026-07-31-ft-v3-restoration-testnet.md`: record final tests and fresh accepted FT v3 broadcasts.

### Task 1: Establish Failing FT v3 Compatibility Tests

**Files:**

- Modify: `lib/contract/ft_v3_test.go:151-191`
- Modify: `lib/contract/script_parity_test.go:33-140`
- Modify: `lib/util/ftversion_test.go:78-119`
- Modify: `lib/contract/htlc_token_test.go:135`
- Delete: `lib/contract/ft_version_test.go`

- [ ] **Step 1: Replace the FT v4 mint assertion with the required FT v3 behavior**

Use this test in `lib/contract/ft_v3_test.go`:

```go
func TestMintFTUsesReleasedV3Script(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	ft, err := NewFT(&FtParams{
		Name: "Released", Symbol: "FT3", Amount: 1_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	raws, err := ft.MintFT(fx.senderKey, fx.sender, fx.feeUTXO)
	if err != nil {
		t.Fatal(err)
	}
	mint := mustTx(t, raws[1])
	info, err := util.ClassifyFTScript(mint.Outputs[0].LockingScript)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != util.FTVersion3 {
		t.Fatalf("minted FT version = %d, want v3", info.Version)
	}
	if got := mint.Outputs[0].LockingScript.Len(); got != 1884 {
		t.Fatalf("minted FT code length = %d, want 1884", got)
	}
}

func TestComputeFtPartialHashUsesV3Boundary(t *testing.T) {
	code, err := getFTmintCode(
		strings.Repeat("11", 32),
		0,
		"1BitcoinEaterAddressDontSendf59kuE",
		80,
	)
	if err != nil {
		t.Fatal(err)
	}
	const v3PartialOffset = 1856
	want := partialsha256.CalculatePartialHash(code.Bytes()[:v3PartialOffset])
	got, err := ComputeFtPartialHash(code.ToHex(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("FT v3 partial hash = %s, want %s", got, want)
	}
}
```

- [ ] **Step 2: Restore strict JavaScript FT script parity expectations**

Remove only `ftV3Mint` from the skip in `TestRenderedScriptsMatchJS165`, leave
the two TokenOrder names skipped until their sources are deleted in Task 2,
and delete `TestFTMintUsesMultiContractV4TemplateHash`. The fixture already
requires:

```json
"ftV3Mint": {
  "length": 1884,
  "sha256": "93a0613a9eb50f7c42059ebb517b8ad023fde2e3587cd86fe34ce3f5cb214f62"
}
```

- [ ] **Step 3: Require the classifier to reject retired FT v4**

Replace the FT v4 recognition/boundary test in `lib/util/ftversion_test.go` with:

```go
func TestClassifyFTScriptRejectsRetiredV4(t *testing.T) {
	v4 := syntheticFTScript(t, 1948, bscript.Op15)
	if _, err := ClassifyFTScript(v4); err == nil {
		t.Fatal("expected retired 1948-byte FT v4 script to be rejected")
	}
}
```

Delete `lib/contract/ft_version_test.go`, and change the HTLC output length assertion from `1948` to `1884`.

- [ ] **Step 4: Run the focused tests and verify RED**

Run:

```bash
go test ./lib/contract ./lib/util -run 'TestMintFTUsesReleasedV3Script|TestComputeFtPartialHashUsesV3Boundary|TestRenderedScriptsMatchJS165|TestClassifyFTScriptRejectsRetiredV4|TestTokenHTLC' -count=1
```

Expected: FAIL because minting still produces 1,948-byte FT v4 and the classifier still accepts it.

### Task 2: Restore FT v3 and Remove TokenOrder

**Files:**

- Modify: `lib/contract/asm/ft_mint.asm`
- Modify: `lib/contract/ft.go:779-889,1168-1210`
- Modify: `lib/util/ftversion.go:9-61`
- Modify: `lib/util/ftunlock.go:19-59`
- Modify: `lib/util/poolnftunlock.go:52-137,538-640`
- Modify: `lib/contract/orderbook.go:27-75,279-301`
- Modify: `lib/contract/poolnft2.go:699-725,1032-1205,1577-1620,1750-1795`
- Delete: `lib/contract/orderbook_token.go`
- Delete: `lib/contract/orderbook_token_match.go`
- Delete: `lib/contract/orderbook_token_online.go`
- Delete: `lib/contract/orderbook_token_test.go`
- Delete: `lib/contract/asm/orderbook_token_buy.hex`
- Delete: `lib/contract/asm/orderbook_token_sell.hex`
- Modify: `lib/util/orderbookunlock.go:17-286`
- Modify: `lib/contract/testdata/js-1.6.5/script-hashes.json`
- Modify: `scripts/generate-js-parity-fixtures.js`
- Modify: `scripts/sync-js-asm-templates.js`

- [ ] **Step 1: Restore the FT v3 ASM template**

The FT v4 change added exactly 57 `OP_NOP` instructions after
`OP_NIP OP_15 OP_DROP`. Remove that run so the ending is:

```text
OP_SHA256 OP_7 OP_PUSH_META OP_EQUAL OP_NIP OP_15 OP_DROP OP_RETURN 0x15 0x${hash} 0x05 0x32436f6465
```

Do not change the existing `${utxoHex}`, `${hash}`, or `${tapeSizeHex}`
substitutions.

- [ ] **Step 2: Restore FT v3-only version semantics**

In `lib/util/ftversion.go`, leave only:

```go
const (
	FTVersion1 FTVersion = 1
	FTVersion2 FTVersion = 2
	FTVersion3 FTVersion = 3
)
```

`ClassifyFTScript` must accept lengths `1564`, `1884`, and `2012`; every other
length, including `1948`, must return `unsupported FT code length`.

In `lib/contract/ft.go`, both swap unlock builders must append the contract
input selector only when:

```go
if ftVersion == util.FTVersion3 {
	marker, err = ftV3InputIndexMarker(currentUnlockIndex, isContractTXs)
}
```

Rename the private helper back to `ftV3InputIndexMarker` and restore the
version-specific error message.

- [ ] **Step 3: Remove FT v4 serializer and composed-contract branches**

Remove `ftV4Length`, `ftV4PartialOffset`, `poolFtV4Length`,
`poolFtV4PartialOffset`, and every branch using them. Ordinary v3 must continue
to use length `1884` and offset `1856`; StableCoin remains `2012/1984`.

In `lib/contract/orderbook.go`, `ComputeFtPartialHash` must reduce to:

```go
partialOffset := obFTv2Partial
if isCoin || info.IsCoin {
	partialOffset = obCoinPartial
} else if info.Version == util.FTVersion1 {
	partialOffset = 1536
}
```

In `lib/contract/poolnft2.go`, delete `toMultiContractFtlpCodePre` and every
`FTVersion4` case. Retain the existing v1, v2, v3, and StableCoin branches.

- [ ] **Step 4: Remove the TokenOrder API, serializers, and fixtures**

First confirm the unsupported API exists:

```bash
rg -n 'TokenOrder|TokenSaleVolume|FtBPartialHash|GetTokenOrderPreTxdataOB|GetCurrentTxOutputsDataOBFixed' lib
```

Delete the six TokenOrder Go/ASM files listed for this task. Remove these
fields from `OrderBook`:

```go
TokenSaleVolume *big.Int
TokenFeeRate    *big.Int
TokenUnitPrice  *big.Int
FtBPartialHash  string
FtBID           string
```

Keep the `math/big` import because ordinary OrderBook amount construction uses
`big.Int`.

Remove TokenOrder and FT v4 constants plus `GetTokenOrderPreTxdataOB` from
`lib/util/orderbookunlock.go`. Preserve the corrected general padding formula,
but make the fixed-count implementation private:

```diff
 func GetCurrentTxOutputsDataOB(tx *bt.Tx) (string, error) {
-	return GetCurrentTxOutputsDataOBFixed(tx, 10)
+	return getCurrentTxOutputsDataOBFixed(tx, 10)
 }
 
-func GetCurrentTxOutputsDataOBFixed(tx *bt.Tx, fixedOutputCount int) (string, error) {
+func getCurrentTxOutputsDataOBFixed(tx *bt.Tx, fixedOutputCount int) (string, error) {
 	if fixedOutputCount < 0 {
-		return "", fmt.Errorf("GetCurrentTxOutputsDataOBFixed: fixed output count must be non-negative")
+		return "", fmt.Errorf("getCurrentTxOutputsDataOBFixed: fixed output count must be non-negative")
 	}
```

FT/coin pair detection must contain only `obFTCodeLength` and
`obCoinCodeLength`. Remove `tokenSellOrder` and `tokenBuyOrder` from the JSON
fixture and JavaScript fixture generator. Remove the two TokenOrder template
sync calls from `scripts/sync-js-asm-templates.js`.

Finally, remove TokenOrder fields/builders from
`renderAllReferenceScripts` and remove the two retired fixture names from the
skip in `TestRenderedScriptsMatchJS165`.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
gofmt -w lib/contract/ft.go lib/contract/ft_v3_test.go lib/contract/htlc_token_test.go lib/contract/orderbook.go lib/contract/poolnft2.go lib/contract/script_parity_test.go lib/util/ftunlock.go lib/util/ftversion.go lib/util/ftversion_test.go lib/util/orderbookunlock.go lib/util/poolnftunlock.go
if rg -n 'TokenOrder|TokenSaleVolume|FtBPartialHash|GetTokenOrderPreTxdataOB|GetCurrentTxOutputsDataOBFixed' lib; then
  exit 1
fi
go test ./lib/contract ./lib/util -count=1
```

Expected: no retired API matches and all tests PASS, including strict JS 1.6.5
FT mint hash parity and ordinary OrderBook tests.

- [ ] **Step 6: Commit the FT v3 restoration and TokenOrder removal**

```bash
git add lib/contract/asm/ft_mint.asm lib/contract/ft.go lib/contract/ft_v3_test.go lib/contract/htlc_token_test.go lib/contract/orderbook.go lib/contract/poolnft2.go lib/contract/script_parity_test.go lib/contract/testdata/js-1.6.5/script-hashes.json lib/util/ftunlock.go lib/util/ftversion.go lib/util/ftversion_test.go lib/util/orderbookunlock.go lib/util/poolnftunlock.go scripts/generate-js-parity-fixtures.js scripts/sync-js-asm-templates.js
git add -u lib/contract
git commit -m "fix: restore FT v3 and remove token order protocol"
```

### Task 3: Remove TokenOrder Testnet Paths and Update Documentation

**Files:**

- Modify: `test/testnet-parity/main.go:1-1383`
- Modify: `test/testnet-parity/main_test.go:1-87`
- Modify: `docs/合约库说明.md:28-90`
- Modify: `docs/testnet-parity-report.md:15-50`
- Modify: `docs/verification/2026-07-30-fee-safety-testnet.md:51-183`

- [ ] **Step 1: Remove TokenOrder-only testnet configuration and stages**

From `config`, remove `SellerWIF`, `MatcherWIF`, `TaxAddress`,
`OrderCreateTXID`, `OrderSellTXID`, and `OrderBuyTXID`. Remove their environment
loads, `validateTokenOrderMatchV2Config`, and the `orders` and
`token-order-match-v2` branches from `run`.

Delete `harnessOrderBookClient`, `prePreKey`, `liveFTInput`,
`fetchLiveFTInput`, `orderUTXOFromTX`, `testnetAddressForWIF`,
`testnetFeeTarget`, `reportTransactionFee`, `fundTokenOrderRoles`,
`buildTokenOrderMatcherFunding`, `initializedLiveFT`,
`transferExactTokenOrderInput`, `runTokenOrderMatchV2`, `runTokenOrders`,
`broadcastTokenOrderMatchWithFreshMatcher`, `broadcastTokenOrderMatch`,
`buildValidatedTokenOrderMatch`, and
`buildValidatedTokenOrderMatchWithInputs`. Retain `utxoFromTX`,
`ftUTXOFromTX`, and `localPrePre` because the live HTLC flow uses them.

Remove the no-longer-used local interpreter/debug imports. Keep `crypto/rand`
and `crypto` because the HTLC flow creates a preimage and hashlock.

Before broadcasting a mint, require:

```go
if len(mintTX.Outputs) < 2 || mintTX.Outputs[0].LockingScript.Len() != 1884 {
	return nil, nil, fmt.Errorf("%s mint is not released FT v3", label)
}
fmt.Printf("%s mint_code_bytes=%d\n", label, mintTX.Outputs[0].LockingScript.Len())
```

After every successful broadcast in `broadcastOne` and `mintAndBroadcast`,
call `api.FetchTXRaw(txid, network)` and verify the fetched transaction ID
equals the locally parsed transaction ID. This makes accepted/refetched status
part of the repeatable live runner.

- [ ] **Step 2: Remove TokenOrder-only config tests**

Delete:

```go
func TestLoadConfigReadsTokenOrderMatchV2Roles(t *testing.T)
func TestValidateTokenOrderMatchV2ConfigRequiresSeparateRuntimeRoles(t *testing.T)
```

Keep the three testnet/WIF safety tests.

- [ ] **Step 3: Update active documentation to the supported scope**

In `docs/合约库说明.md`, state:

```markdown
- **FT v1-v3**：v1-v3 与 StableCoin 由 `util.ClassifyFTScript` 统一识别；新铸造使用与 JS/Rust 一致的 FT v3（1884 字节、partial boundary 1856）。
```

The OrderBook section must describe ordinary TBC-to-FT orders only. Remove all
FT v4 and TokenOrder bullets, including PoolNFT2 FTLP v4 text.

Remove TokenOrder rows/findings from `docs/testnet-parity-report.md`. Remove the
FT v4/TokenOrder section and TokenOrder rows from the 2026-07-30 verification
report while retaining its fee, MultiSig, StableCoin, HTLC, PoolNFT2, and Rust
fee-strategy evidence.

- [ ] **Step 4: Run formatting, documentation search, and package tests**

Run:

```bash
gofmt -w test/testnet-parity/main.go test/testnet-parity/main_test.go
if rg -n -i 'token.?order|token.?to.?token|FT v4|1948|1920' README.md docs lib test scripts --glob '!docs/superpowers/**'; then
  exit 1
fi
go test ./... -count=1
```

Expected: no retired-protocol references and all packages PASS.

- [ ] **Step 5: Commit harness and documentation cleanup**

```bash
git add test/testnet-parity/main.go test/testnet-parity/main_test.go docs/合约库说明.md docs/testnet-parity-report.md docs/verification/2026-07-30-fee-safety-testnet.md
git commit -m "docs: align Go SDK with released contract scope"
```

### Task 4: Full Static and Concurrency Verification

**Files:**

- Verify all Go packages and repository state.

- [ ] **Step 1: Run the complete test suite**

```bash
go test ./... -count=1
```

Expected: every package exits successfully.

- [ ] **Step 2: Run the race detector**

```bash
go test -race ./... -count=1
```

Expected: every package exits successfully with no race report.

- [ ] **Step 3: Run static analysis**

```bash
go vet ./...
git diff --check
```

Expected: no output and zero exit status.

### Task 5: Fresh Testnet FT v3 Broadcast Verification

**Files:**

- Create: `docs/verification/2026-07-31-ft-v3-restoration-testnet.md`

- [ ] **Step 1: Confirm the key resolves to testnet and run dry mode**

Export the already-approved testnet-only WIF at runtime without writing it to
the repository, then run:

```bash
TBC_TESTNET_NETWORK=testnet \
TBC_TESTNET_BROADCAST=0 \
TBC_TESTNET_WIF="$TBC_TESTNET_WIF" \
go run ./test/testnet-parity
```

Expected: `dry-run pass; broadcast disabled`, and the mint builder completes
without exposing the WIF.

- [ ] **Step 2: Broadcast fresh FT v3 source and mint transactions**

```bash
TBC_TESTNET_NETWORK=testnet \
TBC_TESTNET_BROADCAST=1 \
TBC_TESTNET_WIF="$TBC_TESTNET_WIF" \
go run ./test/testnet-parity
```

Expected: two fresh token source/mint pairs are accepted. The printed FT Code
script length for each mint is 1,884 bytes and the source/mint fees meet the
signed-size target.

- [ ] **Step 3: Broadcast a normal FT v3 transfer and HTLC chain**

Use the first freshly printed contract ID:

```bash
TBC_TESTNET_NETWORK=testnet \
TBC_TESTNET_BROADCAST=1 \
TBC_TESTNET_STAGE=htlc \
TBC_TESTNET_TOKEN_A="$FRESH_FT_V3_CONTRACT_ID" \
TBC_TESTNET_WIF="$TBC_TESTNET_WIF" \
go run ./test/testnet-parity
```

Expected: the FT v3 self-transfer and both HTLC withdraw/refund branches are
accepted in chain order.

- [ ] **Step 4: Fetch and record accepted transactions**

Pass every printed txid to the existing fee reporter:

```bash
go run ./test/testnet-fee-report "$SOURCE_A_TXID" "$MINT_A_TXID" \
  "$SOURCE_B_TXID" "$MINT_B_TXID" "$FT_TRANSFER_TXID" \
  "$HTLC_WITHDRAW_DEPLOY_TXID" "$HTLC_WITHDRAW_TXID" \
  "$HTLC_REFUND_DEPLOY_TXID" "$HTLC_REFUND_TXID"
```

The reporter refetches each transaction and all parent inputs, then prints
byte size, paid fee, required fee, effective rate, and status. Write those
concrete results plus the runner's 1,884-byte mint check and
`accepted/refetched` status into
`docs/verification/2026-07-31-ft-v3-restoration-testnet.md`.

- [ ] **Step 5: Commit live evidence**

```bash
git add docs/verification/2026-07-31-ft-v3-restoration-testnet.md
git commit -m "test: verify restored FT v3 on testnet"
```

### Task 6: Final Scope Audit

**Files:**

- Review all changes since design commit `6a791e0`.

- [ ] **Step 1: Confirm exact protocol scope**

```bash
if rg -n -i 'TokenOrder|token.?to.?token|FTVersion4|ftV4|1948|1920' README.md docs lib test scripts --glob '!docs/superpowers/**'; then
  exit 1
fi
rg -n 'FTVersion1|FTVersion2|FTVersion3|1884|1856' lib docs
```

Expected: the retired-protocol search returns no matches; v1-v3 and
1,884/1,856 references remain.

- [ ] **Step 2: Re-run final verification after documentation**

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
git diff --check
git status --short --branch
```

Expected: tests/race/vet pass, diff check is clean, and the branch has no
uncommitted changes.

- [ ] **Step 3: Inspect the final commit range**

```bash
git log --oneline 6a791e0..HEAD
git diff --stat 6a791e0..HEAD
```

Expected: commits cover FT v3 restoration, TokenOrder removal, scope docs, and
fresh testnet verification only.
