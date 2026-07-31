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
	coinV2, err := ClassifyFTScript(syntheticFTScript(t, 2012, bscript.Op16))
	if err != nil {
		t.Fatal(err)
	}
	coinV3, err := ClassifyFTScript(syntheticFTScript(t, 2012, bscript.OpDATA2))
	if err != nil {
		t.Fatal(err)
	}
	if v1 != (FTScriptInfo{Version: FTVersion1}) {
		t.Fatalf("classified v1 as %+v", v1)
	}
	if coinV2 != (FTScriptInfo{Version: FTVersion2, IsCoin: true}) {
		t.Fatalf("classified v2 coin as %+v", coinV2)
	}
	if coinV3 != (FTScriptInfo{Version: FTVersion3, IsCoin: true}) {
		t.Fatalf("classified v3 coin as %+v", coinV3)
	}
}

func TestClassifyFTScriptRejectsRetiredV4(t *testing.T) {
	v4 := syntheticFTScript(t, 1948, bscript.Op15)
	if _, err := ClassifyFTScript(v4); err == nil {
		t.Fatal("expected retired 1948-byte FT v4 script to be rejected")
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
