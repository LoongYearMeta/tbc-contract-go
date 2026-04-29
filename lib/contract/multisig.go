package contract

// Port of tbc-contract/lib/contract/multiSig.ts.
// MultiSig contract: multi-signature wallets with optional FT transfer support.
//
// Key design notes (vs TS):
//   - FT amounts are *big.Int (caller must have already applied decimal scaling).
//   - TBC amounts are uint64 satoshis (caller applies decimal conversion).
//   - buildHoldScript is byte-built (no ASM parser dependency).
//   - Base58 encode/decode via github.com/LoongYearMeta/tbc-lib-go/base58.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/base58"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

// ft_v2_length is the byte length of an FT v2 code script.
// Mirrors the TS constant ft_v2_length = 1884.
const multiSigFTV2Length = 1884

// MultiSigTxRaw holds a partially-built multi-sig tx together with the input
// satoshi amounts needed to re-attach locking scripts for signing.
type MultiSigTxRaw struct {
	TxRaw   string   // hex-serialised tx
	Amounts []uint64 // satoshis per input (in tx input order)
}

// --------------------------------------------------------------------------
// Address helpers
// --------------------------------------------------------------------------

// multiSigGetHash mirrors MultiSig.getHash(pubKeys):
// Hash160(concat(pubKeys...)).
func multiSigGetHash(pubKeys []string) ([]byte, error) {
	var combined []byte
	for _, pk := range pubKeys {
		b, err := hex.DecodeString(pk)
		if err != nil {
			return nil, fmt.Errorf("multiSigGetHash: invalid pubkey hex: %w", err)
		}
		combined = append(combined, b...)
	}
	return crypto.Hash160(combined), nil
}

// GetMultiSigAddress returns the TBC multi-sig address for the given public
// keys, signature count, and total public key count.
// Mirrors MultiSig.getMultiSigAddress.
func GetMultiSigAddress(pubKeys []string, signatureCount, publicKeyCount int) (string, error) {
	if signatureCount < 1 || signatureCount > 6 {
		return "", fmt.Errorf("invalid signatureCount")
	}
	if publicKeyCount < 3 || publicKeyCount > 10 {
		return "", fmt.Errorf("invalid publicKeyCount")
	}
	if signatureCount > publicKeyCount {
		return "", fmt.Errorf("signatureCount must be less than publicKeyCount")
	}
	hash, err := multiSigGetHash(pubKeys)
	if err != nil {
		return "", err
	}
	prefix := byte((signatureCount << 4) | (publicKeyCount & 0x0f))
	payload := append([]byte{prefix}, hash...)
	checksum := crypto.Sha256d(payload)[:4]
	full := append(payload, checksum...)
	return base58.Encode(full), nil
}

// GetSignatureAndPublicKeyCount decodes a multi-sig address and returns its
// embedded (signatureCount, publicKeyCount).
// Mirrors MultiSig.getSignatureAndPublicKeyCount.
func GetSignatureAndPublicKeyCount(address string) (sigCnt, pkCnt int, err error) {
	buf := base58.Decode(address)
	if len(buf) == 0 {
		return 0, 0, fmt.Errorf("invalid multisig address")
	}
	prefix := buf[0]
	return int((prefix >> 4) & 0x0f), int(prefix & 0x0f), nil
}

// VerifyMultiSigAddress returns true when the Hash160 of the concatenated
// pubKeys matches the hash embedded in the address.
// Mirrors MultiSig.verifyMultiSigAddress.
func VerifyMultiSigAddress(pubKeys []string, address string) (bool, error) {
	hash, err := multiSigGetHash(pubKeys)
	if err != nil {
		return false, err
	}
	buf := base58.Decode(address)
	if len(buf) < 21 {
		return false, fmt.Errorf("invalid multisig address length")
	}
	return hex.EncodeToString(hash) == hex.EncodeToString(buf[1:21]), nil
}

