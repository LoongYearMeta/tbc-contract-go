package util

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/encoding"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"

	bt "github.com/LoongYearMeta/tbc-lib-go"
)

const (
	ftVersion   = 10
	ftHashLen   = 32
	ftAmountLen = 8

	// Constants align with tbc-contract/lib/util/ftunlock.ts
	ftV1Length        = 1564
	ftV1PartialOffset = 1536
	ftV2Length        = 1884
	ftV2PartialOffset = 1856
	ftV4Length        = 1948
	ftV4PartialOffset = 1920
	coinLength        = 2012
	coinPartialOffset = 1984
)

// partialOffsetGetPreTx corresponds to getPreTxdata (preTxdata path).
// Matches tbc-contract/lib/util/ftunlock.ts:
// - coin exact match → coin_partial_offset
// - ft v2 range [ft_v2_length, coin_length) → ft_v2_partial_offset
// - else default ft_v1_partial_offset
func partialOffsetGetPreTx(scriptLen int) int {
	if scriptLen == coinLength {
		return coinPartialOffset
	}
	if scriptLen == ftV4Length {
		return ftV4PartialOffset
	}
	if scriptLen >= ftV2Length && scriptLen < coinLength {
		return ftV2PartialOffset
	}
	return ftV1PartialOffset
}

// partialOffsetGetPrePre corresponds to getPrePreTxdata / getCurrentTxdata FT code+tape pair branch.
// Matches ftunlock.ts: v1/coin exact match; v2 is range [ft_v2_length, coin_length); else off=0 for generic split.
func partialOffsetGetPrePre(scriptLen int) int {
	switch scriptLen {
	case ftV1Length:
		return ftV1PartialOffset
	case ftV4Length:
		return ftV4PartialOffset
	case coinLength:
		return coinPartialOffset
	default:
		if scriptLen >= ftV2Length && scriptLen < coinLength {
			return ftV2PartialOffset
		}
		return 0
	}
}

// getLengthHex returns variable-length integer encoding (OP_PUSHDATA1/2).
func getLengthHex(length int) []byte {
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

// getSize returns script length in little-endian encoding.
func getSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}

