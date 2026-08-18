package contract

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

const (
	TBC20MaxInputs         = util.TBC20MaxInputs
	TBC20MaxOutputGroups   = util.TBC20MaxOutputGroups
	TBC20MaxOutputs        = util.TBC20MaxOutputs
	TBC20CodeSatoshis      = util.TBC20CodeSatoshis
	TBC20TapeSatoshis      = util.TBC20TapeSatoshis
	TBC20AmountSlots       = util.TBC20AmountSlots
	TBC20MinTapeBytes      = 61
	TBC20MaxTapeBytes      = util.TBC20MaxTapeBytes
	TBC20CodeBytes         = 2396
	TBC20CodePartialOffset = 2368
	TBC20ArtifactSHA256    = "e06235404815def601948893adab791e0ddf7b3ffc08ed1b4961e46477dabeb1"
	TBC20MetadataVersion   = 1
	TBC20MaxDecimal        = 18
)

//go:embed asm/tbc20_lock.hex
var tbc20LockHexTemplate string

var tbc20HumanPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.([0-9]+))?$`)

type TBC20Definition struct {
	Name    string
	Symbol  string
	Supply  string
	Decimal uint8
}

type TBC20Config struct {
	Definition    *TBC20Definition
	CodeScript    *bscript.Script
	TapeScript    *bscript.Script
	ContractTxID  string
	TapeSize      int
	ExtensionData []byte
}

type TBC20 struct {
	Definition        *TBC20Definition
	CodeScript        *bscript.Script
	TapeScript        *bscript.Script
	ContractTxID      string
	TapeSize          int
	ExtensionData     []byte
	DeclaredSupplyRaw *big.Int
}

type TBC20Outpoint struct {
	TxID        string
	OutputIndex uint32
}

type TBC20ParsedTape struct {
	Amounts       [TBC20AmountSlots]uint64
	Balance       *big.Int
	ExtensionData []byte
	Size          int
}

func TBC20HumanToRaw(value string, decimal uint8) (*big.Int, error) {
	if decimal > TBC20MaxDecimal {
		return nil, fmt.Errorf("TBC20: decimal must be in [0, %d]", TBC20MaxDecimal)
	}
	match := tbc20HumanPattern.FindStringSubmatch(value)
	if match == nil {
		return nil, fmt.Errorf("TBC20: amount must be a canonical unsigned decimal string")
	}
	fraction := match[2]
	if len(fraction) > int(decimal) {
		return nil, fmt.Errorf("TBC20: amount has more than %d fractional digits", decimal)
	}
	integer := new(big.Int)
	integer.SetString(match[1], 10)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimal)), nil)
	result := new(big.Int).Mul(integer, scale)
	if fraction != "" {
		fraction += strings.Repeat("0", int(decimal)-len(fraction))
		fractionInt := new(big.Int)
		fractionInt.SetString(fraction, 10)
		result.Add(result, fractionInt)
	}
	if result.Cmp(new(big.Int).SetUint64(util.TBC20MaxSlotAmount)) > 0 {
		return nil, fmt.Errorf("TBC20: amount exceeds maximum slot amount")
	}
	return result, nil
}

func TBC20RawToHuman(value *big.Int, decimal uint8) (string, error) {
	if value == nil || value.Sign() < 0 || value.Cmp(new(big.Int).SetUint64(util.TBC20MaxSlotAmount)) > 0 {
		return "", fmt.Errorf("TBC20: raw amount is out of range")
	}
	if decimal > TBC20MaxDecimal {
		return "", fmt.Errorf("TBC20: decimal must be in [0, %d]", TBC20MaxDecimal)
	}
	if decimal == 0 {
		return value.String(), nil
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimal)), nil)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(value, scale, fraction)
	fractionText := fmt.Sprintf("%0*s", int(decimal), fraction.String())
	fractionText = strings.TrimRight(fractionText, "0")
	if fractionText == "" {
		return whole.String(), nil
	}
	return whole.String() + "." + fractionText, nil
}

func EncodeTBC20OriginalUTXO(outpoint TBC20Outpoint) ([]byte, error) {
	if len(outpoint.TxID) != 64 {
		return nil, fmt.Errorf("TBC20: originalUTXO.txId must be 64 hexadecimal characters")
	}
	txid, err := hex.DecodeString(outpoint.TxID)
	if err != nil {
		return nil, fmt.Errorf("TBC20: invalid originalUTXO.txId: %w", err)
	}
	for left, right := 0, len(txid)-1; left < right; left, right = left+1, right-1 {
		txid[left], txid[right] = txid[right], txid[left]
	}
	result := make([]byte, 36)
	copy(result, txid)
	binary.LittleEndian.PutUint32(result[32:], outpoint.OutputIndex)
	return result, nil
}

func validateTBC20TapeSize(size int) error {
	if size < TBC20MinTapeBytes || size > TBC20MaxTapeBytes {
		return fmt.Errorf("TBC20: tapeSize must be an integer in [%d, %d]", TBC20MinTapeBytes, TBC20MaxTapeBytes)
	}
	return nil
}

func InstantiateTBC20Code(original TBC20Outpoint, controller [21]byte, tapeSize int) (*bscript.Script, error) {
	if err := validateTBC20TapeSize(tapeSize); err != nil {
		return nil, err
	}
	if controller[20] == 0x80 {
		return nil, fmt.Errorf("TBC20: controller option 80 is non-canonical ScriptNum negative zero")
	}
	originalBytes, err := EncodeTBC20OriginalUTXO(original)
	if err != nil {
		return nil, err
	}
	template := strings.TrimSpace(tbc20LockHexTemplate)
	if strings.Count(template, "<self.OriginalUTXO36>") != 1 || strings.Count(template, "<self.ConstTapeSize1>") != 17 || strings.Count(template, "<self.Controller21>") != 1 {
		return nil, fmt.Errorf("TBC20: embedded artifact placeholders changed unexpectedly")
	}
	hexCode := strings.NewReplacer(
		"<self.OriginalUTXO36>", "24"+hex.EncodeToString(originalBytes),
		"<self.ConstTapeSize1>", fmt.Sprintf("01%02x", tapeSize),
		"<self.Controller21>", "15"+hex.EncodeToString(controller[:]),
	).Replace(template)
	code, err := bscript.NewFromHexString(hexCode)
	if err != nil {
		return nil, fmt.Errorf("TBC20: instantiate compiled code: %w", err)
	}
	if len(code.Bytes()) != TBC20CodeBytes {
		return nil, fmt.Errorf("TBC20: instantiated code must be %d bytes, got %d", TBC20CodeBytes, len(code.Bytes()))
	}
	return code, nil
}

func TBC20Controller(codeScript *bscript.Script) ([21]byte, error) {
	return util.GetTBC20Controller(codeScript)
}

func TBC20CodeIdentity(codeScript *bscript.Script) ([]byte, error) {
	return util.GetTBC20CodeIdentity(codeScript)
}

func ReplaceTBC20Controller(codeScript *bscript.Script, controller [21]byte) (*bscript.Script, error) {
	if codeScript == nil || len(codeScript.Bytes()) != TBC20CodeBytes {
		return nil, fmt.Errorf("TBC20: codeScript must be %d bytes", TBC20CodeBytes)
	}
	if _, err := TBC20Controller(codeScript); err != nil {
		return nil, err
	}
	before, err := TBC20CodeIdentity(codeScript)
	if err != nil {
		return nil, err
	}
	code := append([]byte(nil), codeScript.Bytes()...)
	offset := len(code) - 28
	copy(code[offset+1:offset+22], controller[:])
	updated := bscript.NewFromBytes(code)
	after, err := TBC20CodeIdentity(updated)
	if err != nil || !bytes.Equal(before, after) {
		return nil, fmt.Errorf("TBC20: controller replacement changed code identity")
	}
	return updated, nil
}

func tbc20StrictPushOnly(script []byte) bool {
	for offset := 0; offset < len(script); {
		opcode := script[offset]
		offset++
		dataLength := 0
		switch {
		case opcode == 0 || (opcode >= 0x51 && opcode <= 0x60):
			continue
		case opcode >= 1 && opcode <= 75:
			dataLength = int(opcode)
		case opcode == 0x4c:
			if offset >= len(script) {
				return false
			}
			dataLength = int(script[offset])
			offset++
		case opcode == 0x4d:
			if offset+2 > len(script) {
				return false
			}
			dataLength = int(binary.LittleEndian.Uint16(script[offset:]))
			offset += 2
		default:
			return false
		}
		if offset+dataLength > len(script) {
			return false
		}
		offset += dataLength
	}
	return true
}

func BuildTBC20Tape(amounts [TBC20AmountSlots]uint64, tapeSize int, extension []byte) (*bscript.Script, error) {
	if err := validateTBC20TapeSize(tapeSize); err != nil {
		return nil, err
	}
	if len(extension) != tapeSize-util.TBC20MinTapeBytes {
		return nil, fmt.Errorf("TBC20: tapeSize %d requires exactly %d extension bytes", tapeSize, tapeSize-util.TBC20MinTapeBytes)
	}
	result := make([]byte, 0, tapeSize)
	result = append(result, util.TBC20TapePrefix...)
	for i, amount := range amounts {
		if amount > util.TBC20MaxSlotAmount {
			return nil, fmt.Errorf("TBC20: amounts[%d] exceeds maximum slot amount", i)
		}
		result = append(result, util.EncodeTBC20UInt64LE(amount)...)
	}
	result = append(result, extension...)
	result = append(result, util.TBC20TapeMarker...)
	if !tbc20StrictPushOnly(result[2:]) {
		return nil, fmt.Errorf("TBC20: tape after OP_RETURN must use strict canonical pushes")
	}
	return bscript.NewFromBytes(result), nil
}

func ParseTBC20Tape(script *bscript.Script) (TBC20ParsedTape, error) {
	var result TBC20ParsedTape
	amounts, err := util.ReadTBC20TapeAmounts(script)
	if err != nil {
		return result, err
	}
	if len(script.Bytes()) < TBC20MinTapeBytes || !tbc20StrictPushOnly(script.Bytes()[2:]) {
		return result, fmt.Errorf("TBC20: tape is not relay-safe canonical push-only data")
	}
	result.Amounts = amounts
	result.Balance = new(big.Int)
	for _, amount := range amounts {
		result.Balance.Add(result.Balance, new(big.Int).SetUint64(amount))
	}
	result.Size = len(script.Bytes())
	extensionStart := len(util.TBC20TapePrefix) + util.TBC20AmountBytes
	result.ExtensionData = append([]byte(nil), script.Bytes()[extensionStart:result.Size-len(util.TBC20TapeMarker)]...)
	return result, nil
}

func BuildTBC20MetadataExtension(definition TBC20Definition) ([]byte, *big.Int, error) {
	if definition.Name == "" || !utf8.ValidString(definition.Name) {
		return nil, nil, fmt.Errorf("TBC20: metadata.name must be non-empty valid UTF-8")
	}
	if definition.Symbol == "" {
		return nil, nil, fmt.Errorf("TBC20: metadata.symbol is required")
	}
	for _, char := range []byte(definition.Symbol) {
		if char < 0x21 || char > 0x7e {
			return nil, nil, fmt.Errorf("TBC20: metadata.symbol must be printable ASCII without whitespace")
		}
	}
	supply, err := TBC20HumanToRaw(definition.Supply, definition.Decimal)
	if err != nil || supply.Sign() <= 0 {
		return nil, nil, fmt.Errorf("TBC20: metadata supply must be positive: %w", err)
	}
	name := []byte(definition.Name)
	symbol := []byte(definition.Symbol)
	payloadLength := 12 + len(name) + len(symbol)
	if payloadLength > 75 || payloadLength+2 > TBC20MaxTapeBytes-util.TBC20MinTapeBytes {
		return nil, nil, fmt.Errorf("TBC20: metadata name and symbol are too long")
	}
	payload := make([]byte, 0, payloadLength)
	payload = append(payload, TBC20MetadataVersion, definition.Decimal)
	payload = append(payload, util.EncodeTBC20UInt64LE(supply.Uint64())...)
	payload = append(payload, byte(len(name)), byte(len(symbol)))
	payload = append(payload, name...)
	payload = append(payload, symbol...)
	extension := append([]byte{byte(len(payload))}, payload...)
	extension = append(extension, byte(len(util.TBC20TapeMarker)))
	return extension, supply, nil
}

func NewTBC20(config TBC20Config) (*TBC20, error) {
	t := &TBC20{CodeScript: config.CodeScript, TapeScript: config.TapeScript, ContractTxID: strings.ToLower(config.ContractTxID)}
	if config.ContractTxID != "" {
		if len(config.ContractTxID) != 64 {
			return nil, fmt.Errorf("TBC20: contractTxid must be 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(config.ContractTxID); err != nil {
			return nil, err
		}
	}
	extension := append([]byte(nil), config.ExtensionData...)
	if config.Definition != nil {
		metadataExtension, supply, err := BuildTBC20MetadataExtension(*config.Definition)
		if err != nil {
			return nil, err
		}
		if len(extension) > 0 && !bytes.Equal(extension, metadataExtension) {
			return nil, fmt.Errorf("TBC20: extensionData differs from canonical metadata")
		}
		extension = metadataExtension
		t.Definition = config.Definition
		t.DeclaredSupplyRaw = supply
	}
	if config.TapeScript != nil {
		parsed, err := ParseTBC20Tape(config.TapeScript)
		if err != nil {
			return nil, err
		}
		if len(extension) > 0 && !bytes.Equal(extension, parsed.ExtensionData) {
			return nil, fmt.Errorf("TBC20: extensionData differs from tapeScript")
		}
		extension = parsed.ExtensionData
		t.TapeSize = parsed.Size
	}
	if len(extension) == 0 {
		extension = []byte{byte(len(util.TBC20TapeMarker))}
	}
	if t.TapeSize == 0 {
		t.TapeSize = config.TapeSize
	}
	if t.TapeSize == 0 {
		t.TapeSize = util.TBC20MinTapeBytes + len(extension)
	}
	if err := validateTBC20TapeSize(t.TapeSize); err != nil {
		return nil, err
	}
	if t.TapeSize != util.TBC20MinTapeBytes+len(extension) {
		return nil, fmt.Errorf("TBC20: tapeSize does not match extension length")
	}
	t.ExtensionData = extension
	return t, nil
}
