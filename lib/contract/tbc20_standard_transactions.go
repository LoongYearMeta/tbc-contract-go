package contract

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/script/interpreter"
)

const TBC20StandardMaxSlotAmount = util.TBC20StandardMaxSlotAmount

var tbc20StandardMaximumLowSSignature = mustTBC20StandardHex("304502210080000000000000000000000000000000000000000000000000000000000000000220010000000000000000000000000000000000000000000000000000000000000041")

type TBC20StandardTokenOutput struct {
	CodeVout       int
	TapeVout       int
	Amount         *big.Int
	AmountsByInput [TBC20StandardAmountSlots]uint64
	Controller     [21]byte
}

type TBC20StandardBuildResult struct {
	Transaction  *bt.Tx
	TxRaw        string
	FeeSatoshis  uint64
	TokenOutputs []TBC20StandardTokenOutput
	OutputGroups []util.TBC20StandardOutputGroup
}

type TBC20StandardMintResult struct {
	TBC20StandardBuildResult
	SourceTransaction *bt.Tx
	SourceTxRaw       string
	SourceFeeSatoshis uint64
	OriginalUTXO      TBC20StandardOutpoint
}

type TBC20StandardAncestorResolver = util.TBC20StandardAncestorResolver

type TBC20StandardMintOptions struct {
	Verify *bool
}

type TBC20StandardTransferOptions struct {
	TBCChangeAddress string
	Verify           *bool
}

type TBC20StandardMergeOptions struct {
	TBC20StandardTransferOptions
	Controller string
}

type tbc20StandardValidatedInput struct {
	utxo       *bt.UTXO
	parent     *bt.Tx
	ancestors  TBC20StandardAncestorResolver
	balance    *big.Int
	code       *bscript.Script
	tape       *bscript.Script
	controller [21]byte
	identity   []byte
}

type tbc20StandardOutputPlan struct {
	controller [21]byte
	amount     *big.Int
	slots      [TBC20StandardAmountSlots]uint64
}

