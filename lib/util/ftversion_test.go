package util

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func syntheticFTScript(t *testing.T, length int, fillOpcode byte) *bscript.Script {
	t.Helper()
	if length < 7 {
		t.Fatal("synthetic FT length too small")
	}

	// The classifier reads the fifth chunk from the end. OP_DATA_2 consumes
	// two bytes, while every other marker below is a one-byte opcode.
	suffix := []byte{fillOpcode}
	if fillOpcode == bscript.OpDATA2 {
		suffix = append(suffix, 0xaa, 0xbb)
	}
	suffix = append(suffix, bscript.OpNOP, bscript.OpNOP, bscript.OpNOP, bscript.OpNOP)
	body := bytes.Repeat([]byte{bscript.OpNOP}, length-len(suffix))
	body = append(body, suffix...)
	return bscript.NewFromBytes(body)
}

func TestClassifyFTScriptDistinguishesV2AndV3AtSameLength(t *testing.T) {
	v2 := syntheticFTScript(t, 1884, bscript.Op16)
	v3FillOne := syntheticFTScript(t, 1884, 95)
	v3FillTwo := syntheticFTScript(t, 1884, bscript.OpDATA2)

	if len(v2.Bytes()) != len(v3FillOne.Bytes()) || len(v2.Bytes()) != len(v3FillTwo.Bytes()) {
		t.Fatal("fixtures must prove same-length versions")
	}

	gotV2, err := ClassifyFTScript(v2)
	if err != nil {
		t.Fatal(err)
	}
	gotV3One, err := ClassifyFTScript(v3FillOne)
	if err != nil {
		t.Fatal(err)
	}
	gotV3Two, err := ClassifyFTScript(v3FillTwo)
	if err != nil {
		t.Fatal(err)
	}
	if gotV2.Version != FTVersion2 || gotV2.IsCoin {
		t.Fatalf("classified v2 as %+v", gotV2)
	}
	if gotV3One.Version != FTVersion3 || gotV3One.IsCoin {
		t.Fatalf("classified v3 fill=1 as %+v", gotV3One)
	}
	if gotV3Two.Version != FTVersion3 || gotV3Two.IsCoin {
		t.Fatalf("classified v3 fill=2 as %+v", gotV3Two)
	}
}

func TestClassifyFTScriptRecognizesV1AndCoin(t *testing.T) {
	v1, err := ClassifyFTScript(syntheticFTScript(t, 1564, bscript.Op16))
	if err != nil {
		t.Fatal(err)
	}
	coin, err := ClassifyFTScript(syntheticFTScript(t, 2012, bscript.Op16))
	if err != nil {
		t.Fatal(err)
	}
	if v1 != (FTScriptInfo{Version: FTVersion1}) {
		t.Fatalf("classified v1 as %+v", v1)
	}
	if coin != (FTScriptInfo{Version: FTVersion2, IsCoin: true}) {
		t.Fatalf("classified coin as %+v", coin)
	}
}

func TestClassifyFTScriptRecognizesMultiContractV4(t *testing.T) {
	v4, err := ClassifyFTScript(syntheticFTScript(t, 1948, bscript.Op15))
	if err != nil {
		t.Fatal(err)
	}
	if v4 != (FTScriptInfo{Version: FTVersion4}) {
		t.Fatalf("classified v4 as %+v", v4)
	}
}

func TestFTV4PartialHashOffsetStaysSHA256BlockAligned(t *testing.T) {
	const (
		v4CodeLength    = 1948
		v4PartialOffset = 1920
	)
	if got := partialOffsetGetPreTx(v4CodeLength); got != v4PartialOffset {
		t.Fatalf("pre-tx partial offset = %d, want %d", got, v4PartialOffset)
	}
	if got := partialOffsetGetPrePre(v4CodeLength); got != v4PartialOffset {
		t.Fatalf("pre-pre-tx partial offset = %d, want %d", got, v4PartialOffset)
	}
	if v4PartialOffset%64 != 0 {
		t.Fatalf("v4 partial offset %d is not SHA-256 block aligned", v4PartialOffset)
	}
	if suffix := v4CodeLength - v4PartialOffset; suffix != 28 {
		t.Fatalf("v4 mutable suffix = %d bytes, want 28", suffix)
	}
	if obFTV4CodeLength != v4CodeLength || obFTV4PartialOffset != v4PartialOffset {
		t.Fatalf(
			"orderbook v4 length/offset = %d/%d, want %d/%d",
			obFTV4CodeLength, obFTV4PartialOffset, v4CodeLength, v4PartialOffset,
		)
	}
	if got := poolPartialOffsetForLength(v4CodeLength); got != v4PartialOffset {
		t.Fatalf("pool v4 partial offset = %d, want %d", got, v4PartialOffset)
	}
	if !poolIsFTScript(v4CodeLength) || !poolIsFTOrCoinScript(v4CodeLength) {
		t.Fatal("pool serializers do not recognize FT v4 as an FT script")
	}
}

func TestClassifyFTScriptHexRejectsUnsupportedLength(t *testing.T) {
	_, err := ClassifyFTScriptHex(hex.EncodeToString(bytes.Repeat([]byte{bscript.OpNOP}, 100)))
	if err == nil {
		t.Fatal("expected unsupported-length error")
	}
}

func TestFillCharLengthInFTRejectsShortScript(t *testing.T) {
	_, err := FillCharLengthInFT(bscript.NewFromBytes([]byte{bscript.OpNOP}))
	if err == nil {
		t.Fatal("expected short-script error")
	}
}
