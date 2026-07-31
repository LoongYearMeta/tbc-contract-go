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
// in poolnftunlock.ts. option: 1=addLP, 2=removeLP, 3=swapFTtoTBC, 4=swapTBCtoFT.
// withLock: 1 if the pool has a lock output, 0 otherwise.
// swapOption: 1 or 2 for swap direction (only used in option=3, validated for 3 and 4).
func GetCurrentTxOutputsDataforPool2(tx *bt.Tx, option int, withLock int, swapOption int) (string, error) {
	if withLock != 0 && withLock != 1 {
		return "", fmt.Errorf("GetCurrentTxOutputsDataforPool2: invalid withLock %d (must be 0 or 1)", withLock)
	}

	// TS computes lockingscript/offset/size/partialhash/suffixdata over tx.outputs[2]
	// before the switch — these become the "outer scope" values that some case 2
	// sub-branches reference (notably the FT-LP detection in case 9 / case 10).
	var outerOffset int
	if len(tx.Outputs) >= 3 {
		outerLen := len(tx.Outputs[2].LockingScript.Bytes())
		outerOffset = poolPartialOffsetForLength(outerLen)
	}

	var buf []byte
	var err error
	switch option {
	case 1: // Add LP
		buf, err = poolAddLPOutputsData(tx, withLock)
	case 2: // Remove LP / LP -> Tokens
		buf, err = poolRemoveLPOutputsData(tx, outerOffset)
	case 3: // Swap FT->TBC (TBC换Tokens / Tokens换TBC dispatched by swapOption)
		if swapOption != 1 && swapOption != 2 {
			return "", fmt.Errorf("GetCurrentTxOutputsDataforPool2: option=3 requires swapOption in {1,2}, got %d", swapOption)
		}
		buf, err = poolSwapFTtoTBCOutputsData(tx, swapOption)
	case 4: // Swap TBC->FT, also reused by MergeFTinPool. TS does not branch on
		// swapOption for case 4 (the layout is fixed: 5 outputs). Don't validate
		// swapOption here — both swapOption=0 (mergeFTinPool) and 1/2 are accepted.
		buf, err = poolSwapTBCtoFTOutputsData(tx)
	default:
		return "", fmt.Errorf("GetCurrentTxOutputsDataforPool2: unknown option %d", option)
	}
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
}

// --------------------------------------------------------------------------
// Inline write primitives (mirror TS BufferWriter calls)
// --------------------------------------------------------------------------

// poolPartialOffsetForLength mirrors the TS ternary
//
//	lockingscript.length === ft_v1_length ? ft_v1_partial_offset
//	    : lockingscript.length === coin_length ? coin_partial_offset
//	    : ft_v2_partial_offset
//
// i.e. ft_v2_partial_offset is the default when length doesn't match v1 or coin.
func poolPartialOffsetForLength(scriptLen int) int {
	switch scriptLen {
	case poolFtV1Length:
		return poolFtV1PartialOffset
	case poolCoinLength:
		return poolCoinPartialOffset
	default:
		return poolFtV2PartialOffset
	}
}

// poolWriteFTOutput writes an FT-style output:
//
//	amountlength | satoshis(LE64) | getLengthHex(suffix.len) | suffix
//	| hashlength | partialhash | getLengthHex(size.len) | size
//
// `partialOffset` is taken from the caller (matching TS's outer-scope reuse).
func poolWriteFTOutput(buf []byte, satoshis uint64, lockScript []byte, partialOffset int) ([]byte, error) {
	sizeBytes := PoolGetSize(len(lockScript))
	if partialOffset < 0 || partialOffset > len(lockScript) {
		return buf, fmt.Errorf("poolWriteFTOutput: partial offset %d out of range for script of length %d", partialOffset, len(lockScript))
	}
	prefix := lockScript[:partialOffset]
	suffix := lockScript[partialOffset:]
	phHex := partialsha256.CalculatePartialHash(prefix)
	phBytes, err := hex.DecodeString(phHex)
	if err != nil {
		return buf, fmt.Errorf("poolWriteFTOutput: %w", err)
	}

	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolGetLengthHex(len(suffix))...)
	buf = append(buf, suffix...)
	buf = append(buf, poolHashlength)
	buf = append(buf, phBytes...)
	buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
	buf = append(buf, sizeBytes...)
	return buf, nil
}