func mustTBC20StandardHex(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

func tbc20StandardVerifyEnabled(value *bool) bool {
	return value == nil || *value
}

func tbc20StandardAddressController(address string) ([21]byte, error) {
	var result [21]byte
	parsed, err := bscript.NewAddressFromString(address)
	if err != nil {
		return result, fmt.Errorf("TBC20Standard: invalid controller address: %w", err)
	}
	hash, err := hex.DecodeString(parsed.PublicKeyHash)
	if err != nil || len(hash) != 20 {
		return result, fmt.Errorf("TBC20Standard: controller address must contain a 20-byte public-key hash")
	}
	copy(result[:20], hash)
	return result, nil
}

func tbc20StandardKeyAddress(privateKey *bec.PrivateKey) (*bscript.Address, error) {
	if signerMissing(privateKey) {
		return nil, fmt.Errorf("TBC20Standard: private key is required")
	}
	return bscript.NewAddressFromPublicKey(privateKey.PubKey(), true)
}

func tbc20StandardAssertP2PKHOwner(utxo *bt.UTXO, privateKey TransactionSigner, name string) error {
	if utxo == nil || utxo.LockingScript == nil || len(utxo.TxID) != 32 {
		return fmt.Errorf("TBC20Standard: %s is invalid", name)
	}
	if !utxo.LockingScript.IsP2PKH() {
		return fmt.Errorf("TBC20Standard: %s must be a P2PKH output", name)
	}
	want, err := utxo.LockingScript.PublicKeyHash()
	if err != nil {
		return err
	}
	if signerMissing(privateKey) || !bytes.Equal(want, crypto.Hash160(privateKey.PubKey().SerialiseCompressed())) {
		return fmt.Errorf("TBC20Standard: %s is controlled by a different private key", name)
	}
	return nil
}

func tbc20StandardPlaceholderP2PKH(privateKey *bec.PrivateKey) (*bscript.Script, error) {
	script := bscript.NewFromBytes(nil)
	if err := script.AppendPushData(tbc20StandardMaximumLowSSignature); err != nil {
		return nil, err
	}
	if err := script.AppendPushData(privateKey.PubKey().SerialiseCompressed()); err != nil {
		return nil, err
	}
	return script, nil
}

func tbc20StandardPaidFee(inputs []*bt.UTXO, tx *bt.Tx) (uint64, error) {
	var inputTotal uint64
	for _, input := range inputs {
		if input == nil || ^uint64(0)-inputTotal < input.Satoshis {
			return 0, fmt.Errorf("TBC20Standard: input satoshi total overflows")
		}
		inputTotal += input.Satoshis
	}
	var outputTotal uint64
	for _, output := range tx.Outputs {
		if output == nil || ^uint64(0)-outputTotal < output.Satoshis {
			return 0, fmt.Errorf("TBC20Standard: output satoshi total overflows")
		}
		outputTotal += output.Satoshis
	}
	if outputTotal > inputTotal {
		return 0, fmt.Errorf("TBC20Standard: transaction outputs exceed inputs")
	}
	return inputTotal - outputTotal, nil
}

func tbc20StandardVerifyInput(tx *bt.Tx, inputIndex int, utxo *bt.UTXO) error {
	return interpreter.NewEngine().Execute(
		interpreter.WithTx(tx, inputIndex, &bt.Output{LockingScript: utxo.LockingScript, Satoshis: utxo.Satoshis}),
		interpreter.WithAfterGenesis(),
		interpreter.WithForkID(),
	)
}

func (t *TBC20Standard) Mint(privateKey *bec.PrivateKey, recipient string, funding *bt.UTXO, options *TBC20StandardMintOptions) (*TBC20StandardMintResult, error) {
	if t == nil || t.DeclaredSupplyRaw == nil {
		return nil, fmt.Errorf("TBC20Standard: mint requires constructor metadata with declared supply")
	}
	if t.CodeScript != nil || t.TapeScript != nil || t.ContractTxID != "" {
		return nil, fmt.Errorf("TBC20Standard: mint requires an uninitialized token")
	}
	if err := tbc20StandardAssertP2PKHOwner(funding, privateKey, "fundingUTXO"); err != nil {
		return nil, err
	}
	owner, err := tbc20StandardKeyAddress(privateKey)
	if err != nil {
		return nil, err
	}
	sourceScript, err := bscript.NewP2PKHFromAddress(owner.AddressString)
	if err != nil {
		return nil, err
	}
	if funding.Satoshis <= contractMinimumFee {
		return nil, fmt.Errorf("TBC20Standard: funding UTXO is too small for source fee")
	}
	sourceValue := funding.Satoshis - contractMinimumFee
	source := newFTTx()
	if err := source.FromUTXOs(funding); err != nil {
		return nil, err
	}
	source.AddOutput(&bt.Output{LockingScript: sourceScript, Satoshis: sourceValue})
	if err := signP2PKHInput(source, privateKey, 0); err != nil {
		return nil, err
	}
	sourceTarget, err := contractTargetFee(len(source.Bytes()))
	if err != nil {
		return nil, err
	}
	if sourceTarget != contractMinimumFee {
		return nil, fmt.Errorf("TBC20Standard: source transaction unexpectedly exceeds the minimum-fee size")
	}
	sourceFee, err := tbc20StandardPaidFee([]*bt.UTXO{funding}, source)
	if err != nil {
		return nil, err
	}

	original := TBC20StandardOutpoint{TxID: source.TxID(), OutputIndex: 0}
	controller, err := tbc20StandardAddressController(recipient)
	if err != nil {
		return nil, err
	}
	code, err := InstantiateTBC20StandardCode(original, controller, t.TapeSize)
	if err != nil {
		return nil, err
	}
	var genesisAmounts [TBC20StandardAmountSlots]uint64
	genesisAmounts[0] = t.DeclaredSupplyRaw.Uint64()
	tape, err := BuildTBC20StandardTape(genesisAmounts, t.TapeSize, t.ExtensionData)
	if err != nil {
		return nil, err
	}
	sourceUTXO := &bt.UTXO{TxID: mustTBC20StandardHex(source.TxID()), Vout: 0, LockingScript: sourceScript, Satoshis: sourceValue}
	buildGenesis := func(change *uint64, estimated bool) (*bt.Tx, []util.TBC20StandardOutputGroup, error) {
		tx := newFTTx()
		if err := tx.FromUTXOs(sourceUTXO); err != nil {
			return nil, nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: TBC20StandardCodeSatoshis})
		tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: TBC20StandardTapeSatoshis})
		tapeVout := 1
		groups := []util.TBC20StandardOutputGroup{{CodeVout: 0, TapeVout: &tapeVout}}
		if change != nil {
			tx.AddOutput(&bt.Output{LockingScript: sourceScript, Satoshis: *change})
			groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: 2})
		}
		if estimated {
			placeholder, err := tbc20StandardPlaceholderP2PKH(privateKey)
			if err != nil {
				return nil, nil, err
			}
			if err := tx.InsertInputUnlockingScript(0, placeholder); err != nil {
				return nil, nil, err
			}
		} else if err := signP2PKHInput(tx, privateKey, 0); err != nil {
			return nil, nil, err
		}
		return tx, groups, nil
	}
	available := sourceValue - TBC20StandardCodeSatoshis
	dust := sdkDustLimit
	withChange, _, err := buildGenesis(&dust, true)
	if err != nil {
		return nil, err
	}
	withChangeFee, err := contractTargetFee(len(withChange.Bytes()))
	if err != nil {
		return nil, err
	}
	var selectedChange *uint64
	if available >= withChangeFee && available-withChangeFee >= sdkDustLimit {
		value := available - withChangeFee
		selectedChange = &value
	} else {
		withoutChange, _, err := buildGenesis(nil, true)
		if err != nil {
			return nil, err
		}
		fee, err := contractTargetFee(len(withoutChange.Bytes()))
		if err != nil || available < fee {
			return nil, fmt.Errorf("TBC20Standard: source output cannot pay genesis fee")
		}
	}
	genesis, groups, err := buildGenesis(selectedChange, false)
	if err != nil {
		return nil, err
	}
	genesisFee, err := tbc20StandardPaidFee([]*bt.UTXO{sourceUTXO}, genesis)
	if err != nil {
		return nil, err
	}
	var verifyOption *bool
	if options != nil {
		verifyOption = options.Verify
	}
	if tbc20StandardVerifyEnabled(verifyOption) {
		if err := tbc20StandardVerifyInput(source, 0, funding); err != nil {
			return nil, fmt.Errorf("TBC20Standard: source input failed local verification: %w", err)
		}
		if err := tbc20StandardVerifyInput(genesis, 0, sourceUTXO); err != nil {
			return nil, fmt.Errorf("TBC20Standard: genesis input failed local verification: %w", err)
		}
	}
	t.CodeScript, t.TapeScript, t.ContractTxID = code, tape, genesis.TxID()
	return &TBC20StandardMintResult{
		TBC20StandardBuildResult: TBC20StandardBuildResult{
			Transaction: genesis, TxRaw: genesis.String(), FeeSatoshis: genesisFee,
			TokenOutputs: []TBC20StandardTokenOutput{{CodeVout: 0, TapeVout: 1, Amount: new(big.Int).Set(t.DeclaredSupplyRaw), AmountsByInput: genesisAmounts, Controller: controller}},
			OutputGroups: groups,
		},
		SourceTransaction: source, SourceTxRaw: source.String(), SourceFeeSatoshis: sourceFee, OriginalUTXO: original,
	}, nil
}

