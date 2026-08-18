package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type tbc20TransactionFixture struct {
	PrivateKeyHex       string   `json:"privateKeyHex"`
	Recipient           string   `json:"recipient"`
	FundingScriptHex    string   `json:"fundingScriptHex"`
	SourceRaw           string   `json:"sourceRaw"`
	SourceFeeSatoshis   uint64   `json:"sourceFeeSatoshis"`
	MintRaw             string   `json:"mintRaw"`
	MintFeeSatoshis     uint64   `json:"mintFeeSatoshis"`
	TransferRaw         string   `json:"transferRaw"`
	TransferFeeSatoshis uint64   `json:"transferFeeSatoshis"`
	TransferTokenAmount []string `json:"transferTokenAmounts"`
	SplitRaw            string   `json:"splitRaw"`
	SplitFeeSatoshis    uint64   `json:"splitFeeSatoshis"`
	MergeRaw            string   `json:"mergeRaw"`
	MergeFeeSatoshis    uint64   `json:"mergeFeeSatoshis"`
	MergeTokenAmounts   []string `json:"mergeTokenAmounts"`
}

type tbc20AncestorMap map[string]*bt.Tx

func (resolver tbc20AncestorMap) ResolveTBC20Ancestor(txid string) (*bt.Tx, bool) {
	tx, ok := resolver[txid]
	return tx, ok
}

