package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
)

func TestLiveGoStableCoinOrderBookMatchesAcceptedJS165Transaction(t *testing.T) {
	if os.Getenv("TBC_LIVE_STABLE_ORDERBOOK_FIXTURE") != "1" {
		t.Skip("set TBC_LIVE_STABLE_ORDERBOOK_FIXTURE=1 for the read-only testnet fixture")
	}

	const (
		buyTxID      = "91f2ac4bc4a17b5c58b5bfa4f72b23f5d1ddf25e9b912b61876376547ab5c1c9"
		sellTxID     = "4bae13fcf9884a4f262b20605516736e3e44c5f10542493f4ebcbd732d75d2a6"
		feeTxID      = "77d3fffce347c75405827e1f2aad7e9074f5f9a7660cb45e8ae9fbd44a4622e4"
		mintTxID     = "4ace51a9682a141e7ed4d358f30570630c33b9a2f9f1ccfa1b595bf8097dcac1"
		acceptedTxID = "72c25b7bca31b67d20efc7e50d7276df95c65b84c73681f349c7efee1a5eb804"
	)

	fetch := func(txid string) *bt.Tx {
		t.Helper()
		tx, err := api.FetchTXRaw(txid, "testnet")
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	buy, sell, feeParent, mint, accepted := fetch(buyTxID), fetch(sellTxID), fetch(feeTxID), fetch(mintTxID), fetch(acceptedTxID)
	matcher, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{43}, 32))
	matcherAddress, err := orderBookAddress(matcher)
	if err != nil {
		t.Fatal(err)
	}
	buyOrder, err := orderBookUTXO(buy, 0)
	if err != nil {
		t.Fatal(err)
	}
	buyFT, err := ftUTXOFromTX(buy, 1)
	if err != nil {
		t.Fatal(err)
	}
	sellOrder, err := orderBookUTXO(sell, 0)
	if err != nil {
		t.Fatal(err)
	}
	feeUTXO, err := orderBookUTXO(feeParent, 1)
	if err != nil {
		t.Fatal(err)
	}
	prePre, err := contractutil.BuildFtPrePreTxData(buy, 1, []*bt.Tx{mint})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := contract.NewOrderBook().MatchOrder(
		matcher,
		buyOrder, buy,
		buyFT, buy, prePre,
		sellOrder, sell,
		[]*bt.UTXO{feeUTXO},
		matcherAddress, matcherAddress,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if matched.Version != accepted.Version || matched.LockTime != accepted.LockTime {
		t.Fatal("Go StableCoin match header differs from the accepted JS/Rust transaction")
	}
	if len(matched.Inputs) != len(accepted.Inputs) || len(matched.Outputs) != len(accepted.Outputs) {
		t.Fatal("Go StableCoin match shape differs from the accepted JS/Rust transaction")
	}
	for i := range matched.Inputs {
		if matched.Inputs[i].PreviousTxIDStr() != accepted.Inputs[i].PreviousTxIDStr() ||
			matched.Inputs[i].PreviousTxOutIndex != accepted.Inputs[i].PreviousTxOutIndex ||
			matched.Inputs[i].SequenceNumber != accepted.Inputs[i].SequenceNumber {
			t.Fatalf("Go StableCoin match input %d differs from accepted structure", i)
		}
	}
	for i := range matched.Outputs {
		if matched.Outputs[i].Satoshis != accepted.Outputs[i].Satoshis ||
			matched.Outputs[i].LockingScript.ToHex() != accepted.Outputs[i].LockingScript.ToHex() {
			t.Fatalf("Go StableCoin match output %d differs from accepted structure", i)
		}
	}
	parents := map[string]*bt.Tx{
		buyTxID: buy, sellTxID: sell, feeTxID: feeParent,
	}
	if err := validateInputScriptsFromParents(matched, func(txid string) (*bt.Tx, error) {
		parent, ok := parents[txid]
		if !ok {
			return nil, fmt.Errorf("missing parent %s", txid)
		}
		return parent, nil
	}); err != nil {
		t.Fatal(err)
	}
	if matched.TxID() != acceptedTxID {
		t.Fatalf("valid Go txid=%s want accepted JS/Rust txid=%s", matched.TxID(), acceptedTxID)
	}
}

func TestValidateResidualSaleVolume(t *testing.T) {
	before := &contract.OrderData{SaleVolume: 100_000}
	after := &contract.OrderData{SaleVolume: 40_000}
	if err := validateResidualSaleVolume(before, after, 60_000); err != nil {
		t.Fatal(err)
	}
	if err := validateResidualSaleVolume(before, after, 59_999); err == nil {
		t.Fatal("expected incorrect matched-volume rejection")
	}
	if err := validateResidualSaleVolume(before, nil, 60_000); err == nil {
		t.Fatal("expected nil residual order rejection")
	}
}

func TestOrderBookStageUsesOrdinaryFTV3(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Order Matrix", Symbol: "OMX", Amount: 1_000_000, Decimal: 2,
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
	if err := validateOrderBookFTCode(mint.Outputs[0].LockingScript); err != nil {
		t.Fatal(err)
	}
	if contract.IsCoinScript(mint.Outputs[0].LockingScript.ToHex()) {
		t.Fatal("ordinary orderbook stage selected the StableCoin branch")
	}
}

