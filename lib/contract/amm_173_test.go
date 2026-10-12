package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"os"
	"strconv"
	"testing"
)

func TestAMM173ExplicitFeesAndTags(t *testing.T) {
	raw, e := os.ReadFile("testdata/js-1.7.3/amm.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		FeeVectors []struct {
			Rates  []uint16
			Base   string
			Result map[string]string
			Error  string
		}
		Vectors []struct{ Code, Tape, Tag string }
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, v := range f.FeeVectors {
		base, _ := strconv.ParseUint(v.Base, 10, 64)
		r := v.Rates
		got, e := CalculateTBCAMMFeesWithRates(base, TBCAMMFeeRates{r[0], r[1], r[2], r[3]})
		if v.Error != "" {
			if e == nil {
				t.Fatalf("accepted invalid fees: %+v", v)
			}
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		for name, n := range map[string]uint64{"totalFeeSat": got.Total, "lpFeeSat": got.LP, "serviceFeeAccruedSat": got.ServiceAccrued, "serviceFeePaidSat": got.ServicePaid, "netAmountSat": got.Net} {
			if strconv.FormatUint(n, 10) != v.Result[name] {
				t.Fatalf("%s differs: %+v", name, v)
			}
		}
	}
	for _, v := range f.Vectors {
		script, _ := bscript.NewFromHexString(v.Code)
		c, e := ParseTBCAMMCode(script)
		if e != nil || hex.EncodeToString(c.ServiceProviderTag) != v.Tag {
			t.Fatalf("tag differs: %v", e)
		}
		bad := append(script.Bytes(), 0)
		if _, e = ParseTBCAMMCode(bscript.NewFromBytes(bad)); e == nil {
			t.Fatal("accepted trailing opcode")
		}
		b, _ := hex.DecodeString(v.Tape)
		b[159] = 9
		tape, e := ParseTBCAMMTape(bscript.NewFromBytes(b))
		if e != nil {
			t.Fatal(e)
		}
		encoded, _ := tape.Script()
		if encoded.Bytes()[159] != 8 {
			t.Fatal("marker was not normalized")
		}
		tape.Fees = TBCAMMFeeRates{65535, 65535, 65535, 65535}
		encoded, e = tape.Script()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = ParseTBCAMMTape(encoded); e != nil {
			t.Fatal(e)
		}
		if _, e = tape.Fees.Total(); e == nil {
			t.Fatal("invalid operational fees accepted")
		}
	}
}

func TestAMM173RestoredFeeBinding(t *testing.T) {
	raw, e := os.ReadFile("testdata/js-1.7.2/tbc20-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]interface{}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	genesis, e := bt.NewTxFromString(f["transactionFixture"].(map[string]interface{})["mintRaw"].(string))
	if e != nil {
		t.Fatal(e)
	}
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	address, _ := privKeyToAddress(key)
	script, _ := bscript.NewP2PKHFromAddress(address)
	rates := TBCAMMFeeRates{17, 23, 31, 43}
	a, e := NewTBCAMMWithFees(genesis, rates, address, nil, false, []byte{0, 1, 2})
	if e != nil {
		t.Fatal(e)
	}
	txs, e := a.Mint(key, &bt.UTXO{TxID: bytes.Repeat([]byte{0x12}, 32), Vout: 0, Satoshis: 100000, LockingScript: script})
	if e != nil {
		t.Fatal(e)
	}
	pool := txs[1]
	recovered, e := TBCAMMFromPool(pool, genesis, "")
	if e != nil {
		t.Fatal(e)
	}
	_, tape, _, e := recovered.ReadState(pool)
	if e != nil || tape.Fees != rates {
		t.Fatal(e)
	}
	if _, e = TBCAMMFromPool(pool, genesis, "1N7rf2AuAHB2aCrVgnbQhSWhaUVk3rGhjm"); e == nil {
		t.Fatal("wrong fee recipient accepted")
	}
	bad, _ := bt.NewTxFromString(pool.String())
	tape.FeeAddress[0] ^= 1
	bad.Outputs[1].LockingScript, _ = tape.Script()
	if _, _, _, e = a.ReadState(bad); e == nil {
		t.Fatal("modified fee hash accepted")
	}
	code, _ := ParseTBCAMMCode(pool.Outputs[0].LockingScript)
	code.ServiceProviderTag = append(code.ServiceProviderTag, 9)
	bad, _ = bt.NewTxFromString(pool.String())
	bad.Outputs[0].LockingScript, _ = code.Script()
	if _, _, _, e = a.ReadState(bad); e == nil {
		t.Fatal("modified tag accepted")
	}
}

func TestAMM173FeeRecipientMustBeP2PKH(t *testing.T) {
	for _, address := range []string{"3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy", "1N7rf2AuAHB2aCrVgnbQhSWhaUVk3rGhjn"} {
		if _, e := ammFeeScript(address); e == nil {
			t.Fatalf("invalid recipient accepted: %s", address)
		}
	}
	if _, e := ammFeeScript("1N7rf2AuAHB2aCrVgnbQhSWhaUVk3rGhjm"); e != nil {
		t.Fatal(e)
	}
}
