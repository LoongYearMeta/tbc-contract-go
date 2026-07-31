package util

// Port of tbc-contract/lib/util/orderbookunlock.ts.
// OrderBook txdata helpers used when constructing order unlock scripts.
// All function names carry the OB suffix to avoid collision with the
// identically-structured helpers in ftunlock.go.

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
)

// OB-specific length constants from orderbookunlock.ts.
const (
	obFTCodeLength            = 1884
	obFTV4CodeLength          = 1948
	obCoinCodeLength          = 2012
	obBuyCodeLength           = 960 + 114  // 1074
	obSellCodeLength          = 832 + 114  // 946
	obTokenOrderLength        = 1152 + 180 // 1332
	obFTPartialOffset         = 1856
	obFTV4PartialOffset       = 1920
	obCoinPartialOffset       = 1984
	obBuyPartialOffset        = 960
	obSellPartialOffset       = 832
	obTokenOrderPartialOffset = 1152
)

// obGetSize mirrors getSize(length) in orderbookunlock.ts.
func obGetSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}

// obGetLengthHex mirrors getLengthHex(length) in orderbookunlock.ts.
func obGetLengthHex(length int) []byte {
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

func obAppendUint32LE(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func obAppendUint64LE(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return append(buf, b...)
}

// GetPreTxdataOB mirrors getPreTxdata(tx, vout, contractOutputNumber) in orderbookunlock.ts.
// vout is the output index of the contract being unlocked in tx.
// contractOutputNumber is how many consecutive outputs to treat as the contract group.
func GetPreTxdataOB(tx *bt.Tx, vout int, contractOutputNumber int) (string, error) {
	return getPreTxdataOBFixed(tx, vout, contractOutputNumber, 10)
}

// GetTokenOrderPreTxdataOB serializes a TokenOrder parent transaction using
// the twelve physical output slots consumed by the TokenOrder locking script.
func GetTokenOrderPreTxdataOB(tx *bt.Tx, vout int, contractOutputNumber int) (string, error) {
	if len(tx.Outputs) > 12 {
		return "", fmt.Errorf("GetTokenOrderPreTxdataOB: transaction has %d outputs, maximum is 12", len(tx.Outputs))
	}
	return getPreTxdataOBFixed(tx, vout, contractOutputNumber, 12)
}

func getPreTxdataOBFixed(
	tx *bt.Tx,
	vout int,
	contractOutputNumber int,
	fixedOutputCount int,
) (string, error) {
	var buf []byte

	// Version, LockTime, InputCount, OutputCount header (16 bytes)
	buf = append(buf, 0x10) // vliolength
	buf = obAppendUint32LE(buf, 10)
	buf = obAppendUint32LE(buf, tx.LockTime)
	buf = obAppendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = obAppendUint32LE(buf, uint32(len(tx.Outputs)))

	// Inputs: each prefixed with 0x28 (40 bytes: 32 txid + 4 vout + 4 seq)
	var inputHashBuf []byte
	for _, inp := range tx.Inputs {
		buf = append(buf, 0x28) // inputslength
		prevID := inp.PreviousTxID()
		for i := 31; i >= 0; i-- {
			buf = append(buf, prevID[i])
		}
		buf = obAppendUint32LE(buf, inp.PreviousTxOutIndex)
		buf = obAppendUint32LE(buf, inp.SequenceNumber)
		inputHashBuf = append(inputHashBuf, crypto.Sha256(inp.UnlockingScript.Bytes())...)
	}
	// Pad to 10 inputs
	for i := len(tx.Inputs); i < 10; i++ {
		buf = append(buf, 0x00)
	}

	// UnlockingScriptHash
	buf = append(buf, 0x20)
	buf = append(buf, crypto.Sha256(inputHashBuf)...)

	// Outputs
	i := 0
	for i < len(tx.Outputs) {
		out := tx.Outputs[i]
		lockScript := out.LockingScript.Bytes()
		scriptLen := len(lockScript)
		sizeBytes := obGetSize(scriptLen)
		isCurrentContract := (i == vout)

		var partialHash string
		var suffixData []byte

		if isCurrentContract {
			partialOffset := 0
			if scriptLen == obBuyCodeLength {
				partialOffset = obBuyPartialOffset
			} else if scriptLen == obSellCodeLength {
				partialOffset = obSellPartialOffset
			} else if scriptLen == obTokenOrderLength {
				partialOffset = obTokenOrderPartialOffset
			}
			partialHash = partialsha256.CalculatePartialHash(lockScript[:partialOffset])
			suffixData = lockScript[partialOffset:]
		} else {
			if scriptLen < 64 {
				partialHash = "00"
				suffixData = lockScript
			} else {
				maxOff := (scriptLen / 64) * 64
				partialHash = partialsha256.CalculatePartialHash(lockScript[:maxOff])
				suffixData = lockScript[maxOff:]
			}
		}

		buf = append(buf, 0x08)
		buf = obAppendUint64LE(buf, out.Satoshis)
		buf = append(buf, obGetLengthHex(len(suffixData))...)
		buf = append(buf, suffixData...)

		if len(partialHash) > 2 || isCurrentContract {
			buf = append(buf, 0x20)
		}
		phBytes, err := hex.DecodeString(partialHash)
		if err != nil {
			return "", fmt.Errorf("GetPreTxdataOB: decode partial hash: %w", err)
		}
		buf = append(buf, phBytes...)
		buf = append(buf, obGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		if isCurrentContract {
			for j := 1; j < contractOutputNumber; j++ {
				if i+j >= len(tx.Outputs) {
					break
				}
				nextOut := tx.Outputs[i+j]
				nextScript := nextOut.LockingScript.Bytes()
				nextSize := obGetSize(len(nextScript))
				buf = append(buf, 0x08)
				buf = obAppendUint64LE(buf, nextOut.Satoshis)
				buf = append(buf, obGetLengthHex(len(nextScript))...)
				buf = append(buf, nextScript...)
				buf = append(buf, 0x00)
				buf = append(buf, obGetLengthHex(len(nextSize))...)
				buf = append(buf, nextSize...)
			}
			i += contractOutputNumber - 1
		}
		i++
	}
	for i := len(tx.Outputs); i < fixedOutputCount; i++ {
		buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	}

	return hex.EncodeToString(buf), nil
}

// GetCurrentTxOutputsDataOB mirrors getCurrentTxOutputsData(tx) in
// orderbookunlock.ts with its default fixedOutputCount of 10.
func GetCurrentTxOutputsDataOB(tx *bt.Tx) (string, error) {
	return GetCurrentTxOutputsDataOBFixed(tx, 10)
}

// GetCurrentTxOutputsDataOBFixed mirrors
// getCurrentTxOutputsData(tx, fixedOutputCount) in orderbookunlock.ts.
func GetCurrentTxOutputsDataOBFixed(tx *bt.Tx, fixedOutputCount int) (string, error) {
	if fixedOutputCount < 0 {
		return "", fmt.Errorf("GetCurrentTxOutputsDataOBFixed: fixed output count must be non-negative")
	}
	var buf []byte
	i := 0
	for i < len(tx.Outputs) {
		out := tx.Outputs[i]
		lockScript := out.LockingScript.Bytes()
		scriptLen := len(lockScript)
		sizeBytes := obGetSize(scriptLen)

		partialOffset := 0
		if scriptLen == obFTCodeLength {
			partialOffset = obFTPartialOffset
		} else if scriptLen == obFTV4CodeLength {
			partialOffset = obFTV4PartialOffset
		} else if scriptLen == obCoinCodeLength {
			partialOffset = obCoinPartialOffset
		} else if scriptLen == obBuyCodeLength {
			partialOffset = obBuyPartialOffset
		} else if scriptLen == obSellCodeLength {
			partialOffset = obSellPartialOffset
		} else if scriptLen == obTokenOrderLength {
			partialOffset = obTokenOrderPartialOffset
		}

		isSpecial := partialOffset > 0
		var partialHash string
		var suffixData []byte

		if isSpecial {
			partialHash = partialsha256.CalculatePartialHash(lockScript[:partialOffset])
			suffixData = lockScript[partialOffset:]
		} else {
			if scriptLen < 64 {
				partialHash = "00"
				suffixData = lockScript
			} else {
				maxOff := (scriptLen / 64) * 64
				partialHash = partialsha256.CalculatePartialHash(lockScript[:maxOff])
				suffixData = lockScript[maxOff:]
			}
		}

		buf = append(buf, 0x08)
		buf = obAppendUint64LE(buf, out.Satoshis)
		buf = append(buf, obGetLengthHex(len(suffixData))...)
		buf = append(buf, suffixData...)

		if isSpecial {
			buf = append(buf, 0x20)
		}
		phBytes, err := hex.DecodeString(partialHash)
		if err != nil {
			return "", fmt.Errorf("GetCurrentTxOutputsDataOB: decode partial hash: %w", err)
		}
		buf = append(buf, phBytes...)
		buf = append(buf, obGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		// FT / Coin code outputs are always followed by their tape as a pair.
		if scriptLen == obFTCodeLength || scriptLen == obFTV4CodeLength || scriptLen == obCoinCodeLength {
			if i+1 < len(tx.Outputs) {
				nextOut := tx.Outputs[i+1]
				nextScript := nextOut.LockingScript.Bytes()
				buf = append(buf, 0x08)
				buf = obAppendUint64LE(buf, nextOut.Satoshis)
				buf = append(buf, obGetLengthHex(len(nextScript))...)
				buf = append(buf, nextScript...)
				i++ // consumed the tape output
			}
		}
		i++
	}

	// JS pads each missing logical output with four zero bytes, except for
	// the two terminal bytes already represented by the contract layout.
	paddingCount := 0
	if missingOutputCount := fixedOutputCount - len(tx.Outputs); missingOutputCount > 0 {
		paddingCount = missingOutputCount*4 - 2
	}
	for i := 0; i < paddingCount; i++ {
		buf = append(buf, 0x00)
	}

	return hex.EncodeToString(buf), nil
}
