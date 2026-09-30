package contract

import (
	"bytes"
	"encoding/json"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"os"
	"strconv"
	"testing"
)

func ammFixture(t *testing.T) map[string]interface{} {
	t.Helper()
	b, e := os.ReadFile("testdata/js-1.7.2/amm.json")
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]interface{}
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestTBCAMMPinnedJS172CodeAndTape(t *testing.T) {
	for _, item := range ammFixture(t)["vectors"].([]interface{}) {
		v := item.(map[string]interface{})
		script, _ := bscript.NewFromHexString(v["code"].(string))
		code, e := ParseTBCAMMCode(script)
		if e != nil {
			t.Fatal(e)
		}
		if len(code.Controllers) != int(v["count"].(float64)) {
			t.Fatal("controller count")
		}
		rebuilt, e := code.Script()
		if e != nil || !bytes.Equal(rebuilt.Bytes(), script.Bytes()) {
			t.Fatal("Code roundtrip", e)
		}
		tape, _ := bscript.NewFromHexString(v["tape"].(string))
		parsed, e := ParseTBCAMMTape(tape)
		if e != nil {
			t.Fatal(e)
		}
		rebuilt, e = parsed.Script()
		if e != nil || !bytes.Equal(rebuilt.Bytes(), tape.Bytes()) {
			t.Fatal("Tape roundtrip", e)
		}
		bad := append([]byte(nil), script.Bytes()...)
		bad[0] ^= 1
		if _, e = ParseTBCAMMCode(bscript.NewFromBytes(bad)); e == nil {
			t.Fatal("accepted modified artifact")
		}
	}
}
func TestTBCAMMQuotesMatchJS172(t *testing.T) {
	number := func(v interface{}) uint64 {
		n, e := strconv.ParseUint(v.(string), 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		return n
	}
	state := func(v interface{}) TBCAMMMathState {
		s := v.(map[string]interface{})
		return TBCAMMMathState{number(s["ftLpAmount"]), number(s["ftAAmount"]), number(s["tbcAmount"]), number(s["poolValue"])}
	}
	for _, item := range ammFixture(t)["quotes"].([]interface{}) {
		v := item.(map[string]interface{})
		s := state(v["state"])
		amount := number(v["amount"])
		minimum := uint64(0)
		if m, ok := v["minimum"]; ok {
			minimum = number(m)
		}
		plan := uint8(v["plan"].(float64))
		var q *TBCAMMQuote
		var e error
		switch v["operation"] {
		case "addTBC":
			var first *uint64
			if s.LP == 0 {
				n := uint64(777)
				first = &n
			}
			q, e = s.Add(amount, true, first)
		case "addFT":
			q, e = s.Add(amount, false, nil)
		case "remove":
			q, e = s.Remove(amount)
		case "swapFT":
			q, e = s.SwapFT(amount, plan, minimum)
		case "swapTBC":
			q, e = s.SwapTBC(amount, plan, minimum)
		}
		if _, ok := v["error"]; ok {
			if e == nil {
				t.Fatalf("expected rejection: %v", v)
			}
			continue
		}
		if e != nil {
			t.Fatal(e, v)
		}
		want := v["result"].(map[string]interface{})
		if q.Next != state(want["nextState"]) {
			t.Fatalf("rounding mismatch: %+v %v", q.Next, want)
		}
		if q.Fees != nil {
			f := want["fees"].(map[string]interface{})
			if q.Fees.Total != number(f["totalFeeSat"]) || q.Fees.ServicePaid != number(f["serviceFeePaidSat"]) {
				t.Fatal("fee mismatch")
			}
		}
	}
}
