package util

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
)

var reSHA256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var reHex = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// FtUTXO mirrors what TS treats as Transaction.IUnspentOutput + ftBalance.
// Field types align with bt.UTXO so FtUTXOToUTXO() is zero-copy.
type FtUTXO struct {
	TxID           []byte
	Vout           uint32
	LockingScript  *bscript.Script
	Satoshis       uint64
	SequenceNumber uint32
	FtBalance      *big.Int
}

// NFTInfo is the canonical NFT metadata struct, used both by lib/api/api_nft.go
// (response shape) and by lib/contract/nft.go (constructor input). Defined here
// to avoid an api↔contract import cycle.
type NFTInfo struct {
	CollectionID         string
	CollectionIndex      int
	CollectionName       string
	NFTName              string
	NFTSymbol            string
	NFTAttributes        string
	NFTDescription       string
	NFTTransferTimeCount int
	NFTIcon              string
}

// NFTBrief is the listing-row form returned by FetchNFTs.
type NFTBrief struct {
	NFTHolder     string
	NFTContractID string
}

// FtUTXOToUTXO drops the FtBalance field, returning a plain *bt.UTXO suitable
// as a transaction input.
func FtUTXOToUTXO(f *FtUTXO) *bt.UTXO {
	return &bt.UTXO{
		TxID:           f.TxID,
		Vout:           f.Vout,
		LockingScript:  f.LockingScript,
		Satoshis:       f.Satoshis,
		SequenceNumber: f.SequenceNumber,
	}
}

// FtUTXOsToUTXOs is the slice form of FtUTXOToUTXO.
func FtUTXOsToUTXOs(list []*FtUTXO) []*bt.UTXO {
	result := make([]*bt.UTXO, len(list))
	for i, f := range list {
		result[i] = FtUTXOToUTXO(f)
	}
	return result
}

// BuildUTXO constructs a FtUTXO from a tx output, optionally reading the FT
// balance from the tape output at vout+1. Mirrors TS buildUTXO(tx, vout, isFT?).
func BuildUTXO(tx *bt.Tx, vout int, isFT bool) (*FtUTXO, error) {
	if vout >= len(tx.Outputs) {
		return nil, errors.New("output at index does not exist")
	}
	output := tx.Outputs[vout]
	var ftBalance *big.Int
	if isFT && vout+1 < len(tx.Outputs) {
		var err error
		ftBalance, err = GetFtBalanceFromTape(hex.EncodeToString(tx.Outputs[vout+1].LockingScript.Bytes()))
		if err != nil {
			ftBalance = big.NewInt(0)
		}
	} else {
		ftBalance = big.NewInt(0)
	}
	txIDBytes, err := hex.DecodeString(tx.TxID())
	if err != nil {
		return nil, err
	}
	return &FtUTXO{
		TxID:          txIDBytes,
		Vout:          uint32(vout),
		LockingScript: output.LockingScript,
		Satoshis:      output.Satoshis,
		FtBalance:     ftBalance,
	}, nil
}

// BuildFtPrePreTxData walks preTX's tape, recursively pulling pre-pre tx data
// from localTXs. Mirrors TS buildFtPrePreTxData.
func BuildFtPrePreTxData(preTX *bt.Tx, preTxVout int, localTXs []*bt.Tx) (string, error) {
	if preTxVout+1 >= len(preTX.Outputs) {
		return "", errors.New("output at index does not exist")
	}
	tapeScript := preTX.Outputs[preTxVout+1].LockingScript.Bytes()
	if len(tapeScript) < 51 {
		return "", errors.New("tape script too short")
	}
	tapeSlice := tapeScript[3:51]
	tapeHex := hex.EncodeToString(tapeSlice)

	var prepretxdata string
	for i := len(tapeHex) - 16; i >= 0; i -= 16 {
		chunk := tapeHex[i : i+16]
		if chunk != "0000000000000000" {
			inputIndex := i / 16
			if inputIndex >= len(preTX.Inputs) {
				return "", errors.New("input index out of range")
			}
			prevTxID := hex.EncodeToString(preTX.Inputs[inputIndex].PreviousTxID())
			prepreTX, err := SelectTXfromLocal(localTXs, prevTxID)
			if err != nil {
				return "", err
			}
			data, err := GetPrePreTxdata(prepreTX, int(preTX.Inputs[inputIndex].PreviousTxOutIndex))
			if err != nil {
				return "", err
			}
			prepretxdata += data
		}
	}
	return "57" + prepretxdata, nil
}

// GetFtBalanceFromTape reads the 6-slot uint64 tape (48 bytes) and sums it.
// Mirrors TS getFtBalanceFromTape.
func GetFtBalanceFromTape(tapeHex string) (*big.Int, error) {
	data, err := hex.DecodeString(tapeHex)
	if err != nil {
		return nil, fmt.Errorf("invalid tape hex: %w", err)
	}
	if len(data) < 51 {
		return nil, errors.New("tape too short")
	}
	tapeSlice := data[3 : 3+48]
	balance := new(big.Int)
	for i := 0; i < 6; i++ {
		v := binary.LittleEndian.Uint64(tapeSlice[i*8 : i*8+8])
		balance.Add(balance, new(big.Int).SetUint64(v))
	}
	return balance, nil
}

