package util

// Port of tbc-contract/lib/util/poolnftunlock.ts (1850 lines).
// Pool NFT 2.0 txdata helpers used when constructing pool unlock scripts.
// Function names are kept identical to TS (no Pool suffix needed since they
// do not collide with ftunlock.go names).

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
)

// Service fee addresses per LP plan.
var ServiceFeeAddress = map[int]string{
	1: "13oCEJaqyyiC8iRrfup6PDL2GKZ3xQrsZL",
	2: "1Fa6Uy64Ub4qNdB896zX2pNMx4a8zMhtCy",
	3: "125fTLNsraQxTYqT4EeQNF2ggzcqicveKL",
	4: "19DetoaaohQkjFVJ6oGXd83xhZYQSbpE1g",
	5: "15EKrhuD8Yf3SfhjAgbizYqfnBbKh9ZMZ7",
}

// serviceFeeAddressPKHSet: Hash160 of each service fee address, computed once.
var serviceFeeAddressPKHSet map[string]struct{}

func init() {
	serviceFeeAddressPKHSet = make(map[string]struct{})
	for _, addr := range ServiceFeeAddress {
		a, err := bscript.NewAddressFromString(addr)
		if err == nil {
			serviceFeeAddressPKHSet[a.PublicKeyHash] = struct{}{}
		}
	}
}

// IsServiceFeePkh returns whether pubKeyHash is one of the service fee addresses.
// Mirrors TS isServiceFeePkh.
func IsServiceFeePkh(pubKeyHash string) bool {
	_, ok := serviceFeeAddressPKHSet[pubKeyHash]
	return ok
}

// Pool-specific constants (mirror TS names exactly).
const (
	poolVersion           = 10
	poolVliolength        = 0x10
	poolAmountlength      = 0x08
	poolHashlength        = 0x20
	poolFtV1Length        = 1564
	poolFtV1PartialOffset = 1536
	poolFtV2Length        = 1884
	poolFtV2PartialOffset = 1856
	poolCoinLength        = 2012
	poolCoinPartialOffset = 1984
)

// --------------------------------------------------------------------------
// Low-level helpers
// --------------------------------------------------------------------------

// poolGetLengthHex mirrors getLengthHex(length) in poolnftunlock.ts.
// Returns a Script PUSHDATA length prefix.
func poolGetLengthHex(length int) []byte {
	if length < 76 {
		return []byte{byte(length)}
	}
	if length < 256 {
		return []byte{0x4c, byte(length)}
	}
	b := make([]byte, 3)
	b[0] = 0x4d
	binary.LittleEndian.PutUint16(b[1:], uint16(length))
	return b
}

// PoolGetSize mirrors getSize(length) in poolnftunlock.ts.
// Returns the script-size encoding (1 or 2 bytes, little-endian).
// Exported so poolnft2.go can use it directly for tape size computations.
func PoolGetSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}

