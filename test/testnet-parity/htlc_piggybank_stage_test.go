package main

import (
	"encoding/hex"
	"fmt"
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

func TestBaseHTLCAndPiggyBankMinimumFundingBuildsPlans(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = baseHTLCStageFundingMinimumSatoshis
	if _, err := buildBaseHTLCPlan(
		privateKey,
		address,
		funding,
		12345,
	); err != nil {
		t.Fatalf("base HTLC minimum funding: %v", err)
	}
	funding.Satoshis = piggyBankStageFundingMinimumSatoshis
	if _, err := buildPiggyBankPlan(
		privateKey,
		address,
		funding,
		12345,
	); err != nil {
		t.Fatalf("PiggyBank minimum funding: %v", err)
	}
}

func TestBaseHTLCAndPiggyBankPayFinalSignedSizeFee(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	tests := []struct {
		name         string
		fundingValue uint64
		build        func(*bt.UTXO) ([]plannedTransaction, []*bt.Tx, error)
	}{
		{
			name:         "base-htlc",
			fundingValue: baseHTLCStageFundingMinimumSatoshis,
			build: func(input *bt.UTXO) ([]plannedTransaction, []*bt.Tx, error) {
				plan, err := buildBaseHTLCPlan(
					privateKey,
					address,
					input,
					12345,
				)
				if err != nil {
					return nil, nil, err
				}
				return plan.Transactions, []*bt.Tx{
					plan.Plain,
					plan.WithdrawDeploy,
					plan.Withdraw,
					plan.RefundDeploy,
					plan.Refund,
				}, nil
			},
		},
		{
			name:         "piggybank",
			fundingValue: piggyBankStageFundingMinimumSatoshis,
			build: func(input *bt.UTXO) ([]plannedTransaction, []*bt.Tx, error) {
				plan, err := buildPiggyBankPlan(
					privateKey,
					address,
					input,
					12345,
				)
				if err != nil {
					return nil, nil, err
				}
				return plan.Transactions, []*bt.Tx{
					plan.Freeze,
					plan.Unfreeze,
				}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := *funding
			input.Satoshis = test.fundingValue
			transactions, txs, err := test.build(&input)
			if err != nil {
				t.Fatal(err)
			}
			externalParent := bt.NewTx()
			externalParent.Outputs = make([]*bt.Output, int(input.Vout)+1)
			externalParent.Outputs[input.Vout] = &bt.Output{
				LockingScript: input.LockingScript,
				Satoshis:      input.Satoshis,
			}
			parents := map[string]*bt.Tx{
				hex.EncodeToString(input.TxID): externalParent,
			}
			for _, tx := range txs {
				parents[tx.TxID()] = tx
			}
			for _, item := range transactions {
				tx, err := bt.NewTxFromString(item.Raw)
				if err != nil {
					t.Fatal(err)
				}
				paid, err := feeFromParents(
					tx,
					func(txid string) (*bt.Tx, error) {
						parent, ok := parents[txid]
						if !ok {
							return nil, fmt.Errorf("missing parent %s", txid)
						}
						return parent, nil
					},
				)
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
		})
	}
}