func (t *TBC20Standard) validateTransferInputs(privateKey *bec.PrivateKey, tokenUTXOs []*bt.UTXO, parentTXs []*bt.Tx, ancestors []TBC20StandardAncestorResolver) ([]tbc20StandardValidatedInput, error) {
	if signerMissing(privateKey) {
		return nil, fmt.Errorf("TBC20Standard: private key is required")
	}
	if len(tokenUTXOs) < 1 || len(tokenUTXOs) > TBC20StandardMaxInputs-1 || len(parentTXs) != len(tokenUTXOs) || len(ancestors) != len(tokenUTXOs) {
		return nil, fmt.Errorf("TBC20Standard: positional transfer requires 1-%d token inputs with matching parents and ancestor resolvers", TBC20StandardMaxInputs-1)
	}
	keyHash := crypto.Hash160(privateKey.PubKey().SerialiseCompressed())
	validated := make([]tbc20StandardValidatedInput, len(tokenUTXOs))
	for index, utxo := range tokenUTXOs {
		parent := parentTXs[index]
		if utxo == nil || parent == nil || ancestors[index] == nil || int(utxo.Vout)+1 >= len(parent.Outputs) || !strings.EqualFold(utxo.TxIDStr(), parent.TxID()) {
			return nil, fmt.Errorf("TBC20Standard: token input %d does not match its parent transaction", index)
		}
		codeOutput, tapeOutput := parent.Outputs[utxo.Vout], parent.Outputs[utxo.Vout+1]
		if codeOutput.Satoshis != TBC20StandardCodeSatoshis || tapeOutput.Satoshis != TBC20StandardTapeSatoshis || utxo.Satoshis != codeOutput.Satoshis || !bytes.Equal(utxo.LockingScript.Bytes(), codeOutput.LockingScript.Bytes()) {
			return nil, fmt.Errorf("TBC20Standard: token input %d parent code/tape output differs", index)
		}
		if err := ValidateTBC20StandardCode(codeOutput.LockingScript, t.TapeSize); err != nil {
			return nil, fmt.Errorf("TBC20Standard: token input %d has invalid code: %w", index, err)
		}
		parsed, err := ParseTBC20StandardTape(tapeOutput.LockingScript)
		if err != nil || parsed.Size != t.TapeSize || !bytes.Equal(parsed.ExtensionData, t.ExtensionData) {
			return nil, fmt.Errorf("TBC20Standard: token input %d has incompatible tape", index)
		}
		controller, err := TBC20StandardController(codeOutput.LockingScript)
		if err != nil || controller[20] != 0 || !bytes.Equal(controller[:20], keyHash) {
			return nil, fmt.Errorf("TBC20Standard: token input %d is not controlled by the signing key", index)
		}
		identity, err := TBC20StandardCodeIdentity(codeOutput.LockingScript)
		if err != nil {
			return nil, err
		}
		if t.CodeScript != nil {
			canonical, err := TBC20StandardCodeIdentity(t.CodeScript)
			if err != nil || !bytes.Equal(identity, canonical) {
				return nil, fmt.Errorf("TBC20Standard: token input %d belongs to another token", index)
			}
		}
		balance := new(big.Int)
		for _, amount := range parsed.Amounts {
			balance.Add(balance, new(big.Int).SetUint64(amount))
		}
		validated[index] = tbc20StandardValidatedInput{utxo: utxo, parent: parent, ancestors: ancestors[index], balance: balance, code: codeOutput.LockingScript, tape: tapeOutput.LockingScript, controller: controller, identity: identity}
	}
	canonicalTape := validated[0].tape.Bytes()
	for index := 1; index < len(validated); index++ {
		candidate := validated[index].tape.Bytes()
		if !bytes.Equal(validated[index].identity, validated[0].identity) || !bytes.Equal(append(append([]byte(nil), canonicalTape[:3]...), canonicalTape[51:]...), append(append([]byte(nil), candidate[:3]...), candidate[51:]...)) {
			return nil, fmt.Errorf("TBC20Standard: token input %d belongs to a different identity or tape envelope", index)
		}
	}
	return validated, nil
}

