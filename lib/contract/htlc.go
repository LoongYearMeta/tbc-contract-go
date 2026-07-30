package contract

// Port of tbc-contract/lib/contract/htlc.ts.
// Hash Time Locked Contract (HTLC) implementation.
//
// Key design notes:
//   - amount is uint64 satoshis (caller must apply decimal conversion).
//   - timelock is a uint32 block height / timestamp.
//   - Signing functions return the fully-assembled raw tx hex.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
)

// --------------------------------------------------------------------------
// Script builder
// --------------------------------------------------------------------------

// GetHTLCCode builds the HTLC locking script.
// Mirrors htlc.ts getCode(senderPubHash, receiverPubHash, hashlock, timelock).
func GetHTLCCode(senderPubHash, receiverPubHash, hashlock string, timelock uint32) (*bscript.Script, error) {
	timelockHex := timelockLE(timelock)
	asm := fmt.Sprintf(
		"OP_IF OP_SHA256 %s OP_EQUALVERIFY OP_DUP OP_HASH160 %s OP_ELSE %s OP_BIN2NUM OP_2 OP_PUSH_META OP_BIN2NUM OP_2DUP OP_GREATERTHAN OP_NOTIF OP_2DUP 0065cd1d OP_GREATERTHANOREQUAL OP_IF 0065cd1d OP_GREATERTHANOREQUAL OP_VERIFY OP_LESSTHANOREQUAL OP_ELSE OP_2DROP OP_DROP OP_TRUE OP_ENDIF OP_ELSE OP_FALSE OP_ENDIF OP_VERIFY OP_6 OP_PUSH_META 24 OP_SPLIT OP_NIP OP_BIN2NUM ffffffff OP_NUMNOTEQUAL OP_VERIFY OP_DUP OP_HASH160 %s OP_ENDIF OP_EQUALVERIFY OP_CHECKSIG",
		hashlock, receiverPubHash, timelockHex, senderPubHash,
	)
	return bscript.NewFromASM(asm)
}

// timelockLE returns the 4-byte little-endian hex of a timelock value.
func timelockLE(timelock uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, timelock)
	return hex.EncodeToString(b)
}

// htlcAddressToPKH extracts the 20-byte public key hash (hex) from a P2PKH address.
func htlcAddressToPKH(address string) (string, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return "", fmt.Errorf("htlcAddressToPKH: %w", err)
	}
	return addr.PublicKeyHash, nil
}

// --------------------------------------------------------------------------
// DeployHTLC (unsigned)
// --------------------------------------------------------------------------

// DeployHTLC builds an unsigned HTLC deployment tx.
// Mirrors htlc.ts deployHTLC.
//
// amountSat: satoshis to lock in the HTLC.
// timelock: block-height or timestamp (uint32).
func DeployHTLC(
	sender, receiver, hashlock string,
	timelock uint32,
	amountSat uint64,
	utxo *bt.UTXO,
) (string, error) {
	if !util.IsValidSHA256Hash(hashlock) {
		return "", fmt.Errorf("DeployHTLC: invalid hashlock")
	}
	senderPKH, err := htlcAddressToPKH(sender)
	if err != nil {
		return "", fmt.Errorf("DeployHTLC: invalid sender: %w", err)
	}
	receiverPKH, err := htlcAddressToPKH(receiver)
	if err != nil {
		return "", fmt.Errorf("DeployHTLC: invalid receiver: %w", err)
	}
	script, err := GetHTLCCode(senderPKH, receiverPKH, hashlock, timelock)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: script, Satoshis: amountSat})
	if err := tx.ChangeToAddress(sender, newFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.AdjustImplicitFeeToTarget(80); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// Withdraw (unsigned)
// --------------------------------------------------------------------------

// Withdraw builds an unsigned HTLC withdrawal tx.
// Mirrors htlc.ts withdraw.
func Withdraw(receiver string, htlcUtxo *bt.UTXO) (string, error) {
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(receiver)
	if err != nil {
		return "", err
	}
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("Withdraw: insufficient satoshis")
	}
	recipientSatoshis := htlcUtxo.Satoshis - 80
	if err := requireOrdinaryOutput(recipientSatoshis, "HTLC withdrawal"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: recipientSatoshis})
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// Refund (unsigned)
// --------------------------------------------------------------------------

// Refund builds an unsigned HTLC refund tx (with timelock + sequence).
// Mirrors htlc.ts refund.
func Refund(sender string, htlcUtxo *bt.UTXO, timelock uint32) (string, error) {
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(sender)
	if err != nil {
		return "", err
	}
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("Refund: insufficient satoshis")
	}
	refundSatoshis := htlcUtxo.Satoshis - 80
	if err := requireOrdinaryOutput(refundSatoshis, "HTLC refund"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: refundSatoshis})
	// sequence 0xFFFFFFFE enables nLockTime
	tx.Inputs[0].SequenceNumber = 0xFFFFFFFE
	tx.LockTime = timelock
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// FillSig helpers (apply signatures to unsigned txns)
// --------------------------------------------------------------------------

