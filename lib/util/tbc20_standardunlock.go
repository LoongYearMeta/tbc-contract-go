package util

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
)

const (
	TBC20StandardMaxInputs       = 6
	TBC20StandardMaxOutputGroups = 8
	TBC20StandardMaxOutputs      = TBC20StandardMaxOutputGroups * 2
	TBC20StandardCodeSatoshis    = 500
	TBC20StandardTapeSatoshis    = 0
	TBC20StandardAmountSlots     = 6
	TBC20StandardAmountBytes     = TBC20StandardAmountSlots * 8
	TBC20StandardMinTapeBytes    = 60
	TBC20StandardMaxTapeBytes    = 127
	TBC20StandardMaxSlotAmount   = uint64(1<<63 - 1)
)

var (
	TBC20StandardTapePrefix = []byte{0x00, 0x6a, 0x30}
	TBC20StandardTapeMarker = []byte("TBC20TAPE")
)

type TBC20StandardPartialScriptData struct {
	SuffixData  []byte
	PartialHash []byte
	Size        []byte
}

// EncodeTBC20StandardUnsignedLE returns the minimal non-negative ScriptNum encoding
// used by the APC number fields in the TBC20Standard ABI.
func EncodeTBC20StandardUnsignedLE(value uint64) []byte {
	if value == 0 {
		return nil
	}
	result := make([]byte, 0, 9)
	for value > 0 {
		result = append(result, byte(value))
		value >>= 8
	}
	if result[len(result)-1]&0x80 != 0 {
		result = append(result, 0)
	}
	return result
}

func EncodeTBC20StandardUInt64LE(value uint64) []byte {
	result := make([]byte, 8)
	binary.LittleEndian.PutUint64(result, value)
	return result
}

func GetTBC20StandardPartialScriptData(script *bscript.Script) (TBC20StandardPartialScriptData, error) {
	if script == nil || len(script.Bytes()) == 0 {
		return TBC20StandardPartialScriptData{}, fmt.Errorf("TBC20Standard unlock: locking script must be non-empty")
	}
	lockingScript := script.Bytes()
	partialOffset := len(lockingScript) / 64 * 64
	result := TBC20StandardPartialScriptData{Size: EncodeTBC20StandardUnsignedLE(uint64(len(lockingScript)))}
	if partialOffset == 0 {
		result.SuffixData = append([]byte(nil), lockingScript...)
		return result, nil
	}
	partialHex := partialsha256.CalculatePartialHash(lockingScript[:partialOffset])
	partialHash, err := hex.DecodeString(partialHex)
	if err != nil || len(partialHash) != 32 {
		return TBC20StandardPartialScriptData{}, fmt.Errorf("TBC20Standard unlock: invalid partial SHA-256 state")
	}
	result.PartialHash = partialHash
	result.SuffixData = append([]byte(nil), lockingScript[partialOffset:]...)
	return result, nil
}

func ReadTBC20StandardTapeAmounts(script *bscript.Script) ([TBC20StandardAmountSlots]uint64, error) {
	var amounts [TBC20StandardAmountSlots]uint64
	if script == nil {
		return amounts, fmt.Errorf("TBC20Standard unlock: nil tape script")
	}
	tape := script.Bytes()
	if len(tape) < TBC20StandardMinTapeBytes || len(tape) > TBC20StandardMaxTapeBytes {
		return amounts, fmt.Errorf("TBC20Standard unlock: tape script must be %d-%d bytes, got %d", TBC20StandardMinTapeBytes, TBC20StandardMaxTapeBytes, len(tape))
	}
	if !bytes.Equal(tape[:len(TBC20StandardTapePrefix)], TBC20StandardTapePrefix) {
		return amounts, fmt.Errorf("TBC20Standard unlock: tape script must start with OP_FALSE OP_RETURN PUSH48 (006a30)")
	}
	if !bytes.Equal(tape[len(tape)-len(TBC20StandardTapeMarker):], TBC20StandardTapeMarker) {
		return amounts, fmt.Errorf("TBC20Standard unlock: tape script must end with ASCII TBC20TAPE")
	}
	for i := range amounts {
		amounts[i] = binary.LittleEndian.Uint64(tape[len(TBC20StandardTapePrefix)+i*8:])
		if amounts[i] > TBC20StandardMaxSlotAmount {
			return amounts, fmt.Errorf("TBC20Standard unlock: tape amount slot %d exceeds the contract-safe signed-63-bit range", i)
		}
	}
	return amounts, nil
}

