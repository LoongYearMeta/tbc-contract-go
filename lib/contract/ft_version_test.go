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
		suffix = append(suffix, 0xaa, 0xbb)
	}
	suffix = append(suffix, bscript.OpNOP, bscript.OpNOP, bscript.OpNOP, bscript.OpNOP)
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

	v4 := bytes.Repeat([]byte{bscript.OpNOP}, 1948)
	v4Info, err := classifyOrderBookFTCode(hex.EncodeToString(v4))
	if err != nil {
		t.Fatal(err)
	}
	if v4Info.Version != util.FTVersion4 {
		t.Fatalf("orderbook classified v4 as %+v", v4Info)
	}
	if got := ftCodeSizeHex(false, int(util.FTVersion4)); got != "9c07" {
		t.Fatalf("pool FT v4 code size = %s, want 9c07", got)
	}
}

func TestPoolFTLPV4ScriptsKeepV4PartialBoundary(t *testing.T) {
	pool := &PoolNFT2{}
	const (
		codeHash = "1111111111111111111111111111111111111111111111111111111111111111"
		address  = "1BitcoinEaterAddressDontSendf59kuE"
		tapeSize = 80
	)
	tests := []struct {
		name  string
		build func() (*bscript.Script, error)
	}{
		{
			name: "standard",
			build: func() (*bscript.Script, error) {
				return pool.getFtlpCode(codeHash, address, tapeSize, false, int(util.FTVersion4))
			},
		},
		{
			name: "lock time",
			build: func() (*bscript.Script, error) {
				return pool.getFtlpCodeWithLockTime(codeHash, address, tapeSize, false, int(util.FTVersion4))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			if got := script.Len(); got != 1948 {
				t.Fatalf("FTLP v4 script length = %d, want 1948", got)
			}
			_ = script.Bytes()[:1920]
			info, err := util.ClassifyFTScript(script)
			if err != nil {
				t.Fatal(err)
			}
			if info.Version != util.FTVersion4 || info.IsCoin {
				t.Fatalf("FTLP v4 classified as %+v", info)
			}
			transferred, err := BuildFTtransferCode(script.ToHex(), address)
			if err != nil {
				t.Fatal(err)
			}
			if transferred.Len() != script.Len() {
				t.Fatalf(
					"FTLP v4 transfer code length = %d, want %d",
					transferred.Len(), script.Len(),
				)
			}
		})
	}
}