// getPrePreOutputsData gets grandparent outputs1/outputs2.
// When vout==0, outputs1=0x00 (no length); outputs2 is empty when 0x00 (no length).
func getPrePreOutputsData(tx *bt.Tx, vout int) (outputs1, outputs1len, outputs2, outputs2len []byte) {
	if vout > 0 {
		var buf1 []byte
		for i := 0; i < vout; i++ {
			sat := make([]byte, 8)
			binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
			buf1 = append(buf1, sat...)
			buf1 = append(buf1, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
		}
		outputs1 = buf1
		outputs1len = getLengthHex(len(buf1))
	} else {
		outputs1 = []byte{0x00}
	}
	var buf2 []byte
	for i := vout + 1; i < len(tx.Outputs); i++ {
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
		buf2 = append(buf2, sat...)
		buf2 = append(buf2, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	if len(buf2) > 0 {
		outputs2 = buf2
		outputs2len = getLengthHex(len(buf2))
	} else {
		outputs2 = []byte{0x00}
	}
	return
}

// GetPrePreTxdata gets grandparent transaction txdata for FT unlock.
// Corresponds to JS ftunlock.getPrePreTxdata.
func GetPrePreTxdata(tx *bt.Tx, vout int) (string, error) {
	var buf []byte
	// vliolength: 0x10 (version + nLockTime + inputCount + outputCount = 16 bytes)
	buf = append(buf, 0x10)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, ftVersion)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	buf = append(buf, b...)

	var inputBuf1, inputBuf2 []byte
	for _, in := range tx.Inputs {
		prevID := encoding.ReverseBytes(in.PreviousTxID())
		inputBuf1 = append(inputBuf1, prevID...)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		inputBuf1 = append(inputBuf1, oi...)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		inputBuf1 = append(inputBuf1, oi...)
		scriptHash := crypto.Sha256(in.UnlockingScript.Bytes())
		inputBuf2 = append(inputBuf2, scriptHash...)
	}
	hash1 := crypto.Sha256(inputBuf1)
	hash2 := crypto.Sha256(inputBuf2)
	buf = append(buf, 0x20)
	buf = append(buf, hash1...)
	buf = append(buf, 0x20)
	buf = append(buf, hash2...)

	o1, o1len, o2, o2len := getPrePreOutputsData(tx, vout)
	buf = append(buf, o1len...)
	buf = append(buf, o1...)

	lockScript := tx.Outputs[vout].LockingScript.Bytes()
	scriptLen := len(lockScript)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, tx.Outputs[vout].Satoshis)
	buf = append(buf, 0x08)
	buf = append(buf, sat...)

	if off := partialOffsetGetPrePre(scriptLen); off > 0 {
		suffix := lockScript[off:]
		partialHash := partialsha256.CalculatePartialHash(lockScript[:off])
		ph, err := hex.DecodeString(partialHash)
		if err != nil {
			return "", fmt.Errorf("GetPrePreTxdata: decode partial hash: %w", err)
		}
		buf = append(buf, getLengthHex(len(suffix))...)
		buf = append(buf, suffix...)
		buf = append(buf, 0x20)
		buf = append(buf, ph...)
		buf = append(buf, getLengthHex(len(getSize(scriptLen)))...)
		buf = append(buf, getSize(scriptLen)...)
	} else {
		var suffix, ph []byte
		if scriptLen < 64 {
			suffix = lockScript
			ph = []byte{0x00}
		} else {
			n := scriptLen / 64
			partialLen := 64 * n
			phStr := partialsha256.CalculatePartialHash(lockScript[:partialLen])
			var err error
			ph, err = hex.DecodeString(phStr)
			if err != nil {
				return "", fmt.Errorf("GetPrePreTxdata: decode partial hash: %w", err)
			}
			suffix = lockScript[partialLen:]
		}
		buf = append(buf, getLengthHex(len(suffix))...)
		buf = append(buf, suffix...)
		if len(ph) == 1 {
			buf = append(buf, ph...)
		} else {
			buf = append(buf, 0x20)
			buf = append(buf, ph...)
		}
		buf = append(buf, getLengthHex(len(getSize(scriptLen)))...)
		buf = append(buf, getSize(scriptLen)...)
	}

	buf = append(buf, o2len...)
	buf = append(buf, o2...)

	return hex.EncodeToString(buf) + "52", nil
}

// getPreOutputsData gets parent outputs1/outputs2.
// outputs2 starts from vout+2 (skipping code and tape outputs).
func getPreOutputsData(tx *bt.Tx, vout int) (outputs1, outputs1len, outputs2, outputs2len []byte) {
	if vout > 0 {
		var buf1 []byte
		for i := 0; i < vout; i++ {
			sat := make([]byte, 8)
			binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
			buf1 = append(buf1, sat...)
			buf1 = append(buf1, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
		}
		outputs1 = buf1
		outputs1len = getLengthHex(len(buf1))
	} else {
		outputs1 = []byte{0x00}
	}
	var buf2 []byte
	for i := vout + 2; i < len(tx.Outputs); i++ {
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
		buf2 = append(buf2, sat...)
		buf2 = append(buf2, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	if len(buf2) > 0 {
		outputs2 = buf2
		outputs2len = getLengthHex(len(buf2))
	} else {
		outputs2 = []byte{0x00}
		outputs2len = []byte{}
	}
	return
}

// GetPreTxdata gets parent transaction txdata for FT unlock.
// Corresponds to JS ftunlock.getPreTxdata.
func GetPreTxdata(tx *bt.Tx, vout int) (string, error) {
	if vout+1 >= len(tx.Outputs) {
		return "", fmt.Errorf("vout+1 out of range")
	}
	var buf []byte
	buf = append(buf, 0x10)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, ftVersion)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	buf = append(buf, b...)

	var inputBuf1, inputBuf2 []byte
	for _, in := range tx.Inputs {
		prevID := encoding.ReverseBytes(in.PreviousTxID())
		inputBuf1 = append(inputBuf1, prevID...)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		inputBuf1 = append(inputBuf1, oi...)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		inputBuf1 = append(inputBuf1, oi...)
		scriptHash := crypto.Sha256(in.UnlockingScript.Bytes())
		inputBuf2 = append(inputBuf2, scriptHash...)
	}
	buf = append(buf, getLengthHex(len(inputBuf1))...)
	buf = append(buf, inputBuf1...)
	buf = append(buf, 0x20)
	buf = append(buf, crypto.Sha256(inputBuf2)...)

	o1, o1len, o2, o2len := getPreOutputsData(tx, vout)
	buf = append(buf, o1len...)
	buf = append(buf, o1...)

	lockScript := tx.Outputs[vout].LockingScript.Bytes()
	scriptLen := len(lockScript)
	off := partialOffsetGetPreTx(scriptLen)
	if scriptLen < off {
		return "", fmt.Errorf("lock script too short for FT")
	}
	suffix := lockScript[off:]
	partialHash := partialsha256.CalculatePartialHash(lockScript[:off])
	ph, err := hex.DecodeString(partialHash)
	if err != nil {
		return "", fmt.Errorf("GetPreTxdata: decode partial hash: %w", err)
	}

	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, tx.Outputs[vout].Satoshis)
	buf = append(buf, 0x08)
	buf = append(buf, sat...)
	buf = append(buf, getLengthHex(len(suffix))...)
	buf = append(buf, suffix...)
	buf = append(buf, 0x20)
	buf = append(buf, ph...)
	buf = append(buf, getLengthHex(len(getSize(scriptLen)))...)
	buf = append(buf, getSize(scriptLen)...)

	binary.LittleEndian.PutUint64(sat, tx.Outputs[vout+1].Satoshis)
	buf = append(buf, 0x08)
	buf = append(buf, sat...)
	buf = append(buf, getLengthHex(len(tx.Outputs[vout+1].LockingScript.Bytes()))...)
	buf = append(buf, tx.Outputs[vout+1].LockingScript.Bytes()...)

	buf = append(buf, o2len...)
	buf = append(buf, o2...)

	return hex.EncodeToString(buf), nil
}

// GetCurrentTxdata gets current transaction txdata for FT unlock.
// Corresponds to JS ftunlock.getCurrentTxdata.
func GetCurrentTxdata(tx *bt.Tx, inputIndex int) (string, error) {
	inputIndexMap := map[int]byte{0: 0x00, 1: 0x51, 2: 0x52, 3: 0x53, 4: 0x54, 5: 0x55}
	endTag := byte(0x51)
	var buf []byte
	buf = append(buf, endTag)

	for i := 0; i < len(tx.Outputs); i++ {
		lockScript := tx.Outputs[i].LockingScript.Bytes()
		scriptLen := len(lockScript)

		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
		buf = append(buf, 0x08)
		buf = append(buf, sat...)

		if off := partialOffsetGetPrePre(scriptLen); off > 0 {
			suffix := lockScript[off:]
			partialHash := partialsha256.CalculatePartialHash(lockScript[:off])
			ph, err := hex.DecodeString(partialHash)
			if err != nil {
				return "", fmt.Errorf("GetCurrentTxdata: decode partial hash: %w", err)
			}
			buf = append(buf, getLengthHex(len(suffix))...)
			buf = append(buf, suffix...)
			buf = append(buf, 0x20)
			buf = append(buf, ph...)
			size := getSize(scriptLen)
			buf = append(buf, getLengthHex(len(size))...)
			buf = append(buf, size...)

			i++
			binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
			buf = append(buf, 0x08)
			buf = append(buf, sat...)
			buf = append(buf, getLengthHex(len(tx.Outputs[i].LockingScript.Bytes()))...)
			buf = append(buf, tx.Outputs[i].LockingScript.Bytes()...)
		} else {
			// Aligns with JS ftunlock.getCurrentTxdata:
			// when off==0, if scriptLen < 64: suffixdata=full, partialhash='00'
			// when scriptLen >= 64: suffixdata=tail (after 64*n), partialhash=partial_sha256(head 64*n)
			var suffixdata []byte
			var suffixPartialHash []byte
			if scriptLen < 64 {
				suffixdata = lockScript
				suffixPartialHash = []byte{0x00}
			} else {
				n := scriptLen / 64
				partialLength := 64 * n
				partialHashHex := partialsha256.CalculatePartialHash(lockScript[:partialLength])
				ph, err := hex.DecodeString(partialHashHex)
				if err != nil {
					return "", fmt.Errorf("GetCurrentTxdata: decode partial hash: %w", err)
				}
				suffixdata = lockScript[partialLength:]
				suffixPartialHash = ph
			}

			buf = append(buf, getLengthHex(len(suffixdata))...)
			buf = append(buf, suffixdata...)

			if len(suffixPartialHash) == 1 && suffixPartialHash[0] == 0x00 {
				buf = append(buf, 0x00)
			} else {
				buf = append(buf, 0x20)
				buf = append(buf, suffixPartialHash...)
			}

			size := getSize(scriptLen)
			buf = append(buf, getLengthHex(len(size))...)
			buf = append(buf, size...)
		}
		buf = append(buf, 0x52)
	}

	if idx, ok := inputIndexMap[inputIndex]; ok {
		buf = append(buf, idx)
	}
	return hex.EncodeToString(buf), nil
}

// GetCurrentInputsdata gets current transaction inputs data.
// Corresponds to JS ftunlock.getCurrentInputsdata.
func GetCurrentInputsdata(tx *bt.Tx) (string, error) {
	var inputBuf []byte
	for _, in := range tx.Inputs {
		prevID := encoding.ReverseBytes(in.PreviousTxID())
		inputBuf = append(inputBuf, prevID...)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		inputBuf = append(inputBuf, oi...)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		inputBuf = append(inputBuf, oi...)
	}
	buf := append(getLengthHex(len(inputBuf)), inputBuf...)
	return hex.EncodeToString(buf), nil
}

// GetContractTxdata gets contract transaction data.
// Corresponds to JS ftunlock.getContractTxdata (including vout<0 branch for FT v2 swap).
func GetContractTxdata(tx *bt.Tx, vout int) (string, error) {
	var buf []byte
	buf = append(buf, 0x10)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, ftVersion)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	buf = append(buf, b...)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	buf = append(buf, b...)

	var inputBuf1, inputBuf2 []byte
	for _, in := range tx.Inputs {
		prevID := encoding.ReverseBytes(in.PreviousTxID())
		inputBuf1 = append(inputBuf1, prevID...)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		inputBuf1 = append(inputBuf1, oi...)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		inputBuf1 = append(inputBuf1, oi...)
		scriptHash := crypto.Sha256(in.UnlockingScript.Bytes())
		inputBuf2 = append(inputBuf2, scriptHash...)
	}
	buf = append(buf, 0x20)
	buf = append(buf, crypto.Sha256(inputBuf1)...)
	buf = append(buf, 0x20)
	buf = append(buf, crypto.Sha256(inputBuf2)...)

	if vout < 0 {
		for i := 0; i < len(tx.Outputs); i++ {
			buf = append(buf, 0x08)
			sat := make([]byte, 8)
			binary.LittleEndian.PutUint64(sat, tx.Outputs[i].Satoshis)
			buf = append(buf, sat...)
			buf = append(buf, 0x20)
			buf = append(buf, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
		}
		for i := len(tx.Outputs); i < 15; i++ {
			buf = append(buf, 0x00)
			buf = append(buf, 0x00)
		}
		return hex.EncodeToString(buf), nil
	}

	if vout >= len(tx.Outputs) {
		return "", fmt.Errorf("vout out of range")
	}

	o1, o1len, o2, o2len := getPrePreOutputsData(tx, vout)
	buf = append(buf, o1len...)
	buf = append(buf, o1...)

	buf = append(buf, 0x08)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, tx.Outputs[vout].Satoshis)
	buf = append(buf, sat...)
	buf = append(buf, 0x20)
	buf = append(buf, crypto.Sha256(tx.Outputs[vout].LockingScript.Bytes())...)
	buf = append(buf, o2len...)
	buf = append(buf, o2...)

	return hex.EncodeToString(buf), nil
}

// GetSize returns the script length in little-endian encoding.
// Corresponds to JS ftunlock.getSize. Renamed from GetSizeHex to match TS naming.
func GetSize(length int) []byte {
	return getSize(length)
}

// GetTapePushSize returns the raw length bytes used in stablecoin tape scripts.
// Mirrors ftunlock.getSize for lengths ≤ 65535.
// Moved here from nftunlock.go: this function is FT-tape-related, not NFT-related.
func GetTapePushSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}