func ReplaceTBC20StandardTapeAmounts(script *bscript.Script, amounts [TBC20StandardAmountSlots]uint64) (*bscript.Script, error) {
	if _, err := ReadTBC20StandardTapeAmounts(script); err != nil {
		return nil, err
	}
	result := append([]byte(nil), script.Bytes()...)
	for i, amount := range amounts {
		if amount > TBC20StandardMaxSlotAmount {
			return nil, fmt.Errorf("TBC20Standard unlock: amount slot %d exceeds maximum", i)
		}
		binary.LittleEndian.PutUint64(result[len(TBC20StandardTapePrefix)+i*8:], amount)
	}
	return bscript.NewFromBytes(result), nil
}

func GetTBC20StandardController(codeScript *bscript.Script) ([21]byte, error) {
	var controller [21]byte
	if codeScript == nil {
		return controller, fmt.Errorf("TBC20Standard unlock: nil code script")
	}
	code := codeScript.Bytes()
	terminalMarker := []byte("\x0aTBC20CODE2")
	if len(code) < 33 || !bytes.Equal(code[len(code)-len(terminalMarker):], terminalMarker) {
		return controller, fmt.Errorf("TBC20Standard unlock: code script is missing the terminal TBC20CODE2 marker")
	}
	offset := len(code) - 33
	if code[offset] != 21 {
		return controller, fmt.Errorf("TBC20Standard unlock: terminal controller must be a direct 21-byte push")
	}
	copy(controller[:], code[offset+1:offset+22])
	return controller, nil
}

func GetTBC20StandardCodeIdentity(codeScript *bscript.Script) ([]byte, error) {
	partial, err := GetTBC20StandardPartialScriptData(codeScript)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), partial.PartialHash...), partial.Size...), nil
}

type TBC20StandardOutputGroup struct {
	CodeVout int
	TapeVout *int
}

type TBC20StandardAncestorResolver interface {
	ResolveTBC20StandardAncestor(txid string) (*bt.Tx, bool)
}

type TBC20StandardContractControllerWitness struct {
	Transaction       *bt.Tx
	CurrentInputIndex int
}

type TBC20StandardUnlockOptions struct {
	CurrentTx            *bt.Tx
	InputIndex           int
	PreTx                *bt.Tx
	PreTxVout            int
	OutputGroups         []TBC20StandardOutputGroup
	AncestorTransactions TBC20StandardAncestorResolver
	ContractController   *TBC20StandardContractControllerWitness
	Signature            []byte
	PublicKey            []byte
	PrivateKey           *bec.PrivateKey
}

type tbc20StandardOutputData struct {
	value   []byte
	partial TBC20StandardPartialScriptData
}

type tbc20StandardOutputGroupData struct {
	code       tbc20StandardOutputData
	tapeValue  []byte
	tapeScript []byte
}

type tbc20StandardPreTxData struct {
	vlio                []byte
	inputs              [TBC20StandardMaxInputs][]byte
	unlockingScriptHash []byte
	outputsFirstPart    []byte
	outputsGotData      tbc20StandardOutputGroupData
	outputsLastPart     []byte
}

type tbc20StandardPrePreTxData struct {
	vlio                []byte
	txInputsHashData    []byte
	outputsFirstPart    []byte
	outputsVerifiedData tbc20StandardOutputData
	outputsLastPart     []byte
}

type tbc20StandardContractTxData struct {
	vlio                []byte
	txInputsHashData    []byte
	outputsFirstPart    []byte
	middleValue         []byte
	middleLockingScript []byte
	outputsLastPart     []byte
}

func tbc20StandardAssertVersion10(tx *bt.Tx, name string) error {
	if tx == nil {
		return fmt.Errorf("TBC20Standard unlock: %s must be a transaction", name)
	}
	if tx.Version != 10 {
		return fmt.Errorf("TBC20Standard unlock: %s.version must be exactly 10", name)
	}
	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return fmt.Errorf("TBC20Standard unlock: %s must contain inputs and outputs", name)
	}
	return nil
}

