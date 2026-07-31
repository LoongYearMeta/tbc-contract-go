package contract

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
)

type htlcTokenFixture struct {
	senderKey   *bec.PrivateKey
	receiverKey *bec.PrivateKey
	sender      string
	receiver    string
	hashlock    string
	code        *bscript.Script
	tape        *bscript.Script
	ftUTXO      *util.FtUTXO
	feeUTXO     *bt.UTXO
	preTX       *bt.Tx
}

func newHTLCTokenFixture(t *testing.T, balance int64) htlcTokenFixture {
	t.Helper()
	senderKey := mustTestPrivateKey(t, 21)
	receiverKey := mustTestPrivateKey(t, 22)
	senderAddr, err := bscript.NewAddressFromPublicKey(senderKey.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	receiverAddr, err := bscript.NewAddressFromPublicKey(receiverKey.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	amountHex, _ := BuildTapeAmount(big.NewInt(balance), []*big.Int{big.NewInt(balance)})
	tape, err := bscript.NewFromASM("OP_FALSE OP_RETURN " + amountHex)
	if err != nil {
		t.Fatal(err)
	}
	code, err := getFTmintCode(strings.Repeat("31", 32), 0, senderAddr.AddressString, len(tape.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	preTX := bt.NewTx()
	preTX.AddOutput(&bt.Output{LockingScript: code, Satoshis: 500})
	preTX.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	ftTxID, err := hex.DecodeString(preTX.TxID())
	if err != nil {
		t.Fatal(err)
	}
	feeScript, err := bscript.NewP2PKHFromAddress(senderAddr.AddressString)
	if err != nil {
		t.Fatal(err)
	}
	return htlcTokenFixture{
		senderKey:   senderKey,
		receiverKey: receiverKey,
		sender:      senderAddr.AddressString,
		receiver:    receiverAddr.AddressString,
		hashlock:    hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
		code:        code,
		tape:        tape,
		ftUTXO: &util.FtUTXO{
			TxID:          ftTxID,
			Vout:          0,
			LockingScript: code,
			Satoshis:      500,
			FtBalance:     big.NewInt(balance),
		},
		feeUTXO: &bt.UTXO{
			TxID:          bytes.Repeat([]byte{0x51}, 32),
			Vout:          1,
			LockingScript: feeScript,
			Satoshis:      20_000,
		},
		preTX: preTX,
	}
}

func TestDeployHTLCTokenValidatesInputs(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	tests := []struct {
		name  string
		utxos []*util.FtUTXO
		preTX []*bt.Tx
		pre   []string
		amt   *big.Int
		want  string
	}{
		{"empty", nil, nil, nil, big.NewInt(1), "non-empty"},
		{"too many", []*util.FtUTXO{fx.ftUTXO, fx.ftUTXO, fx.ftUTXO, fx.ftUTXO, fx.ftUTXO, fx.ftUTXO}, make([]*bt.Tx, 6), make([]string, 6), big.NewInt(1), "<= 5"},
		{"ancestry", []*util.FtUTXO{fx.ftUTXO}, nil, nil, big.NewInt(1), "length"},
		{"insufficient", []*util.FtUTXO{fx.ftUTXO}, []*bt.Tx{fx.preTX}, []string{""}, big.NewInt(1_001), "insufficient"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DeployHTLCToken(
				fx.sender, fx.receiver, fx.hashlock, 900_000,
				tt.amt, tt.utxos, fx.feeUTXO, tt.preTX, tt.pre,
			)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDeployHTLCTokenOutputLayout(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	raw, err := DeployHTLCToken(
		fx.sender, fx.receiver, fx.hashlock, 900_000,
		big.NewInt(600), []*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
		[]*bt.Tx{fx.preTX}, []string{""},
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Inputs) != 2 {
		t.Fatalf("inputs = %d, want FT + fee", len(tx.Inputs))
	}
	if len(tx.Outputs) != 6 {
		t.Fatalf("outputs = %d, want HTLC + locked pair + change pair + fee change", len(tx.Outputs))
	}
	if tx.Outputs[0].Satoshis != 100 || tx.Outputs[1].Satoshis != 500 || tx.Outputs[2].Satoshis != 0 {
		t.Fatalf("unexpected locked output values")
	}
	if len(tx.Outputs[1].LockingScript.Bytes()) != 1884 {
		t.Fatalf("locked FT code length = %d", len(tx.Outputs[1].LockingScript.Bytes()))
	}
	lockedBalance, err := util.GetFtBalanceFromTape(hex.EncodeToString(tx.Outputs[2].LockingScript.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	changeBalance, err := util.GetFtBalanceFromTape(hex.EncodeToString(tx.Outputs[4].LockingScript.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if lockedBalance.Cmp(big.NewInt(600)) != 0 || changeBalance.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("balances locked/change = %s/%s", lockedBalance, changeBalance)
	}
}

func TestFillSigDeployHTLCTokenFillsEveryInput(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	raw, err := DeployHTLCToken(
		fx.sender, fx.receiver, fx.hashlock, 900_000,
		big.NewInt(600), []*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
		[]*bt.Tx{fx.preTX}, []string{""},
	)
	if err != nil {
		t.Fatal(err)
	}
	pubKey := hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())
	filled, err := FillSigDeployHTLCToken(raw, []string{"aa", "bb"}, pubKey, []*bt.Tx{fx.preTX}, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, filled)
	for i, input := range tx.Inputs {
		if len(input.UnlockingScript.Bytes()) == 0 {
			t.Fatalf("input %d remains unsigned", i)
		}
	}
}

func deployedHTLCTokenFixture(t *testing.T) (htlcTokenFixture, *bt.Tx, *bt.UTXO, *util.FtUTXO) {
	t.Helper()
	fx := newHTLCTokenFixture(t, 1_000)
	raw, err := DeployHTLCToken(
		fx.sender, fx.receiver, fx.hashlock, 900_000,
		big.NewInt(600), []*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
		[]*bt.Tx{fx.preTX}, []string{""},
	)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	txid, err := hex.DecodeString(tx.TxID())
	if err != nil {
		t.Fatal(err)
	}
	htlcUTXO := &bt.UTXO{
		TxID:          txid,
		Vout:          0,
		LockingScript: tx.Outputs[0].LockingScript,
		Satoshis:      tx.Outputs[0].Satoshis,
	}
	ftUTXO := &util.FtUTXO{
		TxID:          txid,
		Vout:          1,
		LockingScript: tx.Outputs[1].LockingScript,
		Satoshis:      tx.Outputs[1].Satoshis,
		FtBalance:     big.NewInt(600),
	}
	return fx, tx, htlcUTXO, ftUTXO
}

func TestWithdrawHTLCTokenBuildsThreeInputRedemption(t *testing.T) {
	fx, deployTX, htlcUTXO, ftUTXO := deployedHTLCTokenFixture(t)
	raw, err := WithdrawHTLCToken(fx.receiver, htlcUTXO, ftUTXO, deployTX, fx.feeUTXO)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if len(tx.Inputs) != 3 || len(tx.Outputs) != 3 {
		t.Fatalf("shape = %d inputs/%d outputs", len(tx.Inputs), len(tx.Outputs))
	}
	if tx.LockTime != 0 {
		t.Fatalf("withdraw locktime = %d", tx.LockTime)
	}
}

func TestRefundHTLCTokenSetsSequenceAndLockTime(t *testing.T) {
	fx, deployTX, htlcUTXO, ftUTXO := deployedHTLCTokenFixture(t)
	raw, err := RefundHTLCToken(fx.sender, htlcUTXO, ftUTXO, deployTX, fx.feeUTXO, 900_000)
	if err != nil {
		t.Fatal(err)
	}
	tx := mustTx(t, raw)
	if tx.Inputs[0].SequenceNumber != 0xfffffffe || tx.LockTime != 900_000 {
		t.Fatalf("refund sequence/locktime = %x/%d", tx.Inputs[0].SequenceNumber, tx.LockTime)
	}
}

func TestFillSigWithdrawAndRefundHTLCTokenFillThreeInputs(t *testing.T) {
	fx, deployTX, htlcUTXO, ftUTXO := deployedHTLCTokenFixture(t)
	pubKey := hex.EncodeToString(fx.receiverKey.PubKey().SerialiseCompressed())
	secret := "1234"
	sigs := []string{"aa", "bb", "cc"}

	withdrawRaw, err := WithdrawHTLCToken(fx.receiver, htlcUTXO, ftUTXO, deployTX, fx.feeUTXO)
	if err != nil {
		t.Fatal(err)
	}
	withdrawFilled, err := FillSigWithdrawHTLCToken(withdrawRaw, sigs, pubKey, secret, deployTX, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, input := range mustTx(t, withdrawFilled).Inputs {
		if len(input.UnlockingScript.Bytes()) == 0 {
			t.Fatalf("withdraw input %d remains unsigned", i)
		}
	}

	refundRaw, err := RefundHTLCToken(fx.sender, htlcUTXO, ftUTXO, deployTX, fx.feeUTXO, 900_000)
	if err != nil {
		t.Fatal(err)
	}
	refundFilled, err := FillSigRefundHTLCToken(refundRaw, sigs, pubKey, deployTX, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, input := range mustTx(t, refundFilled).Inputs {
		if len(input.UnlockingScript.Bytes()) == 0 {
			t.Fatalf("refund input %d remains unsigned", i)
		}
	}
}

func signHTLCTokenFixtureInputs(
	t *testing.T,
	raw string,
	inputs []*bt.UTXO,
	key *bec.PrivateKey,
) []string {
	t.Helper()
	tx := mustTx(t, raw)
	if len(tx.Inputs) != len(inputs) {
		t.Fatalf("sign fixture input count = %d, want %d", len(tx.Inputs), len(inputs))
	}
	sigs := make([]string, len(inputs))
	for i, input := range inputs {
		tx.Inputs[i].PreviousTxScript = input.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = input.Satoshis
		hash, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := key.Sign(hash)
		if err != nil {
			t.Fatal(err)
		}
		sigs[i] = hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
	}
	return sigs
}

func feeUTXOForAddress(t *testing.T, address string, marker byte) *bt.UTXO {
	t.Helper()
	script, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	return &bt.UTXO{
		TxID:          bytes.Repeat([]byte{marker}, 32),
		Vout:          0,
		LockingScript: script,
		Satoshis:      20_000,
	}
}

func TestDeployHTLCTokenWithSignMatchesFilledTransaction(t *testing.T) {
	fx := newHTLCTokenFixture(t, 1_000)
	unsigned, err := DeployHTLCToken(
		fx.sender, fx.receiver, fx.hashlock, 900_000,
		big.NewInt(600), []*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
		[]*bt.Tx{fx.preTX}, []string{""},
	)
	if err != nil {
		t.Fatal(err)
	}
	sigs := signHTLCTokenFixtureInputs(
		t, unsigned,
		[]*bt.UTXO{util.FtUTXOToUTXO(fx.ftUTXO), fx.feeUTXO},
		fx.senderKey,
	)
	pubKey := hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())
	filled, err := FillSigDeployHTLCToken(unsigned, sigs, pubKey, []*bt.Tx{fx.preTX}, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := DeployHTLCTokenWithSign(
		fx.sender, fx.receiver, fx.hashlock, 900_000,
		big.NewInt(600), []*util.FtUTXO{fx.ftUTXO}, fx.feeUTXO,
		[]*bt.Tx{fx.preTX}, []string{""}, fx.senderKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTransactionStructureAndScripts(t, direct, filled)
}

func TestWithdrawHTLCTokenWithSignMatchesFilledTransaction(t *testing.T) {
	fx, deployTX, htlcUTXO, ftUTXO := deployedHTLCTokenFixture(t)
	feeUTXO := feeUTXOForAddress(t, fx.receiver, 0x61)
	unsigned, err := WithdrawHTLCToken(fx.receiver, htlcUTXO, ftUTXO, deployTX, feeUTXO)
	if err != nil {
		t.Fatal(err)
	}
	sigs := signHTLCTokenFixtureInputs(
		t, unsigned,
		[]*bt.UTXO{htlcUTXO, util.FtUTXOToUTXO(ftUTXO), feeUTXO},
		fx.receiverKey,
	)
	pubKey := hex.EncodeToString(fx.receiverKey.PubKey().SerialiseCompressed())
	filled, err := FillSigWithdrawHTLCToken(unsigned, sigs, pubKey, "1234", deployTX, "")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := WithdrawHTLCTokenWithSign(
		fx.receiverKey, fx.receiver, htlcUTXO, ftUTXO,
		deployTX, "", feeUTXO, "1234",
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTransactionStructureAndScripts(t, direct, filled)
}

func TestRefundHTLCTokenWithSignMatchesFilledTransaction(t *testing.T) {
	fx, deployTX, htlcUTXO, ftUTXO := deployedHTLCTokenFixture(t)
	unsigned, err := RefundHTLCToken(fx.sender, htlcUTXO, ftUTXO, deployTX, fx.feeUTXO, 900_000)
	if err != nil {
		t.Fatal(err)
	}
	sigs := signHTLCTokenFixtureInputs(
		t, unsigned,
		[]*bt.UTXO{htlcUTXO, util.FtUTXOToUTXO(ftUTXO), fx.feeUTXO},
		fx.senderKey,
	)
	pubKey := hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())
	filled, err := FillSigRefundHTLCToken(unsigned, sigs, pubKey, deployTX, "")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := RefundHTLCTokenWithSign(
		fx.senderKey, fx.sender, htlcUTXO, ftUTXO,
		deployTX, "", fx.feeUTXO, 900_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSameTransactionStructureAndScripts(t, direct, filled)
}
