package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

func TestMintDefaultsMatchJS166V4(t *testing.T) {
	const (
		txid         = "1111111111111111111111111111111111111111111111111111111111111111"
		address      = "1BitcoinEaterAddressDontSendf59kuE"
		adminPubHash = "3333333333333333333333333333333333333333"
		codeHash     = "2222222222222222222222222222222222222222222222222222222222222222"
	)
	fixtures := loadJS166Fixtures(t)
	tests := []struct {
		name string
		coin bool
		make func() ([]byte, error)
	}{
		{
			name: "ftV4Mint",
			make: func() ([]byte, error) {
				script, err := getFTmintCode(txid, 0, address, 80)
				if err != nil {
					return nil, err
				}
				return script.Bytes(), nil
			},
		},
		{
			name: "stableCoinV4Mint",
			coin: true,
			make: func() ([]byte, error) {
				script, err := GetCoinMintCode(adminPubHash, address, codeHash, 80)
				if err != nil {
					return nil, err
				}
				return script.Bytes(), nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, err := test.make()
			if err != nil {
				t.Fatal(err)
			}
			if len(code) != util.FTV4CodeLength {
				t.Fatalf("code length = %d, want %d", len(code), util.FTV4CodeLength)
			}
			info, err := util.ClassifyFTScriptHex(hex.EncodeToString(code))
			if err != nil {
				t.Fatal(err)
			}
			if info != (util.FTScriptInfo{Version: util.FTVersion4, IsCoin: test.coin}) {
				t.Fatalf("classification = %+v", info)
			}
			gotHash := sha256.Sum256(code)
			want := fixtures[test.name]
			if len(code) != want.Length || hex.EncodeToString(gotHash[:]) != want.SHA256 {
				t.Fatalf("length/hash differ from JavaScript 1.6.6")
			}
		})
	}
}
