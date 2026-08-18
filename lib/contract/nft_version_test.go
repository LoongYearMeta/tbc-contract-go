package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestNFTVersionAndPublishedCodeMatchJS166(t *testing.T) {
	const txid = "1111111111111111111111111111111111111111111111111111111111111111"
	fixtures := loadJS166Fixtures(t)
	tests := []struct {
		name    string
		version NFTVersion
		length  int
		build   func() (*bscript.Script, error)
	}{
		{name: "nftV0", version: NFTVersion0, length: 142, build: func() (*bscript.Script, error) {
			return BuildCodeScriptV0(txid, 0)
		}},
		{name: "nftV1Code", version: NFTVersion1, length: 125, build: func() (*bscript.Script, error) {
			return BuildCodeScriptV1(txid, 0)
		}},
		{name: "nftV2Code", version: NFTVersion2, length: 140, build: func() (*bscript.Script, error) {
			return BuildCodeScript(txid, 0)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			if code.Len() != test.length || GetNFTVersion(code) != test.version {
				t.Fatalf("length/version = %d/%d", code.Len(), GetNFTVersion(code))
			}
			if fixture, ok := fixtures[test.name]; ok {
				hash := sha256.Sum256(code.Bytes())
				if hex.EncodeToString(hash[:]) != fixture.SHA256 {
					t.Fatalf("hash differs from JavaScript 1.6.6")
				}
			}
		})
	}
}

func TestNFTVersionRejectsMalformedLengthMarkerPairs(t *testing.T) {
	for _, raw := range [][]byte{
		make([]byte, 125),
		append(make([]byte, 135), []byte{5, '3', 'C', 'o', 'd', 'e'}...),
		append(make([]byte, 137), []byte{5, '3', 'C', 'o', 'd', 'e'}...),
	} {
		if got := GetNFTVersion(bscript.NewFromBytes(raw)); got != NFTVersionUnknown {
			t.Fatalf("malformed code classified as %d", got)
		}
	}
}