func poolAppendUint32LE(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func poolAppendUint64LE(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return append(buf, b...)
}

// poolPartialHashAndSuffix computes the partial hash and suffix for a locking script.
// Returns ("00", full script) for short/non-FT scripts.
func poolPartialHashAndSuffix(script []byte) (phHex string, suffix []byte) {
	l := len(script)
	switch l {
	case poolFtV1Length:
		phHex = partialsha256.CalculatePartialHash(script[:poolFtV1PartialOffset])
		suffix = script[poolFtV1PartialOffset:]
	case poolFtV2Length:
		phHex = partialsha256.CalculatePartialHash(script[:poolFtV2PartialOffset])
		suffix = script[poolFtV2PartialOffset:]
	case poolCoinLength:
		phHex = partialsha256.CalculatePartialHash(script[:poolCoinPartialOffset])
		suffix = script[poolCoinPartialOffset:]
	default:
		phHex = "00"
		suffix = script
	}
	return
}

// poolIsFTScript returns true if the locking script is an FT/coin code script.
func poolIsFTScript(l int) bool {
	return l == poolFtV1Length || l == poolFtV2Length || l == poolCoinLength
}

// --------------------------------------------------------------------------
// GetInputsTxOutputsData (internal helper, mirrors TS getInputsTxOutputsData)
// --------------------------------------------------------------------------

type poolOutputsData struct {
	outputs1       []byte
	outputs1length []byte
	outputs2       []byte
	outputs2length []byte
}

// poolGetInputsTxOutputsData mirrors TS getInputsTxOutputsData(tx, vout, isTape).
// offset: 1 normally, 2 when isTape=true.
func poolGetInputsTxOutputsData(tx *bt.Tx, vout int, isTape bool) poolOutputsData {
	offset := 1
	if isTape {
		offset = 2
	}

	var out poolOutputsData

	// outputs1: outputs before vout
	if vout == 0 {
		out.outputs1 = []byte{0x00}
		out.outputs1length = nil
	} else {
		var buf []byte
		for i := 0; i < vout; i++ {
			buf = poolAppendUint64LE(buf, tx.Outputs[i].Satoshis)
			buf = append(buf, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
		}
		out.outputs1 = buf
		out.outputs1length = poolGetLengthHex(len(buf))
	}

	// outputs2: outputs from vout+offset onwards
	var buf2 []byte
	for i := vout + offset; i < len(tx.Outputs); i++ {
		buf2 = poolAppendUint64LE(buf2, tx.Outputs[i].Satoshis)
		buf2 = append(buf2, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	if len(buf2) == 0 {
		out.outputs2 = []byte{0x00}
		out.outputs2length = nil
	} else {
		out.outputs2 = buf2
		out.outputs2length = poolGetLengthHex(len(buf2))
	}

	return out
}

// --------------------------------------------------------------------------
// GetOutputsData (mirrors TS getOutputsData, used in getPoolNFTPreTxdata)
// --------------------------------------------------------------------------

func poolGetOutputsData(tx *bt.Tx, index int) []byte {
	var outputsBuf []byte
	for i := index; i < len(tx.Outputs); i++ {
		outputsBuf = poolAppendUint64LE(outputsBuf, tx.Outputs[i].Satoshis)
		outputsBuf = append(outputsBuf, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	if len(outputsBuf) == 0 {
		return []byte{0x00}
	}
	result := poolGetLengthHex(len(outputsBuf))
	result = append(result, outputsBuf...)
	return result
}

// --------------------------------------------------------------------------
// GetInputsTxdata (mirrors TS getInputsTxdata)
// --------------------------------------------------------------------------

// GetInputsTxdata mirrors getInputsTxdata(tx, vout) in poolnftunlock.ts.
// Used in pool unlock scripts for "parent tx" data construction.
func GetInputsTxdata(tx *bt.Tx, vout int) (string, error) {
	if vout >= len(tx.Outputs) {
		return "", fmt.Errorf("GetInputsTxdata: vout %d out of range", vout)
	}
	var buf []byte

	// Version + LockTime + InputCount + OutputCount (16 bytes)
	buf = append(buf, poolVliolength)
	buf = poolAppendUint32LE(buf, poolVersion)
	buf = poolAppendUint32LE(buf, tx.LockTime)
	buf = poolAppendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = poolAppendUint32LE(buf, uint32(len(tx.Outputs)))

	// Hash of all inputs (txid+vout+seq) and hash of all unlocking scripts
	var inputsBuf []byte
	var scriptsBuf []byte
	for _, inp := range tx.Inputs {
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			inputsBuf = append(inputsBuf, prevID[i])
		}
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.PreviousTxOutIndex)
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.SequenceNumber)
		scriptsBuf = append(scriptsBuf, crypto.Sha256(inp.UnlockingScript.Bytes())...)
	}
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(inputsBuf)...)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(scriptsBuf)...)

	// Outputs data split
	od := poolGetInputsTxOutputsData(tx, vout, false)
	buf = append(buf, od.outputs1length...)
	buf = append(buf, od.outputs1...)

	// The target output at vout
	lockScript := tx.Outputs[vout].LockingScript.Bytes()
	phHex, suffix := poolPartialHashAndSuffix(lockScript)
	sizeBytes := PoolGetSize(len(lockScript))

	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, tx.Outputs[vout].Satoshis)
	buf = append(buf, poolGetLengthHex(len(suffix))...)
	buf = append(buf, suffix...)
	if poolIsFTScript(len(lockScript)) {
		buf = append(buf, poolHashlength)
	}
	phBytes, err := hex.DecodeString(phHex)
	if err != nil {
		return "", fmt.Errorf("GetInputsTxdata: %w", err)
	}
	buf = append(buf, phBytes...)
	buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
	buf = append(buf, sizeBytes...)

	buf = append(buf, od.outputs2length...)
	buf = append(buf, od.outputs2...)
	buf = append(buf, 0x52) // trailing marker

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// GetInputsTxdataSwap (mirrors TS getInputsTxdataSwap)
// --------------------------------------------------------------------------

// GetInputsTxdataSwap mirrors getInputsTxdataSwap(tx, vout) in poolnftunlock.ts.
// Used for swap input tx data.
func GetInputsTxdataSwap(tx *bt.Tx, vout int) (string, error) {
	if vout >= len(tx.Outputs) {
		return "", fmt.Errorf("GetInputsTxdataSwap: vout %d out of range", vout)
	}
	var buf []byte

	buf = append(buf, poolVliolength)
	buf = poolAppendUint32LE(buf, poolVersion)
	buf = poolAppendUint32LE(buf, tx.LockTime)
	buf = poolAppendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = poolAppendUint32LE(buf, uint32(len(tx.Outputs)))

	var inputsBuf []byte
	var scriptsBuf []byte
	for _, inp := range tx.Inputs {
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			inputsBuf = append(inputsBuf, prevID[i])
		}
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.PreviousTxOutIndex)
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.SequenceNumber)
		scriptsBuf = append(scriptsBuf, crypto.Sha256(inp.UnlockingScript.Bytes())...)
	}
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(inputsBuf)...)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(scriptsBuf)...)

	lockScript := tx.Outputs[vout].LockingScript.Bytes()

	if poolIsFTScript(len(lockScript)) {
		od := poolGetInputsTxOutputsData(tx, vout, true) // isTape=true
		buf = append(buf, od.outputs1length...)
		buf = append(buf, od.outputs1...)

		phHex, suffix := poolPartialHashAndSuffix(lockScript)
		sizeBytes := PoolGetSize(len(lockScript))

		buf = append(buf, poolAmountlength)
		buf = poolAppendUint64LE(buf, tx.Outputs[vout].Satoshis)
		buf = append(buf, poolGetLengthHex(len(suffix))...)
		buf = append(buf, suffix...)
		buf = append(buf, poolHashlength)
		phBytes, err := hex.DecodeString(phHex)
		if err != nil {
			return "", fmt.Errorf("GetInputsTxdataSwap: %w", err)
		}
		buf = append(buf, phBytes...)
		buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		// Tape output at vout+1
		if vout+1 < len(tx.Outputs) {
			tapeScript := tx.Outputs[vout+1].LockingScript.Bytes()
			buf = append(buf, poolAmountlength)
			buf = poolAppendUint64LE(buf, tx.Outputs[vout+1].Satoshis)
			buf = append(buf, poolGetLengthHex(len(tapeScript))...)
			buf = append(buf, tapeScript...)
		}

		buf = append(buf, od.outputs2length...)
		buf = append(buf, od.outputs2...)
	} else {
		od := poolGetInputsTxOutputsData(tx, vout, false)
		buf = append(buf, od.outputs1length...)
		buf = append(buf, od.outputs1...)

		sizeBytes := PoolGetSize(len(lockScript))
		buf = append(buf, poolAmountlength)
		buf = poolAppendUint64LE(buf, tx.Outputs[vout].Satoshis)
		buf = append(buf, poolGetLengthHex(len(lockScript))...)
		buf = append(buf, lockScript...)
		buf = append(buf, 0x00) // empty partial hash
		buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		buf = append(buf, od.outputs2length...)
		buf = append(buf, od.outputs2...)
	}
	buf = append(buf, 0x52)

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// GetCurrentInputsdataPool (mirrors TS getCurrentInputsdata)
// --------------------------------------------------------------------------