// ValidateMultiSigAddress performs structural validation of a multi-sig address
// (length, prefix, checksum).
// Mirrors MultiSig.validateMultiSigAddress.
func ValidateMultiSigAddress(address string) bool {
	if len(address) != 33 && len(address) != 34 {
		return false
	}
	buf := base58.Decode(address)
	if len(buf) < 5 {
		return false
	}
	prefix := buf[0]
	sigCnt := int((prefix >> 4) & 0x0f)
	pkCnt := int(prefix & 0x0f)
	if sigCnt < 1 || sigCnt > 6 || pkCnt < 3 || pkCnt > 10 || sigCnt > pkCnt {
		return false
	}
	payload := buf[:len(buf)-4]
	checksum := buf[len(buf)-4:]
	calculated := crypto.Sha256d(payload)[:4]
	for i := 0; i < 4; i++ {
		if checksum[i] != calculated[i] {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------
// Script helpers
// --------------------------------------------------------------------------

// GetMultiSigLockScript returns the ASM string for the multi-sig locking script.
// Mirrors MultiSig.getMultiSigLockScript.
func GetMultiSigLockScript(address string) (string, error) {
	sigCnt, pkCnt, err := GetSignatureAndPublicKeyCount(address)
	if err != nil {
		return "", err
	}
	if sigCnt < 1 || sigCnt > 6 {
		return "", fmt.Errorf("invalid signatureCount")
	}
	if pkCnt < 3 || pkCnt > 10 {
		return "", fmt.Errorf("invalid publicKeyCount")
	}
	if sigCnt > pkCnt {
		return "", fmt.Errorf("signatureCount must be less than publicKeyCount")
	}
	buf := base58.Decode(address)
	if len(buf) < 21 {
		return "", fmt.Errorf("invalid multisig address")
	}
	hash := hex.EncodeToString(buf[1:21])

	prefix := ""
	for i := 0; i < pkCnt-1; i++ {
		prefix += "21 OP_SPLIT "
	}
	for i := 0; i < pkCnt; i++ {
		prefix += fmt.Sprintf("OP_%d OP_PICK ", pkCnt-1)
	}
	for i := 0; i < pkCnt-1; i++ {
		prefix += "OP_CAT "
	}

	return fmt.Sprintf("OP_%d OP_SWAP %sOP_HASH160 %s OP_EQUALVERIFY OP_%d OP_CHECKMULTISIG",
		sigCnt, prefix, hash, pkCnt), nil
}

// GetCombineHash returns Hash160(SHA256(lockScript)) + "01" for a multi-sig
// address; used as the FT recipient hash.
// Mirrors MultiSig.getCombineHash.
func GetCombineHash(address string) (string, error) {
	asm, err := GetMultiSigLockScript(address)
	if err != nil {
		return "", err
	}
	s, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", fmt.Errorf("GetCombineHash: %w", err)
	}
	h := crypto.Hash160(s.Bytes())
	return hex.EncodeToString(h) + "01", nil
}

// multiSigLockScript converts an address to a *bscript.Script.
func multiSigLockScript(address string) (*bscript.Script, error) {
	asm, err := GetMultiSigLockScript(address)
	if err != nil {
		return nil, err
	}
	return bscript.NewFromASM(asm)
}

// multiSigScriptHash returns Hash160(lockScript) for a multi-sig address
// as a hex string.  Mirrors the inline hash computation in the TS code.
func multiSigScriptHash(address string) (string, error) {
	s, err := multiSigLockScript(address)
	if err != nil {
		return "", err
	}
	h := crypto.Hash160(s.Bytes())
	return hex.EncodeToString(h), nil
}

// buildMultiSigHoldScript builds the byte-level P2PKH hold script for a single
// public-key participant:
//
//	OP_DUP OP_HASH160 <0x14> <hash> OP_EQUALVERIFY OP_CHECKSIG
//	OP_RETURN <0x08> <"multisig" bytes>
//
// Mirrors MultiSig.buildHoldScript (byte-built, no ASM parser).
func buildMultiSigHoldScript(pubKey string) (*bscript.Script, error) {
	pkBytes, err := hex.DecodeString(pubKey)
	if err != nil {
		return nil, fmt.Errorf("buildMultiSigHoldScript: invalid pubkey: %w", err)
	}
	pkh := crypto.Hash160(pkBytes)
	// "multisig" → 6d756c7469736967
	magic := []byte{0x6d, 0x75, 0x6c, 0x74, 0x69, 0x73, 0x69, 0x67}

	var buf []byte
	buf = append(buf,
		0x76, // OP_DUP
		0xa9, // OP_HASH160
		0x14, // push 20 bytes
	)
	buf = append(buf, pkh...)
	buf = append(buf,
		0x88, // OP_EQUALVERIFY
		0xac, // OP_CHECKSIG
		0x6a, // OP_RETURN
		0x08, // push 8 bytes
	)
	buf = append(buf, magic...)
	s := bscript.NewFromBytes(buf)
	return s, nil
}

// buildTapeScriptMultiSig builds the OP_FALSE OP_RETURN tape for a multi-sig
// wallet creation tx.  Mirrors MultiSig.buildTapeScript.
func buildTapeScriptMultiSig(address string, pubKeys []string) (*bscript.Script, error) {
	data := map[string]interface{}{
		"address": address,
		"pubkeys": pubKeys,
	}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	dataHex := hex.EncodeToString(jsonBytes)
	// "4d54617065" = "MTape"
	asm := "OP_FALSE OP_RETURN " + dataHex + " 4d54617065"
	return bscript.NewFromASM(asm)
}

// --------------------------------------------------------------------------
// Fee helpers (mirrors TS: txSize<1000 → fee(80), else feePerKb(80))
// --------------------------------------------------------------------------

// multiSigEstimateFee computes the flat fee for a tx following the TS pattern:
// if estimatedSize < 1000 → 80 sat; else ceil(size * 80 / 1000).
func multiSigEstimateFee(tx *bt.Tx) int {
	sz := tx.JSEstimateSize()
	if sz < 1000 {
		return 80
	}
	return (sz*80 + 999) / 1000
}

// --------------------------------------------------------------------------
// Signature helper for multi-sig inputs
// --------------------------------------------------------------------------

// calcMultiSigInputSig computes DER+sighash signature bytes for a single input
// where the previous locking script is a multi-sig script.
func calcMultiSigInputSig(tx *bt.Tx, inputIdx uint32, prevScript *bscript.Script, satoshis uint64, privKey *bec.PrivateKey) ([]byte, error) {
	tx.Inputs[inputIdx].PreviousTxScript = prevScript
	tx.Inputs[inputIdx].PreviousTxSatoshis = satoshis
	sh, err := tx.CalcInputSignatureHash(inputIdx, sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	return sigBytes, nil
}

// multiSigUnlockingScript builds the multi-sig unlocking script from an array
// of DER+sighash signatures (hex) and the public key hex strings.
// Mirrors TS: OP_0 <sig0> <sig1> ... <pk0><pk1>...
func multiSigUnlockingScript(sigs []string, pubKeys []string) (*bscript.Script, error) {
	asm := "OP_0"
	for _, s := range sigs {
		asm += " " + s
	}
	for _, pk := range pubKeys {
		asm += " " + pk
	}
	return bscript.NewFromASM(asm)
}

// --------------------------------------------------------------------------
// CreateMultiSigWallet
// --------------------------------------------------------------------------

// CreateMultiSigWallet creates the wallet-setup transaction for a multi-sig
// contract.  Mirrors MultiSig.createMultiSigWallet.
//
// tbcAmountSat: amount to lock in the multisig output (satoshis).
func CreateMultiSigWallet(
	addressFrom string,
	pubKeys []string,
	signatureCount, publicKeyCount int,
	tbcAmountSat uint64,
	utxos []*bt.UTXO,
	privKey *bec.PrivateKey,
) (string, error) {
	address, err := GetMultiSigAddress(pubKeys, signatureCount, publicKeyCount)
	if err != nil {
		return "", err
	}
	lockScript, err := multiSigLockScript(address)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockScript, Satoshis: tbcAmountSat})
	for _, pk := range pubKeys {
		hs, err := buildMultiSigHoldScript(pk)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: hs, Satoshis: 200})
	}
	tape, err := buildTapeScriptMultiSig(address, pubKeys)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})

	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", err
	}
	fee := multiSigEstimateFee(tx)
	if err := tx.AdjustImplicitFeeToTarget(fee); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// P2PKH → MultiSig (TBC send)
