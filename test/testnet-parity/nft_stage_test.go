package main

import (
	"encoding/hex"
	"fmt"
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

func TestNFTStageMinimumFundingBuildsCompleteLifecycle(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = nftStageFundingMinimumSatoshis
	if _, err := buildNFTLifecyclePlan(privateKey, address, funding); err != nil {
		t.Fatalf(
			"minimum stage funding %d cannot build lifecycle: %v",
			nftStageFundingMinimumSatoshis,
			err,
		)
	}
}

func TestNFTLifecyclePaysFinalSignedSizeFee(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = nftStageFundingMinimumSatoshis
	plan, err := buildNFTLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	externalParent := bt.NewTx()
	externalParent.Outputs = make([]*bt.Output, int(funding.Vout)+1)
	externalParent.Outputs[funding.Vout] = &bt.Output{
		LockingScript: funding.LockingScript,
		Satoshis:      funding.Satoshis,
	}
	parents := map[string]*bt.Tx{
		hex.EncodeToString(funding.TxID): externalParent,
	}
	for _, tx := range []*bt.Tx{
		plan.Collection,
		plan.SingleMint,
		plan.BatchMintOne,
		plan.BatchMintTwo,
		plan.TempFunding,
		plan.Transfer,
		plan.TransferWithTBC,
	} {
		parents[tx.TxID()] = tx
	}
	for _, item := range plan.Transactions {
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
