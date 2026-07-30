package contract

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestMultiSigRejectsFeeUnderflow(t *testing.T) {
	pubKeys := make([]string, 3)
	for i := range pubKeys {
		pubKeys[i] = hex.EncodeToString(mustTestPrivateKey(t, byte(i+31)).PubKey().SerialiseCompressed())
	}
	from, err := GetMultiSigAddress(pubKeys, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	to, err := bscript.NewAddressFromPublicKey(mustTestPrivateKey(t, 40).PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := multiSigLockScript(from)
	if err != nil {
		t.Fatal(err)
	}
	utxo := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x41}, 32),
		Vout:          0,
		LockingScript: lock,
		Satoshis:      1_000,
	}

	_, err = BuildMultiSigTransactionSendTBC(from, to.AddressString, 1, []*bt.UTXO{utxo})

	if err == nil {
		t.Fatal("expected insufficient funding error")
	}
}

func TestHTLCRejectsDustResult(t *testing.T) {
	key := mustTestPrivateKey(t, 41)
	address, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	trueScript, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}
	utxo := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x42}, 32),
		Vout:          0,
		LockingScript: trueScript,
		Satoshis:      80,
	}

	if _, err := Withdraw(address.AddressString, utxo); !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("withdraw: got %v, want %v", err, ErrOrdinaryOutputDust)
	}
	if _, err := Refund(address.AddressString, utxo, 900_000); !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("refund: got %v, want %v", err, ErrOrdinaryOutputDust)
	}
}

func TestPiggyBankRejectsDustResult(t *testing.T) {
	key := mustTestPrivateKey(t, 42)
	address, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	trueScript, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}
	utxo := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x43}, 32),
		Vout:          0,
		LockingScript: trueScript,
		Satoshis:      80,
	}

	_, err = UnfreezeTBC(address.AddressString, []*bt.UTXO{utxo}, 900_000)

	if !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("got %v, want %v", err, ErrOrdinaryOutputDust)
	}
}

func TestNFTReturnsFeeAdjustmentError(t *testing.T) {
	key := mustTestPrivateKey(t, 43)
	sender, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := bscript.NewAddressFromPublicKey(mustTestPrivateKey(t, 44).PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}

	nft := &NFT{
		CollectionID:    strings.Repeat("71", 32),
		CollectionIndex: 0,
		NftData: NFTData{
			NftName: "Fee boundary",
			Symbol:  "FEE",
		},
	}
	code, err := BuildCodeScript(nft.CollectionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := BuildNFTHoldScript(sender.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	tape, err := BuildNFTTapeScript(&nft.NftData)
	if err != nil {
		t.Fatal(err)
	}
	preTx := newFTTx()
	preTx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	preTx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	preTx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	prePreTx := newFTTx()
	prePreTx.AddOutput(&bt.Output{LockingScript: bscript.NewFromBytes([]byte{bscript.OpTRUE}), Satoshis: 1})

	feeScript, err := bscript.NewP2PKHFromAddress(sender.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	probeFee := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x45}, 32),
		Vout:          0,
		LockingScript: feeScript,
		Satoshis:      10_000,
	}
	probe := newFTTx()
	if err := probe.From(preTx.TxID(), 0, code.String(), 200); err != nil {
		t.Fatal(err)
	}
	if err := probe.From(preTx.TxID(), 1, hold.String(), 100); err != nil {
		t.Fatal(err)
	}
	if err := probe.FromUTXOs(probeFee); err != nil {
		t.Fatal(err)
	}
	probe.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	receiverHold, err := BuildNFTHoldScript(receiver.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	probe.AddOutput(&bt.Output{LockingScript: receiverHold, Satoshis: 100})
	probe.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := probe.ChangeToAddress(sender.AddressString, nftFeeQuote80()); err != nil {
		t.Fatal(err)
	}
	estimatedFee := probe.TotalInputSatoshis() - probe.TotalOutputSatoshis()

	feeUTXO := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x46}, 32),
		Vout:          0,
		LockingScript: feeScript,
		Satoshis:      estimatedFee + 42,
	}
	_, err = nft.TransferNFT(
		sender.AddressString,
		receiver.AddressString,
		key,
		[]*bt.UTXO{feeUTXO},
		preTx,
		prePreTx,
		false,
	)

	if err == nil {
		t.Fatal("expected actual-size fee adjustment error")
	}
}
