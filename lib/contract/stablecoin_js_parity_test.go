package contract

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func TestStableCoinCodeClassifiesItsJS166V4FillMarker(t *testing.T) {
	fx, _ := stableFeeFixture(t)
	code, err := GetCoinMintCode(
		strings.Repeat("11", 20),
		fx.sender,
		strings.Repeat("22", 32),
		91,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Bytes()) != util.FTV4CodeLength {
		t.Fatalf("StableCoin code length=%d want=%d", len(code.Bytes()), util.FTV4CodeLength)
	}

	info, err := classifyOrderBookFTCode(code.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if info != (util.FTScriptInfo{Version: util.FTVersion4, IsCoin: true}) {
		t.Fatalf("StableCoin classification=%+v want v4 coin", info)
	}

	current, pre, contractTX := ftSwapFixtureTransactions(t, 2)
	preTxData, err := util.GetPreTxdata(pre, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := StaticGetFTUnlockSwap(
		"aa", strings.Repeat("02", 33),
		current, pre, "", contractTX,
		1, 0, info.Version, info.IsCoin, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hex.EncodeToString(unlock.Bytes()), "0051"+preTxData) {
		t.Fatal("StableCoin OrderBook unlock is missing the v4 index and coin markers")
	}
}

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
