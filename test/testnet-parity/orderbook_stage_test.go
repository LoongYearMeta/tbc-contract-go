package main

import (
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
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