func tbc20StandardVLIO(tx *bt.Tx) []byte {
	result := make([]byte, 16)
	binary.LittleEndian.PutUint32(result, tx.Version)
	binary.LittleEndian.PutUint32(result[4:], tx.LockTime)
	binary.LittleEndian.PutUint32(result[8:], uint32(len(tx.Inputs)))
	binary.LittleEndian.PutUint32(result[12:], uint32(len(tx.Outputs)))
	return result
}

func tbc20StandardInputRecord(input *bt.Input) ([]byte, error) {
	if input == nil || len(input.PreviousTxID()) != 32 {
		return nil, fmt.Errorf("TBC20Standard unlock: invalid transaction input")
	}
	result := make([]byte, 40)
	previous := input.PreviousTxID()
	for i := range previous {
		result[i] = previous[len(previous)-1-i]
	}
	binary.LittleEndian.PutUint32(result[32:], input.PreviousTxOutIndex)
	binary.LittleEndian.PutUint32(result[36:], input.SequenceNumber)
	return result, nil
}

func tbc20StandardInputRecords(tx *bt.Tx) ([]byte, error) {
	result := make([]byte, 0, len(tx.Inputs)*40)
	for _, input := range tx.Inputs {
		record, err := tbc20StandardInputRecord(input)
		if err != nil {
			return nil, err
		}
		result = append(result, record...)
	}
	return result, nil
}

func tbc20StandardUnlockingScriptsHash(tx *bt.Tx) []byte {
	hashes := make([]byte, 0, len(tx.Inputs)*32)
	for _, input := range tx.Inputs {
		scriptBytes := []byte(nil)
		if input != nil && input.UnlockingScript != nil {
			scriptBytes = input.UnlockingScript.Bytes()
		}
		hashes = append(hashes, crypto.Sha256(scriptBytes)...)
	}
	return crypto.Sha256(hashes)
}

func tbc20StandardInputsHashData(tx *bt.Tx) ([]byte, error) {
	records, err := tbc20StandardInputRecords(tx)
	if err != nil {
		return nil, err
	}
	return append(crypto.Sha256(records), tbc20StandardUnlockingScriptsHash(tx)...), nil
}

