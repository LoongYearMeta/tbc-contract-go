package validator

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestPublishedV4ArtifactsMatchJS166Registry(t *testing.T) {
	for _, test := range []struct {
		file    string
		family  string
		version uint8
	}{
		{"../util/testdata/js-1.6.6/ft-v4.hex", "FT", 4},
		{"../util/testdata/js-1.6.6/coin-v4.hex", "STABLE_COIN", 4},
	} {
		raw, err := os.ReadFile(test.file)
		if err != nil {
			t.Fatal(err)
		}
		code, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		decoded := decodePublishedFTCode(code)
		if decoded == nil || decoded.artifact.version != test.version || (decoded.artifact.coin && test.family != "STABLE_COIN") || (!decoded.artifact.coin && test.family != "FT") {
			t.Fatalf("%s did not match published registry", test.file)
		}
		code[100] ^= 1
		if decodePublishedFTCode(code) != nil {
			t.Fatalf("%s accepted immutable artifact mutation", test.file)
		}
	}
}
