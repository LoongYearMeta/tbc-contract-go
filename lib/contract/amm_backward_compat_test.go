package contract

import (
	"bytes"
	"encoding/json"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"os"
	"testing"
)

func TestHistoricalAMM172Roundtrip(t *testing.T) {
	raw, e := os.ReadFile("testdata/js-1.7.2/amm.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Vectors []struct{ Code, Tape string } }
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	for _, v := range f.Vectors {
		code, _ := bscript.NewFromHexString(v.Code)
		c, e := ParseTBCAMMCode(code)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := c.Script()
		if e != nil || c.Version != TBCAMMV172 || !bytes.Equal(encoded.Bytes(), code.Bytes()) {
			t.Fatal("old Code changed", e)
		}
		tape, _ := bscript.NewFromHexString(v.Tape)
		tt, e := ParseTBCAMMTape(tape)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e = tt.Script()
		if e != nil || tt.Version() != TBCAMMV172 || !bytes.Equal(encoded.Bytes(), tape.Bytes()) {
			t.Fatal("old Tape changed", e)
		}
		state, e := tt.State(tt.TBC + 1500)
		if e != nil {
			t.Fatal(e)
		}
		state.FT++
		next, e := tt.WithState(state)
		if e != nil || len(next.Bytes()) != 143 {
			t.Fatal("state migrated", e)
		}
		for _, mutation := range [][2]int{{128, 0}, {130, 0}, {130, 7}, {133, 2}, {134, 9}} {
			bad := append([]byte{}, tape.Bytes()...)
			bad[mutation[0]] = byte(mutation[1])
			if _, e = ParseTBCAMMTape(bscript.NewFromBytes(bad)); e == nil {
				t.Fatal("accepted invalid legacy Tape")
			}
		}
		bad := append([]byte{}, code.Bytes()...)
		bad[100] ^= 1
		if _, e = ParseTBCAMMCode(bscript.NewFromBytes(bad)); e == nil {
			t.Fatal("accepted modified Code")
		}
	}
}
func TestLegacyAMMPlansRestoreAndRejectMixedVersions(t *testing.T) {
	raw, e := os.ReadFile("testdata/js-1.7.2/tbc20-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ TransactionFixture struct{ MintRaw string } }
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	genesis, e := bt.NewTxFromString(f.TransactionFixture.MintRaw)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	address, _ := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	script, _ := bscript.NewP2PKHFromAddress(address.AddressString)
	fee := &bt.UTXO{TxID: bytes.Repeat([]byte{0x12}, 32), Vout: 0, Satoshis: 100000, LockingScript: script}
	for plan := uint8(1); plan <= 6; plan++ {
		for _, controlled := range []bool{false, true} {
			var controllers [][20]byte
			if controlled {
				for i := byte(0x22); i <= 0x66; i += 0x11 {
					var h [20]byte
					copy(h[:], bytes.Repeat([]byte{i}, 20))
					controllers = append(controllers, h)
				}
			}
			a, e := NewTBCAMM(genesis, plan, controllers, controlled)
			if e != nil {
				t.Fatal(e)
			}
			txs, e := a.Mint(key, fee)
			if e != nil {
				t.Fatal(e)
			}
			pool := txs[len(txs)-1]
			restored, e := TBCAMMFromPool(pool, genesis, "")
			if e != nil {
				t.Fatal(e)
			}
			code, tape, _, e := restored.ReadState(pool)
			if e != nil || code.Version != TBCAMMV172 || tape.LegacyPlan != plan || len(pool.Outputs[1].LockingScript.Bytes()) != 143 {
				t.Fatal("legacy config lost", e)
			}
			again, e := restored.Mint(key, fee)
			if e != nil {
				t.Fatal(e)
			}
			for i, out := range pool.Outputs {
				other := again[len(again)-1].Outputs[i]
				if out.Satoshis != other.Satoshis || !bytes.Equal(out.LockingScript.Bytes(), other.LockingScript.Bytes()) {
					t.Fatal("restored outputs changed")
				}
			}
			mixed, _ := bt.NewTxFromString(pool.String())
			modern := *tape
			modern.LegacyPlan = 0
			mixed.Outputs[1].LockingScript, _ = modern.Script()
			if _, e = TBCAMMFromPool(mixed, genesis, ""); e == nil {
				t.Fatal("accepted mixed Code/Tape versions")
			}
			mixed, _ = bt.NewTxFromString(pool.String())
			modernCode := *code
			modernCode.Version = TBCAMMV173
			mixed.Outputs[0].LockingScript, _ = modernCode.Script()
			if _, e = TBCAMMFromPool(mixed, genesis, ""); e == nil {
				t.Fatal("accepted mixed Code/Tape versions")
			}
			if _, e = TBCAMMFromPool(pool, genesis, address.AddressString); e == nil {
				t.Fatal("accepted wrong fee recipient")
			}
		}
	}
}
