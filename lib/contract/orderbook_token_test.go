package contract

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractapi "github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
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

type tokenOrderMatchFixture struct {
	key      *bec.PrivateKey
	address  string
	buy      *bt.UTXO
	buyTX    *bt.Tx
	buyFT    *util.FtUTXO
	sell     *bt.UTXO
	sellTX   *bt.Tx
	sellFT   *util.FtUTXO
	feeUTXOs []*bt.UTXO
}

func newTokenOrderMatchFixture(t *testing.T) tokenOrderMatchFixture {
	return newTokenOrderMatchFixtureWithVolumes(t, 600, 400)
}

func newTokenOrderMatchFixtureWithVolumes(t *testing.T, buyVolume, sellVolume int64) tokenOrderMatchFixture {
	t.Helper()
	fx := newHTLCTokenFixture(t, 2_000)
	const unitPrice = int64(2_000_000)
	ftaID := strings.Repeat("71", 32)
	ftbID := strings.Repeat("72", 32)

	buyRaw, err := NewOrderBook().BuildTokenBuyOrderTX(
		fx.sender, fx.sender,
		big.NewInt(buyVolume), big.NewInt(unitPrice), big.NewInt(10_000),
		ftaID, ftbID, fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{fx.feeUTXO}, []*util.FtUTXO{fx.ftUTXO}, []*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	sellFee := feeUTXOForAddress(t, fx.sender, 0x75)
	sellRaw, err := NewOrderBook().BuildTokenSellOrderTX(
		fx.sender, fx.sender,
		big.NewInt(sellVolume), big.NewInt(unitPrice), big.NewInt(10_000),
		ftaID, ftbID, fx.code.ToHex(), fx.code.ToHex(),
		[]*bt.UTXO{sellFee}, []*util.FtUTXO{fx.ftUTXO}, []*bt.Tx{fx.preTX},
	)
	if err != nil {
		t.Fatal(err)
	}
	buyTX := mustTx(t, buyRaw)
	sellTX := mustTx(t, sellRaw)
	buyID, _ := hex.DecodeString(buyTX.TxID())
	sellID, _ := hex.DecodeString(sellTX.TxID())

	return tokenOrderMatchFixture{
		key:     fx.senderKey,
		address: fx.sender,
		buy: &bt.UTXO{
			TxID: buyID, Vout: 0,
			LockingScript: buyTX.Outputs[0].LockingScript,
			Satoshis:      buyTX.Outputs[0].Satoshis,
		},
		buyTX: buyTX,
		buyFT: &util.FtUTXO{
			TxID: buyID, Vout: 1,
			LockingScript: buyTX.Outputs[1].LockingScript,
			Satoshis:      buyTX.Outputs[1].Satoshis,
			FtBalance:     new(big.Int).Mul(big.NewInt(buyVolume), big.NewInt(2)),
		},
		sell: &bt.UTXO{
			TxID: sellID, Vout: 0,
			LockingScript: sellTX.Outputs[0].LockingScript,
			Satoshis:      sellTX.Outputs[0].Satoshis,
		},
		sellTX: sellTX,
		sellFT: &util.FtUTXO{
			TxID: sellID, Vout: 1,
			LockingScript: sellTX.Outputs[1].LockingScript,
			Satoshis:      sellTX.Outputs[1].Satoshis,
			FtBalance:     big.NewInt(sellVolume),
		},
		feeUTXOs: []*bt.UTXO{{
			TxID:          bytesOf(0x76, 32),
			Vout:          0,
			LockingScript: fx.feeUTXO.LockingScript,
			Satoshis:      100_000,
		}},
	}
}

func TestMatchTokenOrderEqualFillHasNoResidualOrder(t *testing.T) {
	fx := newTokenOrderMatchFixtureWithVolumes(t, 400, 400)
	raw, err := NewOrderBook().MatchTokenOrder(
		fx.key,
		fx.buy, fx.buyTX, fx.buyFT, fx.buyTX, "",
		fx.sell, fx.sellTX, fx.sellFT, fx.sellTX, "",
		fx.feeUTXOs, fx.address, fx.address,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Outputs) != 9 {
		t.Fatalf("equal fill outputs = %d, want 8 FT outputs + fee change", len(tx.Outputs))
	}
}

func TestMatchTokenOrderCarriesForwardRemainingSell(t *testing.T) {
	fx := newTokenOrderMatchFixtureWithVolumes(t, 400, 600)
	raw, err := NewOrderBook().MatchTokenOrder(
		fx.key,
		fx.buy, fx.buyTX, fx.buyFT, fx.buyTX, "",
		fx.sell, fx.sellTX, fx.sellFT, fx.sellTX, "",
		fx.feeUTXOs, fx.address, fx.address,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Outputs) != 12 {
		t.Fatalf("partial sell outputs = %d, want 12", len(tx.Outputs))
	}
	remaining, err := GetTokenOrderData(tx.Outputs[9].LockingScript.ToHex(), true)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.SaleVolume.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("remaining sell volume = %s, want 200", remaining.SaleVolume)
	}
	change, err := util.GetFtBalanceFromTape(tx.Outputs[11].LockingScript.ToHex())
	if err != nil {
		t.Fatal(err)
	}
	if change.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("remaining Token A = %s, want 200", change)
	}
}