func readTBC20TransactionFixture(t *testing.T) tbc20TransactionFixture {
	t.Helper()
	body, err := os.ReadFile("testdata/js-1.6.6/tbc20-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Transaction tbc20TransactionFixture `json:"transactionFixture"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return document.Transaction
}

func tbc20FixtureKey(t *testing.T, value string) *bec.PrivateKey {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := bec.PrivKeyFromBytes(bec.S256(), raw)
	return key
}

func tbc20FixtureUTXO(t *testing.T, txid string, vout uint32, scriptHex string, satoshis uint64) *bt.UTXO {
	t.Helper()
	txidBytes, err := hex.DecodeString(txid)
	if err != nil {
		t.Fatal(err)
	}
	script, err := bscript.NewFromHexString(scriptHex)
	if err != nil {
		t.Fatal(err)
	}
	return &bt.UTXO{TxID: txidBytes, Vout: vout, LockingScript: script, Satoshis: satoshis}
}

func TestTBC20MintAndTransferMatchJS166Transactions(t *testing.T) {
	fixture := readTBC20TransactionFixture(t)
	key := tbc20FixtureKey(t, fixture.PrivateKeyHex)
	token, err := NewTBC20(TBC20Config{Definition: &TBC20Definition{
		Name: "Parity Token", Symbol: "PTY", Supply: "1000.00000000", Decimal: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	funding := tbc20FixtureUTXO(t, string(bytes.Repeat([]byte{'a'}, 64)), 0, fixture.FundingScriptHex, 300000)
	owner, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	mint, err := token.Mint(key, owner.AddressString, funding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mint.SourceTxRaw != fixture.SourceRaw || mint.TxRaw != fixture.MintRaw {
		t.Fatalf("Go TBC20 mint transactions differ from JS 1.6.6")
	}
	if mint.SourceFeeSatoshis != fixture.SourceFeeSatoshis || mint.FeeSatoshis != fixture.MintFeeSatoshis {
		t.Fatalf("mint fees = %d/%d, want %d/%d", mint.SourceFeeSatoshis, mint.FeeSatoshis, fixture.SourceFeeSatoshis, fixture.MintFeeSatoshis)
	}
	tokenUTXO, tokenBalance, err := BuildTBC20UTXO(mint.Transaction, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tokenBalance.Cmp(mustBigInt(t, "100000000000")) != 0 {
		t.Fatalf("mint balance = %s", tokenBalance)
	}
	feeUTXO := tbc20FixtureUTXO(t, string(bytes.Repeat([]byte{'b'}, 64)), 1, fixture.FundingScriptHex, 100000)
	transfer, err := token.Transfer(key, fixture.Recipient, "123.45600000", []*bt.UTXO{tokenUTXO}, feeUTXO, []*bt.Tx{mint.Transaction}, []TBC20AncestorResolver{tbc20AncestorMap{mint.SourceTransaction.TxID(): mint.SourceTransaction}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transfer.TxRaw != fixture.TransferRaw {
		t.Fatalf("Go TBC20 transfer transaction differs from JS 1.6.6")
	}
	if transfer.FeeSatoshis != fixture.TransferFeeSatoshis {
		t.Fatalf("transfer fee = %d, want %d", transfer.FeeSatoshis, fixture.TransferFeeSatoshis)
	}
	for index, want := range fixture.TransferTokenAmount {
		if index >= len(transfer.TokenOutputs) || transfer.TokenOutputs[index].Amount.Cmp(mustBigInt(t, want)) != 0 {
			t.Fatalf("token output %d differs", index)
		}
	}

	splitFeeUTXO := tbc20FixtureUTXO(t, string(bytes.Repeat([]byte{'b'}, 64)), 2, fixture.FundingScriptHex, 100000)
	split, err := token.Transfer(key, owner.AddressString, "400.00000000", []*bt.UTXO{tokenUTXO}, splitFeeUTXO, []*bt.Tx{mint.Transaction}, []TBC20AncestorResolver{tbc20AncestorMap{mint.SourceTransaction.TxID(): mint.SourceTransaction}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if split.TxRaw != fixture.SplitRaw || split.FeeSatoshis != fixture.SplitFeeSatoshis {
		t.Fatal("Go TBC20 split transaction differs from JS 1.6.6")
	}
	splitTxID, err := hex.DecodeString(split.Transaction.TxID())
	if err != nil {
		t.Fatal(err)
	}
	mergeInputs := []*bt.UTXO{
		{TxID: splitTxID, Vout: 0, LockingScript: split.Transaction.Outputs[0].LockingScript, Satoshis: split.Transaction.Outputs[0].Satoshis},
		{TxID: splitTxID, Vout: 2, LockingScript: split.Transaction.Outputs[2].LockingScript, Satoshis: split.Transaction.Outputs[2].Satoshis},
	}
	mergeFeeUTXO := tbc20FixtureUTXO(t, string(bytes.Repeat([]byte{'c'}, 64)), 0, fixture.FundingScriptHex, 100000)
	resolver := tbc20AncestorMap{mint.Transaction.TxID(): mint.Transaction}
	merge, err := token.Merge(key, mergeInputs, mergeFeeUTXO, []*bt.Tx{split.Transaction, split.Transaction}, []TBC20AncestorResolver{resolver, resolver}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merge.TxRaw != fixture.MergeRaw || merge.FeeSatoshis != fixture.MergeFeeSatoshis {
		t.Fatal("Go TBC20 merge transaction differs from JS 1.6.6")
	}
	if len(merge.TokenOutputs) != 1 || merge.TokenOutputs[0].Amount.Cmp(mustBigInt(t, fixture.MergeTokenAmounts[0])) != 0 {
		t.Fatal("Go TBC20 merge amount differs from JS 1.6.6")
	}
}

func TestTBC20TransferBoundaries(t *testing.T) {
	if _, err := TBC20HumanToRaw("0.000000001", 8); err == nil {
		t.Fatal("expected excess decimal precision rejection")
	}
	tooLarge := new(big.Int).Add(new(big.Int).SetUint64(TBC20MaxSlotAmount), big.NewInt(1))
	if _, err := TBC20RawToHuman(tooLarge, 8); err == nil {
		t.Fatal("expected signed-63-bit slot boundary rejection")
	}
}

func mustBigInt(t *testing.T, value string) *big.Int {
	t.Helper()
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("invalid fixture integer %q", value)
	}
	return result
}
