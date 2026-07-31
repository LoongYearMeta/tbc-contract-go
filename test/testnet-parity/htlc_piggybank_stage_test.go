package main

import (
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func TestBuildBaseHTLCPlanCoversWithdrawAndRefund(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	const height = uint32(12345)
	plan, err := buildBaseHTLCPlan(privateKey, address, funding, height)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"plain-p2pkh-self-transfer",
		"base-htlc-withdraw-deploy",
		"base-htlc-withdraw",
		"base-htlc-refund-deploy",
		"base-htlc-refund",
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
		if err := validateBaseHTLCTransaction(want, tx, plan); err != nil {
			t.Fatalf("%s: %v", want, err)
		}
	}
	if len(plan.Hashlock) != 64 {
		t.Fatalf("hashlock length=%d want=64", len(plan.Hashlock))
	}
}

func TestBuildPiggyBankPlanCoversFreezeAndUnfreeze(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	const height = uint32(12345)
	plan, err := buildPiggyBankPlan(privateKey, address, funding, height)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Transactions) != 2 {
		t.Fatalf("transactions=%d want=2", len(plan.Transactions))
	}
	for _, item := range plan.Transactions {
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validatePiggyBankTransaction(item.Label, tx, plan); err != nil {
			t.Fatalf("%s: %v", item.Label, err)
		}
	}
	if plan.LockTime != height-1 {
		t.Fatalf("locktime=%d want=%d", plan.LockTime, height-1)
	}
}