func tbc20StandardOutputHashRecords(tx *bt.Tx, start, end int) ([]byte, error) {
	if start < 0 || end < start || end > len(tx.Outputs) {
		return nil, fmt.Errorf("TBC20Standard unlock: output range is invalid")
	}
	result := make([]byte, 0, (end-start)*40)
	for i := start; i < end; i++ {
		if tx.Outputs[i] == nil || tx.Outputs[i].LockingScript == nil {
			return nil, fmt.Errorf("TBC20Standard unlock: output %d is invalid", i)
		}
		result = append(result, EncodeTBC20StandardUInt64LE(tx.Outputs[i].Satoshis)...)
		result = append(result, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	return result, nil
}

func tbc20StandardOutput(tx *bt.Tx, index int) (tbc20StandardOutputData, error) {
	if tx == nil || index < 0 || index >= len(tx.Outputs) || tx.Outputs[index] == nil {
		return tbc20StandardOutputData{}, fmt.Errorf("TBC20Standard unlock: output %d is out of range", index)
	}
	partial, err := GetTBC20StandardPartialScriptData(tx.Outputs[index].LockingScript)
	if err != nil {
		return tbc20StandardOutputData{}, err
	}
	return tbc20StandardOutputData{value: EncodeTBC20StandardUInt64LE(tx.Outputs[index].Satoshis), partial: partial}, nil
}

func tbc20StandardOutputGroup(tx *bt.Tx, codeVout int, tapeVout *int) (tbc20StandardOutputGroupData, error) {
	code, err := tbc20StandardOutput(tx, codeVout)
	if err != nil {
		return tbc20StandardOutputGroupData{}, err
	}
	result := tbc20StandardOutputGroupData{code: code}
	if tapeVout != nil {
		if *tapeVout < 0 || *tapeVout >= len(tx.Outputs) {
			return result, fmt.Errorf("TBC20Standard unlock: tape output is out of range")
		}
		result.tapeValue = EncodeTBC20StandardUInt64LE(tx.Outputs[*tapeVout].Satoshis)
		result.tapeScript = append([]byte(nil), tx.Outputs[*tapeVout].LockingScript.Bytes()...)
	}
	return result, nil
}

func tbc20StandardCurrentOutputData(tx *bt.Tx, outputGroups []TBC20StandardOutputGroup) ([TBC20StandardMaxOutputGroups]tbc20StandardOutputGroupData, error) {
	var result [TBC20StandardMaxOutputGroups]tbc20StandardOutputGroupData
	if err := tbc20StandardAssertVersion10(tx, "currentTx"); err != nil {
		return result, err
	}
	if len(tx.Outputs) > TBC20StandardMaxOutputs || len(outputGroups) < 1 || len(outputGroups) > TBC20StandardMaxOutputGroups {
		return result, fmt.Errorf("TBC20Standard unlock: invalid current output group count")
	}
	next := 0
	for i, group := range outputGroups {
		if group.CodeVout != next {
			return result, fmt.Errorf("TBC20Standard unlock: outputGroups[%d].codeVout must be %d", i, next)
		}
		if group.TapeVout != nil {
			if *group.TapeVout != group.CodeVout+1 {
				return result, fmt.Errorf("TBC20Standard unlock: tape must immediately follow code")
			}
			next += 2
		} else {
			next++
		}
		data, err := tbc20StandardOutputGroup(tx, group.CodeVout, group.TapeVout)
		if err != nil {
			return result, err
		}
		result[i] = data
	}
	if next != len(tx.Outputs) {
		return result, fmt.Errorf("TBC20Standard unlock: output groups do not cover every physical output")
	}
	return result, nil
}

func tbc20StandardPreTx(tx *bt.Tx, codeVout int) (tbc20StandardPreTxData, error) {
	var result tbc20StandardPreTxData
	if err := tbc20StandardAssertVersion10(tx, "preTx"); err != nil {
		return result, err
	}
	if len(tx.Inputs) > TBC20StandardMaxInputs || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20Standard unlock: invalid preTx TBC20Standard output")
	}
	if tx.Outputs[codeVout].Satoshis != TBC20StandardCodeSatoshis || tx.Outputs[codeVout+1].Satoshis != TBC20StandardTapeSatoshis {
		return result, fmt.Errorf("TBC20Standard unlock: preTx code/tape values must be 500/0")
	}
	if _, err := ReadTBC20StandardTapeAmounts(tx.Outputs[codeVout+1].LockingScript); err != nil {
		return result, err
	}
	result.vlio = tbc20StandardVLIO(tx)
	for i, input := range tx.Inputs {
		record, err := tbc20StandardInputRecord(input)
		if err != nil {
			return result, err
		}
		result.inputs[i] = record
	}
	result.unlockingScriptHash = tbc20StandardUnlockingScriptsHash(tx)
	var err error
	result.outputsFirstPart, err = tbc20StandardOutputHashRecords(tx, 0, codeVout)
	if err != nil {
		return result, err
	}
	tapeVout := codeVout + 1
	result.outputsGotData, err = tbc20StandardOutputGroup(tx, codeVout, &tapeVout)
	if err != nil {
		return result, err
	}
	result.outputsLastPart, err = tbc20StandardOutputHashRecords(tx, codeVout+2, len(tx.Outputs))
	return result, err
}

func tbc20StandardPrePreTx(tx *bt.Tx, vout int) (tbc20StandardPrePreTxData, error) {
	var result tbc20StandardPrePreTxData
	if err := tbc20StandardAssertVersion10(tx, "prepreTx"); err != nil {
		return result, err
	}
	if vout < 0 || vout >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20Standard unlock: prepreTx vout is out of range")
	}
	result.vlio = tbc20StandardVLIO(tx)
	var err error
	result.txInputsHashData, err = tbc20StandardInputsHashData(tx)
	if err != nil {
		return result, err
	}
	result.outputsFirstPart, err = tbc20StandardOutputHashRecords(tx, 0, vout)
	if err != nil {
		return result, err
	}
	result.outputsVerifiedData, err = tbc20StandardOutput(tx, vout)
	if err != nil {
		return result, err
	}
	result.outputsLastPart, err = tbc20StandardOutputHashRecords(tx, vout+1, len(tx.Outputs))
	return result, err
}

