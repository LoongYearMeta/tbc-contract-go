package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"math/big"
	"os"
	"testing"
)

type completionFixture struct {
	Address string
	Funding string
	NFT     struct {
		Collection string
		Data       []*NFTData
		Expected   []string
	}
	Stable struct {
		Certificate, Mint string
		RecipientsRaw     []string
		Batch, Merge      []string
		MergeInputs       []struct {
			Parent    string
			Vout      int
			Ancestors []string
		}
		Info, InfoTx, PaymentTx string
	}
}

func loadCompletion(t *testing.T) completionFixture {
	t.Helper()
	b, e := os.ReadFile("testdata/js-1.7.2/completion.json")
	if e != nil {
		t.Fatal(e)
	}
	var f completionFixture
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func completionTx(t *testing.T, s string) *bt.Tx {
	t.Helper()
	v, e := bt.NewTxFromString(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func completionUTXO(t *testing.T, tx *bt.Tx, v int) *bt.UTXO {
	t.Helper()
	u, e := util.BuildUTXO(tx, v, false)
	if e != nil {
		t.Fatal(e)
	}
	return util.FtUTXOToUTXO(u)
}
func completionCompare(t *testing.T, a, b *bt.Tx) {
	t.Helper()
	if len(a.Outputs) != len(b.Outputs) || len(a.Inputs) != len(b.Inputs) || a.LockTime != b.LockTime {
		t.Fatal("layout/lock mismatch")
	}
	for i := 0; i < len(a.Outputs)-1; i++ {
		if a.Outputs[i].Satoshis != b.Outputs[i].Satoshis || !bytes.Equal(a.Outputs[i].LockingScript.Bytes(), b.Outputs[i].LockingScript.Bytes()) {
			t.Fatalf("contract output %d differs", i)
		}
	}
	for i := range a.Inputs {
		if a.Inputs[i].SequenceNumber != b.Inputs[i].SequenceNumber {
			t.Fatal("sequence mismatch")
		}
	}
}
func TestTBC721BatchMintMatchesJSAndChainsFunding(t *testing.T) {
	f := loadCompletion(t)
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	c := completionTx(t, f.NFT.Collection)
	slots := []*bt.UTXO{}
	for i := 1; i <= 3; i++ {
		slots = append(slots, completionUTXO(t, c, i))
	}
	fund := []*bt.UTXO{completionUTXO(t, c, 4)}
	result, e := (TBC721Standard{}).BatchCreateNFT(c.TxID(), f.Address, key, f.NFT.Data, fund, slots)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 3 {
		t.Fatal("batch count")
	}
	for i, raw := range result {
		a := completionTx(t, raw)
		completionCompare(t, a, completionTx(t, f.NFT.Expected[i]))
		if i > 0 && a.Inputs[1].PreviousTxIDStr() != completionTx(t, result[i-1]).TxID() {
			t.Fatal("funding chain")
		}
	}
	if _, e = (TBC721Standard{}).BatchCreateNFT(c.TxID(), f.Address, key, f.NFT.Data, fund, []*bt.UTXO{slots[0], slots[0], slots[2]}); e == nil {
		t.Fatal("duplicate slot accepted")
	}
}
func TestStablecoinBatchMergeAndExtrasMatchJS(t *testing.T) {
	f := loadCompletion(t)
	s := f.Stable
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	mint := completionTx(t, s.Mint)
	cert := completionTx(t, s.Certificate)
	spend := ModernTokenSpend{Parent: mint, CodeVout: 3, Ancestors: ModernTokenAncestors{cert.TxID(): cert}}
	fee := completionUTXO(t, mint, 5)
	owner, _ := TBC20StandardAddressController(f.Address)
	recipients := []StablecoinRecipient{}
	for _, raw := range s.RecipientsRaw {
		n, _ := new(big.Int).SetString(raw, 10)
		recipients = append(recipients, StablecoinRecipient{owner, n})
	}
	coin := TBC20Stablecoin{}
	result, e := coin.BatchTransfer(key, key, []ModernTokenSpend{spend}, fee, recipients)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 2 {
		t.Fatal("batch count")
	}
	for i, a := range result {
		completionCompare(t, a, completionTx(t, s.Batch[i]))
	}
	if result[1].Inputs[0].PreviousTxIDStr() != result[0].TxID() || result[1].Inputs[0].PreviousTxOutIndex != 10 {
		t.Fatal("token change chain")
	}
	inputs := []ModernTokenSpend{}
	for _, r := range s.MergeInputs {
		parents := ModernTokenAncestors{}
		for _, raw := range r.Ancestors {
			p := completionTx(t, raw)
			parents[p.TxID()] = p
		}
		inputs = append(inputs, ModernTokenSpend{Parent: completionTx(t, r.Parent), CodeVout: r.Vout, Ancestors: parents})
	}
	result, e = coin.MergeCoin(key, key, inputs, completionUTXO(t, completionTx(t, s.Batch[1]), 6))
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 2 {
		t.Fatal("merge count")
	}
	for i, a := range result {
		completionCompare(t, a, completionTx(t, s.Merge[i]))
	}
	if result[1].Inputs[0].PreviousTxIDStr() != result[0].TxID() {
		t.Fatal("merge ancestry chain")
	}
	data, _ := hex.DecodeString(s.Info)
	a, e := coin.TransferWithExtras(key, key, []ModernTokenSpend{spend}, fee, recipients[:1], &StablecoinTransferExtras{AdditionalInfo: data})
	if e != nil {
		t.Fatal(e)
	}
	completionCompare(t, a, completionTx(t, s.InfoTx))
	a, e = coin.TransferWithExtras(key, key, []ModernTokenSpend{spend}, fee, recipients[:1], &StablecoinTransferExtras{PaymentAddress: f.Address, PaymentSat: 24})
	if e != nil {
		t.Fatal(e)
	}
	completionCompare(t, a, completionTx(t, s.PaymentTx))
	if _, e = coin.MergeCoin(key, key, []ModernTokenSpend{spend, spend}, fee); e == nil {
		t.Fatal("duplicate merge input accepted")
	}
}

func TestModernPreparedAdminExternalSigning(t *testing.T) {
	f := loadCompletion(t)
	key, _ := bec.PrivKeyFromBytes(bec.S256(), bytes.Repeat([]byte{0x11}, 32))
	funding := completionTx(t, f.Funding)
	var pub [32]byte
	copy(pub[:], modernXOnly(key))
	coin := TBC20Stablecoin{Name: "BatchCoin", Symbol: "BC", Decimal: 6}
	prepare := func() *PreparedAdmin {
		p, e := coin.PrepareCreate(pub, key, f.Address, 20000000, completionUTXO(t, funding, 0), funding, "")
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	p := prepare()
	signatures := [][]byte{}
	for _, r := range p.Sighashes() {
		sig, e := modernSign(key, r.Sighash[:], true)
		if e != nil {
			t.Fatal(e)
		}
		signatures = append(signatures, sig[:64])
	}
	signed, e := p.Finalize(signatures)
	if e != nil {
		t.Fatal(e)
	}
	completionCompare(t, signed[1], completionTx(t, f.Stable.Mint))
	if _, e = p.Finalize(signatures); e == nil {
		t.Fatal("double finalization accepted")
	}
	if _, e = prepare().Finalize([][]byte{make([]byte, 64), make([]byte, 64)}); e == nil {
		t.Fatal("invalid signatures accepted")
	}
	if _, e = prepare().Finalize(nil); e == nil {
		t.Fatal("missing signatures accepted")
	}
	mint := signed[1]
	p, e = coin.PrepareSetLockTime(pub, key, []ModernTokenSpend{{Parent: mint, CodeVout: 3, Ancestors: ModernTokenAncestors{signed[0].TxID(): signed[0]}}}, completionUTXO(t, mint, len(mint.Outputs)-1), 2000000000)
	if e != nil {
		t.Fatal(e)
	}
	signatures = nil
	for _, r := range p.Sighashes() {
		sig, e := modernSign(key, r.Sighash[:], true)
		if e != nil {
			t.Fatal(e)
		}
		signatures = append(signatures, sig[:64])
	}
	if _, e = p.Finalize(signatures); e != nil {
		t.Fatal(e)
	}
}
