package contract

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func ftSwapFixtureTransactions(t *testing.T, inputCount int) (current, pre, contractTX *bt.Tx) {
	t.Helper()
	trueScript, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}

	current = bt.NewTx()
	for i := 0; i < inputCount; i++ {
		txid := hex.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32))
		if err := current.From(txid, uint32(i), trueScript.String(), 10_000); err != nil {
			t.Fatal(err)
		}
	}
	current.AddOutput(&bt.Output{LockingScript: trueScript, Satoshis: 1_000})

	pre = bt.NewTx()
	pre.AddOutput(&bt.Output{
		LockingScript: contractSyntheticFTScript(t, 95),
		Satoshis:      500,
	})
	pre.AddOutput(&bt.Output{LockingScript: trueScript, Satoshis: 0})

	contractTX = bt.NewTx()
	contractTX.AddOutput(&bt.Output{LockingScript: trueScript, Satoshis: 900})
	return current, pre, contractTX
}

func TestStaticGetFTUnlockSwapV3AddsContractInputIndex(t *testing.T) {
	current, pre, contractTX := ftSwapFixtureTransactions(t, 4)
	preTxData, err := util.GetPreTxdata(pre, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, err := StaticGetFTUnlockSwap(
		"aa", strings.Repeat("02", 33),
		current, pre, "", contractTX,
		3, 0, util.FTVersion3, false, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotHex := hex.EncodeToString(got.Bytes())
	if !strings.HasSuffix(gotHex, "52"+preTxData) {
		t.Fatalf("v3 unlock missing contract input marker 52")
	}
}

func TestGetFTUnlockSwapV3AddsContractInputIndex(t *testing.T) {
	current, pre, contractTX := ftSwapFixtureTransactions(t, 4)
	preTxData, err := util.GetPreTxdata(pre, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, err := (&FT{}).GetFTUnlockSwap(
		mustTestPrivateKey(t, 9),
		current, pre, "", contractTX,
		3, 0, util.FTVersion3, false, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotHex := hex.EncodeToString(got.Bytes())
	if !strings.HasSuffix(gotHex, "52"+preTxData) {
		t.Fatalf("v3 signed unlock missing contract input marker 52")
	}
}

func TestStaticGetFTUnlockSwapV3DefaultsMarkerToZero(t *testing.T) {
	current, pre, contractTX := ftSwapFixtureTransactions(t, 2)
	preTxData, err := util.GetPreTxdata(pre, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, err := StaticGetFTUnlockSwap(
		"aa", strings.Repeat("02", 33),
		current, pre, "", contractTX,
		1, 0, util.FTVersion3, true, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	gotHex := hex.EncodeToString(got.Bytes())
	if !strings.HasSuffix(gotHex, "0051"+preTxData) {
		t.Fatalf("v3 coin unlock missing zero marker and coin flag")
	}
}

func TestStaticGetFTUnlockSwapV1V2RemainCompatible(t *testing.T) {
	current, pre, contractTX := ftSwapFixtureTransactions(t, 2)
	for _, version := range []util.FTVersion{util.FTVersion1, util.FTVersion2} {
		t.Run(versionString(version), func(t *testing.T) {
			legacy, err := StaticGetFTunlockSwap(
				"aa", strings.Repeat("02", 33),
				current, pre, "", contractTX,
				1, 0, int(version), false,
			)
			if err != nil {
				t.Fatal(err)
			}
			typed, err := StaticGetFTUnlockSwap(
				"aa", strings.Repeat("02", 33),
				current, pre, "", contractTX,
				1, 0, version, false, false,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(legacy.Bytes(), typed.Bytes()) {
				t.Fatalf("version %d compatibility output changed", version)
			}
		})
	}
}

func TestStaticGetFTUnlockSwapV3RejectsUnsupportedContractInputIndex(t *testing.T) {
	current, pre, contractTX := ftSwapFixtureTransactions(t, 8)
	_, err := StaticGetFTUnlockSwap(
		"aa", strings.Repeat("02", 33),
		current, pre, "", contractTX,
		7, 0, util.FTVersion3, false, true,
	)
	if err == nil || !strings.Contains(err.Error(), "outside 0..5") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func versionString(version util.FTVersion) string {
	return "v" + string(rune('0'+version))
}