// GetCurrentInputsdataPool mirrors getCurrentInputsdata(tx) in poolnftunlock.ts.
// Note: renamed to avoid conflict with ftunlock.go's GetCurrentInputsdata.
func GetCurrentInputsdataPool(tx *bt.Tx) (string, error) {
	var inputsBuf []byte
	for _, inp := range tx.Inputs {
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			inputsBuf = append(inputsBuf, prevID[i])
		}
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.PreviousTxOutIndex)
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.SequenceNumber)
	}
	var buf []byte
	buf = append(buf, poolGetLengthHex(len(inputsBuf))...)
	buf = append(buf, inputsBuf...)
	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// GetPoolNFTPreTxdata (mirrors TS getPoolNFTPreTxdata)
// --------------------------------------------------------------------------

// GetPoolNFTPreTxdata mirrors getPoolNFTPreTxdata(tx) in poolnftunlock.ts.
func GetPoolNFTPreTxdata(tx *bt.Tx) (string, error) {
	var buf []byte

	buf = append(buf, poolVliolength)
	buf = poolAppendUint32LE(buf, poolVersion)
	buf = poolAppendUint32LE(buf, tx.LockTime)
	buf = poolAppendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = poolAppendUint32LE(buf, uint32(len(tx.Outputs)))

	var inputsBuf []byte
	var scriptsBuf []byte
	for _, inp := range tx.Inputs {
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			inputsBuf = append(inputsBuf, prevID[i])
		}
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.PreviousTxOutIndex)
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.SequenceNumber)
		scriptsBuf = append(scriptsBuf, crypto.Sha256(inp.UnlockingScript.Bytes())...)
	}
	buf = append(buf, poolGetLengthHex(len(inputsBuf))...)
	buf = append(buf, inputsBuf...)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(scriptsBuf)...)

	// outputs[0]: poolnft code (hash)
	if len(tx.Outputs) < 2 {
		return "", fmt.Errorf("GetPoolNFTPreTxdata: tx has fewer than 2 outputs")
	}
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, tx.Outputs[0].Satoshis)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(tx.Outputs[0].LockingScript.Bytes())...)

	// outputs[1]: poolnft tape (inline)
	tape1 := tx.Outputs[1].LockingScript.Bytes()
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, tx.Outputs[1].Satoshis)
	buf = append(buf, poolGetLengthHex(len(tape1))...)
	buf = append(buf, tape1...)

	// remaining outputs from index 2
	buf = append(buf, poolGetOutputsData(tx, 2)...)

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// GetPoolNFTPrePreTxdata (mirrors TS getPoolNFTPrePreTxdata)
// --------------------------------------------------------------------------