// SelectTXfromLocal returns the tx in txs whose hash matches txid.
func SelectTXfromLocal(txs []*bt.Tx, txid string) (*bt.Tx, error) {
	for _, tx := range txs {
		if tx.TxID() == txid {
			return tx, nil
		}
	}
	return nil, fmt.Errorf("transaction not found: %s", txid)
}

// ParseDecimalToBigInt is the Go equivalent of TS parseDecimalToBigInt(amount, decimal).
// Crucially: takes the raw decimal string, never goes through float64.
func ParseDecimalToBigInt(amount string, decimal int) (*big.Int, error) {
	s := strings.TrimSpace(amount)
	parts := strings.SplitN(s, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		intPart = "0"
	}
	frac := ""
	if len(parts) > 1 {
		frac = parts[1]
	}
	for len(frac) < decimal {
		frac += "0"
	}
	if len(frac) > decimal {
		frac = frac[:decimal]
	}
	combined := intPart + frac
	n := new(big.Int)
	if _, ok := n.SetString(combined, 10); !ok {
		return nil, fmt.Errorf("invalid decimal string: %q", amount)
	}
	return n, nil
}

// IsValidSHA256Hash is true when s is exactly 64 hex characters.
func IsValidSHA256Hash(s string) bool {
	return reSHA256Hex.MatchString(s)
}

// IsValidHexString is true when s is even-length and only [0-9a-fA-F].
func IsValidHexString(s string) bool {
	if s == "" {
		return false
	}
	return reHex.MatchString(s)
}

// IsLock returns whether lockTime indicates a frozen UTXO. Mirrors TS isLock.
// TS: length > 6600 → 1 (locked), else 0 (unlocked).
func IsLock(lockTime uint32) bool {
	return lockTime > 6600
}

// SafeJSONParse mirrors TS safeJSONParse — returns the parsed value or an error
// without panicking on malformed input.
func SafeJSONParse(data []byte) (interface{}, error) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// GetOpCode returns the opcode string for a small integer 0..255. Mirrors TS getOpCode.
// For 0..15 it returns OP_N; for 16..255 it returns the 2-char hex value.
func GetOpCode(n int) (string, error) {
	if n < 0 {
		return "", errors.New("number must be >= 0")
	}
	if n < 16 {
		names := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"}
		return "OP_" + names[n], nil
	}
	if n < 256 {
		return fmt.Sprintf("%02x", n), nil
	}
	return "", errors.New("number must be < 256")
}

type lpCostData struct {
	pubKeyHash []byte
	amount     []byte
}

func getLpCostData(poolCode string) (lpCostData, error) {
	if poolCode == "" || len(poolCode)%2 != 0 {
		return lpCostData{}, errors.New("Invalid pool code")
	}
	poolScript, err := bscript.NewFromHexString(poolCode)
	if err != nil {
		return lpCostData{}, errors.New("Invalid pool code")
	}
	chunks := poolScript.Chunks()
	matches := make([]lpCostData, 0, 1)
	for i := 0; i+5 < len(chunks); i++ {
		addressChunk := chunks[i]
		amountChunk := chunks[i+4]
		if len(addressChunk.Buf) == 20 &&
			chunks[i+1].OpcodeNum == bscript.OpEQUALVERIFY &&
			chunks[i+2].OpcodeNum == bscript.OpPARTIALHASH &&
			chunks[i+3].OpcodeNum == bscript.OpOVER &&
			len(amountChunk.Buf) == 8 &&
			chunks[i+5].OpcodeNum == bscript.OpEQUALVERIFY {
			matches = append(matches, lpCostData{
				pubKeyHash: append([]byte(nil), addressChunk.Buf...),
				amount:     append([]byte(nil), amountChunk.Buf...),
			})
		}
	}
	if len(matches) != 1 {
		return lpCostData{}, errors.New("Invalid locked pool code: expected exactly one LP cost entry")
	}
	return matches[0], nil
}

// GetLpCostAddress mirrors JS 1.6.6 getLpCostAddress and locates the LP fee
// entry by opcode context so it remains valid across Pool code versions.
func GetLpCostAddress(poolCode string) (string, error) {
	data, err := getLpCostData(poolCode)
	if err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromPublicKeyHash(data.pubKeyHash, true)
	if err != nil {
		return "", fmt.Errorf("address from pubKeyHash: %w", err)
	}
	return addr.AddressString, nil
}

// GetLpCostAmount mirrors JS 1.6.6 getLpCostAmount.
func GetLpCostAmount(poolCode string) (uint64, error) {
	data, err := getLpCostData(poolCode)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(data.amount), nil
}

// scriptHash computes the TBC indexer's scriptpubkeyhash (reversed SHA256).
// Used internally in api packages.
func ScriptHash(scriptHex string) (string, error) {
	b, err := hex.DecodeString(scriptHex)
	if err != nil {
		return "", err
	}
	h := crypto.Sha256(b)
	rev := bt.ReverseBytes(h)
	return hex.EncodeToString(rev), nil
}
