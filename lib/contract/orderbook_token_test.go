package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
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

func TestBuildTokenSellOrderOutputLayout(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	order := NewOrderBook()
	raw, err := order.BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(600), big.NewInt(1_000_000), big.NewInt(3),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Inputs) != 2 {
		t.Fatalf("inputs = %d, want FT + fee", len(tx.Inputs))
	}
	if len(tx.Outputs) != 6 {
		t.Fatalf("outputs = %d, want order + locked pair + FT change pair + fee change", len(tx.Outputs))
	}
	if tx.Outputs[0].LockingScript.Len() != tokenOrderLength || tx.Outputs[0].Satoshis != 300 {
		t.Fatalf("unexpected order output length/value")
	}
	locked, err := util.GetFtBalanceFromTape(tx.Outputs[2].LockingScript.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	change, err := util.GetFtBalanceFromTape(tx.Outputs[4].LockingScript.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if locked.Cmp(big.NewInt(600)) != 0 || change.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("locked/change = %s/%s, want 600/400", locked, change)
	}
}

func TestBuildTokenBuyOrderOutputLayout(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	order := NewOrderBook()
	raw, err := order.BuildTokenBuyOrderTX(
		fx.sender, fx.sender,
		big.NewInt(300), big.NewInt(2_000_000), big.NewInt(3),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Outputs) != 6 {
		t.Fatalf("outputs = %d, want order + locked pair + FT change pair + fee change", len(tx.Outputs))
	}
	locked, err := util.GetFtBalanceFromTape(tx.Outputs[2].LockingScript.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if locked.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("locked Token B = %s, want 600", locked)
	}
	data, err := GetTokenOrderData(tx.Outputs[0].LockingScript.ToHex(), true)
	if err != nil {
		t.Fatal(err)
	}
	if data.FTAID != strings.Repeat("71", 32) || data.FTBID != strings.Repeat("72", 32) {
		t.Fatalf("wrong pair IDs: %#v", data)
	}
}

func TestBuildTokenOrderRejectsInsufficientBalance(t *testing.T) {
	fx := newHTLCTokenFixture(t, 500)
	_, err := NewOrderBook().BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(501), big.NewInt(1_000_000), big.NewInt(0),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "insufficient") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildCancelTokenSellOrderReturnsLockedToken(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	order := NewOrderBook()
	raw, err := order.BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(600), big.NewInt(1_000_000), big.NewInt(3),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	createTX := mustTx(t, raw)
	createID, err := hex.DecodeString(createTX.TxID())
	if err != nil {
		t.Fatal(err)
	}
	orderUTXO := &bt.UTXO{
		TxID: createID, Vout: 0,
		LockingScript: createTX.Outputs[0].LockingScript,
		Satoshis:      createTX.Outputs[0].Satoshis,
	}
	lockedFT := &util.FtUTXO{
		TxID: createID, Vout: 1,
		LockingScript: createTX.Outputs[1].LockingScript,
		Satoshis:      createTX.Outputs[1].Satoshis,
		FtBalance:     big.NewInt(600),
	}
	cancelFee := &bt.UTXO{
		TxID:          bytesOf(0x73, 32),
		Vout:          0,
		LockingScript: fx.feeUTXO.LockingScript,
		Satoshis:      20_000,
	}
	cancelRaw, err := order.BuildCancelTokenSellOrderTX(
		orderUTXO, lockedFT, createTX, []*bt.UTXO{cancelFee},
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelTX := mustTx(t, cancelRaw)
	if len(cancelTX.Inputs) != 3 || len(cancelTX.Outputs) != 3 {
		t.Fatalf("cancel inputs/outputs = %d/%d, want 3/3", len(cancelTX.Inputs), len(cancelTX.Outputs))
	}
	returned, err := util.GetFtBalanceFromTape(cancelTX.Outputs[1].LockingScript.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if returned.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("returned balance = %s, want 600", returned)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}

func TestFillSigsMakeTokenSellOrderPopulatesEveryInput(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	order := NewOrderBook()
	raw, err := order.BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(600), big.NewInt(1_000_000), big.NewInt(3),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	sigs := signHTLCTokenFixtureInputs(
		t, raw,
		[]*bt.UTXO{util.FtUTXOToUTXO(fx.ftUTXO), fx.feeUTXO},
		fx.senderKey,
	)
	pubKey := hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())
	filled, err := order.FillSigsMakeTokenSellOrder(
		raw, sigs, pubKey, []*bt.Tx{fx.preTX}, []string{""},
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, filled)
	for i, input := range tx.Inputs {
		if input.UnlockingScript == nil || input.UnlockingScript.Len() == 0 {
			t.Fatalf("input %d has empty unlocking script", i)
		}
	}
}

func TestFillSigsCancelTokenSellOrderUsesOrderAndFTUnlocks(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	order := NewOrderBook()
	createRaw, err := order.BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(600), big.NewInt(1_000_000), big.NewInt(3),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
		fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO},
		[]*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	createTX := mustTx(t, createRaw)
	createID, _ := hex.DecodeString(createTX.TxID())
	orderUTXO := &bt.UTXO{
		TxID: createID, Vout: 0,
		LockingScript: createTX.Outputs[0].LockingScript,
		Satoshis:      createTX.Outputs[0].Satoshis,
	}
	lockedFT := &util.FtUTXO{
		TxID: createID, Vout: 1,
		LockingScript: createTX.Outputs[1].LockingScript,
		Satoshis:      createTX.Outputs[1].Satoshis,
		FtBalance:     big.NewInt(600),
	}
	cancelFee := feeUTXOForAddress(t, fx.sender, 0x74)
	cancelRaw, err := order.BuildCancelTokenSellOrderTX(
		orderUTXO, lockedFT, createTX, []*bt.UTXO{cancelFee},
	)
	if err != nil {
		t.Fatal(err)
	}
	sigs := signHTLCTokenFixtureInputs(
		t, cancelRaw,
		[]*bt.UTXO{orderUTXO, util.FtUTXOToUTXO(lockedFT), cancelFee},
		fx.senderKey,
	)
	pubKey := hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())
	filled, err := order.FillSigsCancelTokenSellOrder(
		cancelRaw, sigs, pubKey, createTX, createTX, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, filled)
	if got := tx.Inputs[0].UnlockingScript.Chunks(); len(got) != 3 ||
		got[len(got)-1].OpcodeNum != bscript.Op2 {
		t.Fatalf("order cancel unlock does not end in OP_2")
	}
	if tx.Inputs[1].UnlockingScript.Len() < 100 {
		t.Fatalf("FT swap unlock is unexpectedly short: %d", tx.Inputs[1].UnlockingScript.Len())
	}
	if tx.Inputs[2].UnlockingScript.Len() == 0 {
		t.Fatal("fee input is unsigned")
	}
}