// GetPoolNFTPrePreTxdata mirrors getPoolNFTPrePreTxdata(tx) in poolnftunlock.ts.
func GetPoolNFTPrePreTxdata(tx *bt.Tx) (string, error) {
	var buf []byte

	buf = append(buf, poolVliolength)
	buf = poolAppendUint32LE(buf, poolVersion)
	buf = poolAppendUint32LE(buf, tx.LockTime)
	buf = poolAppendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = poolAppendUint32LE(buf, uint32(len(tx.Outputs)))

	var inputsBuf []byte
	var scriptsBuf []byte
	for _, inp := range tx.Inputs {
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			inputsBuf = append(inputsBuf, prevID[i])
		}
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.PreviousTxOutIndex)
		inputsBuf = poolAppendUint32LE(inputsBuf, inp.SequenceNumber)
		scriptsBuf = append(scriptsBuf, crypto.Sha256(inp.UnlockingScript.Bytes())...)
	}
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(inputsBuf)...)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(scriptsBuf)...)

	// outputs[0]: poolnft code (hash)
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("GetPoolNFTPrePreTxdata: tx has no outputs")
	}
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, tx.Outputs[0].Satoshis)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(tx.Outputs[0].LockingScript.Bytes())...)

	// remaining outputs from index 1
	buf = append(buf, poolGetOutputsData(tx, 1)...)

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// GetCurrentTxOutputsDataforPool2 (mirrors TS getCurrentTxOutputsDataforPool2)
// --------------------------------------------------------------------------

