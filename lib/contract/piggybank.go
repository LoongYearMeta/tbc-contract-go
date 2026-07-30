package contract

// Port of tbc-contract/lib/contract/piggyBank.ts.
// PiggyBank contract: time-locked TBC savings.
//
// Key design notes:
//   - tbcAmountSat is uint64 satoshis (caller must convert from decimal).
//   - lockTime is a uint32 block height.
//   - UnfreezeTBC requires the current block height from the API; callers
//     must pass it in (use api.FetchTBCLockTime or api.FetchBlockHeaders).
//   - Fee pattern: JSEstimateSize < 1000 → 80; else ceil(size*80/1000).
//     Unfreeze adds 100 to the estimate before computing the fee.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
)

// --------------------------------------------------------------------------
// Script builder
// --------------------------------------------------------------------------

// GetPiggyBankCode builds the PiggyBank locking script.
// Mirrors piggyBank.ts getPiggyBankCode(address, lockTime).
func GetPiggyBankCode(address string, lockTime uint32) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, fmt.Errorf("GetPiggyBankCode: invalid address: %w", err)
	}
	pubKeyHash := addr.PublicKeyHash
	lockTimeHex := piggyBankTimelockLE(lockTime)
	asm := fmt.Sprintf(
		"OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_6 OP_PUSH_META 24 OP_SPLIT OP_NIP OP_BIN2NUM ffffffff OP_BIN2NUM OP_NUMNOTEQUAL OP_1 OP_EQUALVERIFY %s OP_BIN2NUM OP_2 OP_PUSH_META OP_BIN2NUM OP_LESSTHANOREQUAL OP_1 OP_EQUAL",
		pubKeyHash, lockTimeHex,
	)
	return bscript.NewFromASM(asm)
}

func piggyBankTimelockLE(lockTime uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, lockTime)
	return hex.EncodeToString(b)
}

// piggyBankEstimateFee mirrors the TS fee pattern.
// extra: additional bytes to add to the estimate before applying the rate.
func piggyBankEstimateFee(tx *bt.Tx, extra int) int {
	sz := tx.JSEstimateSize() + extra
	if sz < 1000 {
		return 80
	}
	return (sz*80 + 999) / 1000
}

// --------------------------------------------------------------------------
// FreezeTBC
// --------------------------------------------------------------------------

// FreezeTBC builds an unsigned PiggyBank freeze tx.
// Mirrors piggyBank.ts freezeTBC.
//
// tbcAmountSat: satoshis to freeze.
// lockTime: block height after which the funds can be unfrozen.
func FreezeTBC(address string, tbcAmountSat uint64, lockTime uint32, utxos []*bt.UTXO) (string, error) {
	code, err := GetPiggyBankCode(address, lockTime)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: tbcAmountSat})
	if err := tx.ChangeToAddress(address, newFeeQuote80()); err != nil {
		return "", err
	}
	fee := piggyBankEstimateFee(tx, 0)
	if err := tx.AdjustImplicitFeeToTarget(fee); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// UnfreezeTBC
// --------------------------------------------------------------------------

// UnfreezeTBC builds an unsigned PiggyBank unfreeze tx.
// Mirrors piggyBank.ts unfreezeTBC (without the async API call).
//
// utxos: the frozen PiggyBank UTXOs to spend.
// currentBlockHeight: the current block height (from api.FetchTBCLockTime).
func UnfreezeTBC(address string, utxos []*bt.UTXO, currentBlockHeight uint32) (string, error) {
	sumAmount := uint64(0)
	for _, u := range utxos {
		next, carry := bits.Add64(sumAmount, u.Satoshis, 0)
		if carry != 0 {
			return "", bt.ErrAmountOverflow
		}
		sumAmount = next
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return "", err
	}
	// Estimate fee (TS adds 100 to account for unlock scripts)
	fee := piggyBankEstimateFee(tx, 100)
	if sumAmount < uint64(fee) {
		return "", fmt.Errorf("UnfreezeTBC: insufficient satoshis")
	}
	unfrozenSatoshis := sumAmount - uint64(fee)
	if err := requireOrdinaryOutput(unfrozenSatoshis, "PiggyBank unfreeze"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: unfrozenSatoshis})
	// Enable nLockTime for all inputs
	for i := range tx.Inputs {
		tx.Inputs[i].SequenceNumber = 0xFFFFFFFE
	}
	tx.LockTime = currentBlockHeight
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// FreezeTBCWithSign
// --------------------------------------------------------------------------

