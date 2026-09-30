package contract

import (
	"encoding/hex"
	"encoding/json"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"os"
	"testing"
)

func TestLOPPinnedTemplates(t *testing.T) {
	type token struct{ ID, Code, Tape string }
	var f struct {
		Vectors []struct {
			Owner      string `json:"owner"`
			TaxAddress string `json:"tax_address"`
			Side, Code string
			TokenA     token  `json:"token_a"`
			TokenB     *token `json:"token_b"`
		}
	}
	raw, e := os.ReadFile("testdata/js-1.7.2/lop.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	parse := func(v token) LOPToken {
		code, e := bscript.NewFromHexString(v.Code)
		if e != nil {
			t.Fatal(e)
		}
		tape, e := bscript.NewFromHexString(v.Tape)
		if e != nil {
			t.Fatal(e)
		}
		a, e := ParseTokenAsset(code, tape)
		if e != nil {
			t.Fatal(e)
		}
		id, e := hex.DecodeString(v.ID)
		if e != nil {
			t.Fatal(e)
		}
		x := LOPToken{Asset: a}
		copy(x.ID[:], id)
		return x
	}
	for _, v := range f.Vectors {
		o := TBCLOP{Owner: v.Owner, TaxAddress: v.TaxAddress, Volume: 400, Price: 1000000, FeeRate: 10000, TokenA: parse(v.TokenA)}
		if v.Side == "sell" {
			o.Side = LOPSell
		}
		if v.TokenB != nil {
			b := parse(*v.TokenB)
			o.TokenB = &b
		}
		code, e := o.Script()
		if e != nil {
			t.Fatal(e)
		}
		if code.String() != v.Code {
			t.Fatal("JS template differs")
		}
		o.FeeRate = 1000000
		if _, e = o.Script(); e == nil {
			t.Fatal("accepted confiscatory rate")
		}
		o.FeeRate = 10000
		o.Price = 0
		if _, e = o.Script(); e == nil {
			t.Fatal("accepted zero price")
		}
	}
}