// GetCurrentTxOutputsDataforPool2 mirrors getCurrentTxOutputsDataforPool2
// in poolnftunlock.ts. option: 1=addLP, 2=removLP, 3=swapFTtoTBC, 4=swapTBCtoFT.
// withLock: 1 if the pool has a lock output, 0 otherwise.
// swapOption: 1 or 2 for swap direction (only used in option=3,4).
func GetCurrentTxOutputsDataforPool2(tx *bt.Tx, option int, withLock int, swapOption int) (string, error) {
	var buf []byte

	switch option {
	case 1: // Add LP
		buf = append(buf, poolAddLPOutputsData(tx, withLock)...)
	case 2: // Remove LP / LP→Tokens
		buf = append(buf, poolRemoveLPOutputsData(tx)...)
	case 3: // Swap FT→TBC
		buf = append(buf, poolSwapFTtoTBCOutputsData(tx, withLock, swapOption)...)
	case 4: // Swap TBC→FT
		buf = append(buf, poolSwapTBCtoFTOutputsData(tx, withLock, swapOption)...)
	default:
		return "", fmt.Errorf("GetCurrentTxOutputsDataforPool2: unknown option %d", option)
	}

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// Output encoding helpers
// --------------------------------------------------------------------------

// poolEncodeOutput encodes a single FT or plain output.
// Uses partial hash for FT scripts, full script for others.
func poolEncodeOutput(satoshis uint64, lockScript []byte) []byte {
	var buf []byte
	phHex, suffix := poolPartialHashAndSuffix(lockScript)
	sizeBytes := PoolGetSize(len(lockScript))

	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	if phHex == "00" {
		buf = append(buf, poolGetLengthHex(len(suffix))...)
		buf = append(buf, suffix...)
		buf = append(buf, 0x00)
	} else {
		buf = append(buf, poolGetLengthHex(len(suffix))...)
		buf = append(buf, suffix...)
		buf = append(buf, poolHashlength)
		phBytes, _ := hex.DecodeString(phHex)
		buf = append(buf, phBytes...)
	}
	buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
	buf = append(buf, sizeBytes...)
	return buf
}

// poolEncodeOutputHash encodes an output as its SHA256 hash (for PoolNFT code output).
func poolEncodeOutputHash(satoshis uint64, lockScript []byte) []byte {
	var buf []byte
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(lockScript)...)
	return buf
}

// poolEncodeTapeOutput encodes a tape output (full script inline, no partial hash).
func poolEncodeTapeOutput(satoshis uint64, tapeScript []byte) []byte {
	var buf []byte
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolGetLengthHex(len(tapeScript))...)
	buf = append(buf, tapeScript...)
	return buf
}

// --------------------------------------------------------------------------
// Option-specific output data builders
// --------------------------------------------------------------------------

// poolAddLPOutputsData builds the output data for option=1 (Add LP).
func poolAddLPOutputsData(tx *bt.Tx, withLock int) []byte {
	var buf []byte
	if len(tx.Outputs) < 6 {
		return buf
	}
	// poolnft code (output 0): by hash
	buf = append(buf, poolEncodeOutputHash(tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())...)
	// poolnft tape (output 1): inline
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())...)
	// FTAbyC code+tape (outputs 2,3)
	buf = append(buf, poolEncodeOutput(tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())...)
	// FT-LP code+tape (outputs 4,5)
	buf = append(buf, poolEncodeOutput(tx.Outputs[4].Satoshis, tx.Outputs[4].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())...)

	// Optional lock output
	if withLock != 0 && len(tx.Outputs) > 6 {
		buf = append(buf, poolEncodeOutput(tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())...)
	} else if withLock == 0 {
		// Check if pool code indicates a longer pool code path
		chunks := tx.Outputs[0].LockingScript.Chunks()
		poolCode := tx.Outputs[0].LockingScript.Bytes()
		if len(chunks) >= 2 {
			sub := len(chunks[len(chunks)-2].Buf) + 1
			poolCodeLength := len(poolCode) - sub
			if poolCodeLength > 3284 {
				buf = append(buf, 0x00)
			}
		}
	}

	// Trailing change outputs
	baseIdx := 6 + withLock
	switch len(tx.Outputs) - withLock {
	case 6:
		buf = append(buf, 0x00, 0x00)
	case 7:
		if baseIdx < len(tx.Outputs) {
			buf = append(buf, 0x00)
			buf = append(buf, poolEncodeOutput(tx.Outputs[baseIdx].Satoshis, tx.Outputs[baseIdx].LockingScript.Bytes())...)
		}
	case 8:
		for i := baseIdx; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
				buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
				buf = append(buf, 0x00)
				i++
			} else {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
			}
		}
	case 9:
		for i := baseIdx; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
				buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
				i++
			} else {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
			}
		}
	}
	return buf
}