// FillSigDeploy applies a P2PKH signature to the deployment tx.
// Mirrors htlc.ts fillSigDepoly.
func FillSigDeploy(deployHTLCTxRaw, sig, publicKey string) (string, error) {
	tx, err := bt.NewTxFromString(deployHTLCTxRaw)
	if err != nil {
		return "", err
	}
	asm := sig + " " + publicKey
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// FillSigWithdraw applies the secret + signature to the withdrawal tx.
// Mirrors htlc.ts fillSigWithdraw.
// secret: hex string of the preimage.
func FillSigWithdraw(withdrawTxRaw, secret, sig, publicKey string) (string, error) {
	tx, err := bt.NewTxFromString(withdrawTxRaw)
	if err != nil {
		return "", err
	}
	// ASM: <sig> <pubkey> <secret> OP_TRUE
	// Use OP_TRUE rather than the literal "1": tbc-lib-go's bscript.NewFromASM
	// only special-cases registered opcode names; bare numeric tokens are
	// hex-decoded, and "1" is odd-length hex → ErrInvalidOpCode. WithdrawWithSign
	// below uses the same OP_TRUE convention.
	asm := sig + " " + publicKey + " " + secret + " OP_TRUE"
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// FillSigRefund applies the sender signature to the refund tx.
// Mirrors htlc.ts fillSigRefund.
func FillSigRefund(refundTxRaw, sig, publicKey string) (string, error) {
	tx, err := bt.NewTxFromString(refundTxRaw)
	if err != nil {
		return "", err
	}
	// ASM: <sig> <pubkey> OP_FALSE
	// See FillSigWithdraw above for why we cannot use literal "0".
	asm := sig + " " + publicKey + " OP_FALSE"
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// Combined sign-and-build helpers
// --------------------------------------------------------------------------

// htlcSign computes a DER+sighash signature for an HTLC input.
// prevScript must be the locking script of the HTLC UTXO.
func htlcSign(tx *bt.Tx, inputIdx uint32, prevScript *bscript.Script, satoshis uint64, privKey *bec.PrivateKey) (string, error) {
	tx.Inputs[inputIdx].PreviousTxScript = prevScript
	tx.Inputs[inputIdx].PreviousTxSatoshis = satoshis
	sh, err := tx.CalcInputSignatureHash(inputIdx, sighash.AllForkID)
	if err != nil {
		return "", err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return "", err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	return hex.EncodeToString(sigBytes), nil
}

// DeployHTLCWithSign builds and signs the HTLC deployment tx.
// Mirrors htlc.ts deployHTLCWithSign.
func DeployHTLCWithSign(
	sender, receiver, hashlock string,
	timelock uint32,
	amountSat uint64,
	utxo *bt.UTXO,
	privKey *bec.PrivateKey,
) (string, error) {
	if !util.IsValidSHA256Hash(hashlock) {
		return "", fmt.Errorf("DeployHTLCWithSign: invalid hashlock")
	}
	senderPKH, err := htlcAddressToPKH(sender)
	if err != nil {
		return "", fmt.Errorf("DeployHTLCWithSign: invalid sender: %w", err)
	}
	receiverPKH, err := htlcAddressToPKH(receiver)
	if err != nil {
		return "", fmt.Errorf("DeployHTLCWithSign: invalid receiver: %w", err)
	}
	script, err := GetHTLCCode(senderPKH, receiverPKH, hashlock, timelock)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: script, Satoshis: amountSat})
	if err := tx.ChangeToAddress(sender, newFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.AdjustImplicitFeeToTarget(80); err != nil {
		return "", err
	}
	// Sign the P2PKH input
	if err := signP2PKHInput(tx, privKey, 0); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// WithdrawWithSign builds the withdrawal tx and signs it with the secret.
// Mirrors htlc.ts withdrawWithSign.
func WithdrawWithSign(
	privKey *bec.PrivateKey,
	receiver string,
	htlcUtxo *bt.UTXO,
	secret string,
) (string, error) {
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(receiver)
	if err != nil {
		return "", err
	}
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("WithdrawWithSign: insufficient satoshis")
	}
	recipientSatoshis := htlcUtxo.Satoshis - 80
	if err := requireOrdinaryOutput(recipientSatoshis, "HTLC signed withdrawal"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: recipientSatoshis})

	pubKey := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	sig, err := htlcSign(tx, 0, htlcUtxo.LockingScript, htlcUtxo.Satoshis, privKey)
	if err != nil {
		return "", err
	}
	// ASM: <sig> <pubkey> <secret> OP_TRUE
	asm := sig + " " + pubKey + " " + secret + " OP_TRUE"
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// RefundWithSign builds the refund tx with timelock and signs it.
// Mirrors htlc.ts refundWithSign.
func RefundWithSign(
	sender string,
	htlcUtxo *bt.UTXO,
	privKey *bec.PrivateKey,
	timelock uint32,
) (string, error) {
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(sender)
	if err != nil {
		return "", err
	}
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("RefundWithSign: insufficient satoshis")
	}
	refundSatoshis := htlcUtxo.Satoshis - 80
	if err := requireOrdinaryOutput(refundSatoshis, "HTLC signed refund"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: refundSatoshis})
	tx.Inputs[0].SequenceNumber = 0xFFFFFFFE
	tx.LockTime = timelock

	pubKey := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	sig, err := htlcSign(tx, 0, htlcUtxo.LockingScript, htlcUtxo.Satoshis, privKey)
	if err != nil {
		return "", err
	}
	// ASM: <sig> <pubkey> OP_FALSE
	asm := sig + " " + pubKey + " OP_FALSE"
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}
