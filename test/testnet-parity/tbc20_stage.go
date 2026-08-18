package main

import (
	"context"
	"fmt"
	"os"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/validator"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const tbc20StageFundingMinimumSatoshis = uint64(500_000)

type tbc20Resolver map[string]*bt.Tx

func (resolver tbc20Resolver) ResolveTBC20Ancestor(txid string) (*bt.Tx, bool) {
	tx, ok := resolver[txid]
	return tx, ok
}

type tbc20LifecyclePlan struct {
	Token        *contract.TBC20
	Transactions []plannedTransaction
	Source       *bt.Tx
	Mint         *bt.Tx
	Transfer     *bt.Tx
	Merge        *bt.Tx
}

func validateTBC20Outputs(tx *bt.Tx, codeVouts ...int) error {
	for _, codeVout := range codeVouts {
		utxo, balance, err := contract.BuildTBC20UTXO(tx, codeVout)
		if err != nil {
			return err
		}
		if utxo.Satoshis != contract.TBC20CodeSatoshis || balance.Sign() < 0 {
			return fmt.Errorf("invalid TBC20 output %d", codeVout)
		}
	}
	return nil
}

func buildTBC20LifecyclePlan(privateKey *bec.PrivateKey, address string, funding *bt.UTXO) (*tbc20LifecyclePlan, error) {
	token, err := contract.NewTBC20(contract.TBC20Config{Definition: &contract.TBC20Definition{
		Name: "Go Full Matrix TBC20", Symbol: "GMT20", Supply: "1000.00000000", Decimal: 8,
	}})
	if err != nil {
		return nil, err
	}
	mintResult, err := token.Mint(privateKey, address, funding, nil)
	if err != nil {
		return nil, fmt.Errorf("TBC20 mint build: %w", err)
	}
	if err := validateTBC20Outputs(mintResult.Transaction, 0); err != nil {
		return nil, err
	}
	minted, _, err := contract.BuildTBC20UTXO(mintResult.Transaction, 0)
	if err != nil {
		return nil, err
	}
	transferFee, err := changeUTXO(mintResult.Transaction)
	if err != nil {
		return nil, err
	}
	transfer, err := token.Transfer(
		privateKey, address, "400.00000000",
		[]*bt.UTXO{minted}, transferFee,
		[]*bt.Tx{mintResult.Transaction},
		[]contract.TBC20AncestorResolver{tbc20Resolver{mintResult.SourceTransaction.TxID(): mintResult.SourceTransaction}},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("TBC20 transfer build: %w", err)
	}
	if err := validateTBC20Outputs(transfer.Transaction, 0, 2); err != nil {
		return nil, err
	}
	mergeOne, _, err := contract.BuildTBC20UTXO(transfer.Transaction, 0)
	if err != nil {
		return nil, err
	}
	mergeTwo, _, err := contract.BuildTBC20UTXO(transfer.Transaction, 2)
	if err != nil {
		return nil, err
	}
	mergeFee, err := changeUTXO(transfer.Transaction)
	if err != nil {
		return nil, err
	}
	ancestor := tbc20Resolver{mintResult.Transaction.TxID(): mintResult.Transaction}
	merge, err := token.Merge(
		privateKey,
		[]*bt.UTXO{mergeOne, mergeTwo}, mergeFee,
		[]*bt.Tx{transfer.Transaction, transfer.Transaction},
		[]contract.TBC20AncestorResolver{ancestor, ancestor},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("TBC20 merge build: %w", err)
	}
	if err := validateTBC20Outputs(merge.Transaction, 0); err != nil {
		return nil, err
	}
	return &tbc20LifecyclePlan{
		Token: token, Source: mintResult.SourceTransaction, Mint: mintResult.Transaction, Transfer: transfer.Transaction, Merge: merge.Transaction,
		Transactions: []plannedTransaction{
			{Label: "tbc20-source", Raw: mintResult.SourceTxRaw},
			{Label: "tbc20-mint", Raw: mintResult.TxRaw},
			{Label: "tbc20-transfer", Raw: transfer.TxRaw},
			{Label: "tbc20-merge", Raw: merge.TxRaw},
		},
	}, nil
}

func executeTBC20LifecyclePlan(plan *tbc20LifecyclePlan, accept plannedTransactionAcceptor) (publicState, error) {
	if plan == nil || accept == nil {
		return publicState{}, fmt.Errorf("TBC20 plan and acceptor are required")
	}
	for _, item := range plan.Transactions {
		validate := func(tx *bt.Tx) error {
			switch item.Label {
			case "tbc20-mint", "tbc20-merge":
				return validateTBC20Outputs(tx, 0)
			case "tbc20-transfer":
				return validateTBC20Outputs(tx, 0, 2)
			default:
				return nil
			}
		}
		if _, err := accept(item, validate); err != nil {
			return publicState{}, err
		}
	}
	return publicState{TokenID: plan.Mint.TxID(), LastTxID: plan.Merge.TxID()}, nil
}

func validateBroadcastTBC20(tx *bt.Tx, network string) error {
	report, err := validator.AssertValidOnChainTransaction(context.Background(), validator.TokenValidationOptions{Transaction: tx, Network: network})
	if err != nil {
		return err
	}
	if report.Protocol == nil || report.Protocol.Family != "TBC20" || report.Status != validator.ValidationValid {
		return fmt.Errorf("unexpected TBC20 validator report")
	}
	return nil
}

func runTBC20Stage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(address, float64(tbc20StageFundingMinimumSatoshis)/1e6, cfg.Network)
	if err != nil {
		return err
	}
	plan, err := buildTBC20LifecyclePlan(decoded.PrivKey, address, funding)
	if err != nil {
		return err
	}
	state, err := executeTBC20LifecyclePlan(plan, func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
		tx, _, err := broadcastAndVerify(item.Label, item.Raw, cfg.Network, "tbc20-js-1.6.6", validate)
		return tx, err
	})
	if err != nil {
		return err
	}
	if err := validateBroadcastTBC20(plan.Transfer, cfg.Network); err != nil {
		return fmt.Errorf("validate broadcast transfer: %w", err)
	}
	if err := validateBroadcastTBC20(plan.Merge, cfg.Network); err != nil {
		return fmt.Errorf("validate broadcast merge: %w", err)
	}
	return writePublicState(os.Stdout, state)
}
