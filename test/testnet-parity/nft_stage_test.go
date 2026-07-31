package main

import (
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestValidateNFTOutputsRequiresCodeHoldTape(t *testing.T) {
	script, err := bscript.NewFromHexString("51")
	if err != nil {
		t.Fatal(err)
	}
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 200, LockingScript: script})
	if err := validateNFTOutputs(tx); err == nil {
		t.Fatal("expected incomplete NFT output triple to fail")
	}
}

func TestValidateNFTPaymentOutput(t *testing.T) {
	_, address, _ := ftStageFixture(t)
	script, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 10_000, LockingScript: script})
	if err := validatePaymentOutput(tx, address, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := validatePaymentOutput(tx, address, 10_001); err == nil {
		t.Fatal("expected wrong payment amount rejection")
	}
}

func TestBuildNFTLifecyclePlanCoversAllReleasedOperations(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildNFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"nft-collection-create",
		"nft-single-mint",
		"nft-batch-mint-1",
		"nft-batch-mint-2",
		"nft-temp-funding",
		"nft-transfer",
		"nft-transfer-with-tbc",
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
		if err := validateNFTLifecycleTransaction(want, tx, plan); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
	}
	if plan.Collection.TxID() == "" || plan.SingleMint.TxID() == "" {
		t.Fatal("missing public collection or NFT identifiers")
	}
}

func TestValidateNFTLifecycleRejectsWrongCollectionIndexCode(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildNFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	wrongCode, err := bscript.NewFromHexString("51")
	if err != nil {
		t.Fatal(err)
	}
	plan.SingleMint.Outputs[0].LockingScript = wrongCode
	if err := validateNFTLifecycleTransaction(
		"nft-single-mint",
		plan.SingleMint,
		plan,
	); err == nil {
		t.Fatal("expected wrong collection/index Code rejection")
	}
}
