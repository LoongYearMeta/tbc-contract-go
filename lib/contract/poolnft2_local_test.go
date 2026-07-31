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

func TestUnlockedPoolCodeRejectsTagThatCrossesLockSizeBoundary(t *testing.T) {
	pool := NewPoolNFT2(nil)
	txid := strings.Repeat("11", 32)
	shortCode, err := pool.getPoolNftCode(
		txid,
		0,
		2,
		3,
		"parity",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(shortCode.Bytes()) > 3_300 {
		t.Fatalf("short unlocked pool code bytes=%d want<=3300", len(shortCode.Bytes()))
	}
	if _, err := pool.getPoolNftCode(
		txid,
		0,
		2,
		3,
		"0123456789abcdef",
		false,
	); err == nil {
		t.Fatal("expected ambiguous unlocked pool tag to be rejected")
	}
}

func TestBuildFtlpTapeWithLockTimePreservesFTTapeByteLength(t *testing.T) {
	const ftTapeBytes = 81
	tape, err := buildFtlpTapeWithLockTime(big.NewInt(1_000_000), ftTapeBytes, 1_750_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(tape.Bytes()); got != ftTapeBytes {
		t.Fatalf("locked FT-LP tape bytes=%d want=%d", got, ftTapeBytes)
	}
}