// --------------------------------------------------------------------------

// P2PKHToMultiSigSendTBC sends TBC from a P2PKH address to a multi-sig address.
// Mirrors MultiSig.p2pkhToMultiSig_sendTBC.
//
// tbcAmountSat: satoshis to send.
func P2PKHToMultiSigSendTBC(
	addressFrom, addressTo string,
	tbcAmountSat uint64,
	utxos []*bt.UTXO,
	privKey *bec.PrivateKey,
) (string, error) {
	lockScript, err := multiSigLockScript(addressTo)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockScript, Satoshis: tbcAmountSat})
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", err
	}
	fee := multiSigEstimateFee(tx)
	if err := tx.AdjustImplicitFeeToTarget(fee); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// BuildMultiSigTransactionSendTBC
// --------------------------------------------------------------------------

// BuildMultiSigTransactionSendTBC builds an unsigned multi-sig TBC-send tx.
// Returns the raw tx hex plus the input satoshi amounts needed for signing.
// Mirrors MultiSig.buildMultiSigTransaction_sendTBC.
//
// tbcAmountSat: satoshis to send to addressTo.
func BuildMultiSigTransactionSendTBC(
	addressFrom, addressTo string,
	tbcAmountSat uint64,
	utxos []*bt.UTXO,
) (*MultiSigTxRaw, error) {
	scriptFrom, err := multiSigLockScript(addressFrom)
	if err != nil {
		return nil, err
	}

	totalIn := uint64(0)
	amounts := make([]uint64, len(utxos))
	for i, u := range utxos {
		totalIn += u.Satoshis
		amounts[i] = u.Satoshis
	}
	fee := uint64((len(utxos)+9)/10) * 1000 // ceil(len/10)*1000

	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return nil, err
	}

	// Change back to multisig from-address
	tx.AddOutput(&bt.Output{
		LockingScript: scriptFrom,
		Satoshis:      totalIn - tbcAmountSat - fee,
	})

	// Recipient output
	if len(addressTo) > 0 && addressTo[0] == '1' {
		// P2PKH recipient
		p2pkh, err := bscript.NewP2PKHFromAddress(addressTo)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: p2pkh, Satoshis: tbcAmountSat})
	} else {
		scriptTo, err := multiSigLockScript(addressTo)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: scriptTo, Satoshis: tbcAmountSat})
	}

	return &MultiSigTxRaw{TxRaw: tx.String(), Amounts: amounts}, nil
}

