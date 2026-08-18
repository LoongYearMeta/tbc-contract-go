package validator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

var validatorHexPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{2})+$`)

type apiTokenFetcher struct{}

func (apiTokenFetcher) FetchTokenTransaction(_ context.Context, txid, network string) (*bt.Tx, error) {
	return api.FetchTXRaw(txid, network)
}

type decodedCode struct {
	kind         string
	identity     string
	protocol     TokenProtocolDescriptor
	originalWire []byte
	tapeSize     int
}

type decodedTape struct {
	slots        [contract.TBC20AmountSlots]uint64
	balance      *big.Int
	envelope     []byte
	envelopeHash string
}

type decodedPair struct {
	codeVout int
	tapeVout int
	code     decodedCode
	tape     decodedTape
}

type resolvedInput struct {
	evidence *TokenInputEvidence
	pair     *decodedPair
	parent   *bt.Tx
}

type validatorContext struct {
	result       *TokenValidationResult
	invalid      []TokenValidationIssue
	unknown      []TokenValidationIssue
	warnings     []TokenValidationIssue
	fetcher      TokenTransactionFetcher
	network      string
	exactTape    bool
	fetchCache   map[string]*bt.Tx
	fetchFailed  map[string]bool
	requiredSeen map[string]bool
	lineageWant  int
	lineageGot   int
}

func issueMessage(code string) string { return strings.ToLower(strings.ReplaceAll(code, "_", " ")) }

func (ctx *validatorContext) addInvalid(code, stage string, issue TokenValidationIssue) {
	issue.Code, issue.Severity, issue.Stage = code, "error", stage
	if issue.Message == "" {
		issue.Message = issueMessage(code)
	}
	ctx.invalid = append(ctx.invalid, issue)
}

func (ctx *validatorContext) addUnknown(code, stage string, issue TokenValidationIssue) {
	issue.Code, issue.Severity, issue.Stage = code, "error", stage
	if issue.Message == "" {
		issue.Message = issueMessage(code)
	}
	ctx.unknown = append(ctx.unknown, issue)
}

func (ctx *validatorContext) addWarning(code, stage string, issue TokenValidationIssue) {
	issue.Code, issue.Severity, issue.Stage = code, "warning", stage
	if issue.Message == "" {
		issue.Message = issueMessage(code)
	}
	ctx.warnings = append(ctx.warnings, issue)
}

func newValidationResult() *TokenValidationResult {
	return &TokenValidationResult{Kind: "UNDETERMINED", Assurances: []string{}, Issues: []TokenValidationIssue{}, Inputs: []TokenInputEvidence{}, OutputGroups: []TokenOutputGroup{}, Assets: []TokenAssetEvidence{}, Matrix: [][]*big.Int{}, AncestorEdges: []TokenAncestorEdge{}}
}

func invalidPolicy(message string) *TokenValidationResult {
	result := newValidationResult()
	result.Status = ValidationInvalid
	result.Issues = []TokenValidationIssue{{Code: "INVALID_POLICY", Severity: "error", Stage: "ROOT", Message: message}}
	return result
}

func normalizeValidationOptions(options TokenValidationOptions) (*bt.Tx, string, bool, TokenTransactionFetcher, *TokenValidationResult) {
	if options.Transaction == nil && options.RawHex == "" {
		return nil, "", false, nil, invalidPolicy("transaction must be a Transaction, Buffer, or hex string")
	}
	if strings.TrimSpace(options.Network) == "" {
		return nil, "", false, nil, invalidPolicy("network must be a non-empty string")
	}
	preset := options.Policy.Preset
	if preset == "" {
		preset = "strict"
	}
	if preset != "strict" && preset != "relaxed-metadata" {
		return nil, "", false, nil, invalidPolicy("policy.preset is unsupported")
	}
	exact := preset == "strict"
	if options.Policy.RequireExactTapeEnvelope != nil {
		exact = *options.Policy.RequireExactTapeEnvelope
		if preset == "strict" && !exact {
			return nil, "", false, nil, invalidPolicy("strict policy cannot disable exact Tape envelopes")
		}
	}
	var tx *bt.Tx
	if options.Transaction != nil {
		raw := options.Transaction.String()
		parsed, err := bt.NewTxFromString(raw)
		if err == nil && parsed.String() == raw {
			tx = parsed
		}
	} else if validatorHexPattern.MatchString(options.RawHex) {
		parsed, err := bt.NewTxFromString(options.RawHex)
		if err == nil && strings.EqualFold(parsed.String(), options.RawHex) {
			tx = parsed
		}
	}
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = apiTokenFetcher{}
	}
	return tx, strings.TrimSpace(options.Network), exact, fetcher, nil
}

func finalizeValidation(ctx *validatorContext) *TokenValidationResult {
	if len(ctx.invalid) == 0 && len(ctx.unknown) == 0 && ctx.result.Kind == "TRANSITION" {
		mandatory := []string{"STRUCTURE", "TRANSITION", "OUTPUT_SOURCE_LINEAGE", "OUTPUT_SOURCE_GRAPH_RESOLVED"}
		for _, assurance := range mandatory {
			found := false
			for _, current := range ctx.result.Assurances {
				found = found || current == assurance
			}
			if !found {
				ctx.addUnknown("VALIDATOR_INTERNAL_INCOMPLETE", "ROOT", TokenValidationIssue{Message: "mandatory validation assurance is incomplete"})
				break
			}
		}
	}
	switch {
	case len(ctx.invalid) > 0:
		ctx.result.Status = ValidationInvalid
	case len(ctx.unknown) > 0:
		ctx.result.Status = ValidationUnknown
	case ctx.result.Kind == "TRANSITION":
		ctx.result.Status = ValidationValid
	default:
		ctx.result.Status = ValidationInvalid
	}
	ctx.result.Issues = append(append(append([]TokenValidationIssue{}, ctx.invalid...), ctx.unknown...), ctx.warnings...)
	return ctx.result
}

func codeIdentity(script *bscript.Script) (string, error) {
	identity, err := util.GetTBC20CodeIdentity(script)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(identity), nil
}

func decodeCode(script *bscript.Script) (*decodedCode, *TokenProtocolDescriptor, error) {
	if script == nil || len(script.Bytes()) == 0 {
		return nil, nil, fmt.Errorf("empty locking script")
	}
	codeBytes := script.Bytes()
	if len(codeBytes) == contract.TBC20CodeBytes {
		if err := contract.ValidateTBC20Code(script, 0); err != nil {
			if contract.IsTBC20ArtifactCandidate(script) {
				return nil, nil, fmt.Errorf("INVALID_TBC20_CODE: %w", err)
			}
			return nil, nil, nil
		}
		identity, err := codeIdentity(script)
		if err != nil {
			return nil, nil, err
		}
		return &decodedCode{kind: "TBC20", identity: identity, protocol: TokenProtocolDescriptor{Family: "TBC20", Version: 1}, originalWire: append([]byte(nil), codeBytes[560:596]...), tapeSize: int(codeBytes[179])}, nil, nil
	}
	artifact := decodePublishedFTCode(codeBytes)
	if artifact == nil {
		return nil, nil, nil
	}
	protocol := TokenProtocolDescriptor{Version: artifact.artifact.version}
	if artifact.artifact.coin {
		protocol.Family = "STABLE_COIN"
		return nil, &protocol, nil
	}
	protocol.Family = "FT"
	identity, err := codeIdentity(script)
	if err != nil {
		return nil, nil, err
	}
	return &decodedCode{kind: "FT", identity: identity, protocol: protocol, originalWire: artifact.originalUTXOWire, tapeSize: artifact.tapeSize}, nil, nil
}

func decodeTapeForCode(script *bscript.Script, code decodedCode) (decodedTape, error) {
	var result decodedTape
	if script == nil || len(script.Bytes()) != code.tapeSize {
		return result, fmt.Errorf("Tape size differs from Code size")
	}
	if code.kind == "TBC20" {
		parsed, err := contract.ParseTBC20Tape(script)
		if err != nil {
			return result, err
		}
		result.slots = parsed.Amounts
		result.balance = parsed.Balance
	} else {
		if !isPublishedFTTape(script.Bytes()) {
			return result, fmt.Errorf("Tape is not a canonical published FT envelope")
		}
		result.balance = new(big.Int)
		for index := range result.slots {
			result.slots[index] = binary.LittleEndian.Uint64(script.Bytes()[3+index*8:])
			if result.slots[index] > contract.TBC20MaxSlotAmount {
				return result, fmt.Errorf("Tape amount exceeds signed-63 range")
			}
			result.balance.Add(result.balance, new(big.Int).SetUint64(result.slots[index]))
		}
	}
	bytesValue := script.Bytes()
	result.envelope = append(append([]byte(nil), bytesValue[:3]...), bytesValue[51:]...)
	hash := sha256.Sum256(result.envelope)
	result.envelopeHash = hex.EncodeToString(hash[:])
	return result, nil
}

func decodePairAt(tx *bt.Tx, vout int) (*decodedPair, *TokenProtocolDescriptor, string, error) {
	if tx == nil || vout < 0 || vout >= len(tx.Outputs) {
		return nil, nil, "", nil
	}
	code, unsupported, err := decodeCode(tx.Outputs[vout].LockingScript)
	if err != nil || unsupported != nil || code == nil {
		return nil, unsupported, "", err
	}
	if vout+1 >= len(tx.Outputs) {
		return nil, nil, code.kind, fmt.Errorf("token Code has no adjacent Tape output")
	}
	if tx.Outputs[vout].Satoshis != contract.TBC20CodeSatoshis || tx.Outputs[vout+1].Satoshis != contract.TBC20TapeSatoshis {
		return nil, nil, code.kind, fmt.Errorf("token Code/Tape values must be exactly 500/0")
	}
	tape, err := decodeTapeForCode(tx.Outputs[vout+1].LockingScript, *code)
	if err != nil {
		return nil, nil, code.kind, err
	}
	return &decodedPair{codeVout: vout, tapeVout: vout + 1, code: *code, tape: tape}, nil, code.kind, nil
}

func scanOutputs(tx *bt.Tx, ctx *validatorContext) map[int]*decodedPair {
	pairs := make(map[int]*decodedPair)
	if len(tx.Outputs) > contract.TBC20MaxOutputs {
		ctx.addInvalid("OUTPUT_LIMIT_EXCEEDED", "ROOT", TokenValidationIssue{Message: fmt.Sprintf("transaction has %d physical outputs; maximum is %d", len(tx.Outputs), contract.TBC20MaxOutputs)})
		return pairs
	}
	protocolKeys := make(map[string]bool)
	logical := 0
	for vout := 0; vout < len(tx.Outputs); {
		if tx.Outputs[vout].LockingScript == nil || len(tx.Outputs[vout].LockingScript.Bytes()) == 0 {
			value := vout
			ctx.addInvalid("EMPTY_LOCKING_SCRIPT", "OUTPUT_SCAN", TokenValidationIssue{Vout: &value})
			return pairs
		}
		pair, unsupported, candidate, err := decodePairAt(tx, vout)
		if err != nil {
			value := vout
			code := "INVALID_TOKEN_CODE"
			if candidate == "TBC20" || strings.Contains(err.Error(), "TBC20") {
				code = "INVALID_TBC20_CODE"
			} else if strings.Contains(err.Error(), "Tape") {
				code = "INVALID_TOKEN_TAPE"
			}
			ctx.addInvalid(code, "OUTPUT_SCAN", TokenValidationIssue{Vout: &value, Message: err.Error()})
			return pairs
		}
		if unsupported != nil {
			protocol := *unsupported
			ctx.result.OutputGroups = append(ctx.result.OutputGroups, TokenOutputGroup{LogicalIndex: logical, Kind: "CONTRACT", FirstVout: vout, PhysicalVoutCount: 1, RecognizedContract: &protocol})
			protocolKeys[fmt.Sprintf("%s:v%d", protocol.Family, protocol.Version)] = true
			logical++
			vout++
			continue
		}
		if pair == nil {
			ctx.result.OutputGroups = append(ctx.result.OutputGroups, TokenOutputGroup{LogicalIndex: logical, Kind: "ORDINARY", FirstVout: vout, PhysicalVoutCount: 1})
			logical++
			vout++
			continue
		}
		codeVout, tapeVout := vout, vout+1
		protocol := pair.code.protocol
		group := TokenOutputGroup{LogicalIndex: logical, Kind: pair.code.kind, FirstVout: vout, PhysicalVoutCount: 2, CodeVout: &codeVout, TapeVout: &tapeVout, Identity: pair.code.identity, Protocol: &protocol, BalanceRaw: new(big.Int).Set(pair.tape.balance), Slots: append([]uint64(nil), pair.tape.slots[:]...)}
		ctx.result.OutputGroups = append(ctx.result.OutputGroups, group)
		pairs[logical] = pair
		protocolKeys[fmt.Sprintf("%s:v%d", protocol.Family, protocol.Version)] = true
		logical++
		vout += 2
	}
	if len(protocolKeys) > 1 {
		ctx.addInvalid("MIXED_TOKEN_PROTOCOLS", "OUTPUT_SCAN", TokenValidationIssue{})
	}
	for _, pair := range pairs {
		protocol := pair.code.protocol
		ctx.result.Protocol = &protocol
		break
	}
	return pairs
}

func inputTxID(input *bt.Input) string { return strings.ToLower(input.PreviousTxIDStr()) }

func outpointWire(txid string, vout uint32) []byte {
	decoded, _ := hex.DecodeString(txid)
	for left, right := 0, len(decoded)-1; left < right; left, right = left+1, right-1 {
		decoded[left], decoded[right] = decoded[right], decoded[left]
	}
	result := append([]byte(nil), decoded...)
	voutBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(voutBytes, vout)
	return append(result, voutBytes...)
}

func (ctx *validatorContext) requireTxID(txid string) {
	if !ctx.requiredSeen[txid] {
		ctx.requiredSeen[txid] = true
		ctx.result.Source.RequiredSourceTxIDs = append(ctx.result.Source.RequiredSourceTxIDs, txid)
	}
}

func (ctx *validatorContext) fetch(ctxGo context.Context, txid string) (*bt.Tx, bool) {
	txid = strings.ToLower(txid)
	if tx, exists := ctx.fetchCache[txid]; exists {
		return tx, true
	}
	if ctx.fetchFailed[txid] {
		return nil, false
	}
	ctx.result.Source.QueriedTxIDs = append(ctx.result.Source.QueriedTxIDs, txid)
	tx, err := ctx.fetcher.FetchTokenTransaction(ctxGo, txid, ctx.network)
	if err != nil || tx == nil {
		ctx.fetchFailed[txid] = true
		return nil, false
	}
	ctx.fetchCache[txid] = tx
	ctx.result.Source.ResolvedTxIDs = append(ctx.result.Source.ResolvedTxIDs, txid)
	ctx.result.ResolvedTransactions++
	return tx, true
}

func resolveSource(ctxGo context.Context, root *bt.Tx, vin int, ctx *validatorContext) *resolvedInput {
	evidence := &ctx.result.Inputs[vin]
	evidence.Resolution = "UNAVAILABLE"
	ctx.requireTxID(evidence.PrevTxID)
	parent, ok := ctx.fetch(ctxGo, evidence.PrevTxID)
	if !ok {
		vinCopy := vin
		ctx.addUnknown("PARENT_FETCH_FAILED", "PARENT", TokenValidationIssue{Vin: &vinCopy, TxID: evidence.PrevTxID})
		return &resolvedInput{evidence: evidence}
	}
	if int(evidence.PrevVout) >= len(parent.Outputs) {
		vinCopy, voutCopy := vin, int(evidence.PrevVout)
		evidence.Resolution = "INVALID"
		ctx.addInvalid("PARENT_VOUT_OUT_OF_RANGE", "PARENT", TokenValidationIssue{Vin: &vinCopy, Vout: &voutCopy, TxID: evidence.PrevTxID})
		return &resolvedInput{evidence: evidence, parent: parent}
	}
	ctx.result.ParentsChecked++
	pair, unsupported, _, err := decodePairAt(parent, int(evidence.PrevVout))
	if err != nil {
		vinCopy := vin
		evidence.Resolution = "INVALID"
		ctx.addInvalid("INVALID_TOKEN_TAPE", "PARENT", TokenValidationIssue{Vin: &vinCopy, TxID: evidence.PrevTxID, Message: err.Error()})
		return &resolvedInput{evidence: evidence, parent: parent}
	}
	if unsupported != nil || pair == nil {
		vinCopy := vin
		evidence.Kind, evidence.Resolution = "ORDINARY", "RESOLVED"
		ctx.addInvalid("AMOUNT_SLOT_WITHOUT_TOKEN_INPUT", "MATRIX", TokenValidationIssue{Vin: &vinCopy})
		return &resolvedInput{evidence: evidence, parent: parent}
	}
	evidence.Kind, evidence.Resolution, evidence.SourceRole, evidence.ParentTxID = pair.code.kind, "RESOLVED", "POSITIVE_SOURCE", evidence.PrevTxID
	codeVout, tapeVout := pair.codeVout, pair.tapeVout
	evidence.CodeVout, evidence.TapeVout, evidence.Identity = &codeVout, &tapeVout, pair.code.identity
	protocol := pair.code.protocol
	evidence.Protocol, evidence.Slots, evidence.BalanceRaw = &protocol, append([]uint64(nil), pair.tape.slots[:]...), new(big.Int).Set(pair.tape.balance)
	return &resolvedInput{evidence: evidence, pair: pair, parent: parent}
}

func ValidateOnChainTransaction(ctxGo context.Context, options TokenValidationOptions) (*TokenValidationResult, error) {
	root, network, exactTape, fetcher, invalid := normalizeValidationOptions(options)
	if invalid != nil {
		return invalid, nil
	}
	result := newValidationResult()
	result.Source = &TokenValidationSource{Network: network, API: "API.fetchTXraw", TrustModel: "API_FETCH_TXRAW_FULLY_TRUSTED", RootTrustModel: "CALLER_ASSERTED_ON_CHAIN_AND_INPUT_SCRIPTS_VALID", QueriedTxIDs: []string{}, ResolvedTxIDs: []string{}, RequiredSourceTxIDs: []string{}}
	ctx := &validatorContext{result: result, fetcher: fetcher, network: network, exactTape: exactTape, fetchCache: make(map[string]*bt.Tx), fetchFailed: make(map[string]bool), requiredSeen: make(map[string]bool)}
	if root == nil {
		ctx.addUnknown("ROOT_RAW_INVALID", "ROOT", TokenValidationIssue{})
		return finalizeValidation(ctx), nil
	}
	result.TxID = root.TxID()
	for vin, input := range root.Inputs {
		result.Inputs = append(result.Inputs, TokenInputEvidence{Vin: vin, PrevTxID: inputTxID(input), PrevVout: input.PreviousTxOutIndex, Kind: "UNRESOLVED", Resolution: "NOT_REQUESTED"})
	}
	if root.Version != 10 {
		ctx.addInvalid("INVALID_TRANSACTION_VERSION", "ROOT", TokenValidationIssue{Message: "root transaction version must be 10"})
	}
	if len(root.Inputs) < 1 || len(root.Inputs) > contract.TBC20MaxInputs {
		ctx.addInvalid("INPUT_LIMIT_EXCEEDED", "ROOT", TokenValidationIssue{Message: fmt.Sprintf("root transaction must contain 1-%d inputs", contract.TBC20MaxInputs)})
	}
	seen := make(map[string]bool)
	for vin, input := range root.Inputs {
		key := fmt.Sprintf("%s:%d", inputTxID(input), input.PreviousTxOutIndex)
		if seen[key] {
			vinCopy, voutCopy := vin, int(input.PreviousTxOutIndex)
			ctx.addInvalid("DUPLICATE_INPUT_OUTPOINT", "ROOT", TokenValidationIssue{Vin: &vinCopy, Vout: &voutCopy, TxID: inputTxID(input)})
		}
		seen[key] = true
	}
	if len(ctx.invalid) > 0 {
		return finalizeValidation(ctx), nil
	}
	pairs := scanOutputs(root, ctx)
	if len(ctx.invalid) > 0 {
		return finalizeValidation(ctx), nil
	}
	result.Assurances = append(result.Assurances, "STRUCTURE")
	if len(pairs) == 0 {
		result.Kind = "NON_TOKEN"
		result.Assurances = append(result.Assurances, "OUTPUT_SOURCE_GRAPH_RESOLVED")
		ctx.addInvalid("NO_TOKEN_OUTPUT", "OUTPUT_SCAN", TokenValidationIssue{})
		return finalizeValidation(ctx), nil
	}
	result.Kind = "TRANSITION"
	positiveVins := make(map[int]bool)
	identityEnvelopes := make(map[string][]byte)
	logicalKeys := make([]int, 0, len(pairs))
	for logical := range pairs {
		logicalKeys = append(logicalKeys, logical)
	}
	sort.Ints(logicalKeys)
	for _, logical := range logicalKeys {
		pair := pairs[logical]
		row := make([]*big.Int, contract.TBC20AmountSlots)
		for vin, amount := range pair.tape.slots {
			row[vin] = new(big.Int).SetUint64(amount)
			if amount > 0 {
				positiveVins[vin] = true
				if vin >= len(root.Inputs) {
					vinCopy := vin
					ctx.addInvalid("AMOUNT_SLOT_WITHOUT_INPUT", "MATRIX", TokenValidationIssue{Vin: &vinCopy})
				}
			}
		}
		result.Matrix = append(result.Matrix, row)
		if existing, ok := identityEnvelopes[pair.code.identity]; ok && !bytes.Equal(existing, pair.tape.envelope) {
			if exactTape {
				ctx.addInvalid("TAPE_ENVELOPE_MISMATCH", "MATRIX", TokenValidationIssue{Identity: pair.code.identity})
			} else {
				ctx.addWarning("TAPE_ENVELOPE_MISMATCH", "MATRIX", TokenValidationIssue{Identity: pair.code.identity})
			}
		} else {
			identityEnvelopes[pair.code.identity] = pair.tape.envelope
		}
	}
	if len(ctx.invalid) > 0 {
		return finalizeValidation(ctx), nil
	}
	vins := make([]int, 0, len(positiveVins))
	for vin := range positiveVins {
		vins = append(vins, vin)
	}
	sort.Ints(vins)
	sources := make([]*resolvedInput, 0, len(vins))
	complete := true
	for _, vin := range vins {
		source := resolveSource(ctxGo, root, vin, ctx)
		sources = append(sources, source)
		if source.pair == nil {
			complete = false
			continue
		}
		if result.Protocol == nil || source.pair.code.protocol != *result.Protocol {
			vinCopy := vin
			ctx.addInvalid("MIXED_TOKEN_PROTOCOLS", "PARENT", TokenValidationIssue{Vin: &vinCopy, TxID: source.evidence.PrevTxID})
			continue
		}
		column := new(big.Int)
		identities := make(map[string]bool)
		for _, pair := range pairs {
			if pair.tape.slots[vin] > 0 {
				column.Add(column, new(big.Int).SetUint64(pair.tape.slots[vin]))
				identities[pair.code.identity] = true
			}
		}
		if len(identities) != 1 || !identities[source.pair.code.identity] {
			vinCopy := vin
			ctx.addInvalid("OUTPUT_INPUT_IDENTITY_MISMATCH", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: source.pair.code.identity})
		}
		if column.Cmp(source.pair.tape.balance) != 0 {
			vinCopy := vin
			ctx.addInvalid("VIN_AMOUNT_NOT_CONSERVED", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: source.pair.code.identity, Message: fmt.Sprintf("vin %d contributes %s raw but parent balance is %s", vin, column, source.pair.tape.balance)})
		}
		if expected := identityEnvelopes[source.pair.code.identity]; !bytes.Equal(expected, source.pair.tape.envelope) {
			if exactTape {
				vinCopy := vin
				ctx.addInvalid("TAPE_ENVELOPE_MISMATCH", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: source.pair.code.identity})
			} else {
				vinCopy := vin
				ctx.addWarning("TAPE_ENVELOPE_MISMATCH", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: source.pair.code.identity})
			}
		}
	}
	if len(ctx.invalid) > 0 {
		return finalizeValidation(ctx), nil
	}
	positiveOutputIdentities := make(map[string]bool)
	zeroTargets := make(map[string]bool)
	for _, pair := range pairs {
		if pair.tape.balance.Sign() > 0 {
			positiveOutputIdentities[pair.code.identity] = true
		}
	}
	for _, pair := range pairs {
		if pair.tape.balance.Sign() == 0 && !positiveOutputIdentities[pair.code.identity] {
			zeroTargets[pair.code.identity] = true
		}
	}
	failedZeroCandidates := 0
	for vin := 0; vin < len(root.Inputs) && len(zeroTargets) > 0; vin++ {
		if positiveVins[vin] {
			continue
		}
		evidence := &result.Inputs[vin]
		parent, ok := ctx.fetch(ctxGo, evidence.PrevTxID)
		if !ok {
			failedZeroCandidates++
			continue
		}
		if int(evidence.PrevVout) >= len(parent.Outputs) {
			vinCopy, voutCopy := vin, int(evidence.PrevVout)
			evidence.Resolution = "INVALID"
			ctx.addInvalid("PARENT_VOUT_OUT_OF_RANGE", "PARENT", TokenValidationIssue{Vin: &vinCopy, Vout: &voutCopy, TxID: evidence.PrevTxID})
			continue
		}
		result.ParentsChecked++
		pair, unsupported, _, err := decodePairAt(parent, int(evidence.PrevVout))
		if err != nil {
			evidence.Resolution = "INVALID"
			continue
		}
		if unsupported != nil || pair == nil {
			evidence.Kind, evidence.Resolution, evidence.ParentTxID = "ORDINARY", "RESOLVED", evidence.PrevTxID
			continue
		}
		evidence.Kind, evidence.Resolution, evidence.ParentTxID = pair.code.kind, "RESOLVED", evidence.PrevTxID
		if result.Protocol == nil || pair.code.protocol != *result.Protocol || !zeroTargets[pair.code.identity] {
			continue
		}
		if pair.tape.balance.Sign() > 0 {
			vinCopy := vin
			ctx.addInvalid("VIN_AMOUNT_NOT_CONSERVED", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: pair.code.identity})
			continue
		}
		if expected := identityEnvelopes[pair.code.identity]; exactTape && !bytes.Equal(expected, pair.tape.envelope) {
			continue
		} else if !exactTape && !bytes.Equal(expected, pair.tape.envelope) {
			vinCopy := vin
			ctx.addWarning("TAPE_ENVELOPE_MISMATCH", "MATRIX", TokenValidationIssue{Vin: &vinCopy, Identity: pair.code.identity})
		}
		evidence.SourceRole, evidence.Identity = "ZERO_IDENTITY_WITNESS", pair.code.identity
		codeVout, tapeVout := pair.codeVout, pair.tapeVout
		evidence.CodeVout, evidence.TapeVout = &codeVout, &tapeVout
		protocol := pair.code.protocol
		evidence.Protocol, evidence.Slots, evidence.BalanceRaw = &protocol, append([]uint64(nil), pair.tape.slots[:]...), new(big.Int).Set(pair.tape.balance)
		source := &resolvedInput{evidence: evidence, pair: pair, parent: parent}
		sources = append(sources, source)
		ctx.requireTxID(evidence.PrevTxID)
		delete(zeroTargets, pair.code.identity)
	}
	if len(zeroTargets) > 0 {
		complete = false
		if failedZeroCandidates >= len(zeroTargets) {
			ctx.addUnknown("ZERO_IDENTITY_WITNESS_UNRESOLVED", "MATRIX", TokenValidationIssue{})
		} else {
			for identity := range zeroTargets {
				ctx.addInvalid("OUTPUT_IDENTITY_WITHOUT_INPUT", "MATRIX", TokenValidationIssue{Identity: identity})
			}
		}
	}
	if len(ctx.invalid) > 0 {
		return finalizeValidation(ctx), nil
	}
	if complete {
		assets := make(map[string]*TokenAssetEvidence)
		for _, source := range sources {
			asset := assets[source.pair.code.identity]
			if asset == nil {
				asset = &TokenAssetEvidence{Identity: source.pair.code.identity, Protocol: source.pair.code.protocol, InputVins: []int{}, OutputGroups: []int{}, InputRaw: new(big.Int), OutputRaw: new(big.Int)}
				assets[source.pair.code.identity] = asset
			}
			asset.InputVins = append(asset.InputVins, source.evidence.Vin)
			asset.InputRaw.Add(asset.InputRaw, source.pair.tape.balance)
		}
		for _, logical := range logicalKeys {
			pair := pairs[logical]
			asset := assets[pair.code.identity]
			if asset == nil {
				ctx.addInvalid("OUTPUT_IDENTITY_WITHOUT_INPUT", "MATRIX", TokenValidationIssue{Identity: pair.code.identity})
				continue
			}
			asset.OutputGroups = append(asset.OutputGroups, logical)
			asset.OutputRaw.Add(asset.OutputRaw, pair.tape.balance)
			asset.EnvelopeHash = pair.tape.envelopeHash
		}
		keys := make([]string, 0, len(assets))
		for identity := range assets {
			keys = append(keys, identity)
		}
		sort.Strings(keys)
		for _, identity := range keys {
			asset := assets[identity]
			if asset.InputRaw.Cmp(asset.OutputRaw) != 0 {
				ctx.addInvalid("IDENTITY_AMOUNT_NOT_CONSERVED", "MATRIX", TokenValidationIssue{Identity: identity})
			}
			result.Assets = append(result.Assets, *asset)
		}
		if len(ctx.invalid) == 0 {
			result.Assurances = append(result.Assurances, "TRANSITION")
		}
	}
	for _, source := range sources {
		if source.pair == nil || source.parent == nil {
			continue
		}
		for slot, amount := range source.pair.tape.slots {
			if amount == 0 {
				continue
			}
			ctx.lineageWant++
			if slot >= len(source.parent.Inputs) {
				vinCopy, slotCopy := source.evidence.Vin, slot
				ctx.addInvalid("PARENT_SLOT_WITHOUT_VIN", "PARENT", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, TxID: source.evidence.PrevTxID})
				continue
			}
			parentInput := source.parent.Inputs[slot]
			ancestorTxID := inputTxID(parentInput)
			ctx.requireTxID(ancestorTxID)
			ancestor, ok := ctx.fetch(ctxGo, ancestorTxID)
			if !ok {
				vinCopy, slotCopy := source.evidence.Vin, slot
				ctx.addUnknown("ANCESTOR_FETCH_FAILED", "ANCESTOR", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, TxID: ancestorTxID})
				continue
			}
			if int(parentInput.PreviousTxOutIndex) >= len(ancestor.Outputs) {
				vinCopy, slotCopy, voutCopy := source.evidence.Vin, slot, int(parentInput.PreviousTxOutIndex)
				ctx.addInvalid("ANCESTOR_VOUT_OUT_OF_RANGE", "ANCESTOR", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, Vout: &voutCopy, TxID: ancestorTxID})
				continue
			}
			ctx.result.AncestorsChecked++
			ancestorOutput := ancestor.Outputs[parentInput.PreviousTxOutIndex]
			if ancestor.Version != 10 {
				vinCopy, slotCopy := source.evidence.Vin, slot
				ctx.addInvalid("INVALID_TRANSACTION_VERSION", "ANCESTOR", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, TxID: ancestorTxID})
				continue
			}
			ancestorIdentity, err := codeIdentity(ancestorOutput.LockingScript)
			if err != nil {
				vinCopy, slotCopy := source.evidence.Vin, slot
				ctx.addInvalid("ANCESTOR_EMPTY_LOCKING_SCRIPT", "ANCESTOR", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, TxID: ancestorTxID})
				continue
			}
			edge := TokenAncestorEdge{CurrentVin: source.evidence.Vin, ParentTxID: source.evidence.PrevTxID, ParentCodeVout: source.pair.codeVout, ParentSlot: slot, ParentVin: slot, ABIPrepreIndex: 5 - slot, AncestorTxID: ancestorTxID, AncestorVout: parentInput.PreviousTxOutIndex, ParentIdentity: source.pair.code.identity, AncestorIdentity: ancestorIdentity}
			switch {
			case ancestorIdentity == source.pair.code.identity:
				edge.Resolution = "SAME_IDENTITY"
				ctx.lineageGot++
				result.AncestorEdges = append(result.AncestorEdges, edge)
			case slot == 0 && bytes.Equal(outpointWire(ancestorTxID, parentInput.PreviousTxOutIndex), source.pair.code.originalWire):
				edge.Resolution = "ORIGINAL_UTXO"
				ctx.lineageGot++
				result.OriginalUTXOBoundaries++
				result.AncestorEdges = append(result.AncestorEdges, edge)
			default:
				vinCopy, slotCopy, voutCopy := source.evidence.Vin, slot, int(parentInput.PreviousTxOutIndex)
				code := "ANCESTOR_IDENTITY_MISMATCH"
				if slot == 0 {
					code = "ORIGINAL_UTXO_MISMATCH"
				}
				ctx.addInvalid(code, "ANCESTOR", TokenValidationIssue{Vin: &vinCopy, Slot: &slotCopy, Vout: &voutCopy, TxID: ancestorTxID})
			}
		}
	}
	if len(ctx.invalid) == 0 && complete && ctx.lineageWant == ctx.lineageGot {
		result.Assurances = append(result.Assurances, "OUTPUT_SOURCE_LINEAGE")
		if len(ctx.unknown) == 0 {
			result.Assurances = append(result.Assurances, "OUTPUT_SOURCE_GRAPH_RESOLVED")
		}
	}
	return finalizeValidation(ctx), nil
}

func AssertValidOnChainTransaction(ctx context.Context, options TokenValidationOptions) (*TokenValidationResult, error) {
	report, err := ValidateOnChainTransaction(ctx, options)
	if err != nil {
		return nil, err
	}
	if report.Status != ValidationValid || report.Kind != "TRANSITION" {
		return nil, &TokenValidationError{Report: report}
	}
	return report, nil
}