// poolWriteFTOutputAuto computes partial offset from the script length itself.
func poolWriteFTOutputAuto(buf []byte, satoshis uint64, lockScript []byte) ([]byte, error) {
	return poolWriteFTOutput(buf, satoshis, lockScript, poolPartialOffsetForLength(len(lockScript)))
}

// poolWriteNonFTOutput writes a non-FT (P2PKH / lock) output:
//
//	amountlength | satoshis(LE64) | getLengthHex(scriptLen) | script
//	| 0x00  | getLengthHex(size.len) | size
func poolWriteNonFTOutput(buf []byte, satoshis uint64, lockScript []byte) []byte {
	sizeBytes := PoolGetSize(len(lockScript))
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolGetLengthHex(len(lockScript))...)
	buf = append(buf, lockScript...)
	buf = append(buf, 0x00) // partialhash placeholder (single byte)
	buf = append(buf, poolGetLengthHex(len(sizeBytes))...)
	buf = append(buf, sizeBytes...)
	return buf
}

// poolWriteTapeOutput writes a tape (full inline script) output:
//
//	amountlength | satoshis(LE64) | getLengthHex(scriptLen) | script
func poolWriteTapeOutput(buf []byte, satoshis uint64, lockScript []byte) []byte {
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolGetLengthHex(len(lockScript))...)
	buf = append(buf, lockScript...)
	return buf
}

// poolWritePoolNFTHashOutput writes the canonical poolnft-code-by-hash output:
//
//	amountlength | satoshis(LE64) | hashlength | sha256(script)
func poolWritePoolNFTHashOutput(buf []byte, satoshis uint64, lockScript []byte) []byte {
	buf = append(buf, poolAmountlength)
	buf = poolAppendUint64LE(buf, satoshis)
	buf = append(buf, poolHashlength)
	buf = append(buf, crypto.Sha256(lockScript)...)
	return buf
}

// poolIsFTOrCoinScript matches the TS triple-or check
//
//	lockingscript.length === ft_v1_length || lockingscript.length === ft_v2_length
//	    || lockingscript.length === coin_length
//
// (alias kept distinct from poolIsFTScript only to make call-sites obviously
// mirror the TS predicate).
func poolIsFTOrCoinScript(scriptLen int) bool {
	return scriptLen == poolFtV1Length || scriptLen == poolFtV2Length ||
		scriptLen == poolCoinLength
}

// poolP2PKHPubKeyHashHex extracts the 20-byte pubKeyHash from a P2PKH locking
// script as hex, mirroring TS's `lockingscript.subarray(3, 23).toString('hex')`.
// Returns "" if the script is shorter than 23 bytes.
func poolP2PKHPubKeyHashHex(lockScript []byte) string {
	if len(lockScript) < 23 {
		return ""
	}
	return hex.EncodeToString(lockScript[3:23])
}

// --------------------------------------------------------------------------
// case 1: Add LP (TS lines 795-948)
// --------------------------------------------------------------------------

