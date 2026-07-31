package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func ftStageFixture(t *testing.T) (*bec.PrivateKey, string, *bt.UTXO) {
	t.Helper()
	privateKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		t.Fatal(err)
	}
	address, err := bscript.NewAddressFromPublicKey(privateKey.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	lockingScript, err := bscript.NewP2PKHFromAddress(address.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	txid, err := hex.DecodeString(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, address.AddressString, &bt.UTXO{
		TxID:          txid,
		Vout:          0,
		LockingScript: lockingScript,
		Satoshis:      2_000_000,
	}
}

func TestValidateFTV3Outputs(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Matrix", Symbol: "MFT", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	raws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(raws[1])
	if err != nil {
		t.Fatal(err)
	}
	balance, err := validateFTV3Outputs(mint, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := big.NewInt(100_000_000)
	if balance.Cmp(want) != 0 {
		t.Fatalf("balance=%s want=%s", balance, want)
	}
}

func TestValidateFTV3OutputsRejectsMissingTape(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Matrix", Symbol: "MFT", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	raws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(raws[1])
	if err != nil {
		t.Fatal(err)
	}
	mint.Outputs = mint.Outputs[:1]
	if _, err := validateFTV3Outputs(mint, 0); err == nil {
		t.Fatal("expected missing FT Tape rejection")
	}
}

func TestBuildFTLifecyclePlanCoversTransferBatchAndMerge(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"ft-source",
		"ft-mint",
		"ft-transfer",
		"ft-transfer-additional-info",
		"ft-batch-transfer",
		"ft-merge",
	}
	if len(plan.Transactions) != len(wantLabels) {
		t.Fatalf("transactions=%d want=%d", len(plan.Transactions), len(wantLabels))
	}
	for i, want := range wantLabels {
		if plan.Transactions[i].Label != want {
			t.Fatalf("transaction %d label=%q want=%q", i, plan.Transactions[i].Label, want)
		}
		if _, err := bt.NewTxFromString(plan.Transactions[i].Raw); err != nil {
			t.Fatalf("%s parse: %v", want, err)
		}
	}
	if len(plan.Additional.Outputs) < 2 {
		t.Fatal("additional-info transaction has too few outputs")
	}
	infoOutput := plan.Additional.Outputs[len(plan.Additional.Outputs)-2]
	if !infoOutput.LockingScript.IsSafeDataOut() || infoOutput.Satoshis != 0 {
		t.Fatal("additional-info output is not immediately before TBC change")
	}
	if len(plan.Batch.Outputs) != 7 {
		t.Fatalf("batch outputs=%d want=7", len(plan.Batch.Outputs))
	}
	mergedBalance, err := validateFTV3Outputs(plan.Merge, 0)
	if err != nil {
		t.Fatal(err)
	}
	if mergedBalance.Cmp(big.NewInt(88_000_000)) != 0 {
		t.Fatalf("merged balance=%s want=88000000", mergedBalance)
	}
	for _, item := range plan.Transactions {
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateFTLifecycleTransaction(item.Label, tx); err != nil {
			t.Fatalf("%s: %v", item.Label, err)
		}
	}
}

func TestTokenHTLCLifecyclePaysFinalSignedSizeFee(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	ftPlan, err := buildFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	htlcPlan, err := buildTokenHTLCPlan(ftPlan, privateKey, address, 900_000)
	if err != nil {
		t.Fatal(err)
	}
	parents := map[string]*bt.Tx{}
	for _, tx := range []*bt.Tx{
		ftPlan.Source,
		ftPlan.Mint,
		ftPlan.Transfer,
		ftPlan.Additional,
		ftPlan.Batch,
		ftPlan.Merge,
		htlcPlan.WithdrawDeploy,
		htlcPlan.Withdraw,
		htlcPlan.RefundDeploy,
		htlcPlan.Refund,
	} {
		parents[tx.TxID()] = tx
	}
	for _, item := range htlcPlan.Transactions {
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			t.Fatal(err)
		}
		paid, err := feeFromParents(tx, func(txid string) (*bt.Tx, error) {
			parent, ok := parents[txid]
			if !ok {
				return nil, fmt.Errorf("missing parent %s", txid)
			}
			return parent, nil
		})
		if err != nil {
			t.Fatalf("%s fee: %v", item.Label, err)
		}
		target := targetFee80(len(tx.Bytes()))
		if paid < target {
			t.Fatalf(
				"%s fee=%d target=%d bytes=%d",
				item.Label,
				paid,
				target,
				len(tx.Bytes()),
			)
		}
	}
}

func TestExecuteFTLifecyclePlanRunsStrictlyInOrder(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	state, err := executeFTLifecyclePlan(
		plan,
		func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
			tx, err := bt.NewTxFromString(item.Raw)
			if err != nil {
				return nil, err
			}
			if err := validate(tx); err != nil {
				return nil, err
			}
			labels = append(labels, item.Label)
			return tx, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(labels, ",") != "ft-source,ft-mint,ft-transfer,ft-transfer-additional-info,ft-batch-transfer,ft-merge" {
		t.Fatalf("unexpected execution order %v", labels)
	}
	if state.TokenID != plan.Mint.TxID() || state.LastTxID != plan.Merge.TxID() {
		t.Fatalf("unexpected public state %#v", state)
	}
}

func TestExecuteFTLifecyclePlanStopsAtFirstFailure(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = executeFTLifecyclePlan(
		plan,
		func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
			calls++
			if item.Label == "ft-transfer" {
				return nil, fmt.Errorf("rejected")
			}
			tx, parseErr := bt.NewTxFromString(item.Raw)
			if parseErr != nil {
				return nil, parseErr
			}
			return tx, validate(tx)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "ft-transfer") {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("accept calls=%d want=3", calls)
	}
}

func TestBuildTokenHTLCPlanCoversWithdrawAndRefund(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	ftPlan, err := buildFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	const timelock = uint32(12345)
	plan, err := buildTokenHTLCPlan(ftPlan, privateKey, address, timelock)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"token-htlc-withdraw-deploy",
		"token-htlc-withdraw",
		"token-htlc-refund-deploy",
		"token-htlc-refund",
	}
	if len(plan.Transactions) != len(wantLabels) {
		t.Fatalf("transactions=%d want=%d", len(plan.Transactions), len(wantLabels))
	}
	for i, want := range wantLabels {
		if plan.Transactions[i].Label != want {
			t.Fatalf("transaction %d label=%q want=%q", i, plan.Transactions[i].Label, want)
		}
		tx, err := bt.NewTxFromString(plan.Transactions[i].Raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateTokenHTLCTransaction(want, tx, timelock); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
	}
	if len(plan.Hashlock) != 64 {
		t.Fatalf("hashlock length=%d want=64", len(plan.Hashlock))
	}
	if plan.Refund.LockTime != timelock || plan.Refund.Inputs[0].SequenceNumber != 0xfffffffe {
		t.Fatalf("refund locktime/sequence mismatch")
	}
}
