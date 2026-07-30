package contract

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func assertRawPaysContractFee(t *testing.T, raw string, inputSatoshis uint64) {
	t.Helper()

	tx := mustTx(t, raw)
	outputSatoshis, err := checkedOutputSatoshis(tx)
	if err != nil {
		t.Fatal(err)
	}
	if inputSatoshis < outputSatoshis {
		t.Fatalf("outputs %d exceed inputs %d", outputSatoshis, inputSatoshis)
	}
	paid := inputSatoshis - outputSatoshis
	target, err := contractTargetFee(len(tx.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if paid < target {
		t.Fatalf("paid fee %d below target %d for %d signed bytes", paid, target, len(tx.Bytes()))
	}
}

func TestMintFTPaysFeeFromActualSignedBytes(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	ft, err := NewFT(&FtParams{
		Name: "FeeSafety", Symbol: "FEE", Amount: 1_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	raws, err := ft.MintFT(fx.senderKey, fx.sender, fx.feeUTXO)
	if err != nil {
		t.Fatal(err)
	}
	if len(raws) != 2 {
		t.Fatalf("mint raws = %d, want 2", len(raws))
	}
	assertRawPaysContractFee(t, raws[0], fx.feeUTXO.Satoshis)
	assertRawPaysContractFee(t, raws[1], 10_000)
}

func TestFTTransferAndBatchPayFeeFromActualSignedBytes(t *testing.T) {
	t.Run("transfer", func(t *testing.T) {
		fx := newHTLCTokenFixture(t, 1_000)
		ft := &FT{CodeScript: fx.code.ToHex(), TapeScript: fx.tape.ToHex(), Decimal: 6}

		raw, err := ft.Transfer(
			fx.senderKey, fx.receiver, big.NewInt(600),
			[]*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
			[]*bt.Tx{fx.preTX}, []string{""}, 0,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertRawPaysContractFee(t, raw, fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis)
	})

	t.Run("batch transfer", func(t *testing.T) {
		fx := newHTLCTokenFixture(t, 1_000)
		ft := &FT{CodeScript: fx.code.ToHex(), TapeScript: fx.tape.ToHex(), Decimal: 6}

		raws, err := ft.BatchTransfer(
			fx.senderKey,
			[]AddressAmount{{Address: fx.receiver, Amount: big.NewInt(600)}},
			[]*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
			[]*bt.Tx{fx.preTX}, []string{""},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(raws) != 1 {
			t.Fatalf("batch raws = %d, want 1", len(raws))
		}
		assertRawPaysContractFee(t, raws[0], fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis)
	})
}

func stableFeeFixture(t *testing.T) (htlcTokenFixture, *StableCoin) {
	t.Helper()

	fx := newHTLCTokenFixture(t, 1_000)
	tape, err := buildStableCoinTapeScript("Stable", "ST", 6, big.NewInt(1_000), 0)
	if err != nil {
		t.Fatal(err)
	}
	code, err := getFTmintCode(strings.Repeat("61", 32), 0, fx.sender, len(tape.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	preTX := bt.NewTx()
	preTX.AddOutput(&bt.Output{LockingScript: code, Satoshis: 500})
	preTX.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	txid, err := hex.DecodeString(preTX.TxID())
	if err != nil {
		t.Fatal(err)
	}
	fx.code = code
	fx.tape = tape
	fx.preTX = preTX
	fx.ftUTXO = &util.FtUTXO{
		TxID:          txid,
		Vout:          0,
		LockingScript: code,
		Satoshis:      500,
		FtBalance:     big.NewInt(1_000),
	}
	return fx, &StableCoin{FT: &FT{
		CodeScript: code.ToHex(),
		TapeScript: tape.ToHex(),
		Decimal:    6,
	}}
}

func TestStableCoinSingleInputTransferAndBatchPayActualFee(t *testing.T) {
	t.Run("transfer", func(t *testing.T) {
		fx, stable := stableFeeFixture(t)

		raw, err := stable.Transfer(
			fx.senderKey, fx.receiver, big.NewInt(600),
			[]*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
			[]*bt.Tx{fx.preTX}, []string{""}, 0,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertRawPaysContractFee(t, raw, fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis)
	})

	t.Run("batch transfer", func(t *testing.T) {
		fx, stable := stableFeeFixture(t)

		raws, err := stable.BatchTransfer(
			fx.senderKey,
			[]AddressAmount{{Address: fx.receiver, Amount: big.NewInt(600)}},
			[]*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
			[]*bt.Tx{fx.preTX}, []string{""},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(raws) != 1 {
			t.Fatalf("batch raws = %d, want 1", len(raws))
		}
		assertRawPaysContractFee(t, raws[0], fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis)
	})
}

func TestFTAndStableCoinMergePayActualFee(t *testing.T) {
	t.Run("FT merge", func(t *testing.T) {
		fx := newHTLCTokenFixture(t, 1_000)
		ft := &FT{CodeScript: fx.code.ToHex(), TapeScript: fx.tape.ToHex(), Decimal: 6}

		tx, err := ft.mergeFTSingle(
			fx.senderKey,
			[]*util.FtUTXO{fx.ftUTXO, fx.ftUTXO},
			[]*bt.Tx{fx.preTX, fx.preTX},
			[]string{"", ""},
			fx.feeUTXO,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertRawPaysContractFee(
			t,
			hex.EncodeToString(tx.Bytes()),
			2*fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis,
		)
	})

	t.Run("stablecoin merge", func(t *testing.T) {
		fx, stable := stableFeeFixture(t)

		tx, err := stable.mergeCoinSingle(
			fx.senderKey,
			[]*util.FtUTXO{fx.ftUTXO, fx.ftUTXO},
			[]*bt.Tx{fx.preTX, fx.preTX},
			[]string{"", ""},
			fx.feeUTXO,
		)
		if err != nil {
			t.Fatal(err)
		}
		assertRawPaysContractFee(
			t,
			hex.EncodeToString(tx.Bytes()),
			2*fx.ftUTXO.Satoshis+fx.feeUTXO.Satoshis,
		)
	})
}
