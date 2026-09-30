package contract

import (
	"bytes"
	"encoding/json"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"os"
	"testing"
)

func TestPreparedSigningCommitsToSnapshot(t *testing.T) {
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	address, _ := privKeyToAddress(key)
	script, _ := bscript.NewP2PKHFromAddress(address)
	fee := &bt.UTXO{TxID: bytes.Repeat([]byte{0x12}, 32), Vout: 0, Satoshis: 100000, LockingScript: script}
	h := TBCHTLC{Sender: address, Receiver: address, Timelock: 1}
	copy(h.Hashlock[:], crypto.Sha256([]byte("secret")))
	p, e := h.PrepareDeploy(key.PubKey(), fee, 10000)
	if e != nil {
		t.Fatal(e)
	}
	before := p.TransactionHex()
	if _, e = p.Finalize(nil); e == nil {
		t.Fatal("signature count")
	}
	wrong, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x22}, 32))
	r := p.SigningRequests()[0]
	s, _ := wrong.Sign(r.Sighash[:])
	if _, e = p.Finalize([][]byte{append(s.Serialise(), 0x41)}); e == nil {
		t.Fatal("wrong key accepted")
	}
	modified := p.SigningRequests()
	modified[0].Sighash[0] ^= 1
	s, _ = key.Sign(modified[0].Sighash[:])
	if _, e = p.Finalize([][]byte{append(s.Serialise(), 0x41)}); e == nil {
		t.Fatal("wrong digest accepted")
	}
	if p.TransactionHex() != before {
		t.Fatal("failed finalize changed snapshot")
	}
	s, _ = key.Sign(r.Sighash[:])
	tx, e := p.Finalize([][]byte{append(s.Serialise(), 0x41)})
	if e != nil {
		t.Fatal(e)
	}
	report, e := ValidateTBCAMMTransaction(tx, []*bt.Output{{Satoshis: fee.Satoshis, LockingScript: script}})
	if e != nil || !report.Success {
		t.Fatal(report, e)
	}
	if _, e = p.Finalize([][]byte{append(s.Serialise(), 0x41)}); e == nil {
		t.Fatal("duplicate finalize accepted")
	}
	if _, e = h.PrepareDeploy(nil, fee, 10000); e == nil {
		t.Fatal("nil key accepted")
	}
}
func TestAMMPreparedMintNoChangeBoundary(t *testing.T) {
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
	a, e := NewTBCAMM(genesis, 6, nil, false)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	address, _ := privKeyToAddress(key)
	script, _ := bscript.NewP2PKHFromAddress(address)
	fee := &bt.UTXO{TxID: bytes.Repeat([]byte{0x12}, 32), Satoshis: 100000, LockingScript: script}
	p, e := a.PrepareMintTbcAmm(key.PubKey(), fee)
	if e != nil {
		t.Fatal(e)
	}
	target := p.FeeSat()
	fee.Satoshis = 2000 + target
	p, e = a.PrepareMintTbcAmm(key.PubKey(), fee)
	if e != nil {
		t.Fatal(e)
	}
	tx, _ := bt.NewTxFromString(p.TransactionHex())
	if len(tx.Outputs) != 4 {
		t.Fatal("expected no change")
	}
	r := p.SigningRequests()[0]
	s, _ := key.Sign(r.Sighash[:])
	tx, e = p.Finalize([][]byte{append(s.Serialise(), 0x41)})
	if e != nil {
		t.Fatal(e)
	}
	report, e := ValidateTBCAMMTransaction(tx, []*bt.Output{{Satoshis: fee.Satoshis, LockingScript: script}})
	if e != nil || !report.Success {
		t.Fatal(report, e)
	}
	fee.Satoshis = 2000
	if _, e = a.PrepareMintTbcAmm(key.PubKey(), fee); e == nil {
		t.Fatal("insufficient fee accepted")
	}
	fee.Satoshis = 2000 + target + 10
	p, e = a.PrepareMintTbcAmm(key.PubKey(), fee)
	if e != nil {
		t.Fatal(e)
	}
	tx, _ = bt.NewTxFromString(p.TransactionHex())
	if len(tx.Outputs) != 5 || tx.Outputs[4].Satoshis != 10 {
		t.Fatal("change boundary")
	}
}

func TestAMMValidatorFailuresAndCallerIsolation(t *testing.T) {
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	address, _ := privKeyToAddress(key)
	script, _ := bscript.NewP2PKHFromAddress(address)
	fee := &bt.UTXO{TxID: bytes.Repeat([]byte{0x12}, 32), Satoshis: 100000, LockingScript: script}
	h := TBCHTLC{Sender: address, Receiver: address, Timelock: 1}
	p, e := h.PrepareDeploy(key.PubKey(), fee, 10000)
	if e != nil {
		t.Fatal(e)
	}
	r := p.SigningRequests()[0]
	sig, _ := key.Sign(r.Sighash[:])
	tx, e := p.Finalize([][]byte{append(sig.Serialise(), 0x41)})
	if e != nil {
		t.Fatal(e)
	}
	tx.Inputs[0].PreviousTxScript = nil
	previous := []*bt.Output{{Satoshis: fee.Satoshis, LockingScript: script}}
	report, e := ValidateTBCAMMTransaction(tx, previous)
	if e != nil || !report.Success {
		t.Fatal(report, e)
	}
	if tx.Inputs[0].PreviousTxScript != nil {
		t.Fatal("validation mutated caller metadata")
	}
	tx.Outputs[0].Satoshis++
	report, e = ValidateTBCAMMTransaction(tx, previous)
	if e != nil || report.Success {
		t.Fatal("accepted modified output", e)
	}
	tx.Inputs = append(tx.Inputs, tx.Inputs[0])
	if _, e = ValidateTBCAMMTransaction(tx, append(previous, previous[0])); e == nil {
		t.Fatal("accepted duplicate outpoint")
	}
}