func TestMatchTokenOrderBuildsAtomicTwelveOutputSettlement(t *testing.T) {
	fx := newTokenOrderMatchFixture(t)
	raw, err := NewOrderBook().MatchTokenOrder(
		fx.key,
		fx.buy, fx.buyTX, fx.buyFT, fx.buyTX, "",
		fx.sell, fx.sellTX, fx.sellFT, fx.sellTX, "",
		fx.feeUTXOs, fx.address, fx.address,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Inputs) != 5 || len(tx.Outputs) != 12 {
		t.Fatalf("match inputs/outputs = %d/%d, want 5/12", len(tx.Inputs), len(tx.Outputs))
	}
	wantBalances := map[int]int64{
		1:  396, // Token A to buyer after 1% fee
		3:  4,   // Token A fee
		5:  792, // Token B to seller after 1% fee
		7:  8,   // Token B fee
		11: 400, // remaining Token B locked to the residual buy order
	}
	for output, want := range wantBalances {
		got, err := util.GetFtBalanceFromTape(tx.Outputs[output].LockingScript.ToHex())
		if err != nil {
			t.Fatal(err)
		}
		if got.Cmp(big.NewInt(want)) != 0 {
			t.Fatalf("output %d balance = %s, want %d", output, got, want)
		}
	}
	remaining, err := GetTokenOrderData(tx.Outputs[9].LockingScript.ToHex(), true)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.SaleVolume.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("remaining buy volume = %s, want 200", remaining.SaleVolume)
	}
	for i, input := range tx.Inputs {
		if input.UnlockingScript == nil || input.UnlockingScript.Len() == 0 {
			t.Fatalf("input %d is unsigned", i)
		}
	}
}

