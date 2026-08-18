package util

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type tbc20FixtureResolver map[string]*bt.Tx

func (resolver tbc20FixtureResolver) ResolveTBC20Ancestor(txid string) (*bt.Tx, bool) {
	tx, ok := resolver[txid]
	return tx, ok
}

func TestTBC20ABIConstantsAndUnsignedEncodingJS166(t *testing.T) {
	if TBC20MaxInputs != 6 || TBC20MaxOutputGroups != 8 || TBC20MaxOutputs != 16 || TBC20CodeSatoshis != 500 || TBC20AmountSlots != 6 || TBC20MinTapeBytes != 60 || TBC20MaxTapeBytes != 127 {
		t.Fatal("TBC20 ABI constants differ from JS 1.6.6")
	}
	for _, tc := range []struct {
		value uint64
		hex   string
	}{{0, ""}, {1, "01"}, {127, "7f"}, {128, "8000"}, {255, "ff00"}, {256, "0001"}} {
		got := EncodeTBC20UnsignedLE(tc.value)
		if hex.EncodeToString(got) != tc.hex {
			t.Fatalf("encode(%d)=%x want %s", tc.value, got, tc.hex)
		}
	}
}

func TestTBC20PartialScriptDataUses64ByteBoundary(t *testing.T) {
	script := bscript.NewFromBytes(bytes.Repeat([]byte{bscript.OpNOP}, 130))
	partial, err := GetTBC20PartialScriptData(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.PartialHash) != 32 || len(partial.SuffixData) != 2 || !bytes.Equal(partial.Size, []byte{0x82, 0}) {
		t.Fatalf("partial = %+v", partial)
	}
}

func TestBuildTBC20UnlockScriptMatchesJS166ByteForByte(t *testing.T) {
	data, err := os.ReadFile("../contract/testdata/js-1.6.6/tbc20-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Transaction struct {
			SourceRaw            string `json:"sourceRaw"`
			MintRaw              string `json:"mintRaw"`
			TransferRaw          string `json:"transferRaw"`
			TransferUnlockHex    string `json:"transferUnlockHex"`
			TransferUnlockChunks int    `json:"transferUnlockChunks"`
			SignatureHex         string `json:"transferSignatureHex"`
			PublicKeyHex         string `json:"publicKeyHex"`
		} `json:"transactionFixture"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	source, err := bt.NewTxFromString(fixture.Transaction.SourceRaw)
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(fixture.Transaction.MintRaw)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := bt.NewTxFromString(fixture.Transaction.TransferRaw)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := hex.DecodeString(fixture.Transaction.SignatureHex)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := hex.DecodeString(fixture.Transaction.PublicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	tape1, tape3 := 1, 3
	got, err := BuildTBC20UnlockScriptWithSignature(TBC20UnlockOptions{
		CurrentTx:    transfer,
		InputIndex:   0,
		PreTx:        mint,
		PreTxVout:    0,
		OutputGroups: []TBC20OutputGroup{{CodeVout: 0, TapeVout: &tape1}, {CodeVout: 2, TapeVout: &tape3}, {CodeVout: 4}},
		AncestorTransactions: tbc20FixtureResolver{
			source.TxID(): source,
		},
		Signature: signature,
		PublicKey: publicKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != fixture.Transaction.TransferUnlockHex {
		t.Fatalf("Go TBC20 unlock differs from JS 1.6.6\ngot:  %s\nwant: %s", got.String(), fixture.Transaction.TransferUnlockHex)
	}
	if len(got.Chunks()) != fixture.Transaction.TransferUnlockChunks {
		t.Fatalf("unlock chunk count = %d, want %d", len(got.Chunks()), fixture.Transaction.TransferUnlockChunks)
	}
}