func poolAddLPOutputsData(tx *bt.Tx, withLock int) ([]byte, error) {
	if len(tx.Outputs) < 6 {
		return nil, fmt.Errorf("poolAddLPOutputsData: option=1 requires at least 6 outputs, got %d", len(tx.Outputs))
	}
	var buf []byte
	var err error

	// poolnft code (output 0): by hash
	buf = poolWritePoolNFTHashOutput(buf, tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())
	// poolnft tape (output 1): inline
	buf = poolWriteTapeOutput(buf, tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())

	// FTAbyC code+tape (outputs 2,3)
	buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())
	if err != nil {
		return nil, err
	}
	buf = poolWriteTapeOutput(buf, tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())

	// FT-LP code+tape (outputs 4,5)
	buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[4].Satoshis, tx.Outputs[4].LockingScript.Bytes())
	if err != nil {
		return nil, err
	}
	buf = poolWriteTapeOutput(buf, tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())

	// withLock branch
	if withLock == 1 {
		if len(tx.Outputs) < 7 {
			return nil, fmt.Errorf("poolAddLPOutputsData: withLock=1 requires output[6]")
		}
		// TS writes the lock P2PKH as a non-FT block (lines 845-856).
		buf = poolWriteNonFTOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
	} else {
		// TS lines 858-862: peek pool code length and write a 0x00 if pool code > 3284.
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

	// Trailing change outputs, switching on (tx.outputs.length - withLock)
	switch len(tx.Outputs) - withLock {
	case 6:
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
	case 7:
		idx := 6 + withLock
		if idx >= len(tx.Outputs) {
			return nil, fmt.Errorf("poolAddLPOutputsData: case 7 expected output[%d]", idx)
		}
		buf = append(buf, 0x00)
		buf = poolWriteNonFTOutput(buf, tx.Outputs[idx].Satoshis, tx.Outputs[idx].LockingScript.Bytes())
	case 8:
		// TS for-loop with i++ inside iterates ONCE consuming outputs[i] and outputs[i+1]
		// regardless of FT-or-not (the TS code unconditionally reads outputs[i+1] in this branch).
		// The TS branch processes a single FTAbyA (FT) pair and writes a trailing 0x00.
		i := 6 + withLock
		if i+1 >= len(tx.Outputs) {
			return nil, fmt.Errorf("poolAddLPOutputsData: case 8 expected output[%d..%d]", i, i+1)
		}
		buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, tx.Outputs[i].LockingScript.Bytes())
		if err != nil {
			return nil, err
		}
		buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
		buf = append(buf, 0x00)
	case 9:
		// TS for-loop iterates over outputs from (6 + withLock) to end; FT pair = i,i+1; otherwise single.
		for i := 6 + withLock; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTOrCoinScript(len(ls)) {
				if i+1 >= len(tx.Outputs) {
					return nil, fmt.Errorf("poolAddLPOutputsData: case 9 missing tape for FT at output[%d]", i)
				}
				buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
				if err != nil {
					return nil, err
				}
				buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
				i++
			} else {
				buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
			}
		}
	default:
		return nil, fmt.Errorf("poolAddLPOutputsData: invalid transaction (outputs=%d, withLock=%d)", len(tx.Outputs), withLock)
	}
	return buf, nil
}

// --------------------------------------------------------------------------
// case 2: Remove LP / LP -> Tokens (TS lines 951-1202)
// --------------------------------------------------------------------------