func TestMatchTokenOrderRejectsPairMismatch(t *testing.T) {
	fx := newTokenOrderMatchFixture(t)
	mutated := append([]byte(nil), fx.sell.LockingScript.Bytes()...)
	copy(mutated[len(mutated)-32:], bytesOf(0x99, 32))
	fx.sell.LockingScript = bscript.NewFromBytes(mutated)

	_, err := NewOrderBook().MatchTokenOrder(
		fx.key,
		fx.buy, fx.buyTX, fx.buyFT, fx.buyTX, "",
		fx.sell, fx.sellTX, fx.sellFT, fx.sellTX, "",
		fx.feeUTXOs, fx.address, fx.address,
	)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "pair mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMatchTokenOrderRejectsInvalidSettlementInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tokenOrderMatchFixture)
		want   string
	}{
		{
			name: "code hash mismatch",
			mutate: func(fx *tokenOrderMatchFixture) {
				script := append([]byte(nil), fx.sell.LockingScript.Bytes()...)
				script[len(script)-180+31] ^= 0xff
				fx.sell.LockingScript = bscript.NewFromBytes(script)
			},
			want: "code hash mismatch",
		},
		{
			name: "price mismatch",
			mutate: func(fx *tokenOrderMatchFixture) {
				script := append([]byte(nil), fx.sell.LockingScript.Bytes()...)
				binary.LittleEndian.PutUint64(script[len(script)-180+106:], 3_000_000)
				fx.sell.LockingScript = bscript.NewFromBytes(script)
			},
			want: "unit price mismatch",
		},
		{
			name: "insufficient token b",
			mutate: func(fx *tokenOrderMatchFixture) {
				fx.buyFT.FtBalance = big.NewInt(799)
			},
			want: "buy order ft balance is insufficient",
		},
		{
			name: "fee dust",
			mutate: func(fx *tokenOrderMatchFixture) {
				fx.feeUTXOs[0].Satoshis = 1
			},
			want: "insufficient tbc fee",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newTokenOrderMatchFixture(t)
			test.mutate(&fx)
			_, err := NewOrderBook().MatchTokenOrder(
				fx.key,
				fx.buy, fx.buyTX, fx.buyFT, fx.buyTX, "",
				fx.sell, fx.sellTX, fx.sellFT, fx.sellTX, "",
				fx.feeUTXOs, fx.address, fx.address,
			)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

type memoryOrderBookAPI struct {
	infos   map[string]*contractapi.FtInfoResponse
	ftUTXOs map[string][]*util.FtUTXO
	txs     map[string]*bt.Tx
	fees    []*bt.UTXO
	calls   []string
}

func (m *memoryOrderBookAPI) FetchFtInfo(contractID, network string) (*contractapi.FtInfoResponse, error) {
	m.calls = append(m.calls, "info:"+contractID+":"+network)
	info, ok := m.infos[contractID]
	if !ok {
		return nil, fmt.Errorf("missing info %s", contractID)
	}
	return info, nil
}

func (m *memoryOrderBookAPI) FetchFtUTXOs(contractID, owner, code, network string, amount *big.Int) ([]*util.FtUTXO, error) {
	m.calls = append(m.calls, "ft:"+contractID+":"+network)
	return m.ftUTXOs[contractID], nil
}

func (m *memoryOrderBookAPI) FetchUTXOs(address, network string) ([]*bt.UTXO, error) {
	m.calls = append(m.calls, "fee:"+network)
	return m.fees, nil
}

func (m *memoryOrderBookAPI) FetchTXRaw(txid, network string) (*bt.Tx, error) {
	m.calls = append(m.calls, "tx:"+network)
	tx, ok := m.txs[txid]
	if !ok {
		return nil, fmt.Errorf("missing tx %s", txid)
	}
	return tx, nil
}

func (m *memoryOrderBookAPI) FetchFtPrePreTxData(preTX *bt.Tx, vout int, network string) (string, error) {
	m.calls = append(m.calls, "prepre:"+network)
	return "", nil
}

func TestTokenOrderOnlineMakeSellComposesInjectedAPI(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	ftaID := strings.Repeat("71", 32)
	ftbID := strings.Repeat("72", 32)
	client := &memoryOrderBookAPI{
		infos: map[string]*contractapi.FtInfoResponse{
			ftaID: {CodeScript: fx.code.ToHex(), TapeScript: fx.tape.ToHex()},
			ftbID: {CodeScript: fx.code.ToHex(), TapeScript: fx.tape.ToHex()},
		},
		ftUTXOs: map[string][]*util.FtUTXO{ftaID: {fx.ftUTXO}},
		txs:     map[string]*bt.Tx{hex.EncodeToString(fx.ftUTXO.TxID): fx.preTX},
		fees:    []*bt.UTXO{fx.feeUTXO},
	}
	online := NewOnlineOrderBook(client, "testnet")
	raw, err := online.MakeTokenSellOrderWithSign(
		fx.senderKey, fx.sender,
		big.NewInt(600), big.NewInt(1_000_000), big.NewInt(3),
		ftaID, ftbID,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Inputs) != 2 || tx.Inputs[0].UnlockingScript.Len() == 0 ||
		tx.Inputs[1].UnlockingScript.Len() == 0 {
		t.Fatalf("online sell transaction was not fully composed and signed")
	}
	if len(client.calls) < 6 {
		t.Fatalf("online wrapper made too few discovery calls: %v", client.calls)
	}
}

func TestTokenOrderOnlineCancelAndMatchComposeInjectedAPI(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		fx := newTokenOrderMatchFixture(t)
		client := &memoryOrderBookAPI{
			txs: map[string]*bt.Tx{
				hex.EncodeToString(fx.sell.TxID): fx.sellTX,
			},
			fees: fx.feeUTXOs,
		}
		raw, err := NewOnlineOrderBook(client, "testnet").
			CancelTokenSellOrderWithSign(fx.key, fx.sell)
		if err != nil {
			t.Fatal(err)
		}
		tx := mustTx(t, raw)
		if len(tx.Inputs) != 3 {
			t.Fatalf("cancel inputs = %d, want order + FT + fee", len(tx.Inputs))
		}
		for i, input := range tx.Inputs {
			if input.UnlockingScript.Len() == 0 {
				t.Fatalf("cancel input %d is unsigned", i)
			}
		}
	})

	t.Run("match", func(t *testing.T) {
		fx := newTokenOrderMatchFixture(t)
		client := &memoryOrderBookAPI{
			txs: map[string]*bt.Tx{
				hex.EncodeToString(fx.buy.TxID):  fx.buyTX,
				hex.EncodeToString(fx.sell.TxID): fx.sellTX,
			},
			fees: fx.feeUTXOs,
		}
		feeAddress, err := bscript.NewAddressFromPublicKey(fx.key.PubKey(), false)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := NewOnlineOrderBook(client, "testnet").MatchTokenOrderWithSign(
			fx.key, fx.buy, fx.sell,
			feeAddress.AddressString, feeAddress.AddressString,
		)
		if err != nil {
			t.Fatal(err)
		}
		tx := mustTx(t, raw)
		if len(tx.Outputs) != 12 {
			t.Fatalf("online match outputs = %d, want 12", len(tx.Outputs))
		}
		for i, input := range tx.Inputs {
			if input.UnlockingScript.Len() == 0 {
				t.Fatalf("match input %d is unsigned", i)
			}
		}
	})
}

func TestTokenOrderOnlineRejectsAmountsBeforeDiscovery(t *testing.T) {
	client := &memoryOrderBookAPI{}
	_, err := NewOnlineOrderBook(client, "testnet").MakeTokenBuyOrderWithSign(
		mustTestPrivateKey(t, 42), "1BitcoinEaterAddressDontSendf59kuE",
		nil, big.NewInt(1), big.NewInt(0),
		strings.Repeat("71", 32), strings.Repeat("72", 32),
	)
	if err == nil {
		t.Fatal("expected invalid amount error")
	}
	if len(client.calls) != 0 {
		t.Fatalf("invalid request performed discovery calls: %v", client.calls)
	}
}
