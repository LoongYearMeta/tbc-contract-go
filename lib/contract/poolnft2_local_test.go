package contract

import (
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

func TestSwapToTBCLocalValidatesSuppliedAncestryBeforeNetwork(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	pool := NewPoolNFT2(&PoolNFT2Config{
		ContractTxID: strings.Repeat("91", 32),
		Network:      "http://127.0.0.1:1/",
	})
	_, err := pool.SwapToTBCLocal(
		fx.senderKey, fx.sender,
		fx.ftUTXO, nil, nil,
		big.NewInt(100), 1, fx.feeUTXO,
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ancestry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSwapToTBCLocalRejectsInsufficientSuppliedFT(t *testing.T) {
	fx := newHTLCTokenFixture(t, 100)
	pool := NewPoolNFT2(&PoolNFT2Config{
		ContractTxID: strings.Repeat("91", 32),
		Network:      "http://127.0.0.1:1/",
	})
	_, err := pool.SwapToTBCLocal(
		fx.senderKey, fx.sender,
		&util.FtUTXO{
			TxID: fx.ftUTXO.TxID, Vout: fx.ftUTXO.Vout,
			LockingScript: fx.ftUTXO.LockingScript,
			Satoshis:      fx.ftUTXO.Satoshis,
			FtBalance:     big.NewInt(100),
		},
		[]*bt.Tx{fx.preTX}, []string{""},
		big.NewInt(101), 1, fx.feeUTXO,
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "insufficient") {
		t.Fatalf("unexpected error: %v", err)
	}
}