func poolRemoveLPOutputsData(tx *bt.Tx, outerOffset int) ([]byte, error) {
	if len(tx.Outputs) < 7 {
		return nil, fmt.Errorf("poolRemoveLPOutputsData: option=2 requires at least 7 outputs, got %d", len(tx.Outputs))
	}
	var buf []byte
	var err error

	// poolnft code+tape
	buf = poolWritePoolNFTHashOutput(buf, tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())
	buf = poolWriteTapeOutput(buf, tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())

	// outputs[2..6]: FT/non-FT loop with i++ on FT match
	for i := 2; i < 7; i++ {
		ls := tx.Outputs[i].LockingScript.Bytes()
		if poolIsFTOrCoinScript(len(ls)) {
			if i+1 >= len(tx.Outputs) {
				return nil, fmt.Errorf("poolRemoveLPOutputsData: missing tape for FT at output[%d]", i)
			}
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
			i++
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
		}
	}

	switch len(tx.Outputs) {
	case 7:
		// 无任何找零
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
	case 8:
		// 只有普通找零
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
		buf = poolWriteNonFTOutput(buf, tx.Outputs[7].Satoshis, tx.Outputs[7].LockingScript.Bytes())
	case 9:
		// FT-LP (marker 'ffffffffff' at bytes 1428..1433) or FTAbyC change
		ls7 := tx.Outputs[7].LockingScript.Bytes()
		if poolHasFTLPMarker(ls7) {
			// TS: uses outer-scope `offset` (from outputs[2]) to compute partial hash for outputs[7].
			buf, err = poolWriteFTOutput(buf, tx.Outputs[7].Satoshis, ls7, outerOffset)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[8].Satoshis, tx.Outputs[8].LockingScript.Bytes())
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
		} else {
			// FTAbyC change: prepend 0x00, then FT pair using outputs[7]'s OWN offset.
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[7].Satoshis, ls7)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[8].Satoshis, tx.Outputs[8].LockingScript.Bytes())
			buf = append(buf, 0x00)
		}
	case 10:
		// FT-LP/FTAbyC change + 普通找零
		ls7 := tx.Outputs[7].LockingScript.Bytes()
		if poolHasFTLPMarker(ls7) {
			// FT-LP path: outer offset for outputs[7]
			buf, err = poolWriteFTOutput(buf, tx.Outputs[7].Satoshis, ls7, outerOffset)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[8].Satoshis, tx.Outputs[8].LockingScript.Bytes())
			buf = append(buf, 0x00)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[9].Satoshis, tx.Outputs[9].LockingScript.Bytes())
		} else {
			// Else path: prepend 0x00, then FTAbyC pair using outputs[7] OWN offset, then P2PKH.
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[7].Satoshis, ls7)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[8].Satoshis, tx.Outputs[8].LockingScript.Bytes())
			buf = poolWriteNonFTOutput(buf, tx.Outputs[9].Satoshis, tx.Outputs[9].LockingScript.Bytes())
		}
	case 11:
		// 只有 FT-LP、FTAbyC 找零 (no plain P2PKH change). Iterate outputs[7..] and trailing 0x00.
		for i := 7; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTOrCoinScript(len(ls)) {
				if i+1 >= len(tx.Outputs) {
					return nil, fmt.Errorf("poolRemoveLPOutputsData: case 11 missing tape for FT at output[%d]", i)
				}
				buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
				if err != nil {
					return nil, err
				}
				buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
				i++
			} else {
				buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
			}
		}
		buf = append(buf, 0x00)
	case 12:
		// 完整输出: FT-LP + FTAbyC + 普通 P2PKH 找零 (no trailing 0x00).
		for i := 7; i < len(tx.Outputs); i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTOrCoinScript(len(ls)) {
				if i+1 >= len(tx.Outputs) {
					return nil, fmt.Errorf("poolRemoveLPOutputsData: case 12 missing tape for FT at output[%d]", i)
				}
				buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
				if err != nil {
					return nil, err
				}
				buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
				i++
			} else {
				buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
			}
		}
	default:
		return nil, fmt.Errorf("poolRemoveLPOutputsData: invalid transaction (outputs=%d)", len(tx.Outputs))
	}
	return buf, nil
}

