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
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"golang.org/x/text/unicode/norm"
)

const (
	TBC20StandardMaxInputs         = util.TBC20StandardMaxInputs
	TBC20StandardMaxOutputGroups   = util.TBC20StandardMaxOutputGroups
	TBC20StandardMaxOutputs        = util.TBC20StandardMaxOutputs
	TBC20StandardCodeSatoshis      = util.TBC20StandardCodeSatoshis
	TBC20StandardTapeSatoshis      = util.TBC20StandardTapeSatoshis
	TBC20StandardAmountSlots       = util.TBC20StandardAmountSlots
	TBC20StandardMinTapeBytes      = 61
	TBC20StandardMaxTapeBytes      = util.TBC20StandardMaxTapeBytes
	TBC20StandardCodeBytes         = 2657
	TBC20StandardCodePartialOffset = 2624
	TBC20StandardArtifactSHA256    = "0f5db33bb46e4517963cbd383518f74283b5c2c4efb0a733522a1fbc0d4a9f84"
	TBC20StandardMetadataVersion   = 1
	TBC20StandardMaxDecimal        = 18
)

//go:embed asm/tbc20Standard_lock.hex
var tbc20StandardLockHexTemplate string

var tbc20StandardHumanPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.([0-9]+))?$`)

type TBC20StandardDefinition struct {
	Name    string
	Symbol  string
	Supply  string
	Decimal uint8
}

type TBC20StandardConfig struct {
	Definition    *TBC20StandardDefinition
	CodeScript    *bscript.Script
	TapeScript    *bscript.Script
	ContractTxID  string
	TapeSize      int
	ExtensionData []byte
}

type TBC20Standard struct {
	Definition        *TBC20StandardDefinition
	CodeScript        *bscript.Script
	TapeScript        *bscript.Script
	ContractTxID      string
	TapeSize          int
	ExtensionData     []byte
	DeclaredSupplyRaw *big.Int
}

type TBC20StandardOutpoint struct {
	TxID        string
	OutputIndex uint32
}

type TBC20StandardParsedTape struct {
	Amounts       [TBC20StandardAmountSlots]uint64
	Balance       *big.Int
	ExtensionData []byte
	Size          int
}

func TBC20StandardHumanToRaw(value string, decimal uint8) (*big.Int, error) {
	if decimal > TBC20StandardMaxDecimal {
		return nil, fmt.Errorf("TBC20Standard: decimal must be in [0, %d]", TBC20StandardMaxDecimal)
	}
	match := tbc20StandardHumanPattern.FindStringSubmatch(value)
	if match == nil {
		return nil, fmt.Errorf("TBC20Standard: amount must be a canonical unsigned decimal string")
	}
	fraction := match[2]
	if len(fraction) > int(decimal) {
		return nil, fmt.Errorf("TBC20Standard: amount has more than %d fractional digits", decimal)
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
	if result.Cmp(new(big.Int).SetUint64(util.TBC20StandardMaxSlotAmount)) > 0 {
		return nil, fmt.Errorf("TBC20Standard: amount exceeds maximum slot amount")
	}
	return result, nil
}

func TBC20StandardRawToHuman(value *big.Int, decimal uint8) (string, error) {
	if value == nil || value.Sign() < 0 || value.Cmp(new(big.Int).SetUint64(util.TBC20StandardMaxSlotAmount)) > 0 {
		return "", fmt.Errorf("TBC20Standard: raw amount is out of range")
	}
	if decimal > TBC20StandardMaxDecimal {
		return "", fmt.Errorf("TBC20Standard: decimal must be in [0, %d]", TBC20StandardMaxDecimal)
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

func EncodeTBC20StandardOriginalUTXO(outpoint TBC20StandardOutpoint) ([]byte, error) {
	if len(outpoint.TxID) != 64 {
		return nil, fmt.Errorf("TBC20Standard: originalUTXO.txId must be 64 hexadecimal characters")
	}
	txid, err := hex.DecodeString(outpoint.TxID)
	if err != nil {
		return nil, fmt.Errorf("TBC20Standard: invalid originalUTXO.txId: %w", err)
	}
	for left, right := 0, len(txid)-1; left < right; left, right = left+1, right-1 {
		txid[left], txid[right] = txid[right], txid[left]
	}
	result := make([]byte, 36)
	copy(result, txid)
	binary.LittleEndian.PutUint32(result[32:], outpoint.OutputIndex)
	return result, nil
}

func validateTBC20StandardTapeSize(size int) error {
	if size < TBC20StandardMinTapeBytes || size > TBC20StandardMaxTapeBytes {
		return fmt.Errorf("TBC20Standard: tapeSize must be an integer in [%d, %d]", TBC20StandardMinTapeBytes, TBC20StandardMaxTapeBytes)
	}
	return nil
}

func InstantiateTBC20StandardCode(original TBC20StandardOutpoint, controller [21]byte, tapeSize int) (*bscript.Script, error) {
	if err := validateTBC20StandardTapeSize(tapeSize); err != nil {
		return nil, err
	}
	if controller[20] == 0x80 {
		return nil, fmt.Errorf("TBC20Standard: controller option 80 is non-canonical ScriptNum negative zero")
	}
	originalBytes, err := EncodeTBC20StandardOriginalUTXO(original)
	if err != nil {
		return nil, err
	}
	template := strings.TrimSpace(tbc20StandardLockHexTemplate)
	if strings.Count(template, "<self.OriginalUTXO36>") != 1 || strings.Count(template, "<self.ConstTapeSize1>") != 17 || strings.Count(template, "<self.Controller21>") != 1 {
		return nil, fmt.Errorf("TBC20Standard: embedded artifact placeholders changed unexpectedly")
	}
	hexCode := strings.NewReplacer(
		"<self.OriginalUTXO36>", "24"+hex.EncodeToString(originalBytes),
		"<self.ConstTapeSize1>", fmt.Sprintf("01%02x", tapeSize),
		"<self.Controller21>", "15"+hex.EncodeToString(controller[:]),
	).Replace(template)
	code, err := bscript.NewFromHexString(hexCode)
	if err != nil {
		return nil, fmt.Errorf("TBC20Standard: instantiate compiled code: %w", err)
	}
	if len(code.Bytes()) != TBC20StandardCodeBytes {
		return nil, fmt.Errorf("TBC20Standard: instantiated code must be %d bytes, got %d", TBC20StandardCodeBytes, len(code.Bytes()))
	}
	if err := ValidateTBC20StandardCode(code, tapeSize); err != nil {
		return nil, err
	}
	return code, nil
}

// ValidateTBC20StandardCode verifies every immutable byte of the embedded compiler
// artifact while permitting only the original outpoint, tape-size constants,
// and terminal controller fields to vary.
func ValidateTBC20StandardCode(codeScript *bscript.Script, expectedTapeSize int) error {
	if codeScript == nil || len(codeScript.Bytes()) != TBC20StandardCodeBytes {
		return fmt.Errorf("TBC20Standard: codeScript must be %d bytes", TBC20StandardCodeBytes)
	}
	code := codeScript.Bytes()
	template := strings.TrimSpace(tbc20StandardLockHexTemplate)
	offset := 0
	embeddedTapeSize := -1
	for len(template) > 0 {
		placeholderAt := strings.IndexByte(template, '<')
		if placeholderAt < 0 {
			constant, err := hex.DecodeString(template)
			if err != nil || offset+len(constant) > len(code) || !bytes.Equal(code[offset:offset+len(constant)], constant) {
				return fmt.Errorf("TBC20Standard: codeScript differs from compiler artifact at byte %d", offset)
			}
			offset += len(constant)
			break
		}
		if placeholderAt > 0 {
			constant, err := hex.DecodeString(template[:placeholderAt])
			if err != nil || offset+len(constant) > len(code) || !bytes.Equal(code[offset:offset+len(constant)], constant) {
				return fmt.Errorf("TBC20Standard: codeScript differs from compiler artifact at byte %d", offset)
			}
			offset += len(constant)
			template = template[placeholderAt:]
		}
		end := strings.IndexByte(template, '>')
		if end < 0 {
			return fmt.Errorf("TBC20Standard: embedded compiler artifact has an unterminated placeholder")
		}
		placeholder := template[:end+1]
		template = template[end+1:]
		switch placeholder {
		case "<self.OriginalUTXO36>":
			if offset+37 > len(code) || code[offset] != 36 {
				return fmt.Errorf("TBC20Standard: OriginalUTXO must use a direct 36-byte push")
			}
			offset += 37
		case "<self.ConstTapeSize1>":
			if offset+2 > len(code) || code[offset] != 1 {
				return fmt.Errorf("TBC20Standard: ConstTapeSize must use a direct one-byte push")
			}
			current := int(code[offset+1])
			if embeddedTapeSize >= 0 && embeddedTapeSize != current {
				return fmt.Errorf("TBC20Standard: codeScript contains inconsistent tape sizes")
			}
			embeddedTapeSize = current
			offset += 2
		case "<self.Controller21>":
			if offset+22 > len(code) || code[offset] != 21 || code[offset+21] == 0x80 {
				return fmt.Errorf("TBC20Standard: Controller must use a canonical direct 21-byte push")
			}
			offset += 22
		default:
			return fmt.Errorf("TBC20Standard: unknown compiler artifact placeholder %s", placeholder)
		}
	}
	if offset != len(code) || embeddedTapeSize < 0 {
		return fmt.Errorf("TBC20Standard: codeScript does not exactly match the compiler artifact")
	}
	if err := validateTBC20StandardTapeSize(embeddedTapeSize); err != nil {
		return err
	}
	if expectedTapeSize != 0 && embeddedTapeSize != expectedTapeSize {
		return fmt.Errorf("TBC20Standard: embedded tape size %d differs from expected %d", embeddedTapeSize, expectedTapeSize)
	}
	return nil
}

// IsTBC20StandardArtifactCandidate mirrors the validator's deliberately narrow
// damaged-artifact heuristic: at most eight immutable bytes before the
// partial-hash boundary may differ. Unrelated contracts of the same length
// remain opaque instead of being mislabeled as malformed TBC20Standard.
func IsTBC20StandardArtifactCandidate(codeScript *bscript.Script) bool {
	if codeScript == nil || len(codeScript.Bytes()) != TBC20StandardCodeBytes {
		return false
	}
	code := codeScript.Bytes()
	template := strings.TrimSpace(tbc20StandardLockHexTemplate)
	offset, mismatches := 0, 0
	for len(template) > 0 && offset < TBC20StandardCodePartialOffset {
		placeholderAt := strings.IndexByte(template, '<')
		constantText := template
		if placeholderAt >= 0 {
			constantText = template[:placeholderAt]
		}
		if constantText != "" {
			constant, err := hex.DecodeString(constantText)
			if err != nil {
				return false
			}
			limit := len(constant)
			if offset+limit > TBC20StandardCodePartialOffset {
				limit = TBC20StandardCodePartialOffset - offset
			}
			for index := 0; index < limit; index++ {
				if code[offset+index] != constant[index] {
					mismatches++
					if mismatches > 8 {
						return false
					}
				}
			}
			offset += len(constant)
			template = template[len(constantText):]
			continue
		}
		end := strings.IndexByte(template, '>')
		if end < 0 {
			return false
		}
		placeholder := template[:end+1]
		template = template[end+1:]
		switch placeholder {
		case "<self.OriginalUTXO36>":
			offset += 37
		case "<self.ConstTapeSize1>":
			offset += 2
		case "<self.Controller21>":
			offset += 22
		default:
			return false
		}
	}
	return mismatches <= 8
}

func TBC20StandardController(codeScript *bscript.Script) ([21]byte, error) {
	return util.GetTBC20StandardController(codeScript)
}

func TBC20StandardCodeIdentity(codeScript *bscript.Script) ([]byte, error) {
	return util.GetTBC20StandardCodeIdentity(codeScript)
}

func ReplaceTBC20StandardController(codeScript *bscript.Script, controller [21]byte) (*bscript.Script, error) {
	if controller[20] == 0x80 {
		return nil, fmt.Errorf("TBC20Standard: controller option 80 is non-canonical ScriptNum negative zero")
	}
	if err := ValidateTBC20StandardCode(codeScript, 0); err != nil {
		return nil, err
	}
	before, err := TBC20StandardCodeIdentity(codeScript)
	if err != nil {
		return nil, err
	}
	code := append([]byte(nil), codeScript.Bytes()...)
	offset := len(code) - 33
	copy(code[offset+1:offset+22], controller[:])
	updated := bscript.NewFromBytes(code)
	after, err := TBC20StandardCodeIdentity(updated)
	if err != nil || !bytes.Equal(before, after) {
		return nil, fmt.Errorf("TBC20Standard: controller replacement changed code identity")
	}
	return updated, nil
}

func tbc20StandardStrictPushOnly(script []byte) bool {
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

func BuildTBC20StandardTape(amounts [TBC20StandardAmountSlots]uint64, tapeSize int, extension []byte) (*bscript.Script, error) {
	if err := validateTBC20StandardTapeSize(tapeSize); err != nil {
		return nil, err
	}
	if len(extension) != tapeSize-util.TBC20StandardMinTapeBytes {
		return nil, fmt.Errorf("TBC20Standard: tapeSize %d requires exactly %d extension bytes", tapeSize, tapeSize-util.TBC20StandardMinTapeBytes)
	}
	result := make([]byte, 0, tapeSize)
	result = append(result, util.TBC20StandardTapePrefix...)
	for i, amount := range amounts {
		if amount > util.TBC20StandardMaxSlotAmount {
			return nil, fmt.Errorf("TBC20Standard: amounts[%d] exceeds maximum slot amount", i)
		}
		result = append(result, util.EncodeTBC20StandardUInt64LE(amount)...)
	}
	result = append(result, extension...)
	result = append(result, util.TBC20StandardTapeMarker...)
	if !tbc20StandardStrictPushOnly(result[2:]) {
		return nil, fmt.Errorf("TBC20Standard: tape after OP_RETURN must use strict canonical pushes")
	}
	return bscript.NewFromBytes(result), nil
}

func ParseTBC20StandardTape(script *bscript.Script) (TBC20StandardParsedTape, error) {
	var result TBC20StandardParsedTape
	amounts, err := util.ReadTBC20StandardTapeAmounts(script)
	if err != nil {
		return result, err
	}
	if len(script.Bytes()) < TBC20StandardMinTapeBytes || !tbc20StandardStrictPushOnly(script.Bytes()[2:]) {
		return result, fmt.Errorf("TBC20Standard: tape is not relay-safe canonical push-only data")
	}
	result.Amounts = amounts
	result.Balance = new(big.Int)
	for _, amount := range amounts {
		result.Balance.Add(result.Balance, new(big.Int).SetUint64(amount))
	}
	result.Size = len(script.Bytes())
	extensionStart := len(util.TBC20StandardTapePrefix) + util.TBC20StandardAmountBytes
	result.ExtensionData = append([]byte(nil), script.Bytes()[extensionStart:result.Size-len(util.TBC20StandardTapeMarker)]...)
	return result, nil
}

func BuildTBC20StandardMetadataExtension(definition TBC20StandardDefinition) ([]byte, *big.Int, error) {
	if definition.Name == "" || !utf8.ValidString(definition.Name) {
		return nil, nil, fmt.Errorf("TBC20Standard: metadata.name must be non-empty valid UTF-8")
	}
	if !norm.NFC.IsNormalString(definition.Name) {
		return nil, nil, fmt.Errorf("TBC20Standard: metadata.name must use canonical NFC Unicode normalization")
	}
	if definition.Symbol == "" {
		return nil, nil, fmt.Errorf("TBC20Standard: metadata.symbol is required")
	}
	for _, char := range []byte(definition.Symbol) {
		if char < 0x21 || char > 0x7e {
			return nil, nil, fmt.Errorf("TBC20Standard: metadata.symbol must be printable ASCII without whitespace")
		}
	}
	supply, err := TBC20StandardHumanToRaw(definition.Supply, definition.Decimal)
	if err != nil || supply.Sign() <= 0 {
		return nil, nil, fmt.Errorf("TBC20Standard: metadata supply must be positive: %w", err)
	}
	name := []byte(definition.Name)
	symbol := []byte(definition.Symbol)
	payloadLength := 12 + len(name) + len(symbol)
	if payloadLength > 75 || payloadLength+2 > TBC20StandardMaxTapeBytes-util.TBC20StandardMinTapeBytes {
		return nil, nil, fmt.Errorf("TBC20Standard: metadata name and symbol are too long")
	}
	payload := make([]byte, 0, payloadLength)
	payload = append(payload, TBC20StandardMetadataVersion, definition.Decimal)
	payload = append(payload, util.EncodeTBC20StandardUInt64LE(supply.Uint64())...)
	payload = append(payload, byte(len(name)), byte(len(symbol)))
	payload = append(payload, name...)
	payload = append(payload, symbol...)
	extension := append([]byte{byte(len(payload))}, payload...)
	extension = append(extension, byte(len(util.TBC20StandardTapeMarker)))
	return extension, supply, nil
}

func NewTBC20Standard(config TBC20StandardConfig) (*TBC20Standard, error) {
	t := &TBC20Standard{CodeScript: config.CodeScript, TapeScript: config.TapeScript, ContractTxID: strings.ToLower(config.ContractTxID)}
	if config.ContractTxID != "" {
		if len(config.ContractTxID) != 64 {
			return nil, fmt.Errorf("TBC20Standard: contractTxid must be 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(config.ContractTxID); err != nil {
			return nil, err
		}
	}
	extension := append([]byte(nil), config.ExtensionData...)
	if config.Definition != nil {
		metadataExtension, supply, err := BuildTBC20StandardMetadataExtension(*config.Definition)
		if err != nil {
			return nil, err
		}
		if len(extension) > 0 && !bytes.Equal(extension, metadataExtension) {
			return nil, fmt.Errorf("TBC20Standard: extensionData differs from canonical metadata")
		}
		extension = metadataExtension
		t.Definition = config.Definition
		t.DeclaredSupplyRaw = supply
	}
	if config.TapeScript != nil {
		parsed, err := ParseTBC20StandardTape(config.TapeScript)
		if err != nil {
			return nil, err
		}
		if len(extension) > 0 && !bytes.Equal(extension, parsed.ExtensionData) {
			return nil, fmt.Errorf("TBC20Standard: extensionData differs from tapeScript")
		}
		extension = parsed.ExtensionData
		t.TapeSize = parsed.Size
	}
	if len(extension) == 0 {
		extension = []byte{byte(len(util.TBC20StandardTapeMarker))}
	}
	if t.TapeSize == 0 {
		t.TapeSize = config.TapeSize
	}
	if t.TapeSize == 0 {
		t.TapeSize = util.TBC20StandardMinTapeBytes + len(extension)
	}
	if err := validateTBC20StandardTapeSize(t.TapeSize); err != nil {
		return nil, err
	}
	if t.TapeSize != util.TBC20StandardMinTapeBytes+len(extension) {
		return nil, fmt.Errorf("TBC20Standard: tapeSize does not match extension length")
	}
	if t.CodeScript != nil {
		if err := ValidateTBC20StandardCode(t.CodeScript, t.TapeSize); err != nil {
			return nil, err
		}
	}
	t.ExtensionData = extension
	return t, nil
}

func TBC20StandardAddressController(address string) ([21]byte, error) {
	return tbc20StandardAddressController(address)
}

func TBC20StandardContractController(lockingScript *bscript.Script) ([21]byte, error) {
	var result [21]byte
	if lockingScript == nil || len(lockingScript.Bytes()) == 0 {
		return result, fmt.Errorf("TBC20Standard: contract locking script cannot be empty")
	}
	copy(result[:20], crypto.Hash160(crypto.Sha256(lockingScript.Bytes())))
	result[20] = 1
	return result, nil
}

func BuildTBC20StandardUTXO(tx *bt.Tx, codeVout int) (*bt.UTXO, *big.Int, error) {
	if tx == nil || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return nil, nil, fmt.Errorf("TBC20Standard: code output must be immediately followed by tape")
	}
	code, tape := tx.Outputs[codeVout], tx.Outputs[codeVout+1]
	if code.Satoshis != TBC20StandardCodeSatoshis || tape.Satoshis != TBC20StandardTapeSatoshis {
		return nil, nil, fmt.Errorf("TBC20Standard: code/tape values must be exactly 500/0 satoshis")
	}
	parsed, err := ParseTBC20StandardTape(tape.LockingScript)
	if err != nil {
		return nil, nil, err
	}
	if err := ValidateTBC20StandardCode(code.LockingScript, parsed.Size); err != nil {
		return nil, nil, err
	}
	txid, err := hex.DecodeString(tx.TxID())
	if err != nil {
		return nil, nil, err
	}
	return &bt.UTXO{TxID: txid, Vout: uint32(codeVout), LockingScript: code.LockingScript, Satoshis: code.Satoshis}, parsed.Balance, nil
}
