package main

import (
	"fmt"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
)

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
		privateKey,
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
		privateKey,
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