func tbc20StandardPrePreArray(preTx *bt.Tx, codeVout int, resolver TBC20StandardAncestorResolver) ([TBC20StandardMaxInputs]tbc20StandardPrePreTxData, error) {
	var result [TBC20StandardMaxInputs]tbc20StandardPrePreTxData
	preData, err := tbc20StandardPreTx(preTx, codeVout)
	if err != nil {
		return result, err
	}
	amounts, err := ReadTBC20StandardTapeAmounts(bscript.NewFromBytes(preData.outputsGotData.tapeScript))
	if err != nil {
		return result, err
	}
	for parentIndex := 0; parentIndex < TBC20StandardMaxInputs; parentIndex++ {
		if amounts[parentIndex] == 0 {
			continue
		}
		if parentIndex >= len(preTx.Inputs) {
			return result, fmt.Errorf("TBC20Standard unlock: tape slot %d has no matching input", parentIndex)
		}
		if resolver == nil {
			return result, fmt.Errorf("TBC20Standard unlock: missing ancestor resolver")
		}
		txid := strings.ToLower(hex.EncodeToString(preTx.Inputs[parentIndex].PreviousTxID()))
		ancestor, ok := resolver.ResolveTBC20StandardAncestor(txid)
		if !ok || ancestor == nil {
			return result, fmt.Errorf("TBC20Standard unlock: missing ancestor transaction %s", txid)
		}
		if !strings.EqualFold(ancestor.TxID(), txid) {
			return result, fmt.Errorf("TBC20Standard unlock: ancestor transaction hash mismatch")
		}
		data, err := tbc20StandardPrePreTx(ancestor, int(preTx.Inputs[parentIndex].PreviousTxOutIndex))
		if err != nil {
			return result, err
		}
		result[TBC20StandardMaxInputs-1-parentIndex] = data
	}
	return result, nil
}

func tbc20StandardEmptyContract() tbc20StandardContractTxData { return tbc20StandardContractTxData{} }

func tbc20StandardContract(tx *bt.Tx, vout int) (tbc20StandardContractTxData, error) {
	var result tbc20StandardContractTxData
	if err := tbc20StandardAssertVersion10(tx, "contractTx"); err != nil {
		return result, err
	}
	if vout < 0 || vout >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20Standard unlock: contract vout out of range")
	}
	result.vlio = tbc20StandardVLIO(tx)
	var err error
	result.txInputsHashData, err = tbc20StandardInputsHashData(tx)
	if err != nil {
		return result, err
	}
	result.outputsFirstPart, err = tbc20StandardOutputHashRecords(tx, 0, vout)
	if err != nil {
		return result, err
	}
	result.middleValue = EncodeTBC20StandardUInt64LE(tx.Outputs[vout].Satoshis)
	result.middleLockingScript = crypto.Sha256(tx.Outputs[vout].LockingScript.Bytes())
	result.outputsLastPart, err = tbc20StandardOutputHashRecords(tx, vout+1, len(tx.Outputs))
	return result, err
}

func tbc20StandardResolveController(currentTx *bt.Tx, controller [21]byte, witness *TBC20StandardContractControllerWitness) (tbc20StandardContractTxData, int, error) {
	if controller[20] == 0 {
		if witness != nil {
			return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: contractController must be omitted for address controller")
		}
		return tbc20StandardEmptyContract(), 0, nil
	}
	if controller[20] == 0x80 {
		return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: negative-zero controller")
	}
	if witness == nil || witness.Transaction == nil || witness.CurrentInputIndex < 0 || witness.CurrentInputIndex >= len(currentTx.Inputs) {
		return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: contract controller witness is required")
	}
	input := currentTx.Inputs[witness.CurrentInputIndex]
	if !strings.EqualFold(hex.EncodeToString(input.PreviousTxID()), witness.Transaction.TxID()) {
		return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: controller input transaction mismatch")
	}
	vout := int(input.PreviousTxOutIndex)
	if vout >= len(witness.Transaction.Outputs) {
		return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: controller vout out of range")
	}
	hash := crypto.Hash160(crypto.Sha256(witness.Transaction.Outputs[vout].LockingScript.Bytes()))
	if !bytes.Equal(hash, controller[:20]) {
		return tbc20StandardContractTxData{}, 0, fmt.Errorf("TBC20Standard unlock: controller hash mismatch")
	}
	data, err := tbc20StandardContract(witness.Transaction, vout)
	return data, witness.CurrentInputIndex, err
}

func tbc20StandardAppendPush(script *bscript.Script, data []byte) error {
	if len(data) == 1 && data[0] >= 1 && data[0] <= 16 {
		return script.AppendOpcodes(0x50 + data[0])
	}
	if len(data) == 1 && data[0] == 0x81 {
		return script.AppendOpcodes(0x4f)
	}
	return script.AppendPushData(data)
}

