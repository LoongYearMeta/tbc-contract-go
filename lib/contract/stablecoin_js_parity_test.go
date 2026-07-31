package contract

import (
	"math/big"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func TestStableCoinTransferOutputLayoutMatchesJS165(t *testing.T) {
	fx, stable := stableFeeFixture(t)

	raw, err := stable.Transfer(
		fx.senderKey,
		fx.receiver,
		big.NewInt(600),
		[]*util.FtUTXO{fx.ftUTXO},
		fx.feeUTXO,
		[]*bt.Tx{fx.preTX},
		[]string{""},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}

	// JS tbc-contract 1.6.5 emits:
	// recipient Code/Tape, owner change Code/Tape, TBC fee change.
	if len(tx.Outputs) != 5 {
		t.Fatalf("outputs=%d want=5 (JS 1.6.5 layout)", len(tx.Outputs))
	}
	if tx.Outputs[0].Satoshis != 500 || tx.Outputs[1].Satoshis != 0 {
		t.Fatal("recipient Coin Code/Tape pair is malformed")
	}
	if tx.Outputs[2].Satoshis != 500 || tx.Outputs[3].Satoshis != 0 {
		t.Fatal("owner change Coin Code/Tape pair is malformed")
	}
	if tx.Outputs[4].Satoshis == 0 {
		t.Fatal("TBC fee change output is missing")
	}
}
