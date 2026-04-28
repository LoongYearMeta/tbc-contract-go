package util

// Port of tbc-contract/lib/util/nftunlock.ts.
// NFT txdata helpers (getCurrentTxdata, getPreTxdata, getPrePreTxdata) and
// Go-specific NftEncodeMinimalPushData for SCRIPT_VERIFY_MINIMALDATA compliance.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/script"
)

const nftUnlockTxVersion = 10

var (
	nftVlioLen   = []byte{0x10}
	nftAmountLen = []byte{0x08}
	nftHashLen   = []byte{0x20}
)

// NftGetLengthHex returns the raw push-data length prefix bytes for the given
// byte length, aligning with getLengthHex in nftunlock.ts.
// Prefer NftEncodeMinimalPushData when you have the actual data slice.
func NftGetLengthHex(length int) []byte {
	if length < 76 {
		return []byte{byte(length)}
	}
	if length <= 255 {
		return []byte{0x4c, byte(length)}
	}
	if length <= 65535 {
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(length))
		return append([]byte{0x4d}, b...)
	}
	// length <= 0xFFFFFFFF
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(length))
	return append([]byte{0x4e}, b...)
}

// NftEncodeMinimalPushData encodes data as a single minimal push satisfying
// SCRIPT_VERIFY_MINIMALDATA. Go-specific: tbc-lib-js encodes minimally
// automatically; this wrapper is required for NFT/coinNft unlock scripts in Go.
//
// Rules (match interpreter enforceMinimumDataPush):
//   - empty  → OP_0 (0x00)
//   - 1 byte, value 1..16 → OP_1..OP_16 (0x51..0x60)
//   - 1 byte, value 0x81 → OP_1NEGATE (0x4f)
//   - otherwise → standard PushDataPrefix + data
func NftEncodeMinimalPushData(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return []byte{0x00}, nil
	}
	if len(data) == 1 {
		b := data[0]
		if b >= 1 && b <= 16 {
			return []byte{0x50 + b}, nil // OP_1=0x51 … OP_16=0x60
		}
		if b == 0x81 {
			return []byte{0x4f}, nil // OP_1NEGATE
		}
	}
	prefix, err := script.PushDataPrefix(data)
	if err != nil {
		return nil, fmt.Errorf("NftEncodeMinimalPushData: data exceeds 4 GiB: %w", err)
	}
	return append(prefix, data...), nil
}

// nftAppendOutputsData mirrors getOutputsData(tx, fromIdx) in nftunlock.ts.
// Returns the encoded bytes (length-prefixed or bare 00 if no outputs), or an error.
func nftAppendOutputsData(tx *bt.Tx, fromIdx int) ([]byte, error) {
	var w bytes.Buffer
	for i := fromIdx; i < len(tx.Outputs); i++ {
		o := tx.Outputs[i]
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, o.Satoshis)
		w.Write(sat)
		w.Write(crypto.Sha256(o.LockingScript.Bytes()))
	}
	raw := w.Bytes()
	if len(raw) == 0 {
		return []byte{0x00}, nil
	}
	return NftEncodeMinimalPushData(raw)
}

