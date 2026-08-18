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
	TBC20MaxInputs       = 6
	TBC20MaxOutputGroups = 8
	TBC20MaxOutputs      = TBC20MaxOutputGroups * 2
	TBC20CodeSatoshis    = 500
	TBC20TapeSatoshis    = 0
	TBC20AmountSlots     = 6
	TBC20AmountBytes     = TBC20AmountSlots * 8
	TBC20MinTapeBytes    = 60
	TBC20MaxTapeBytes    = 127
	TBC20MaxSlotAmount   = uint64(1<<63 - 1)
)

var (
	TBC20TapePrefix = []byte{0x00, 0x6a, 0x30}
	TBC20TapeMarker = []byte("TBC20TAPE")
)

type TBC20PartialScriptData struct {
	SuffixData  []byte
	PartialHash []byte
	Size        []byte
}

// EncodeTBC20UnsignedLE returns the minimal non-negative ScriptNum encoding
// used by the APC number fields in the TBC20 ABI.
func EncodeTBC20UnsignedLE(value uint64) []byte {
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

func EncodeTBC20UInt64LE(value uint64) []byte {
	result := make([]byte, 8)
	binary.LittleEndian.PutUint64(result, value)
	return result
}

func GetTBC20PartialScriptData(script *bscript.Script) (TBC20PartialScriptData, error) {
	if script == nil || len(script.Bytes()) == 0 {
		return TBC20PartialScriptData{}, fmt.Errorf("TBC20 unlock: locking script must be non-empty")
	}
	lockingScript := script.Bytes()
	partialOffset := len(lockingScript) / 64 * 64
	result := TBC20PartialScriptData{Size: EncodeTBC20UnsignedLE(uint64(len(lockingScript)))}
	if partialOffset == 0 {
		result.SuffixData = append([]byte(nil), lockingScript...)
		return result, nil
	}
	partialHex := partialsha256.CalculatePartialHash(lockingScript[:partialOffset])
	partialHash, err := hex.DecodeString(partialHex)
	if err != nil || len(partialHash) != 32 {
		return TBC20PartialScriptData{}, fmt.Errorf("TBC20 unlock: invalid partial SHA-256 state")
	}
	result.PartialHash = partialHash
	result.SuffixData = append([]byte(nil), lockingScript[partialOffset:]...)
	return result, nil
}

func ReadTBC20TapeAmounts(script *bscript.Script) ([TBC20AmountSlots]uint64, error) {
	var amounts [TBC20AmountSlots]uint64
	if script == nil {
		return amounts, fmt.Errorf("TBC20 unlock: nil tape script")
	}
	tape := script.Bytes()
	if len(tape) < TBC20MinTapeBytes || len(tape) > TBC20MaxTapeBytes {
		return amounts, fmt.Errorf("TBC20 unlock: tape script must be %d-%d bytes, got %d", TBC20MinTapeBytes, TBC20MaxTapeBytes, len(tape))
	}
	if !bytes.Equal(tape[:len(TBC20TapePrefix)], TBC20TapePrefix) {
		return amounts, fmt.Errorf("TBC20 unlock: tape script must start with OP_FALSE OP_RETURN PUSH48 (006a30)")
	}
	if !bytes.Equal(tape[len(tape)-len(TBC20TapeMarker):], TBC20TapeMarker) {
		return amounts, fmt.Errorf("TBC20 unlock: tape script must end with ASCII TBC20TAPE")
	}
	for i := range amounts {
		amounts[i] = binary.LittleEndian.Uint64(tape[len(TBC20TapePrefix)+i*8:])
		if amounts[i] > TBC20MaxSlotAmount {
			return amounts, fmt.Errorf("TBC20 unlock: tape amount slot %d exceeds the contract-safe signed-63-bit range", i)
		}
	}
	return amounts, nil
}

func ReplaceTBC20TapeAmounts(script *bscript.Script, amounts [TBC20AmountSlots]uint64) (*bscript.Script, error) {
	if _, err := ReadTBC20TapeAmounts(script); err != nil {
		return nil, err
	}
	result := append([]byte(nil), script.Bytes()...)
	for i, amount := range amounts {
		if amount > TBC20MaxSlotAmount {
			return nil, fmt.Errorf("TBC20 unlock: amount slot %d exceeds maximum", i)
		}
		binary.LittleEndian.PutUint64(result[len(TBC20TapePrefix)+i*8:], amount)
	}
	return bscript.NewFromBytes(result), nil
}

func GetTBC20Controller(codeScript *bscript.Script) ([21]byte, error) {
	var controller [21]byte
	if codeScript == nil {
		return controller, fmt.Errorf("TBC20 unlock: nil code script")
	}
	code := codeScript.Bytes()
	terminalMarker := []byte{0x05, 0x32, 0x43, 0x6f, 0x64, 0x65}
	if len(code) < 28 || !bytes.Equal(code[len(code)-len(terminalMarker):], terminalMarker) {
		return controller, fmt.Errorf("TBC20 unlock: code script is missing the terminal 2Code marker")
	}
	offset := len(code) - 28
	if code[offset] != 21 {
		return controller, fmt.Errorf("TBC20 unlock: terminal controller must be a direct 21-byte push")
	}
	copy(controller[:], code[offset+1:offset+22])
	return controller, nil
}

func GetTBC20CodeIdentity(codeScript *bscript.Script) ([]byte, error) {
	partial, err := GetTBC20PartialScriptData(codeScript)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), partial.PartialHash...), partial.Size...), nil
}

