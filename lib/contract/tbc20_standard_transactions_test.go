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

type tbc20StandardTransactionFixture struct {
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

type tbc20StandardAncestorMap map[string]*bt.Tx

func (resolver tbc20StandardAncestorMap) ResolveTBC20StandardAncestor(txid string) (*bt.Tx, bool) {
	tx, ok := resolver[txid]
	return tx, ok
}

func readTBC20StandardTransactionFixture(t *testing.T) tbc20StandardTransactionFixture {
	t.Helper()
	body, err := os.ReadFile("testdata/js-1.7.2/tbc20-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Transaction tbc20StandardTransactionFixture `json:"transactionFixture"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	return document.Transaction
}

func tbc20StandardFixtureKey(t *testing.T, value string) *bec.PrivateKey {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := bec.PrivKeyFromBytes(bec.S256(), raw)
	return key
}

func tbc20StandardFixtureUTXO(t *testing.T, txid string, vout uint32, scriptHex string, satoshis uint64) *bt.UTXO {
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

func TestTBC20StandardMintAndTransferMatchJS172Transactions(t *testing.T) {
	fixture := readTBC20StandardTransactionFixture(t)
	key := tbc20StandardFixtureKey(t, fixture.PrivateKeyHex)
	token, err := NewTBC20Standard(TBC20StandardConfig{Definition: &TBC20StandardDefinition{
		Name: "Parity Token", Symbol: "PTY", Supply: "1000.00000000", Decimal: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	funding := tbc20StandardFixtureUTXO(t, string(bytes.Repeat([]byte{'a'}, 64)), 0, fixture.FundingScriptHex, 300000)
	owner, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	mint, err := token.Mint(key, owner.AddressString, funding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mint.SourceTxRaw != fixture.SourceRaw || mint.TxRaw != fixture.MintRaw {
		t.Fatalf("Go TBC20Standard mint transactions differ from JS 1.7.2")
	}
	if mint.SourceFeeSatoshis != fixture.SourceFeeSatoshis || mint.FeeSatoshis != fixture.MintFeeSatoshis {
		t.Fatalf("mint fees = %d/%d, want %d/%d", mint.SourceFeeSatoshis, mint.FeeSatoshis, fixture.SourceFeeSatoshis, fixture.MintFeeSatoshis)
	}
	tokenUTXO, tokenBalance, err := BuildTBC20StandardUTXO(mint.Transaction, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tokenBalance.Cmp(mustStandardBigInt(t, "100000000000")) != 0 {
		t.Fatalf("mint balance = %s", tokenBalance)
	}
	feeUTXO := tbc20StandardFixtureUTXO(t, string(bytes.Repeat([]byte{'b'}, 64)), 1, fixture.FundingScriptHex, 100000)
	transfer, err := token.Transfer(key, fixture.Recipient, "123.45600000", []*bt.UTXO{tokenUTXO}, feeUTXO, []*bt.Tx{mint.Transaction}, []TBC20StandardAncestorResolver{tbc20StandardAncestorMap{mint.SourceTransaction.TxID(): mint.SourceTransaction}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transfer.TxRaw != fixture.TransferRaw {
		t.Fatalf("Go TBC20Standard transfer transaction differs from JS 1.7.2")
	}
	if transfer.FeeSatoshis != fixture.TransferFeeSatoshis {
		t.Fatalf("transfer fee = %d, want %d", transfer.FeeSatoshis, fixture.TransferFeeSatoshis)
	}
	for index, want := range fixture.TransferTokenAmount {
		if index >= len(transfer.TokenOutputs) || transfer.TokenOutputs[index].Amount.Cmp(mustStandardBigInt(t, want)) != 0 {
			t.Fatalf("token output %d differs", index)
		}
	}

	splitFeeUTXO := tbc20StandardFixtureUTXO(t, string(bytes.Repeat([]byte{'b'}, 64)), 2, fixture.FundingScriptHex, 100000)
	split, err := token.Transfer(key, owner.AddressString, "400.00000000", []*bt.UTXO{tokenUTXO}, splitFeeUTXO, []*bt.Tx{mint.Transaction}, []TBC20StandardAncestorResolver{tbc20StandardAncestorMap{mint.SourceTransaction.TxID(): mint.SourceTransaction}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if split.TxRaw != fixture.SplitRaw || split.FeeSatoshis != fixture.SplitFeeSatoshis {
		t.Fatal("Go TBC20Standard split transaction differs from JS 1.7.2")
	}
	splitTxID, err := hex.DecodeString(split.Transaction.TxID())
	if err != nil {
		t.Fatal(err)
	}
	mergeInputs := []*bt.UTXO{
		{TxID: splitTxID, Vout: 0, LockingScript: split.Transaction.Outputs[0].LockingScript, Satoshis: split.Transaction.Outputs[0].Satoshis},
		{TxID: splitTxID, Vout: 2, LockingScript: split.Transaction.Outputs[2].LockingScript, Satoshis: split.Transaction.Outputs[2].Satoshis},
	}
	mergeFeeUTXO := tbc20StandardFixtureUTXO(t, string(bytes.Repeat([]byte{'c'}, 64)), 0, fixture.FundingScriptHex, 100000)
	resolver := tbc20StandardAncestorMap{mint.Transaction.TxID(): mint.Transaction}
	merge, err := token.Merge(key, mergeInputs, mergeFeeUTXO, []*bt.Tx{split.Transaction, split.Transaction}, []TBC20StandardAncestorResolver{resolver, resolver}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merge.TxRaw != fixture.MergeRaw || merge.FeeSatoshis != fixture.MergeFeeSatoshis {
		t.Fatal("Go TBC20Standard merge transaction differs from JS 1.7.2")
	}
	if len(merge.TokenOutputs) != 1 || merge.TokenOutputs[0].Amount.Cmp(mustStandardBigInt(t, fixture.MergeTokenAmounts[0])) != 0 {
		t.Fatal("Go TBC20Standard merge amount differs from JS 1.7.2")
	}
}

func TestTBC20StandardTransferBoundaries(t *testing.T) {
	if _, err := TBC20StandardHumanToRaw("0.000000001", 8); err == nil {
		t.Fatal("expected excess decimal precision rejection")
	}
	tooLarge := new(big.Int).Add(new(big.Int).SetUint64(TBC20StandardMaxSlotAmount), big.NewInt(1))
	if _, err := TBC20StandardRawToHuman(tooLarge, 8); err == nil {
		t.Fatal("expected signed-63-bit slot boundary rejection")
	}
}

func mustStandardBigInt(t *testing.T, value string) *big.Int {
	t.Helper()
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		t.Fatalf("invalid fixture integer %q", value)
	}
	return result
}