// poolHasFTLPMarker mirrors TS `lockingscript.subarray(1428, 1433).toString('hex') === 'ffffffffff'`.
func poolHasFTLPMarker(lockScript []byte) bool {
	if len(lockScript) < 1433 {
		return false
	}
	for i := 1428; i < 1433; i++ {
		if lockScript[i] != 0xff {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------
// case 3: Swap (TS lines 1204-1422)
//   swapOption=1 -> TBC换Tokens (lines 1207-1422)
//   swapOption=2 -> Tokens换TBC (lines 1425-1659)
// --------------------------------------------------------------------------

func poolSwapFTtoTBCOutputsData(tx *bt.Tx, swapOption int) ([]byte, error) {
	if swapOption == 1 {
		return poolSwap3Option1OutputsData(tx)
	}
	return poolSwap3Option2OutputsData(tx)
}

// case 3 / swapOption=1: TBC换Tokens (TS lines 1207-1422).
func poolSwap3Option1OutputsData(tx *bt.Tx) ([]byte, error) {
	if len(tx.Outputs) < 4 {
		return nil, fmt.Errorf("poolSwap3Option1OutputsData: requires at least 4 outputs, got %d", len(tx.Outputs))
	}
	var buf []byte
	var err error

	// poolnft code+tape
	buf = poolWritePoolNFTHashOutput(buf, tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())
	buf = poolWriteTapeOutput(buf, tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())

	// FTAbyA outputs (2,3): TS for-loop runs once with i++ inside
	buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())
	if err != nil {
		return nil, err
	}
	buf = poolWriteTapeOutput(buf, tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())

	switch len(tx.Outputs) {
	case 4:
		// 没有其他输出
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
	case 5:
		// P2PKH_ServiceFee 输出或找零输出
		ls := tx.Outputs[4].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls)
		if !IsServiceFeePkh(pkh) {
			// 普通找零: prepend two 0x00 then non-FT.
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[4].Satoshis, ls)
		} else {
			// 服务费: write non-FT then two 0x00.
			buf = poolWriteNonFTOutput(buf, tx.Outputs[4].Satoshis, ls)
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
		}
	case 6:
		ls4 := tx.Outputs[4].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls4)
		if !IsServiceFeePkh(pkh) {
			// FTAbyC 找零: prepend 0x00 then FT pair (using OWN offset of outputs[4]) then 0x00.
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[4].Satoshis, ls4)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())
			buf = append(buf, 0x00)
		} else {
			// 两个 P2PKH (服务费 + 普通)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[4].Satoshis, ls4)
			buf = append(buf, 0x00)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())
		}
	case 7:
		// FTAbyC 找零加一个 P2PKH
		ls4 := tx.Outputs[4].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls4)
		if !IsServiceFeePkh(pkh) {
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[4].Satoshis, ls4)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[5].Satoshis, tx.Outputs[5].LockingScript.Bytes())
			buf = poolWriteNonFTOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[4].Satoshis, ls4)
			ls5 := tx.Outputs[5].LockingScript.Bytes()
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[5].Satoshis, ls5)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
			buf = append(buf, 0x00)
		}
	case 8:
		// 完整输出: iterate outputs[4..] like a generic FT/non-FT pairing.
		for i := 4; i < 8; i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTOrCoinScript(len(ls)) {
				if i+1 >= len(tx.Outputs) {
					return nil, fmt.Errorf("poolSwap3Option1OutputsData: case 8 missing tape for FT at output[%d]", i)
				}
				buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
				if err != nil {
					return nil, err
				}
				buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
				i++
			} else {
				buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
			}
		}
	default:
		return nil, fmt.Errorf("poolSwap3Option1OutputsData: invalid transaction (outputs=%d)", len(tx.Outputs))
	}
	return buf, nil
}