func TestOrderBookLifecyclePaysFinalSignedSizeFees(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = 150_000
	sellerKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		t.Fatal(err)
	}
	sellerAddress, err := orderBookAddress(sellerKey)
	if err != nil {
		t.Fatal(err)
	}
	matcherKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		t.Fatal(err)
	}
	matcherAddress, err := orderBookAddress(matcherKey)
	if err != nil {
		t.Fatal(err)
	}
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Order Matrix", Symbol: "OMX", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	mintRaws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	source, err := bt.NewTxFromString(mintRaws[0])
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(mintRaws[1])
	if err != nil {
		t.Fatal(err)
	}
	sourceChange, err := outputUTXO(source, 2)
	if err != nil {
		t.Fatal(err)
	}
	fundingRaw, err := buildOrderBookFunding(
		privateKey,
		sourceChange,
		sellerAddress,
		address,
		matcherAddress,
	)
	if err != nil {
		t.Fatal(err)
	}

	parents := map[string]*bt.Tx{
		source.TxID(): source,
		mint.TxID():   mint,
	}
	record := func(label, raw string) *bt.Tx {
		t.Helper()
		tx, err := bt.NewTxFromString(raw)
		if err != nil {
			t.Fatalf("%s parse: %v", label, err)
		}
		paid, err := feeFromParents(tx, func(txid string) (*bt.Tx, error) {
			parent, ok := parents[txid]
			if !ok {
				return nil, fmt.Errorf("missing parent %s", txid)
			}
			return parent, nil
		})
		if err != nil {
			t.Fatalf("%s fee: %v", label, err)
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
			t.Fatalf("%s script: %v", label, err)
		}
		target := targetFee80(len(tx.Bytes()))
		if paid < target {
			t.Fatalf(
				"%s fee=%d target=%d bytes=%d",
				label,
				paid,
				target,
				len(tx.Bytes()),
			)
		}
		parents[tx.TxID()] = tx
		return tx
	}
	fundingTX := record("orderbook-participant-funding", fundingRaw)

	ftTemplate := mint.Outputs[0].LockingScript
	partialHash, err := contract.ComputeFtPartialHash(
		ftTemplate.ToHex(),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	baseSpec := ordinaryOrderSpec{
		TaxAddress:  orderBookTax,
		UnitPrice:   2_000_000,
		FeeRate:     10_000,
		TokenID:     token.ContractTxid,
		PartialHash: partialHash,
	}

	cancelSellSpec := baseSpec
	cancelSellSpec.Side = "sell"
	cancelSellSpec.Holder = sellerAddress
	cancelSellSpec.Volume = 10_000
	sellerFunding, err := orderBookUTXO(fundingTX, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		cancelSellSpec.Volume,
		cancelSellSpec.UnitPrice,
		cancelSellSpec.FeeRate,
		cancelSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{sellerFunding},
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelSellCreate := record("orderbook-sell-create", raw)
	cancelSellUTXO, err := orderBookUTXO(cancelSellCreate, 0)
	if err != nil {
		t.Fatal(err)
	}
	cancelSellFee, err := changeUTXO(cancelSellCreate)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().CancelSellOrderWithSign(
		sellerKey,
		cancelSellUTXO,
		[]*bt.UTXO{cancelSellFee},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelSellCancel := record("orderbook-sell-cancel", raw)

	cancelBuySpec := baseSpec
	cancelBuySpec.Side = "buy"
	cancelBuySpec.Holder = address
	cancelBuySpec.Volume = 20_000
	mintedFT, err := ftUTXOFromTX(mint, 0)
	if err != nil {
		t.Fatal(err)
	}
	buyerFee, err := orderBookUTXO(fundingTX, 1)
	if err != nil {
		t.Fatal(err)
	}
	mintPrePre, err := localPrePre(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = buildSignedBuyOrder(
		privateKey,
		address,
		cancelBuySpec,
		mintedFT,
		buyerFee,
		mint,
		mintPrePre,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelBuyCreate := record("orderbook-buy-create", raw)
	cancelBuyOrderUTXO, err := orderBookUTXO(cancelBuyCreate, 0)
	if err != nil {
		t.Fatal(err)
	}
	cancelBuyFTUTXO, err := ftUTXOFromTX(cancelBuyCreate, 1)
	if err != nil {
		t.Fatal(err)
	}
	cancelBuyFee, err := changeUTXO(cancelBuyCreate)
	if err != nil {
		t.Fatal(err)
	}
	cancelBuyPrePre, err := localPrePre(mint, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().CancelBuyOrderWithSign(
		privateKey,
		cancelBuyOrderUTXO,
		cancelBuyFTUTXO,
		cancelBuyCreate,
		cancelBuyPrePre,
		[]*bt.UTXO{cancelBuyFee},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelBuyCancel := record("orderbook-buy-cancel", raw)

	fullSellSpec := baseSpec
	fullSellSpec.Side = "sell"
	fullSellSpec.Holder = sellerAddress
	fullSellSpec.Volume = 12_000
	returnedSellTBC, err := orderBookUTXO(cancelSellCancel, 0)
	if err != nil {
		t.Fatal(err)
	}
	returnedSellFee, err := changeUTXO(cancelSellCancel)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		fullSellSpec.Volume,
		fullSellSpec.UnitPrice,
		fullSellSpec.FeeRate,
		fullSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{returnedSellTBC, returnedSellFee},
	)
	if err != nil {
		t.Fatal(err)
	}
	fullSell := record("orderbook-full-sell-create", raw)

	fullBuySpec := baseSpec
	fullBuySpec.Side = "buy"
	fullBuySpec.Holder = address
	fullBuySpec.Volume = fullSellSpec.Volume
	returnedBuyFT, err := ftUTXOFromTX(cancelBuyCancel, 0)
	if err != nil {
		t.Fatal(err)
	}
	returnedBuyFee, err := changeUTXO(cancelBuyCancel)
	if err != nil {
		t.Fatal(err)
	}
	fullBuyPrePre, err := localPrePre(cancelBuyCreate, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = buildSignedBuyOrder(
		privateKey,
		address,
		fullBuySpec,
		returnedBuyFT,
		returnedBuyFee,
		cancelBuyCancel,
		fullBuyPrePre,
	)
	if err != nil {
		t.Fatal(err)
	}
	fullBuy := record("orderbook-full-buy-create", raw)
	fullBuyOrderUTXO, err := orderBookUTXO(fullBuy, 0)
	if err != nil {
		t.Fatal(err)
	}
	fullBuyFTUTXO, err := ftUTXOFromTX(fullBuy, 1)
	if err != nil {
		t.Fatal(err)
	}
	fullSellOrderUTXO, err := orderBookUTXO(fullSell, 0)
	if err != nil {
		t.Fatal(err)
	}
	matcherFee, err := orderBookUTXO(fundingTX, 2)
	if err != nil {
		t.Fatal(err)
	}
	fullMatchPrePre, err := localPrePre(cancelBuyCancel, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().MatchOrder(
		matcherKey,
		fullBuyOrderUTXO,
		fullBuy,
		fullBuyFTUTXO,
		fullBuy,
		fullMatchPrePre,
		fullSellOrderUTXO,
		fullSell,
		[]*bt.UTXO{matcherFee},
		orderBookTax,
		orderBookTax,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	fullMatch := record("orderbook-full-match", raw)

	partialSellSpec := baseSpec
	partialSellSpec.Side = "sell"
	partialSellSpec.Holder = sellerAddress
	partialSellSpec.Volume = 20_000
	partialSellFee, err := changeUTXO(fullSell)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		partialSellSpec.Volume,
		partialSellSpec.UnitPrice,
		partialSellSpec.FeeRate,
		partialSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{partialSellFee},
	)
	if err != nil {
		t.Fatal(err)
	}
	partialSell := record("orderbook-partial-sell-create", raw)

	partialBuySpec := baseSpec
	partialBuySpec.Side = "buy"
	partialBuySpec.Holder = address
	partialBuySpec.Volume = 8_000
	fullBuyChangeFT, err := ftUTXOFromTX(fullBuy, 3)
	if err != nil {
		t.Fatal(err)
	}
	fullBuyFee, err := changeUTXO(fullBuy)
	if err != nil {
		t.Fatal(err)
	}
	partialBuyPrePre, err := localPrePre(cancelBuyCancel, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = buildSignedBuyOrder(
		privateKey,
		address,
		partialBuySpec,
		fullBuyChangeFT,
		fullBuyFee,
		fullBuy,
		partialBuyPrePre,
	)
	if err != nil {
		t.Fatal(err)
	}
	partialBuy := record("orderbook-partial-buy-create", raw)
	partialBuyOrderUTXO, err := orderBookUTXO(partialBuy, 0)
	if err != nil {
		t.Fatal(err)
	}
	partialBuyFTUTXO, err := ftUTXOFromTX(partialBuy, 1)
	if err != nil {
		t.Fatal(err)
	}
	partialSellOrderUTXO, err := orderBookUTXO(partialSell, 0)
	if err != nil {
		t.Fatal(err)
	}
	partialMatchFee, err := changeUTXO(fullMatch)
	if err != nil {
		t.Fatal(err)
	}
	partialMatchPrePre, err := localPrePre(fullBuy, 3)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.NewOrderBook().MatchOrder(
		matcherKey,
		partialBuyOrderUTXO,
		partialBuy,
		partialBuyFTUTXO,
		partialBuy,
		partialMatchPrePre,
		partialSellOrderUTXO,
		partialSell,
		[]*bt.UTXO{partialMatchFee},
		orderBookTax,
		orderBookTax,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = record("orderbook-partial-match", raw)

	if contract.IsCoinScript(ftTemplate.ToHex()) {
		t.Fatal("orderbook lifecycle selected StableCoin")
	}
}
