package main

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func finalizeAdminPreparedForStructure(
	prepared *contract.AdminPrepared,
) ([]string, error) {
	signatures := make([][]byte, len(prepared.Sighashes))
	for i := range signatures {
		signatures[i] = bytes.Repeat([]byte{byte(i + 1)}, 64)
	}
	if err := validateAdminSignatures(signatures, len(prepared.Sighashes)); err != nil {
		return nil, err
	}
	return prepared.Finalize(signatures)
}

func stableCoinStageFixture(
	t *testing.T,
	privateKey *bec.PrivateKey,
) (*bt.Tx, *bt.UTXO, []byte) {
	t.Helper()
	address, err := bscript.NewAddressFromPublicKey(privateKey.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	lockingScript, err := bscript.NewP2PKHFromAddress(address.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	parent := bt.NewTx()
	parent.AddOutput(&bt.Output{
		LockingScript: lockingScript,
		Satoshis:      2_000_000,
	})
	funding, err := outputUTXO(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	aggregateKey := append(
		[]byte(nil),
		privateKey.PubKey().SerialiseCompressed()[1:]...,
	)
	return parent, funding, aggregateKey
}

func TestValidateAdminSignatures(t *testing.T) {
	valid := [][]byte{make([]byte, 64), make([]byte, 64)}
	if err := validateAdminSignatures(valid, 2); err != nil {
		t.Fatalf("valid signatures rejected: %v", err)
	}
	if err := validateAdminSignatures(valid[:1], 2); err == nil {
		t.Fatal("expected signature count rejection")
	}
	invalid := [][]byte{make([]byte, 64), make([]byte, 63)}
	if err := validateAdminSignatures(invalid, 2); err == nil {
		t.Fatal("expected non-64-byte signature rejection")
	}
}

func TestStableCoinTapeLockTimeRoundTrip(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	parent := bt.NewTx()
	parent.AddOutput(&bt.Output{
		LockingScript: funding.LockingScript,
		Satoshis:      funding.Satoshis,
	})
	var err error
	funding, err = outputUTXO(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := contract.NewStableCoin(&contract.FtParams{
		Name: "Matrix Coin", Symbol: "MSC", Amount: 1_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := stable.PrepareCreateCoin(
		bytes.Repeat([]byte{0x02}, 32),
		privateKey,
		address,
		funding,
		parent,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Tx.Outputs) < 5 {
		t.Fatal("prepared initial mint has no Coin Tape")
	}

	const lockTime = uint32(1_800_000_000)
	tape, err := contract.SetLockTimeInTape(
		prepared.Tx.Outputs[4].LockingScript,
		lockTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := contract.GetLockTimeFromTape(tape)
	if err != nil {
		t.Fatal(err)
	}
	if got != lockTime {
		t.Fatalf("lockTime=%d want=%d", got, lockTime)
	}

	if _, err := contract.SetLockTimeInTape(
		bscript.NewFromBytes([]byte{0x51}),
		lockTime,
	); err == nil {
		t.Fatal("expected malformed tape rejection")
	}
}

func TestBuildStableCoinLifecyclePlanCoversJS165Surface(t *testing.T) {
	privateKey, address, _ := ftStageFixture(t)
	parent, funding, aggregateKey := stableCoinStageFixture(t, privateKey)
	const lockTime = uint32(1_800_000_000)

	plan, err := buildStableCoinLifecyclePlan(
		privateKey,
		address,
		funding,
		parent,
		aggregateKey,
		finalizeAdminPreparedForStructure,
		lockTime,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := []string{
		"stablecoin-coin-nft-create",
		"stablecoin-initial-mint",
		"stablecoin-admin-mint",
		"stablecoin-owner-transfer",
		"stablecoin-batch-transfer",
		"stablecoin-merge",
		"stablecoin-freeze",
		"stablecoin-unfreeze",
	}
	if len(plan.Transactions) != len(wantLabels) {
		t.Fatalf("transactions=%d want=%d", len(plan.Transactions), len(wantLabels))
	}
	for i, want := range wantLabels {
		item := plan.Transactions[i]
		if item.Label != want {
			t.Fatalf("transaction %d label=%q want=%q", i, item.Label, want)
		}
		tx, err := bt.NewTxFromString(item.Raw)
		if err != nil {
			t.Fatalf("%s parse: %v", want, err)
		}
		if err := validateStableCoinLifecycleTransaction(want, tx, lockTime); err != nil {
			t.Fatalf("%s validate: %v", want, err)
		}
	}

	if plan.StableCoin.ContractTxid != plan.InitialMint.TxID() {
		t.Fatalf(
			"contract txid=%s want initial mint %s",
			plan.StableCoin.ContractTxid,
			plan.InitialMint.TxID(),
		)
	}
	if plan.StableCoin.TotalSupply.Cmp(big.NewInt(150_000)) != 0 {
		t.Fatalf("total supply=%s want=150000", plan.StableCoin.TotalSupply)
	}
}
