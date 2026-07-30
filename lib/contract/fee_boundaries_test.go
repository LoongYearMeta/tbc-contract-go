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

func TestBuildMultiSigTransactionSendTBCAcceptsTestnetP2PKHRecipient(t *testing.T) {
	pubKeys := make([]string, 3)
	for i := range pubKeys {
		pubKeys[i] = hex.EncodeToString(mustTestPrivateKey(t, byte(i+51)).PubKey().SerialiseCompressed())
	}
	from, err := GetMultiSigAddress(pubKeys, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	to, err := bscript.NewAddressFromPublicKey(mustTestPrivateKey(t, 60).PubKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := multiSigLockScript(from)
	if err != nil {
		t.Fatal(err)
	}
	utxo := &bt.UTXO{
		TxID:          bytes.Repeat([]byte{0x51}, 32),
		Vout:          0,
		LockingScript: lock,
		Satoshis:      100_000,
	}

	raw, err := BuildMultiSigTransactionSendTBC(from, to.AddressString, 50_000, []*bt.UTXO{utxo})
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw.TxRaw)
	if len(tx.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(tx.Outputs))
	}
	if !tx.Outputs[1].LockingScript.IsP2PKH() {
		t.Fatalf("testnet recipient output is not P2PKH: %s", tx.Outputs[1].LockingScript.String())
	}
}

func TestMultiSigUnlockingScriptConcatenatesPublicKeys(t *testing.T) {
	pubKeys := make([]string, 3)
	for i := range pubKeys {
		pubKeys[i] = hex.EncodeToString(mustTestPrivateKey(t, byte(i+61)).PubKey().SerialiseCompressed())
	}

	unlock, err := multiSigUnlockingScript([]string{"aa", "bb"}, pubKeys)
	if err != nil {
		t.Fatal(err)
	}
	chunks := unlock.Chunks()
	if len(chunks) != 4 {
		t.Fatalf("unlock chunks = %d, want OP_0 + 2 signatures + 1 concatenated pubkey blob", len(chunks))
	}
	want, err := hex.DecodeString(strings.Join(pubKeys, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chunks[3].Buf, want) {
		t.Fatalf("last unlock chunk length = %d, want %d", len(chunks[3].Buf), len(want))
	}
}

func TestSchnorrSignatureEncodingSeparatesDataFromPushOpcode(t *testing.T) {
	signature := bytes.Repeat([]byte{0x7a}, 64)

	raw := encodeSchnorrSig65Hex(signature)
	if len(raw) != 65*2 {
		t.Fatalf("raw Schnorr signature hex length = %d, want %d", len(raw), 65*2)
	}
	if strings.HasPrefix(raw, "41") {
		t.Fatal("raw signature unexpectedly contains a 65-byte push opcode")
	}

	pushed := encodeSchnorrSig65PushHex(signature)
	if pushed != "41"+raw {
		t.Fatal("push-encoded signature must contain exactly one 0x41 length prefix")
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