func tbc20StandardAllocate(plans *[]tbc20StandardOutputPlan, remaining []*big.Int, controller [21]byte, requested *big.Int) error {
	needed := new(big.Int).Set(requested)
	maximum := new(big.Int).SetUint64(TBC20StandardMaxSlotAmount)
	for needed.Sign() > 0 {
		plan := tbc20StandardOutputPlan{controller: controller, amount: new(big.Int)}
		for inputIndex := range remaining {
			if needed.Sign() == 0 {
				break
			}
			available := new(big.Int).Set(remaining[inputIndex])
			if available.Cmp(maximum) > 0 {
				available.Set(maximum)
			}
			if available.Cmp(needed) > 0 {
				available.Set(needed)
			}
			if available.Sign() == 0 {
				continue
			}
			plan.slots[inputIndex] = available.Uint64()
			remaining[inputIndex].Sub(remaining[inputIndex], available)
			needed.Sub(needed, available)
			plan.amount.Add(plan.amount, available)
		}
		if plan.amount.Sign() == 0 {
			return fmt.Errorf("TBC20Standard: internal token allocation failure")
		}
		*plans = append(*plans, plan)
		if len(*plans) > TBC20StandardMaxOutputGroups {
			return fmt.Errorf("TBC20Standard: token amounts require more than %d output groups", TBC20StandardMaxOutputGroups)
		}
	}
	return nil
}