// FreezeTBCWithSign builds and signs the PiggyBank freeze tx.
// Mirrors piggyBank.ts _freezeTBC.
func FreezeTBCWithSign(privKey *bec.PrivateKey, tbcAmountSat uint64, lockTime uint32, utxos []*bt.UTXO) (string, error) {
	address, err := privKeyToAddress(privKey)
	if err != nil {
		return "", err
	}
	code, err := GetPiggyBankCode(address, lockTime)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: tbcAmountSat})
	if err := tx.ChangeToAddress(address, newFeeQuote80()); err != nil {
		return "", err
	}
	fee := piggyBankEstimateFee(tx, 0)
	if err := tx.AdjustImplicitFeeToTarget(fee); err != nil {
		return "", err
	}
	for i := range tx.Inputs {
		if err := signP2PKHInput(tx, privKey, uint32(i)); err != nil {
			return "", err
		}
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// UnfreezeTBCWithSign
// --------------------------------------------------------------------------

// UnfreezeTBCWithSign builds and signs the PiggyBank unfreeze tx.
// Mirrors piggyBank.ts _unfreezeTBC (without the async API call).
//
// currentBlockHeight: from api.FetchTBCLockTime.
func UnfreezeTBCWithSign(privKey *bec.PrivateKey, utxos []*bt.UTXO, currentBlockHeight uint32) (string, error) {
	address, err := privKeyToAddress(privKey)
	if err != nil {
		return "", err
	}
	sumAmount := uint64(0)
	for _, u := range utxos {
		next, carry := bits.Add64(sumAmount, u.Satoshis, 0)
		if carry != 0 {
			return "", bt.ErrAmountOverflow
		}
		sumAmount = next
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	p2pkh, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return "", err
	}
	fee := piggyBankEstimateFee(tx, 100)
	if sumAmount < uint64(fee) {
		return "", fmt.Errorf("UnfreezeTBCWithSign: insufficient satoshis")
	}
	unfrozenSatoshis := sumAmount - uint64(fee)
	if err := requireOrdinaryOutput(unfrozenSatoshis, "signed PiggyBank unfreeze"); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: unfrozenSatoshis})
	for i := range tx.Inputs {
		tx.Inputs[i].SequenceNumber = 0xFFFFFFFE
	}
	tx.LockTime = currentBlockHeight

	// Sign each PiggyBank input
	pubKey := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	for i, u := range utxos {
		tx.Inputs[i].PreviousTxScript = u.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = u.Satoshis
		sh, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return "", fmt.Errorf("UnfreezeTBCWithSign: input %d: %w", i, err)
		}
		sig, err := privKey.Sign(sh)
		if err != nil {
			return "", err
		}
		sigBytes := sig.Serialise()
		sigBytes = append(sigBytes, byte(sighash.AllForkID))
		sigHex := hex.EncodeToString(sigBytes)
		us, err := bscript.NewFromASM(sigHex + " " + pubKey)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// FetchTBCLockTime (static helper)
// --------------------------------------------------------------------------

// FetchTBCLockTimeFromScript extracts the lockTime from a PiggyBank locking script.
// Mirrors piggyBank.ts fetchTBCLockTime.
// The script is expected to be 53 bytes (TS validates the equivalent
// 106-character hex string; the byte form is half that).
// Returns the lockTime as a uint32.
func FetchTBCLockTimeFromScript(lockingScript *bscript.Script) (uint32, error) {
	b := lockingScript.Bytes()
	if len(b) != 53 {
		return 0, fmt.Errorf("FetchTBCLockTimeFromScript: invalid PiggyBank script length %d", len(b))
	}
	chunks := lockingScript.Chunks()
	// lockTime chunk is chunks[len-8] (8th from the end)
	if len(chunks) < 8 {
		return 0, fmt.Errorf("FetchTBCLockTimeFromScript: not enough script chunks")
	}
	lockTimeChunk := chunks[len(chunks)-8]
	if len(lockTimeChunk.Buf) < 4 {
		return 0, fmt.Errorf("FetchTBCLockTimeFromScript: lockTime chunk too short")
	}
	return binary.LittleEndian.Uint32(lockTimeChunk.Buf[:4]), nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// privKeyToAddress derives the mainnet P2PKH address string from a private key.
func privKeyToAddress(privKey *bec.PrivateKey) (string, error) {
	a, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return "", fmt.Errorf("privKeyToAddress: %w", err)
	}
	return a.AddressString, nil
}
