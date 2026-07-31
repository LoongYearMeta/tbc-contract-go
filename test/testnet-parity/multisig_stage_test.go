package main

import (
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
		"multisig-tbc-spend",
		"multisig-ft-deposit",
		"multisig-ft-spend",
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
	if plan.MultiSigAddress == "" || plan.Token.ContractTxid == "" {
		t.Fatal("missing public multisig address or FT contract id")
	}
}
