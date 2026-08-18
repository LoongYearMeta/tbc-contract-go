package contract

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func contractSyntheticFTScript(t *testing.T, fillOpcode byte) *bscript.Script {
	t.Helper()
	suffix := []byte{fillOpcode}
	if fillOpcode == bscript.OpDATA2 {
		suffix = append(suffix, 0xff, 0xff)
	}
	suffix = append(suffix, bscript.OpNOP, bscript.OpNOP, bscript.OpNOP)
	suffix = append(suffix, 5)
	suffix = append(suffix, []byte("2Code")...)
	body := bytes.Repeat([]byte{bscript.OpNOP}, 1884-len(suffix))
	return bscript.NewFromBytes(append(body, suffix...))
}

func TestComposedContractsUseSharedFTVersionClassifier(t *testing.T) {
	v2 := contractSyntheticFTScript(t, bscript.Op16)
	v3 := contractSyntheticFTScript(t, 95)

	poolVersion, poolCoin, err := classifyPoolFTCode(hex.EncodeToString(v3.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if poolVersion != int(util.FTVersion3) || poolCoin {
		t.Fatalf("pool classified v3 as version=%d coin=%v", poolVersion, poolCoin)
	}

	multiSigVersion, err := classifyMultiSigFTCode(v3)
	if err != nil {
		t.Fatal(err)
	}
	if multiSigVersion != int(util.FTVersion3) {
		t.Fatalf("multisig classified v3 as version=%d", multiSigVersion)
	}

	orderV2, err := classifyOrderBookFTCode(hex.EncodeToString(v2.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	orderV3, err := classifyOrderBookFTCode(hex.EncodeToString(v3.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if orderV2.Version != util.FTVersion2 || orderV3.Version != util.FTVersion3 {
		t.Fatalf("orderbook classified v2=%+v v3=%+v", orderV2, orderV3)
	}
}