type TBC20OutputGroup struct {
	CodeVout int
	TapeVout *int
}

type TBC20AncestorResolver interface {
	ResolveTBC20Ancestor(txid string) (*bt.Tx, bool)
}

type TBC20ContractControllerWitness struct {
	Transaction       *bt.Tx
	CurrentInputIndex int
}

type TBC20UnlockOptions struct {
	CurrentTx            *bt.Tx
	InputIndex           int
	PreTx                *bt.Tx
	PreTxVout            int
	OutputGroups         []TBC20OutputGroup
	AncestorTransactions TBC20AncestorResolver
	ContractController   *TBC20ContractControllerWitness
	Signature            []byte
	PublicKey            []byte
	PrivateKey           *bec.PrivateKey
}

type tbc20OutputData struct {
	value   []byte
	partial TBC20PartialScriptData
}

type tbc20OutputGroupData struct {
	code       tbc20OutputData
	tapeValue  []byte
	tapeScript []byte
}

type tbc20PreTxData struct {
	vlio                []byte
	inputs              [TBC20MaxInputs][]byte
	unlockingScriptHash []byte
	outputsFirstPart    []byte
	outputsGotData      tbc20OutputGroupData
	outputsLastPart     []byte
}

type tbc20PrePreTxData struct {
	vlio                []byte
	txInputsHashData    []byte
	outputsFirstPart    []byte
	outputsVerifiedData tbc20OutputData
	outputsLastPart     []byte
}

type tbc20ContractTxData struct {
	vlio                []byte
	txInputsHashData    []byte
	outputsFirstPart    []byte
	middleValue         []byte
	middleLockingScript []byte
	outputsLastPart     []byte
}

func tbc20AssertVersion10(tx *bt.Tx, name string) error {
	if tx == nil {
		return fmt.Errorf("TBC20 unlock: %s must be a transaction", name)
	}
	if tx.Version != 10 {
		return fmt.Errorf("TBC20 unlock: %s.version must be exactly 10", name)
	}
	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return fmt.Errorf("TBC20 unlock: %s must contain inputs and outputs", name)
	}
	return nil
}

func tbc20VLIO(tx *bt.Tx) []byte {
	result := make([]byte, 16)
	binary.LittleEndian.PutUint32(result, tx.Version)
	binary.LittleEndian.PutUint32(result[4:], tx.LockTime)
	binary.LittleEndian.PutUint32(result[8:], uint32(len(tx.Inputs)))
	binary.LittleEndian.PutUint32(result[12:], uint32(len(tx.Outputs)))
	return result
}