func (t *TBC20Standard) transferRaw(privateKey *bec.PrivateKey, receiverController [21]byte, amount *big.Int, tokenUTXOs []*bt.UTXO, feeUTXO *bt.UTXO, parentTXs []*bt.Tx, ancestors []TBC20StandardAncestorResolver, options *TBC20StandardTransferOptions) (*TBC20StandardBuildResult, error) {
	if signerMissing(privateKey) || amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("TBC20Standard: positive transfer amount and private key are required")
	}
	if err := tbc20StandardAssertP2PKHOwner(feeUTXO, privateKey, "feeUTXO"); err != nil {
		return nil, err
	}
	validated, err := t.validateTransferInputs(privateKey, tokenUTXOs, parentTXs, ancestors)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(tokenUTXOs)+1)
	for _, input := range append(append([]*bt.UTXO(nil), tokenUTXOs...), feeUTXO) {
		key := fmt.Sprintf("%s:%d", strings.ToLower(input.TxIDStr()), input.Vout)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("TBC20Standard: duplicate input outpoint %s", key)
		}
		seen[key] = struct{}{}
	}
	remaining := make([]*big.Int, len(validated))
	total := new(big.Int)
	for index, input := range validated {
		remaining[index] = new(big.Int).Set(input.balance)
		total.Add(total, input.balance)
	}
	if amount.Cmp(total) > 0 {
		return nil, fmt.Errorf("TBC20Standard: transfer amount exceeds available balance")
	}
	plans := make([]tbc20StandardOutputPlan, 0, 2)
	if err := tbc20StandardAllocate(&plans, remaining, receiverController, amount); err != nil {
		return nil, err
	}
	changeAmount := new(big.Int).Sub(total, amount)
	if changeAmount.Sign() > 0 {
		if err := tbc20StandardAllocate(&plans, remaining, validated[0].controller, changeAmount); err != nil {
			return nil, err
		}
	}
	allUTXOs := append(append([]*bt.UTXO(nil), tokenUTXOs...), feeUTXO)
	var inputSatoshis uint64
	for _, input := range allUTXOs {
		if ^uint64(0)-inputSatoshis < input.Satoshis {
			return nil, fmt.Errorf("TBC20Standard: input satoshi total overflows")
		}
		inputSatoshis += input.Satoshis
	}
	fixedSatoshis := uint64(len(plans) * TBC20StandardCodeSatoshis)
	if inputSatoshis < fixedSatoshis {
		return nil, fmt.Errorf("TBC20Standard: inputs cannot fund token code outputs")
	}
	available := inputSatoshis - fixedSatoshis
	changeAddress := ""
	if options != nil {
		changeAddress = options.TBCChangeAddress
	}
	if changeAddress == "" {
		owner, err := tbc20StandardKeyAddress(privateKey)
		if err != nil {
			return nil, err
		}
		changeAddress = owner.AddressString
	}
	changeScript, err := bscript.NewP2PKHFromAddress(changeAddress)
	if err != nil {
		return nil, err
	}
	build := func(change *uint64, estimated bool) (*TBC20StandardBuildResult, error) {
		tx := newFTTx()
		if err := tx.FromUTXOs(allUTXOs...); err != nil {
			return nil, err
		}
		groups := make([]util.TBC20StandardOutputGroup, 0, len(plans)+1)
		tokenOutputs := make([]TBC20StandardTokenOutput, 0, len(plans))
		for _, plan := range plans {
			codeVout := len(tx.Outputs)
			code, err := ReplaceTBC20StandardController(validated[0].code, plan.controller)
			if err != nil {
				return nil, err
			}
			tape, err := util.ReplaceTBC20StandardTapeAmounts(validated[0].tape, plan.slots)
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: TBC20StandardCodeSatoshis})
			tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: TBC20StandardTapeSatoshis})
			tapeVout := codeVout + 1
			groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: codeVout, TapeVout: &tapeVout})
			tokenOutputs = append(tokenOutputs, TBC20StandardTokenOutput{CodeVout: codeVout, TapeVout: tapeVout, Amount: new(big.Int).Set(plan.amount), AmountsByInput: plan.slots, Controller: plan.controller})
		}
		if change != nil {
			vout := len(tx.Outputs)
			tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: *change})
			groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: vout})
		}
		for index, input := range validated {
			unlockOptions := util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: index, PreTx: input.parent, PreTxVout: int(input.utxo.Vout), OutputGroups: groups, AncestorTransactions: input.ancestors, PublicKey: privateKey.PubKey().SerialiseCompressed()}
			var unlock *bscript.Script
			if estimated {
				unlockOptions.Signature = tbc20StandardMaximumLowSSignature
				unlock, err = util.BuildTBC20StandardUnlockScriptWithSignature(unlockOptions)
			} else {
				unlockOptions.PrivateKey = privateKey
				unlock, err = util.BuildTBC20StandardUnlockScript(unlockOptions)
			}
			if err != nil {
				return nil, err
			}
			if err := tx.InsertInputUnlockingScript(uint32(index), unlock); err != nil {
				return nil, err
			}
		}
		feeIndex := uint32(len(validated))
		if estimated {
			placeholder, err := tbc20StandardPlaceholderP2PKH(privateKey)
			if err != nil {
				return nil, err
			}
			if err := tx.InsertInputUnlockingScript(feeIndex, placeholder); err != nil {
				return nil, err
			}
		} else if err := signP2PKHInput(tx, privateKey, feeIndex); err != nil {
			return nil, err
		}
		fee, err := tbc20StandardPaidFee(allUTXOs, tx)
		if err != nil {
			return nil, err
		}
		return &TBC20StandardBuildResult{Transaction: tx, TxRaw: tx.String(), FeeSatoshis: fee, TokenOutputs: tokenOutputs, OutputGroups: groups}, nil
	}
	canAddChange := len(plans) < TBC20StandardMaxOutputGroups && len(plans)*2+1 <= TBC20StandardMaxOutputs
	var selectedChange *uint64
	if canAddChange {
		dust := sdkDustLimit
		estimate, err := build(&dust, true)
		if err != nil {
			return nil, err
		}
		fee, err := contractTargetFee(len(estimate.Transaction.Bytes()))
		if err != nil {
			return nil, err
		}
		if available >= fee && available-fee >= sdkDustLimit {
			value := available - fee
			selectedChange = &value
		}
	}
	if selectedChange == nil {
		estimate, err := build(nil, true)
		if err != nil {
			return nil, err
		}
		fee, err := contractTargetFee(len(estimate.Transaction.Bytes()))
		if err != nil || available < fee {
			return nil, fmt.Errorf("TBC20Standard: inputs cannot pay automatic transaction fee")
		}
		if !canAddChange && available-fee >= sdkDustLimit {
			return nil, fmt.Errorf("TBC20Standard: no logical output slot remains for TBC change")
		}
	}
	built, err := build(selectedChange, false)
	if err != nil {
		return nil, err
	}
	minimum, err := contractTargetFee(len(built.Transaction.Bytes()))
	if err != nil || built.FeeSatoshis < minimum {
		return nil, fmt.Errorf("TBC20Standard: final transaction underpays automatic fee")
	}
	verify := options == nil || tbc20StandardVerifyEnabled(options.Verify)
	if verify {
		for index, input := range allUTXOs {
			if err := tbc20StandardVerifyInput(built.Transaction, index, input); err != nil {
				return nil, fmt.Errorf("TBC20Standard: current input %d failed local verification: %w", index, err)
			}
		}
	}
	return built, nil
}