func tbc20StandardAppendNumber(script *bscript.Script, value int) error {
	if value < 0 {
		return fmt.Errorf("TBC20Standard unlock: negative number")
	}
	if value == 0 {
		return script.AppendOpcodes(bscript.Op0)
	}
	if value <= 16 {
		return script.AppendOpcodes(bscript.Op1 - 1 + byte(value))
	}
	return script.AppendPushData(EncodeTBC20StandardUnsignedLE(uint64(value)))
}

func tbc20StandardAddOutput(script *bscript.Script, output tbc20StandardOutputData) error {
	for _, data := range [][]byte{output.value, output.partial.SuffixData, output.partial.PartialHash, output.partial.Size} {
		if err := tbc20StandardAppendPush(script, data); err != nil {
			return err
		}
	}
	return nil
}

func tbc20StandardAddGroup(script *bscript.Script, group tbc20StandardOutputGroupData) error {
	if err := tbc20StandardAddOutput(script, group.code); err != nil {
		return err
	}
	if err := tbc20StandardAppendPush(script, group.tapeValue); err != nil {
		return err
	}
	return tbc20StandardAppendPush(script, group.tapeScript)
}

func tbc20StandardAddPrePre(script *bscript.Script, data tbc20StandardPrePreTxData) error {
	if err := tbc20StandardAppendPush(script, data.vlio); err != nil {
		return err
	}
	if err := tbc20StandardAppendPush(script, data.txInputsHashData); err != nil {
		return err
	}
	if err := tbc20StandardAppendPush(script, data.outputsFirstPart); err != nil {
		return err
	}
	if err := tbc20StandardAddOutput(script, data.outputsVerifiedData); err != nil {
		return err
	}
	return tbc20StandardAppendPush(script, data.outputsLastPart)
}

func tbc20StandardAddContract(script *bscript.Script, data tbc20StandardContractTxData) error {
	for _, item := range [][]byte{data.vlio, data.txInputsHashData, data.outputsFirstPart, data.middleValue, data.middleLockingScript, data.outputsLastPart} {
		if err := tbc20StandardAppendPush(script, item); err != nil {
			return err
		}
	}
	return nil
}

func tbc20StandardAddPreTx(script *bscript.Script, data tbc20StandardPreTxData) error {
	if err := tbc20StandardAppendPush(script, data.vlio); err != nil {
		return err
	}
	for _, input := range data.inputs {
		if err := tbc20StandardAppendPush(script, input); err != nil {
			return err
		}
	}
	if err := tbc20StandardAppendPush(script, data.unlockingScriptHash); err != nil {
		return err
	}
	if err := tbc20StandardAppendPush(script, data.outputsFirstPart); err != nil {
		return err
	}
	if err := tbc20StandardAddGroup(script, data.outputsGotData); err != nil {
		return err
	}
	return tbc20StandardAppendPush(script, data.outputsLastPart)
}

func BuildTBC20StandardUnlockScriptWithSignature(options TBC20StandardUnlockOptions) (*bscript.Script, error) {
	if err := tbc20StandardAssertVersion10(options.CurrentTx, "currentTx"); err != nil {
		return nil, err
	}
	if err := tbc20StandardAssertVersion10(options.PreTx, "preTx"); err != nil {
		return nil, err
	}
	if options.InputIndex < 0 || options.InputIndex >= TBC20StandardAmountSlots || options.InputIndex >= len(options.CurrentTx.Inputs) {
		return nil, fmt.Errorf("TBC20Standard unlock: input index out of range")
	}
	if options.PreTxVout < 0 || options.PreTxVout >= len(options.PreTx.Outputs) {
		return nil, fmt.Errorf("TBC20Standard unlock: preTx vout out of range")
	}
	input := options.CurrentTx.Inputs[options.InputIndex]
	if !strings.EqualFold(hex.EncodeToString(input.PreviousTxID()), options.PreTx.TxID()) || int(input.PreviousTxOutIndex) != options.PreTxVout {
		return nil, fmt.Errorf("TBC20Standard unlock: current input does not spend preTx output")
	}
	if len(options.Signature) == 0 || len(options.Signature) > 72 || len(options.PublicKey) != 33 {
		return nil, fmt.Errorf("TBC20Standard unlock: invalid signature or compressed public key")
	}
	controller, err := GetTBC20StandardController(options.PreTx.Outputs[options.PreTxVout].LockingScript)
	if err != nil {
		return nil, err
	}
	return SerializeTBC20TokenUnlock(options, controller)
}

