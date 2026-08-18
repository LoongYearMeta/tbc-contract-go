package main

import (
	"context"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/validator"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

type stageValidatorFetcher map[string]*bt.Tx

func (fetcher stageValidatorFetcher) FetchTokenTransaction(_ context.Context, txid, _ string) (*bt.Tx, error) {
	return fetcher[txid], nil
}

func TestBuildTBC20LifecyclePlanCoversMintTransferAndMerge(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildTBC20LifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tbc20-source", "tbc20-mint", "tbc20-transfer", "tbc20-merge"}
	if len(plan.Transactions) != len(want) {
		t.Fatalf("transactions=%d want=%d", len(plan.Transactions), len(want))
	}
	for index, label := range want {
		if plan.Transactions[index].Label != label {
			t.Fatalf("transaction %d label=%q want=%q", index, plan.Transactions[index].Label, label)
		}
		if _, err := bt.NewTxFromString(plan.Transactions[index].Raw); err != nil {
			t.Fatal(err)
		}
	}
	fetcher := stageValidatorFetcher{
		plan.Source.TxID():   plan.Source,
		plan.Mint.TxID():     plan.Mint,
		plan.Transfer.TxID(): plan.Transfer,
	}
	for _, tx := range []*bt.Tx{plan.Transfer, plan.Merge} {
		report, err := validator.ValidateOnChainTransaction(context.Background(), validator.TokenValidationOptions{Transaction: tx, Network: "testnet", Fetcher: fetcher})
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != validator.ValidationValid {
			t.Fatalf("validator status=%s issues=%+v", report.Status, report.Issues)
		}
	}
}

func TestExecuteTBC20LifecyclePlanRunsStrictlyInOrder(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	plan, err := buildTBC20LifecyclePlan(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	state, err := executeTBC20LifecyclePlan(plan, func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			return nil, err
		}
		if err := validate(tx); err != nil {
			return nil, err
		}
		labels = append(labels, item.Label)
		return tx, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 4 || state.TokenID != plan.Mint.TxID() || state.LastTxID != plan.Merge.TxID() {
		t.Fatalf("labels=%v state=%+v", labels, state)
	}
}