func (t *TBC20Standard) Transfer(privateKey *bec.PrivateKey, recipient, humanAmount string, tokenUTXOs []*bt.UTXO, feeUTXO *bt.UTXO, parentTXs []*bt.Tx, ancestors []TBC20StandardAncestorResolver, options *TBC20StandardTransferOptions) (*TBC20StandardBuildResult, error) {
	if t == nil || t.Definition == nil {
		return nil, fmt.Errorf("TBC20Standard: positional human transfer requires constructor metadata")
	}
	amount, err := TBC20StandardHumanToRaw(humanAmount, t.Definition.Decimal)
	if err != nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("TBC20Standard: transfer amount must be positive: %w", err)
	}
	controller, err := tbc20StandardAddressController(recipient)
	if err != nil {
		return nil, err
	}
	return t.transferRaw(privateKey, controller, amount, tokenUTXOs, feeUTXO, parentTXs, ancestors, options)
}

func (t *TBC20Standard) Merge(privateKey *bec.PrivateKey, tokenUTXOs []*bt.UTXO, feeUTXO *bt.UTXO, parentTXs []*bt.Tx, ancestors []TBC20StandardAncestorResolver, options *TBC20StandardMergeOptions) (*TBC20StandardBuildResult, error) {
	if t == nil || signerMissing(privateKey) {
		return nil, fmt.Errorf("TBC20Standard: token and private key are required")
	}
	if len(tokenUTXOs) < 2 {
		return nil, fmt.Errorf("TBC20Standard: positional merge requires at least two token inputs")
	}
	validated, err := t.validateTransferInputs(privateKey, tokenUTXOs, parentTXs, ancestors)
	if err != nil {
		return nil, err
	}
	total := new(big.Int)
	for _, input := range validated {
		total.Add(total, input.balance)
	}
	controllerAddress := ""
	transferOptions := (*TBC20StandardTransferOptions)(nil)
	if options != nil {
		controllerAddress = options.Controller
		transferOptions = &options.TBC20StandardTransferOptions
	}
	if controllerAddress == "" {
		owner, err := tbc20StandardKeyAddress(privateKey)
		if err != nil {
			return nil, err
		}
		controllerAddress = owner.AddressString
	}
	controller, err := tbc20StandardAddressController(controllerAddress)
	if err != nil {
		return nil, err
	}
	return t.transferRaw(privateKey, controller, total, tokenUTXOs, feeUTXO, parentTXs, ancestors, transferOptions)
}