// case 3 / swapOption=2: Tokens换TBC (TS lines 1425-1658).
func poolSwap3Option2OutputsData(tx *bt.Tx) ([]byte, error) {
	if len(tx.Outputs) < 5 {
		return nil, fmt.Errorf("poolSwap3Option2OutputsData: requires at least 5 outputs, got %d", len(tx.Outputs))
	}
	var buf []byte
	var err error

	// poolnft code+tape
	buf = poolWritePoolNFTHashOutput(buf, tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())
	buf = poolWriteTapeOutput(buf, tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())

	// outputs[2..5]: FT/non-FT loop (TS for i=2;i<5;i++), so i runs over 2,3,4 with FT consumption advancing.
	for i := 2; i < 5; i++ {
		ls := tx.Outputs[i].LockingScript.Bytes()
		if poolIsFTOrCoinScript(len(ls)) {
			if i+1 >= len(tx.Outputs) {
				return nil, fmt.Errorf("poolSwap3Option2OutputsData: missing tape for FT at output[%d]", i)
			}
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
			i++
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
		}
	}

	switch len(tx.Outputs) {
	case 5:
		// 没有其他输出
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
		buf = append(buf, 0x00)
	case 6:
		// P2PKH_ServiceFee 或普通找零
		ls := tx.Outputs[5].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls)
		if !IsServiceFeePkh(pkh) {
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[5].Satoshis, ls)
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[5].Satoshis, ls)
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
		}
	case 7:
		ls5 := tx.Outputs[5].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls5)
		if !IsServiceFeePkh(pkh) {
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[5].Satoshis, ls5)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
			buf = append(buf, 0x00)
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[5].Satoshis, ls5)
			buf = append(buf, 0x00)
			buf = poolWriteNonFTOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
		}
	case 8:
		ls5 := tx.Outputs[5].LockingScript.Bytes()
		pkh := poolP2PKHPubKeyHashHex(ls5)
		if !IsServiceFeePkh(pkh) {
			buf = append(buf, 0x00)
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[5].Satoshis, ls5)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[6].Satoshis, tx.Outputs[6].LockingScript.Bytes())
			buf = poolWriteNonFTOutput(buf, tx.Outputs[7].Satoshis, tx.Outputs[7].LockingScript.Bytes())
		} else {
			buf = poolWriteNonFTOutput(buf, tx.Outputs[5].Satoshis, ls5)
			ls6 := tx.Outputs[6].LockingScript.Bytes()
			buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[6].Satoshis, ls6)
			if err != nil {
				return nil, err
			}
			buf = poolWriteTapeOutput(buf, tx.Outputs[7].Satoshis, tx.Outputs[7].LockingScript.Bytes())
			buf = append(buf, 0x00)
		}
	case 9:
		// 完整输出: TS for i=5;i<9;i++.
		for i := 5; i < 9; i++ {
			ls := tx.Outputs[i].LockingScript.Bytes()
			if poolIsFTOrCoinScript(len(ls)) {
				if i+1 >= len(tx.Outputs) {
					return nil, fmt.Errorf("poolSwap3Option2OutputsData: case 9 missing tape for FT at output[%d]", i)
				}
				buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[i].Satoshis, ls)
				if err != nil {
					return nil, err
				}
				buf = poolWriteTapeOutput(buf, tx.Outputs[i+1].Satoshis, tx.Outputs[i+1].LockingScript.Bytes())
				i++
			} else {
				buf = poolWriteNonFTOutput(buf, tx.Outputs[i].Satoshis, ls)
			}
		}
	default:
		return nil, fmt.Errorf("poolSwap3Option2OutputsData: invalid transaction (outputs=%d)", len(tx.Outputs))
	}
	return buf, nil
}

// --------------------------------------------------------------------------
// case 4: Swap TBC->FT (TS lines 1662-1703).
// Single fixed layout: 5 outputs - poolnft code/tape, FTAbyC code/tape, P2PKH change.
// --------------------------------------------------------------------------

func poolSwapTBCtoFTOutputsData(tx *bt.Tx) ([]byte, error) {
	if len(tx.Outputs) < 5 {
		return nil, fmt.Errorf("poolSwapTBCtoFTOutputsData: option=4 requires at least 5 outputs, got %d", len(tx.Outputs))
	}
	var buf []byte
	var err error

	// poolnft code+tape
	buf = poolWritePoolNFTHashOutput(buf, tx.Outputs[0].Satoshis, tx.Outputs[0].LockingScript.Bytes())
	buf = poolWriteTapeOutput(buf, tx.Outputs[1].Satoshis, tx.Outputs[1].LockingScript.Bytes())

	// FTAbyC code+tape (outputs 2,3)
	buf, err = poolWriteFTOutputAuto(buf, tx.Outputs[2].Satoshis, tx.Outputs[2].LockingScript.Bytes())
	if err != nil {
		return nil, err
	}
	buf = poolWriteTapeOutput(buf, tx.Outputs[3].Satoshis, tx.Outputs[3].LockingScript.Bytes())

	// 普通找零 (output 4): non-FT.
	buf = poolWriteNonFTOutput(buf, tx.Outputs[4].Satoshis, tx.Outputs[4].LockingScript.Bytes())

	return buf, nil
}