func tbc20InputRecord(input *bt.Input) ([]byte, error) {
	if input == nil || len(input.PreviousTxID()) != 32 {
		return nil, fmt.Errorf("TBC20 unlock: invalid transaction input")
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

func tbc20InputRecords(tx *bt.Tx) ([]byte, error) {
	result := make([]byte, 0, len(tx.Inputs)*40)
	for _, input := range tx.Inputs {
		record, err := tbc20InputRecord(input)
		if err != nil {
			return nil, err
		}
		result = append(result, record...)
	}
	return result, nil
}

func tbc20UnlockingScriptsHash(tx *bt.Tx) []byte {
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

func tbc20InputsHashData(tx *bt.Tx) ([]byte, error) {
	records, err := tbc20InputRecords(tx)
	if err != nil {
		return nil, err
	}
	return append(crypto.Sha256(records), tbc20UnlockingScriptsHash(tx)...), nil
}

func tbc20OutputHashRecords(tx *bt.Tx, start, end int) ([]byte, error) {
	if start < 0 || end < start || end > len(tx.Outputs) {
		return nil, fmt.Errorf("TBC20 unlock: output range is invalid")
	}
	result := make([]byte, 0, (end-start)*40)
	for i := start; i < end; i++ {
		if tx.Outputs[i] == nil || tx.Outputs[i].LockingScript == nil {
			return nil, fmt.Errorf("TBC20 unlock: output %d is invalid", i)
		}
		result = append(result, EncodeTBC20UInt64LE(tx.Outputs[i].Satoshis)...)
		result = append(result, crypto.Sha256(tx.Outputs[i].LockingScript.Bytes())...)
	}
	return result, nil
}

func tbc20Output(tx *bt.Tx, index int) (tbc20OutputData, error) {
	if tx == nil || index < 0 || index >= len(tx.Outputs) || tx.Outputs[index] == nil {
		return tbc20OutputData{}, fmt.Errorf("TBC20 unlock: output %d is out of range", index)
	}
	partial, err := GetTBC20PartialScriptData(tx.Outputs[index].LockingScript)
	if err != nil {
		return tbc20OutputData{}, err
	}
	return tbc20OutputData{value: EncodeTBC20UInt64LE(tx.Outputs[index].Satoshis), partial: partial}, nil
}

func tbc20OutputGroup(tx *bt.Tx, codeVout int, tapeVout *int) (tbc20OutputGroupData, error) {
	code, err := tbc20Output(tx, codeVout)
	if err != nil {
		return tbc20OutputGroupData{}, err
	}
	result := tbc20OutputGroupData{code: code}
	if tapeVout != nil {
		if *tapeVout < 0 || *tapeVout >= len(tx.Outputs) {
			return result, fmt.Errorf("TBC20 unlock: tape output is out of range")
		}
		result.tapeValue = EncodeTBC20UInt64LE(tx.Outputs[*tapeVout].Satoshis)
		result.tapeScript = append([]byte(nil), tx.Outputs[*tapeVout].LockingScript.Bytes()...)
	}
	return result, nil
}

func tbc20CurrentOutputData(tx *bt.Tx, outputGroups []TBC20OutputGroup) ([TBC20MaxOutputGroups]tbc20OutputGroupData, error) {
	var result [TBC20MaxOutputGroups]tbc20OutputGroupData
	if err := tbc20AssertVersion10(tx, "currentTx"); err != nil {
		return result, err
	}
	if len(tx.Outputs) > TBC20MaxOutputs || len(outputGroups) < 1 || len(outputGroups) > TBC20MaxOutputGroups {
		return result, fmt.Errorf("TBC20 unlock: invalid current output group count")
	}
	next := 0
	for i, group := range outputGroups {
		if group.CodeVout != next {
			return result, fmt.Errorf("TBC20 unlock: outputGroups[%d].codeVout must be %d", i, next)
		}
		if group.TapeVout != nil {
			if *group.TapeVout != group.CodeVout+1 {
				return result, fmt.Errorf("TBC20 unlock: tape must immediately follow code")
			}
			next += 2
		} else {
			next++
		}
		data, err := tbc20OutputGroup(tx, group.CodeVout, group.TapeVout)
		if err != nil {
			return result, err
		}
		result[i] = data
	}
	if next != len(tx.Outputs) {
		return result, fmt.Errorf("TBC20 unlock: output groups do not cover every physical output")
	}
	return result, nil
}

func tbc20PreTx(tx *bt.Tx, codeVout int) (tbc20PreTxData, error) {
	var result tbc20PreTxData
	if err := tbc20AssertVersion10(tx, "preTx"); err != nil {
		return result, err
	}
	if len(tx.Inputs) > TBC20MaxInputs || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20 unlock: invalid preTx TBC20 output")
	}
	if tx.Outputs[codeVout].Satoshis != TBC20CodeSatoshis || tx.Outputs[codeVout+1].Satoshis != TBC20TapeSatoshis {
		return result, fmt.Errorf("TBC20 unlock: preTx code/tape values must be 500/0")
	}
	if _, err := ReadTBC20TapeAmounts(tx.Outputs[codeVout+1].LockingScript); err != nil {
		return result, err
	}
	result.vlio = tbc20VLIO(tx)
	for i, input := range tx.Inputs {
		record, err := tbc20InputRecord(input)
		if err != nil {
			return result, err
		}
		result.inputs[i] = record
	}
	result.unlockingScriptHash = tbc20UnlockingScriptsHash(tx)
	var err error
	result.outputsFirstPart, err = tbc20OutputHashRecords(tx, 0, codeVout)
	if err != nil {
		return result, err
	}
	tapeVout := codeVout + 1
	result.outputsGotData, err = tbc20OutputGroup(tx, codeVout, &tapeVout)
	if err != nil {
		return result, err
	}
	result.outputsLastPart, err = tbc20OutputHashRecords(tx, codeVout+2, len(tx.Outputs))
	return result, err
}

func tbc20PrePreTx(tx *bt.Tx, vout int) (tbc20PrePreTxData, error) {
	var result tbc20PrePreTxData
	if err := tbc20AssertVersion10(tx, "prepreTx"); err != nil {
		return result, err
	}
	if vout < 0 || vout >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20 unlock: prepreTx vout is out of range")
	}
	result.vlio = tbc20VLIO(tx)
	var err error
	result.txInputsHashData, err = tbc20InputsHashData(tx)
	if err != nil {
		return result, err
	}
	result.outputsFirstPart, err = tbc20OutputHashRecords(tx, 0, vout)
	if err != nil {
		return result, err
	}
	result.outputsVerifiedData, err = tbc20Output(tx, vout)
	if err != nil {
		return result, err
	}
	result.outputsLastPart, err = tbc20OutputHashRecords(tx, vout+1, len(tx.Outputs))
	return result, err
}

func tbc20PrePreArray(preTx *bt.Tx, codeVout int, resolver TBC20AncestorResolver) ([TBC20MaxInputs]tbc20PrePreTxData, error) {
	var result [TBC20MaxInputs]tbc20PrePreTxData
	preData, err := tbc20PreTx(preTx, codeVout)
	if err != nil {
		return result, err
	}
	amounts, err := ReadTBC20TapeAmounts(bscript.NewFromBytes(preData.outputsGotData.tapeScript))
	if err != nil {
		return result, err
	}
	for parentIndex := 0; parentIndex < TBC20MaxInputs; parentIndex++ {
		if amounts[parentIndex] == 0 {
			continue
		}
		if parentIndex >= len(preTx.Inputs) {
			return result, fmt.Errorf("TBC20 unlock: tape slot %d has no matching input", parentIndex)
		}
		if resolver == nil {
			return result, fmt.Errorf("TBC20 unlock: missing ancestor resolver")
		}
		txid := strings.ToLower(hex.EncodeToString(preTx.Inputs[parentIndex].PreviousTxID()))
		ancestor, ok := resolver.ResolveTBC20Ancestor(txid)
		if !ok || ancestor == nil {
			return result, fmt.Errorf("TBC20 unlock: missing ancestor transaction %s", txid)
		}
		if !strings.EqualFold(ancestor.TxID(), txid) {
			return result, fmt.Errorf("TBC20 unlock: ancestor transaction hash mismatch")
		}
		data, err := tbc20PrePreTx(ancestor, int(preTx.Inputs[parentIndex].PreviousTxOutIndex))
		if err != nil {
			return result, err
		}
		result[TBC20MaxInputs-1-parentIndex] = data
	}
	return result, nil
}

func tbc20EmptyContract() tbc20ContractTxData { return tbc20ContractTxData{} }

func tbc20Contract(tx *bt.Tx, vout int) (tbc20ContractTxData, error) {
	var result tbc20ContractTxData
	if err := tbc20AssertVersion10(tx, "contractTx"); err != nil {
		return result, err
	}
	if vout < 0 || vout >= len(tx.Outputs) {
		return result, fmt.Errorf("TBC20 unlock: contract vout out of range")
	}
	result.vlio = tbc20VLIO(tx)
	var err error
	result.txInputsHashData, err = tbc20InputsHashData(tx)
	if err != nil {
		return result, err
	}
	result.outputsFirstPart, err = tbc20OutputHashRecords(tx, 0, vout)
	if err != nil {
		return result, err
	}
	result.middleValue = EncodeTBC20UInt64LE(tx.Outputs[vout].Satoshis)
	result.middleLockingScript = crypto.Sha256(tx.Outputs[vout].LockingScript.Bytes())
	result.outputsLastPart, err = tbc20OutputHashRecords(tx, vout+1, len(tx.Outputs))
	return result, err
}

func tbc20ResolveController(currentTx *bt.Tx, controller [21]byte, witness *TBC20ContractControllerWitness) (tbc20ContractTxData, int, error) {
	if controller[20] == 0 {
		if witness != nil {
			return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: contractController must be omitted for address controller")
		}
		return tbc20EmptyContract(), 0, nil
	}
	if controller[20] == 0x80 {
		return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: negative-zero controller")
	}
	if witness == nil || witness.Transaction == nil || witness.CurrentInputIndex < 0 || witness.CurrentInputIndex >= len(currentTx.Inputs) {
		return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: contract controller witness is required")
	}
	input := currentTx.Inputs[witness.CurrentInputIndex]
	if !strings.EqualFold(hex.EncodeToString(input.PreviousTxID()), witness.Transaction.TxID()) {
		return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: controller input transaction mismatch")
	}
	vout := int(input.PreviousTxOutIndex)
	if vout >= len(witness.Transaction.Outputs) {
		return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: controller vout out of range")
	}
	hash := crypto.Hash160(crypto.Sha256(witness.Transaction.Outputs[vout].LockingScript.Bytes()))
	if !bytes.Equal(hash, controller[:20]) {
		return tbc20ContractTxData{}, 0, fmt.Errorf("TBC20 unlock: controller hash mismatch")
	}
	data, err := tbc20Contract(witness.Transaction, vout)
	return data, witness.CurrentInputIndex, err
}

func tbc20AppendPush(script *bscript.Script, data []byte) error { return script.AppendPushData(data) }

func tbc20AppendNumber(script *bscript.Script, value int) error {
	if value < 0 {
		return fmt.Errorf("TBC20 unlock: negative number")
	}
	if value == 0 {
		return script.AppendOpcodes(bscript.Op0)
	}
	if value <= 16 {
		return script.AppendOpcodes(bscript.Op1 - 1 + byte(value))
	}
	return script.AppendPushData(EncodeTBC20UnsignedLE(uint64(value)))
}

func tbc20AddOutput(script *bscript.Script, output tbc20OutputData) error {
	for _, data := range [][]byte{output.value, output.partial.SuffixData, output.partial.PartialHash, output.partial.Size} {
		if err := tbc20AppendPush(script, data); err != nil {
			return err
		}
	}
	return nil
}

func tbc20AddGroup(script *bscript.Script, group tbc20OutputGroupData) error {
	if err := tbc20AddOutput(script, group.code); err != nil {
		return err
	}
	if err := tbc20AppendPush(script, group.tapeValue); err != nil {
		return err
	}
	return tbc20AppendPush(script, group.tapeScript)
}

func tbc20AddPrePre(script *bscript.Script, data tbc20PrePreTxData) error {
	if err := tbc20AppendPush(script, data.vlio); err != nil {
		return err
	}
	if err := tbc20AppendPush(script, data.txInputsHashData); err != nil {
		return err
	}
	if err := tbc20AppendPush(script, data.outputsFirstPart); err != nil {
		return err
	}
	if err := tbc20AddOutput(script, data.outputsVerifiedData); err != nil {
		return err
	}
	return tbc20AppendPush(script, data.outputsLastPart)
}

func tbc20AddContract(script *bscript.Script, data tbc20ContractTxData) error {
	for _, item := range [][]byte{data.vlio, data.txInputsHashData, data.outputsFirstPart, data.middleValue, data.middleLockingScript, data.outputsLastPart} {
		if err := tbc20AppendPush(script, item); err != nil {
			return err
		}
	}
	return nil
}

func tbc20AddPreTx(script *bscript.Script, data tbc20PreTxData) error {
	if err := tbc20AppendPush(script, data.vlio); err != nil {
		return err
	}
	for _, input := range data.inputs {
		if err := tbc20AppendPush(script, input); err != nil {
			return err
		}
	}
	if err := tbc20AppendPush(script, data.unlockingScriptHash); err != nil {
		return err
	}
	if err := tbc20AppendPush(script, data.outputsFirstPart); err != nil {
		return err
	}
	if err := tbc20AddGroup(script, data.outputsGotData); err != nil {
		return err
	}
	return tbc20AppendPush(script, data.outputsLastPart)
}

func BuildTBC20UnlockScriptWithSignature(options TBC20UnlockOptions) (*bscript.Script, error) {
	if err := tbc20AssertVersion10(options.CurrentTx, "currentTx"); err != nil {
		return nil, err
	}
	if err := tbc20AssertVersion10(options.PreTx, "preTx"); err != nil {
		return nil, err
	}
	if options.InputIndex < 0 || options.InputIndex >= TBC20AmountSlots || options.InputIndex >= len(options.CurrentTx.Inputs) {
		return nil, fmt.Errorf("TBC20 unlock: input index out of range")
	}
	if options.PreTxVout < 0 || options.PreTxVout >= len(options.PreTx.Outputs) {
		return nil, fmt.Errorf("TBC20 unlock: preTx vout out of range")
	}
	input := options.CurrentTx.Inputs[options.InputIndex]
	if !strings.EqualFold(hex.EncodeToString(input.PreviousTxID()), options.PreTx.TxID()) || int(input.PreviousTxOutIndex) != options.PreTxVout {
		return nil, fmt.Errorf("TBC20 unlock: current input does not spend preTx output")
	}
	if len(options.Signature) == 0 || len(options.Signature) > 72 || len(options.PublicKey) != 33 {
		return nil, fmt.Errorf("TBC20 unlock: invalid signature or compressed public key")
	}
	preData, err := tbc20PreTx(options.PreTx, options.PreTxVout)
	if err != nil {
		return nil, err
	}
	controller, err := GetTBC20Controller(options.PreTx.Outputs[options.PreTxVout].LockingScript)
	if err != nil {
		return nil, err
	}
	if controller[20] == 0 && !bytes.Equal(crypto.Hash160(options.PublicKey), controller[:20]) {
		return nil, fmt.Errorf("TBC20 unlock: public key does not match controller")
	}
	contractData, controllerInputIndex, err := tbc20ResolveController(options.CurrentTx, controller, options.ContractController)
	if err != nil {
		return nil, err
	}
	outputs, err := tbc20CurrentOutputData(options.CurrentTx, options.OutputGroups)
	if err != nil {
		return nil, err
	}
	currentInputs, err := tbc20InputRecords(options.CurrentTx)
	if err != nil {
		return nil, err
	}
	ancestors, err := tbc20PrePreArray(options.PreTx, options.PreTxVout, options.AncestorTransactions)
	if err != nil {
		return nil, err
	}

	unlock := bscript.NewFromBytes(nil)
	for _, output := range outputs {
		if err := tbc20AddGroup(unlock, output); err != nil {
			return nil, err
		}
	}
	if err := tbc20AppendPush(unlock, currentInputs); err != nil {
		return nil, err
	}
	if err := tbc20AppendNumber(unlock, options.InputIndex); err != nil {
		return nil, err
	}
	for _, ancestor := range ancestors {
		if err := tbc20AddPrePre(unlock, ancestor); err != nil {
			return nil, err
		}
	}
	if err := tbc20AppendPush(unlock, options.Signature); err != nil {
		return nil, err
	}
	if err := tbc20AppendPush(unlock, options.PublicKey); err != nil {
		return nil, err
	}
	if err := tbc20AddContract(unlock, contractData); err != nil {
		return nil, err
	}
	if err := tbc20AppendNumber(unlock, controllerInputIndex); err != nil {
		return nil, err
	}
	if err := tbc20AddPreTx(unlock, preData); err != nil {
		return nil, err
	}
	if len(unlock.Chunks()) != 123 {
		return nil, fmt.Errorf("TBC20 unlock: internal ABI expected 123 pushes, built %d", len(unlock.Chunks()))
	}
	return unlock, nil
}

func BuildTBC20UnlockScript(options TBC20UnlockOptions) (*bscript.Script, error) {
	if options.PrivateKey == nil {
		return nil, fmt.Errorf("TBC20 unlock: private key is required")
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
	return BuildTBC20UnlockScriptWithSignature(options)
}
