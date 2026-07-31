package main

import (
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func TestNewEphemeralMultiSigSetIsTwoOfThree(t *testing.T) {
	privateKey, _, _ := ftStageFixture(t)
	set, err := newEphemeralMultiSigSet(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if set.Required != 2 || len(set.Signers) != 3 || len(set.PublicKeys) != 3 {
		t.Fatalf("unexpected signer set: %#v", set)
	}
	if !sort.StringsAreSorted(set.PublicKeys) {
		t.Fatal("public keys must use deterministic order")
	}
	ok, err := verifyEphemeralMultiSigSet(set)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("multisig address does not match public key set")
	}
}

func TestBuildMultiSigLifecyclePlanCoversTBCAndFT(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildMultiSigLifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"multisig-ft-source",
		"multisig-ft-mint",
		"multisig-wallet-create",
		"multisig-tbc-deposit",
		"multisig-tbc-spend",
		"multisig-tbc-forward",
		"multisig-ft-deposit",
		"multisig-ft-spend",
		"multisig-ft-forward",
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
		if err := validateMultiSigLifecycleTransaction(want, tx, plan); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
	}
	if plan.MultiSigAddress == "" ||
		plan.DestinationMultiSigAddress == "" ||
		plan.MultiSigAddress == plan.DestinationMultiSigAddress ||
		plan.Token.ContractTxid == "" {
		t.Fatal("missing public multisig address or FT contract id")
	}
}

func TestMultiSigStageMinimumFundingBuildsCompleteLifecycle(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = multiSigStageFundingMinimumSatoshis
	if _, err := buildMultiSigLifecyclePlan(privateKey, address, funding); err != nil {
		t.Fatalf(
			"minimum stage funding %d cannot build lifecycle: %v",
			multiSigStageFundingMinimumSatoshis,
			err,
		)
	}
}

func TestMultiSigLifecyclePaysFinalSignedSizeFee(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = multiSigStageFundingMinimumSatoshis
	plan, err := buildMultiSigLifecyclePlan(privateKey, address, funding)
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
		plan.Source,
		plan.Mint,
		plan.Wallet,
		plan.TBCDeposit,
		plan.TBCSpend,
		plan.TBCForward,
		plan.FTDeposit,
		plan.FTSpend,
		plan.FTForward,
	} {
		parents[tx.TxID()] = tx
	}
	for _, item := range plan.Transactions {
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateInputScriptsFromParents(
			tx,
			func(txid string) (*bt.Tx, error) {
				parent, ok := parents[txid]
				if !ok {
					return nil, fmt.Errorf("missing parent %s", txid)
				}
				return parent, nil
			},
		); err != nil {
			t.Fatalf("%s script: %v", item.Label, err)
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