// SerializeTBC20TokenUnlock serializes the shared 123-field ABI after a family codec authenticates ownership and ancestry.
func SerializeTBC20TokenUnlock(options TBC20StandardUnlockOptions, controller [21]byte) (*bscript.Script, error) {
	if err := tbc20StandardAssertVersion10(options.CurrentTx, "currentTx"); err != nil {
		return nil, err
	}
	if err := tbc20StandardAssertVersion10(options.PreTx, "preTx"); err != nil {
		return nil, err
	}
	if options.InputIndex < 0 || options.InputIndex >= len(options.CurrentTx.Inputs) || options.PreTxVout < 0 || options.PreTxVout >= len(options.PreTx.Outputs) {
		return nil, fmt.Errorf("token input index out of range")
	}
	input := options.CurrentTx.Inputs[options.InputIndex]
	if !strings.EqualFold(hex.EncodeToString(input.PreviousTxID()), options.PreTx.TxID()) || int(input.PreviousTxOutIndex) != options.PreTxVout {
		return nil, fmt.Errorf("token input does not spend parent")
	}
	preData, err := tbc20StandardPreTx(options.PreTx, options.PreTxVout)
	if err != nil {
		return nil, err
	}
	if controller[20] == 0 && !bytes.Equal(crypto.Hash160(options.PublicKey), controller[:20]) {
		return nil, fmt.Errorf("TBC20Standard unlock: public key does not match controller")
	}
	contractData, controllerInputIndex, err := tbc20StandardResolveController(options.CurrentTx, controller, options.ContractController)
	if err != nil {
		return nil, err
	}
	outputs, err := tbc20StandardCurrentOutputData(options.CurrentTx, options.OutputGroups)
	if err != nil {
		return nil, err
	}
	currentInputs, err := tbc20StandardInputRecords(options.CurrentTx)
	if err != nil {
		return nil, err
	}
	ancestors, err := tbc20StandardPrePreArray(options.PreTx, options.PreTxVout, options.AncestorTransactions)
	if err != nil {
		return nil, err
	}

	unlock := bscript.NewFromBytes(nil)
	for _, output := range outputs {
		if err := tbc20StandardAddGroup(unlock, output); err != nil {
			return nil, err
		}
	}
	if err := tbc20StandardAppendPush(unlock, currentInputs); err != nil {
		return nil, err
	}
	if err := tbc20StandardAppendNumber(unlock, options.InputIndex); err != nil {
		return nil, err
	}
	for _, ancestor := range ancestors {
		if err := tbc20StandardAddPrePre(unlock, ancestor); err != nil {
			return nil, err
		}
	}
	if err := tbc20StandardAppendPush(unlock, options.Signature); err != nil {
		return nil, err
	}
	if err := tbc20StandardAppendPush(unlock, options.PublicKey); err != nil {
		return nil, err
	}
	if err := tbc20StandardAddContract(unlock, contractData); err != nil {
		return nil, err
	}
	if err := tbc20StandardAppendNumber(unlock, controllerInputIndex); err != nil {
		return nil, err
	}
	if err := tbc20StandardAddPreTx(unlock, preData); err != nil {
		return nil, err
	}
	if len(unlock.Chunks()) != 123 {
		return nil, fmt.Errorf("TBC20Standard unlock: internal ABI expected 123 pushes, built %d", len(unlock.Chunks()))
	}
	return unlock, nil
}

func BuildTBC20StandardUnlockScript(options TBC20StandardUnlockOptions) (*bscript.Script, error) {
	if options.PrivateKey == nil {
		return nil, fmt.Errorf("TBC20Standard unlock: private key is required")
	}
	hash, err := options.CurrentTx.CalcInputSignatureHash(uint32(options.InputIndex), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	signature, err := options.PrivateKey.Sign(hash)
	if err != nil {
		return nil, err
	}
	options.Signature = append(signature.Serialise(), byte(sighash.AllForkID))
	options.PublicKey = options.PrivateKey.PubKey().SerialiseCompressed()
	return BuildTBC20StandardUnlockScriptWithSignature(options)
}