// poolRemoveLPOutputsData builds the output data for option=2 (Remove LP / LP→Tokens).
func poolRemoveLPOutputsData(tx *bt.Tx) []byte {
	var buf []byte
	if len(tx.Outputs) < 2 {
		return buf
	}
	// poolnft code+tape
	buf = append(buf, poolEncodeOutputHash(tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())...)
	// outputs 2..6: FT-related outputs
	for i := 2; i < 7 && i < len(tx.Outputs); i++ {
		ls := tx.Outputs[i].LockingScript.Bytes()
		if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
			buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
			buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
			i++
		} else {
			buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
		}
	}
	// Trailing change outputs
	switch len(tx.Outputs) {
	case 7:
		buf = append(buf, 0x00, 0x00, 0x00)
	case 8:
		buf = append(buf, 0x00, 0x00)
		buf = append(buf, poolEncodeOutput(tx.Outputs[7].Satoshis, tx.Outputs[7].LockingScript.Bytes())...)
	default:
		if len(tx.Outputs) >= 9 {
			for i := 7; i < len(tx.Outputs); i++ {
				ls := tx.Outputs[i].LockingScript.Bytes()
				if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
					buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
					buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
					i++
				} else {
					buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
				}
			}
		}
	}
	return buf
}

// poolSwapFTtoTBCOutputsData builds the output data for option=3 (Swap FT→TBC).
func poolSwapFTtoTBCOutputsData(tx *bt.Tx, withLock int, swapOption int) []byte {
	var buf []byte
	if len(tx.Outputs) < 4 {
		return buf
	}
	// poolnft code+tape
	buf = append(buf, poolEncodeOutputHash(tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())...)
	// FTAbyC code+tape (outputs 2,3)
	buf = append(buf, poolEncodeOutput(tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())...)
	// TBC output (output 4)
	if len(tx.Outputs) > 4 {
		buf = append(buf, poolEncodeOutput(tx.Outputs[4].Satoshis, tx.Outputs[4].LockingScript.Bytes())...)
	}
	// Optional lock output
	if withLock != 0 && len(tx.Outputs) > 5 {
		buf = append(buf, poolEncodeOutput(tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())...)
	}
	// Trailing change outputs
	baseIdx := 5 + withLock
	if baseIdx < len(tx.Outputs) {
		for i := baseIdx; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
				buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
				i++
			} else {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
			}
		}
	} else {
		buf = append(buf, 0x00)
	}
	return buf
}

// poolSwapTBCtoFTOutputsData builds the output data for option=4 (Swap TBC→FT).
func poolSwapTBCtoFTOutputsData(tx *bt.Tx, withLock int, swapOption int) []byte {
	var buf []byte
	if len(tx.Outputs) < 4 {
		return buf
	}
	// poolnft code+tape
	buf = append(buf, poolEncodeOutputHash(tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())...)
	// FTAbyC code+tape (outputs 2,3)
	buf = append(buf, poolEncodeOutput(tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())...)
	buf = append(buf, poolEncodeTapeOutput(tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())...)
	// Optional lock output
	if withLock != 0 && len(tx.Outputs) > 4 {
		buf = append(buf, poolEncodeOutput(tx.Outputs[4].Satoshis, tx.Outputs[4].LockingScript.Bytes())...)
	}
	// Trailing change outputs
	baseIdx := 4 + withLock
	if baseIdx < len(tx.Outputs) {
		for i := baseIdx; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTScript(len(ls)) && i+1 < len(tx.Outputs) {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
				buf = append(buf, poolEncodeTapeOutput(tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())...)
				i++
			} else {
				buf = append(buf, poolEncodeOutput(tx.Outputs[i].Satoshis, ls)...)
			}
		}
	} else {
		buf = append(buf, 0x00)
	}
	return buf
}