// --------------------------------------------------------------------------
// SignMultiSigTransactionSendTBC
// --------------------------------------------------------------------------

// SignMultiSigTransactionSendTBC produces per-input SIGHASH_ALL|FORKID signatures
// for the multi-sig TBC-send tx built by BuildMultiSigTransactionSendTBC.
// Mirrors MultiSig.signMultiSigTransaction_sendTBC.
func SignMultiSigTransactionSendTBC(
	addressFrom string,
	raw *MultiSigTxRaw,
	privKey *bec.PrivateKey,
) ([]string, error) {
	scriptFrom, err := multiSigLockScript(addressFrom)
	if err != nil {
		return nil, err
	}
	tx, err := bt.NewTxFromString(raw.TxRaw)
	if err != nil {
		return nil, err
	}
	if len(tx.Inputs) != len(raw.Amounts) {
		return nil, fmt.Errorf("SignMultiSigTransactionSendTBC: input count mismatch")
	}
	sigs := make([]string, len(raw.Amounts))
	for i, sat := range raw.Amounts {
		sigBytes, err := calcMultiSigInputSig(tx, uint32(i), scriptFrom, sat, privKey)
		if err != nil {
			return nil, fmt.Errorf("SignMultiSigTransactionSendTBC: input %d: %w", i, err)
		}
		sigs[i] = hex.EncodeToString(sigBytes)
	}
	return sigs, nil
}