// GetNFTCurrentTxdata mirrors getCurrentTxdata(tx) in nftunlock.ts.
// Format: 08 | sat[8] | 20 | sha256(script[0]) | outputsData(1..)
func GetNFTCurrentTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("GetNFTCurrentTxdata: tx has no outputs")
	}
	o0 := tx.Outputs[0]
	var w bytes.Buffer
	w.Write(nftAmountLen)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))
	outputsData, err := nftAppendOutputsData(tx, 1)
	if err != nil {
		return "", fmt.Errorf("GetNFTCurrentTxdata: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPreTxdata mirrors getPreTxdata(tx) in nftunlock.ts.
// (no vout parameter — always references the whole NFT pre-tx)
func GetNFTPreTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 2 {
		return "", fmt.Errorf("GetNFTPreTxdata: tx needs at least 2 outputs")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(b4, uint32(nftUnlockTxVersion))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, tx.LockTime)
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Inputs)))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Outputs)))
	w.Write(b4)

	var in1, in2 bytes.Buffer
	for _, inp := range tx.Inputs {
		prevID := bt.ReverseBytes(inp.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, inp.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, inp.SequenceNumber)
		in1.Write(oi)
		in2.Write(crypto.Sha256(inp.UnlockingScript.Bytes()))
	}
	in1Push, err := NftEncodeMinimalPushData(in1.Bytes())
	if err != nil {
		return "", fmt.Errorf("GetNFTPreTxdata: encode inputs: %w", err)
	}
	w.Write(in1Push)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	sat := make([]byte, 8)
	w.Write(nftAmountLen)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))

	o1 := tx.Outputs[1]
	w.Write(nftAmountLen)
	binary.LittleEndian.PutUint64(sat, o1.Satoshis)
	w.Write(sat)
	o1Push, err := NftEncodeMinimalPushData(o1.LockingScript.Bytes())
	if err != nil {
		return "", fmt.Errorf("GetNFTPreTxdata: encode output[1]: %w", err)
	}
	w.Write(o1Push)
	outputsData, err := nftAppendOutputsData(tx, 2)
	if err != nil {
		return "", fmt.Errorf("GetNFTPreTxdata: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPrePreTxdata mirrors getPrePreTxdata(tx) in nftunlock.ts.
func GetNFTPrePreTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("GetNFTPrePreTxdata: tx has no outputs")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(b4, uint32(nftUnlockTxVersion))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, tx.LockTime)
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Inputs)))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Outputs)))
	w.Write(b4)

	var in1, in2 bytes.Buffer
	for _, inp := range tx.Inputs {
		prevID := bt.ReverseBytes(inp.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, inp.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, inp.SequenceNumber)
		in1.Write(oi)
		in2.Write(crypto.Sha256(inp.UnlockingScript.Bytes()))
	}
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	sat := make([]byte, 8)
	w.Write(nftAmountLen)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))
	outputsData, err := nftAppendOutputsData(tx, 1)
	if err != nil {
		return "", fmt.Errorf("GetNFTPrePreTxdata: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

// ---------------------------------------------------------------------------
// V0 variants (legacy NFT contract version 0)
// ---------------------------------------------------------------------------

// GetNFTCurrentTxdataV0 is the V0 variant of getCurrentTxdata.
// Output[0] script is embedded in full (length-prefixed), not hashed.
func GetNFTCurrentTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("GetNFTCurrentTxdataV0: tx has no outputs")
	}
	o0 := tx.Outputs[0]
	var w bytes.Buffer
	w.Write(nftAmountLen)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	o0Push, err := NftEncodeMinimalPushData(o0.LockingScript.Bytes())
	if err != nil {
		return "", fmt.Errorf("GetNFTCurrentTxdataV0: encode output[0]: %w", err)
	}
	w.Write(o0Push)
	outputsData, err := nftAppendOutputsData(tx, 1)
	if err != nil {
		return "", fmt.Errorf("GetNFTCurrentTxdataV0: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPreTxdataV0 is the V0 variant of getPreTxdata.
// Both output[0] and output[1] scripts are embedded in full.
func GetNFTPreTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 2 {
		return "", fmt.Errorf("GetNFTPreTxdataV0: tx needs at least 2 outputs")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(b4, uint32(nftUnlockTxVersion))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, tx.LockTime)
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Inputs)))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Outputs)))
	w.Write(b4)

	var in1, in2 bytes.Buffer
	for _, inp := range tx.Inputs {
		prevID := bt.ReverseBytes(inp.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, inp.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, inp.SequenceNumber)
		in1.Write(oi)
		in2.Write(crypto.Sha256(inp.UnlockingScript.Bytes()))
	}
	in1Push, err := NftEncodeMinimalPushData(in1.Bytes())
	if err != nil {
		return "", fmt.Errorf("GetNFTPreTxdataV0: encode inputs: %w", err)
	}
	w.Write(in1Push)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	writeOutFull := func(idx int) error {
		o := tx.Outputs[idx]
		sat := make([]byte, 8)
		w.Write(nftAmountLen)
		binary.LittleEndian.PutUint64(sat, o.Satoshis)
		w.Write(sat)
		push, err := NftEncodeMinimalPushData(o.LockingScript.Bytes())
		if err != nil {
			return fmt.Errorf("GetNFTPreTxdataV0: encode output[%d]: %w", idx, err)
		}
		w.Write(push)
		return nil
	}
	if err := writeOutFull(0); err != nil {
		return "", err
	}
	if err := writeOutFull(1); err != nil {
		return "", err
	}
	outputsData, err := nftAppendOutputsData(tx, 2)
	if err != nil {
		return "", fmt.Errorf("GetNFTPreTxdataV0: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPrePreTxdataV0 is the V0 variant of getPrePreTxdata.
// Output[0] script is embedded in full, not hashed.
func GetNFTPrePreTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("GetNFTPrePreTxdataV0: tx has no outputs")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b4 := make([]byte, 4)
	binary.LittleEndian.PutUint32(b4, uint32(nftUnlockTxVersion))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, tx.LockTime)
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Inputs)))
	w.Write(b4)
	binary.LittleEndian.PutUint32(b4, uint32(len(tx.Outputs)))
	w.Write(b4)

	var in1, in2 bytes.Buffer
	for _, inp := range tx.Inputs {
		prevID := bt.ReverseBytes(inp.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, inp.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, inp.SequenceNumber)
		in1.Write(oi)
		in2.Write(crypto.Sha256(inp.UnlockingScript.Bytes()))
	}
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	sat := make([]byte, 8)
	w.Write(nftAmountLen)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	o0Push, err := NftEncodeMinimalPushData(o0.LockingScript.Bytes())
	if err != nil {
		return "", fmt.Errorf("GetNFTPrePreTxdataV0: encode output[0]: %w", err)
	}
	w.Write(o0Push)
	outputsData, err := nftAppendOutputsData(tx, 1)
	if err != nil {
		return "", fmt.Errorf("GetNFTPrePreTxdataV0: %w", err)
	}
	w.Write(outputsData)
	return hex.EncodeToString(w.Bytes()), nil
}

