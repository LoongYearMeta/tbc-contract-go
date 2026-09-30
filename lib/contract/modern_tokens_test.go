package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"os"
	"strconv"
	"testing"
)

func TestModernTokenPinnedJS172Vectors(t *testing.T) {
	b, e := os.ReadFile("testdata/js-1.7.2/modern-tokens.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Vectors []struct {
			Kind                 string
			Size                 int
			LockTime             uint32
			Amounts              []string
			Code, Tape, Identity string
		}
	}
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	for _, v := range data.Vectors {
		t.Run(v.Kind+strconv.Itoa(v.Size), func(t *testing.T) {
			cs, e := bscript.NewFromHexString(v.Code)
			if e != nil {
				t.Fatal(e)
			}
			ts, e := bscript.NewFromHexString(v.Tape)
			if e != nil {
				t.Fatal(e)
			}
			c, e := ParseModernTokenCode(cs)
			if e != nil {
				t.Fatal(e)
			}
			rebuilt, e := c.Script()
			if e != nil || !bytes.Equal(rebuilt.Bytes(), cs.Bytes()) {
				t.Fatal("Code roundtrip", e)
			}
			id, e := c.Identity()
			if e != nil || hex.EncodeToString(id) != v.Identity {
				t.Fatal("JS identity mismatch", e)
			}
			tape, e := ParseModernTokenTape(ts, c)
			if e != nil {
				t.Fatal(e)
			}
			if tape.LockTime != v.LockTime {
				t.Fatal("lock mismatch")
			}
			for i, a := range tape.Amounts {
				if strconv.FormatUint(a, 10) != v.Amounts[i] {
					t.Fatal("amount mismatch")
				}
			}
			rebuilt, e = tape.Script(c.Kind)
			if e != nil || !bytes.Equal(rebuilt.Bytes(), ts.Bytes()) {
				t.Fatal("Tape roundtrip", e)
			}
			bad := append([]byte(nil), cs.Bytes()...)
			bad[0] ^= 1
			if _, e = ParseModernTokenCode(bscript.NewFromBytes(bad)); e == nil {
				t.Fatal("accepted modified template")
			}
			bad = append([]byte(nil), ts.Bytes()...)
			bad[10] = 0x80
			if _, e = ParseModernTokenTape(bscript.NewFromBytes(bad), c); e == nil {
				t.Fatal("accepted amount overflow")
			}
		})
	}
}
func TestModernTokenRejectsMixedLocksAndMalformedMetadata(t *testing.T) {
	if _, e := RequiredModernLockTime([]uint32{42, 1700000000}); e == nil {
		t.Fatal("mixed lock domains")
	}
	tape := ModernTokenTape{TapeSize: 68, Metadata: []byte{2, 1}}
	if _, e := tape.Script(ModernStablecoin); e == nil {
		t.Fatal("truncated metadata")
	}
	tape.Metadata = []byte{1, 1}
	if _, e := tape.Script(ModernTimelockedLP); e == nil {
		t.Fatal("nonzero LP padding")
	}
}
