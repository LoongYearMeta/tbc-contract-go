package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type orderBookJSFixture struct {
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}

func tokenOrderFixture(t *testing.T, name string) orderBookJSFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "js-1.6.5", "script-hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]orderBookJSFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	fixture, ok := fixtures[name]
	if !ok {
		t.Fatalf("missing JS fixture %q", name)
	}
	return fixture
}

func fixtureTokenOrderBook() *OrderBook {
	return &OrderBook{
		HoldAddress:     "1BitcoinEaterAddressDontSendf59kuE",
		TokenSaleVolume: big.NewInt(1000),
		FtPartialHash:   "55" + repeatHexByte("55", 31),
		FtBPartialHash:  repeatHexByte("66", 32),
		TokenFeeRate:    big.NewInt(3),
		TokenUnitPrice:  big.NewInt(4),
		FtID:            repeatHexByte("77", 32),
		FtBID:           repeatHexByte("88", 32),
	}
}

func repeatHexByte(value string, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		out += value
	}
	return out
}

func TestTokenOrderDataRoundTrip(t *testing.T) {
	order := fixtureTokenOrderBook()
	data, err := order.BuildTokenOrderData()
	if err != nil {
		t.Fatal(err)
	}
	if got := data.Len(); got != 180 {
		t.Fatalf("data length = %d, want 180", got)
	}

	decoded, err := GetTokenOrderData(data.ToHex(), true)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.HoldAddress != order.HoldAddress ||
		decoded.SaleVolume.Cmp(order.TokenSaleVolume) != 0 ||
		decoded.FTAPartialHash != order.FtPartialHash ||
		decoded.FTBPartialHash != order.FtBPartialHash ||
		decoded.FeeRate.Cmp(order.TokenFeeRate) != 0 ||
		decoded.UnitPrice.Cmp(order.TokenUnitPrice) != 0 ||
		decoded.FTAID != order.FtID ||
		decoded.FTBID != order.FtBID {
		t.Fatalf("round trip mismatch: %#v", decoded)
	}
}

func TestTokenOrderCodeMatchesJS165(t *testing.T) {
	order := fixtureTokenOrderBook()
	tests := []struct {
		name    string
		fixture string
		build   func(string) ([]byte, error)
	}{
		{
			name:    "sell",
			fixture: "tokenSellOrder",
			build: func(taxAddress string) ([]byte, error) {
				script, err := order.GetTokenSellOrderCode(taxAddress)
				if err != nil {
					return nil, err
				}
				return script.Bytes(), nil
			},
		},
		{
			name:    "buy",
			fixture: "tokenBuyOrder",
			build: func(taxAddress string) ([]byte, error) {
				script, err := order.GetTokenBuyOrderCode(taxAddress)
				if err != nil {
					return nil, err
				}
				return script.Bytes(), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.build(order.HoldAddress)
			if err != nil {
				t.Fatal(err)
			}
			want := tokenOrderFixture(t, test.fixture)
			if len(got) != want.Length {
				t.Fatalf("length = %d, want %d", len(got), want.Length)
			}
			sum := sha256.Sum256(got)
			if gotHash := hex.EncodeToString(sum[:]); gotHash != want.SHA256 {
				t.Fatalf("sha256 = %s, want %s", gotHash, want.SHA256)
			}
		})
	}
}

func TestTokenOrderVolumeUpdate(t *testing.T) {
	order := fixtureTokenOrderBook()
	code, err := order.GetTokenSellOrderCode(order.HoldAddress)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateTokenSaleVolume(code.ToHex(), big.NewInt(4321))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := GetTokenOrderData(updated, true)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SaleVolume.Cmp(big.NewInt(4321)) != 0 {
		t.Fatalf("sale volume = %s, want 4321", decoded.SaleVolume)
	}
	if decoded.FTAID != order.FtID || decoded.FTBID != order.FtBID {
		t.Fatal("volume update changed an asset ID")
	}
}

func TestTokenOrderRejectsOutOfRangeAmounts(t *testing.T) {
	order := fixtureTokenOrderBook()
	order.TokenSaleVolume = new(big.Int).Lsh(big.NewInt(1), 64)
	if _, err := order.BuildTokenOrderData(); err == nil {
		t.Fatal("expected uint64 overflow error")
	}
	if _, err := UpdateTokenSaleVolume("00", big.NewInt(-1)); err == nil {
		t.Fatal("expected negative volume error")
	}
}

func TestTokenOrderUnlockUsesTwelveOutputSlots(t *testing.T) {
	order := fixtureTokenOrderBook()
	code, err := order.GetTokenSellOrderCode(order.HoldAddress)
	if err != nil {
		t.Fatal(err)
	}
	preTX := bt.NewTx()
	preTX.AddOutput(&bt.Output{LockingScript: code, Satoshis: 1000})
	currentTX := bt.NewTx()
	currentTX.AddOutput(&bt.Output{
		LockingScript: bscript.NewFromBytes([]byte{bscript.OpTRUE}),
		Satoshis:      1000,
	})

	standard, err := GetOrderUnlock(currentTX, preTX, 0)
	if err != nil {
		t.Fatal(err)
	}
	token, err := GetTokenOrderUnlock(currentTX, preTX, 0)
	if err != nil {
		t.Fatal(err)
	}
	// JS getTokenOrderUnlock uses fixedOutputCount=12 instead of 10. Two
	// missing slots add eight zero bytes to currentTxData.
	if got, want := len(token), len(standard)+16; got != want {
		t.Fatalf("token unlock hex length = %d, want %d", got, want)
	}
}