// BatchSignMultiSigTransactionSendTBC signs a batch of multi-sig txns.
// Mirrors MultiSig.batchSignMultiSigTransaction_sendTBC.
func BatchSignMultiSigTransactionSendTBC(
	addressFrom string,
	raws []*MultiSigTxRaw,
	privKey *bec.PrivateKey,
) ([][]string, error) {
	allSigs := make([][]string, len(raws))
	for i, raw := range raws {
		sigs, err := SignMultiSigTransactionSendTBC(addressFrom, raw, privKey)
		if err != nil {
			return nil, err
		}
		allSigs[i] = sigs
	}
	return allSigs, nil
}

// --------------------------------------------------------------------------
// FinishMultiSigTransactionSendTBC
// --------------------------------------------------------------------------

// FinishMultiSigTransactionSendTBC assembles a fully-signed multi-sig tx
// from the partial sig sets collected from each signer.
// sigs[j][i] = i-th signer's signature for j-th input.
// Mirrors MultiSig.finishMultiSigTransaction_sendTBC.
func FinishMultiSigTransactionSendTBC(
	txRaw string,
	sigs [][]string,
	pubKeys []string,
) (string, error) {
	tx, err := bt.NewTxFromString(txRaw)
	if err != nil {
		return "", err
	}
	for j := 0; j < len(sigs); j++ {
		us, err := multiSigUnlockingScript(sigs[j], pubKeys)
		if err != nil {
			return "", fmt.Errorf("FinishMultiSigTransactionSendTBC input %d: %w", j, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(j), us); err != nil {
			return "", fmt.Errorf("FinishMultiSigTransactionSendTBC input %d insert: %w", j, err)
		}
	}
	return tx.String(), nil
}

// BatchFinishMultiSigTransactionSendTBC assembles a batch of signed multi-sig txns.
// Mirrors MultiSig.batchFinishMultiSigTransaction_sendTBC.
func BatchFinishMultiSigTransactionSendTBC(
	txRaws []string,
	sigs [][][]string,
	pubKeys []string,
) ([]string, error) {
	result := make([]string, len(txRaws))
	for i, raw := range txRaws {
		finished, err := FinishMultiSigTransactionSendTBC(raw, sigs[i], pubKeys)
		if err != nil {
			return nil, err
		}
		result[i] = finished
	}
	return result, nil
}

// --------------------------------------------------------------------------
// P2PKH → MultiSig (FT transfer)
// --------------------------------------------------------------------------

// P2PKHToMultiSigTransferFT transfers FT from a P2PKH address to a multi-sig
// address, with optional TBC side-payment.
// Mirrors MultiSig.p2pkhToMultiSig_transferFT.
//
// amountBN: pre-scaled *big.Int FT amount (no decimal conversion here).
// tbcAmountSat: optional TBC satoshis to also send; 0 = no TBC output.
func P2PKHToMultiSigTransferFT(
	addressFrom, addressTo string,
	ft *FT,
	amountBN *big.Int,
	utxo *bt.UTXO,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	privKey *bec.PrivateKey,
	tbcAmountSat uint64,
) (string, error) {
	if amountBN.Sign() < 0 {
		return "", fmt.Errorf("P2PKHToMultiSigTransferFT: invalid amount")
	}

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		tapeAmountSet[i] = fu.FtBalance
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
	}
	if amountBN.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("P2PKHToMultiSigTransferFT: insufficient FT balance")
	}

	amountHex, changeHex := BuildTapeAmount(amountBN, tapeAmountSet)

	// Determine FT recipient hash
	var recipientHash string
	if len(addressTo) > 0 && addressTo[0] == '1' {
		recipientHash = addressTo
	} else {
		h, err := multiSigScriptHash(addressTo)
		if err != nil {
			return "", err
		}
		recipientHash = h
	}

	// Inputs: FT inputs first, then the TBC UTXO
	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}

	// Optional TBC output to multi-sig recipient
	if tbcAmountSat > 0 {
		ms, err := multiSigLockScript(addressTo)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: ms, Satoshis: tbcAmountSat})
	}

	// FT code + tape outputs (to recipient)
	codeScript, err := BuildFTtransferCode(ft.CodeScript, recipientHash)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 2000})
	tapeScript := BuildFTtransferTape(ft.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	// Change FT outputs (back to sender)
	if amountBN.Cmp(tapeAmountSum) < 0 {
		changeCode, err := BuildFTtransferCode(ft.CodeScript, addressFrom)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 2000})
		changeTape := BuildFTtransferTape(ft.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	// TBC change
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", err
	}
	fee := multiSigEstimateFee(tx)
	if err := tx.AdjustImplicitFeeToTarget(fee); err != nil {
		return "", err
	}

	// Apply FT unlocking scripts (inputs 0 … len(ftutxos)-1)
	for i := range ftutxos {
		us, err := ft.GetFTunlock(privKey, tx, preTXs[i], prepreTxDatas[i], i, int(ftutxos[i].Vout), false)
		if err != nil {
			return "", fmt.Errorf("P2PKHToMultiSigTransferFT: FT unlock input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	// Sign the TBC (P2PKH) input (last input)
	if err := signP2PKHInput(tx, privKey, uint32(len(ftutxos))); err != nil {
		return "", err
	}

	return tx.String(), nil
}

// --------------------------------------------------------------------------
// BuildMultiSigTransactionTransferFT
// --------------------------------------------------------------------------

// BuildMultiSigTransactionTransferFT builds a partially-signed multi-sig FT
// transfer tx.  The multisig input (index 0) is left unsigned; the FT inputs
// (indices 1…) are signed by privKey.
// Mirrors MultiSig.buildMultiSigTransaction_transferFT.
//
// amountBN: pre-scaled *big.Int.
// contractTX: the on-chain contract tx (needed for GetFTunlockSwap).
func BuildMultiSigTransactionTransferFT(
	addressFrom, addressTo string,
	ft *FT,
	amountBN *big.Int,
	utxo *bt.UTXO,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	contractTX *bt.Tx,
	privKey *bec.PrivateKey,
) (*MultiSigTxRaw, error) {
	if amountBN.Sign() < 0 {
		return nil, fmt.Errorf("BuildMultiSigTransactionTransferFT: invalid amount")
	}

	hashFrom, err := multiSigScriptHash(addressFrom)
	if err != nil {
		return nil, err
	}

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		tapeAmountSet[i] = fu.FtBalance
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
	}
	if amountBN.Cmp(tapeAmountSum) > 0 {
		return nil, fmt.Errorf("BuildMultiSigTransactionTransferFT: insufficient FT balance")
	}

	// ftInputIndex=1 because utxo is input 0 and ftutxos start at 1
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(amountBN, tapeAmountSet, 1)

	// Determine ft script version
	ftVersion := 1
	if len(ftutxos) > 0 && len(ftutxos[0].LockingScript.Bytes()) == multiSigFTV2Length {
		ftVersion = 2
	}

	scriptFrom, err := multiSigLockScript(addressFrom)
	if err != nil {
		return nil, err
	}

	tx := newFTTx()
	// Input order: utxo (multisig), then FT inputs
	if err := tx.FromUTXOs(utxo); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return nil, err
	}

	// Change output back to multisig address (hardcoded fee per # of ftutxos)
	// Mirrors TS switch: 1→4000, 2→5500, 3→7000, 4→8500, 5→10000
	feeTable := map[int]uint64{1: 4000, 2: 5500, 3: 7000, 4: 8500, 5: 10000}
	ftFee, ok := feeTable[len(ftutxos)]
	if !ok {
		ftFee = 4000 + uint64(len(ftutxos)-1)*1500
	}
	changeSat := uint64(0)
	if utxo.Satoshis > ftFee {
		changeSat = utxo.Satoshis - ftFee
	}
	tx.AddOutput(&bt.Output{LockingScript: scriptFrom, Satoshis: changeSat})

	// Recipient FT outputs
	var recipientHash string
	if len(addressTo) > 0 && addressTo[0] == '1' {
		recipientHash = addressTo
	} else {
		h, err := multiSigScriptHash(addressTo)
		if err != nil {
			return nil, err
		}
		recipientHash = h
	}
	codeScript, err := BuildFTtransferCode(ft.CodeScript, recipientHash)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 2000})
	tapeScript := BuildFTtransferTape(ft.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if amountBN.Cmp(tapeAmountSum) < 0 {
		changeCode, err := BuildFTtransferCode(ft.CodeScript, hashFrom)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 2000})
		changeTape := BuildFTtransferTape(ft.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	// Apply FT unlock scripts (inputs 1…)
	for i := range ftutxos {
		inputIdx := i + 1
		us, err := ft.GetFTunlockSwap(privKey, tx, preTXs[i], prepreTxDatas[i], contractTX, inputIdx, int(ftutxos[i].Vout), ftVersion, false)
		if err != nil {
			return nil, fmt.Errorf("BuildMultiSigTransactionTransferFT: FT unlock input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(inputIdx), us); err != nil {
			return nil, err
		}
	}

	return &MultiSigTxRaw{
		TxRaw:   tx.String(),
		Amounts: []uint64{utxo.Satoshis},
	}, nil
}

// --------------------------------------------------------------------------
// SignMultiSigTransactionTransferFT
// --------------------------------------------------------------------------

// SignMultiSigTransactionTransferFT signs the multi-sig input (index 0) of
// the tx built by BuildMultiSigTransactionTransferFT.
// Mirrors MultiSig.signMultiSigTransaction_transferFT.
func SignMultiSigTransactionTransferFT(
	multiSigAddress string,
	raw *MultiSigTxRaw,
	privKey *bec.PrivateKey,
) ([]string, error) {
	scriptFrom, err := multiSigLockScript(multiSigAddress)
	if err != nil {
		return nil, err
	}
	tx, err := bt.NewTxFromString(raw.TxRaw)
	if err != nil {
		return nil, err
	}
	if len(tx.Inputs) == 0 || len(raw.Amounts) == 0 {
		return nil, fmt.Errorf("SignMultiSigTransactionTransferFT: no inputs")
	}
	sigBytes, err := calcMultiSigInputSig(tx, 0, scriptFrom, raw.Amounts[0], privKey)
	if err != nil {
		return nil, err
	}
	return []string{hex.EncodeToString(sigBytes)}, nil
}

// BatchSignMultiSigTransactionTransferFT signs the multi-sig input for a batch
// of FT-transfer txns.
// Mirrors MultiSig.batchSignMultiSigTransaction_transferFT.
func BatchSignMultiSigTransactionTransferFT(
	multiSigAddress string,
	raws []*MultiSigTxRaw,
	privKey *bec.PrivateKey,
) ([][]string, error) {
	allSigs := make([][]string, len(raws))
	for i, raw := range raws {
		sigs, err := SignMultiSigTransactionTransferFT(multiSigAddress, raw, privKey)
		if err != nil {
			return nil, err
		}
		allSigs[i] = sigs
	}
	return allSigs, nil
}

// --------------------------------------------------------------------------
// FinishMultiSigTransactionTransferFT
// --------------------------------------------------------------------------

// FinishMultiSigTransactionTransferFT applies the collected multisig signatures
// to input 0 of the FT-transfer tx.
// Mirrors MultiSig.finishMultiSigTransaction_transferFT.
func FinishMultiSigTransactionTransferFT(
	txRaw string,
	sigs [][]string,
	pubKeys []string,
) (string, error) {
	tx, err := bt.NewTxFromString(txRaw)
	if err != nil {
		return "", err
	}
	// Only input 0 is the multisig input
	us, err := multiSigUnlockingScript(sigs[0], pubKeys)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// BatchFinishMultiSigTransactionTransferFT finalises a batch of FT-transfer txns.
// Mirrors MultiSig.batchFinishMultiSigTransaction_transferFT.
func BatchFinishMultiSigTransactionTransferFT(
	txRaws []string,
	sigs [][][]string,
	pubKeys []string,
) ([]string, error) {
	result := make([]string, len(txRaws))
	for i, raw := range txRaws {
		finished, err := FinishMultiSigTransactionTransferFT(raw, sigs[i], pubKeys)
		if err != nil {
			return nil, err
		}
		result[i] = finished
	}
	return result, nil
}
