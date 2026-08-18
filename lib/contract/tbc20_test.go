package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type tbc20Vector struct {
	ArtifactSHA256  string   `json:"artifactSha256"`
	CodeBytes       int      `json:"codeBytes"`
	PartialOffset   int      `json:"partialOffset"`
	MinTapeBytes    int      `json:"minTapeBytes"`
	MaxTapeBytes    int      `json:"maxTapeBytes"`
	MaxSlotAmount   string   `json:"maxSlotAmount"`
	OriginalUTXOHex string   `json:"originalUTXOHex"`
	ControllerHex   string   `json:"controllerHex"`
	CodeHex         string   `json:"codeHex"`
	CodeIdentityHex string   `json:"codeIdentityHex"`
	TapeHex         string   `json:"tapeHex"`
	TapeAmounts     []string `json:"tapeAmounts"`
	TapeBalance     string   `json:"tapeBalance"`
}

func loadTBC20Vector(t *testing.T) tbc20Vector {
	t.Helper()
	raw, err := os.ReadFile("testdata/js-1.6.6/tbc20-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector tbc20Vector
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}

func TestTBC20CodeAndTapeMatchJS166(t *testing.T) {
	vector := loadTBC20Vector(t)
	if TBC20CodeBytes != vector.CodeBytes || TBC20CodePartialOffset != vector.PartialOffset || TBC20MinTapeBytes != vector.MinTapeBytes || TBC20MaxTapeBytes != vector.MaxTapeBytes {
		t.Fatalf("constants differ from JS vector")
	}

	var amounts [TBC20AmountSlots]uint64
	amounts[0], amounts[1] = 123456, 7
	tape, err := BuildTBC20Tape(amounts, TBC20MinTapeBytes, []byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if tape.ToHex() != vector.TapeHex {
		t.Fatalf("tape differs from JS 1.6.6")
	}
	parsed, err := ParseTBC20Tape(tape)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Balance.Cmp(big.NewInt(123463)) != 0 || parsed.Size != TBC20MinTapeBytes || len(parsed.ExtensionData) != 1 || parsed.ExtensionData[0] != 9 {
		t.Fatalf("parsed tape = %+v", parsed)
	}

	controllerRaw, err := hex.DecodeString(vector.ControllerHex)
	if err != nil {
		t.Fatal(err)
	}
	var controller [21]byte
	copy(controller[:], controllerRaw)
	code, err := InstantiateTBC20Code(TBC20Outpoint{TxID: strings.Repeat("11", 32), OutputIndex: 3}, controller, TBC20MinTapeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if code.ToHex() != vector.CodeHex {
		t.Fatalf("code differs from JS 1.6.6")
	}
	identity, err := TBC20CodeIdentity(code)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(identity) != vector.CodeIdentityHex {
		t.Fatalf("identity = %x", identity)
	}
	gotController, err := TBC20Controller(code)
	if err != nil || gotController != controller {
		t.Fatalf("controller = %x, err=%v", gotController, err)
	}
}

func TestTBC20ControllerReplacementPreservesIdentity(t *testing.T) {
	vector := loadTBC20Vector(t)
	code, err := bscript.NewFromHexString(vector.CodeHex)
	if err != nil {
		t.Fatal(err)
	}
	before, err := TBC20CodeIdentity(code)
	if err != nil {
		t.Fatal(err)
	}
	var replacement [21]byte
	copy(replacement[:20], bytes.Repeat([]byte{0x42}, 20))
	updated, err := ReplaceTBC20Controller(code, replacement)
	if err != nil {
		t.Fatal(err)
	}
	after, err := TBC20CodeIdentity(updated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("controller replacement changed identity")
	}
}

func TestTBC20HumanAmountConversions(t *testing.T) {
	raw, err := TBC20HumanToRaw("123.4500", 4)
	if err != nil || raw.Cmp(big.NewInt(1_234_500)) != 0 {
		t.Fatalf("raw=%v err=%v", raw, err)
	}
	human, err := TBC20RawToHuman(raw, 4)
	if err != nil || human != "123.45" {
		t.Fatalf("human=%q err=%v", human, err)
	}
	if _, err := TBC20HumanToRaw("1e3", 0); err == nil {
		t.Fatal("accepted exponent")
	}
}
