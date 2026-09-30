package contract

import (
	"fmt"
	interpreter "github.com/LoongYearMeta/tbc-contract-go/internal/ammvm"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/script/interpreter/scriptflag"
	"math/big"
)

type TBCAMMInputValidation struct {
	InputIndex    int    `json:"inputIndex"`
	Success       bool   `json:"success"`
	Error         string `json:"error"`
	StackDepth    int    `json:"stackDepth"`
	AltStackDepth int    `json:"altStackDepth"`
}
type TBCAMMValidationReport struct {
	Success               bool                    `json:"success"`
	Inputs                []TBCAMMInputValidation `json:"inputs"`
	ValueConserved        bool                    `json:"valueConserved"`
	NodeAcceptanceChecked bool                    `json:"nodeAcceptanceChecked"`
}

// ValidateTBCAMMTransaction executes every input against the supplied trusted
// previous outputs. It does not check chain availability, finality or acceptance.
func ValidateTBCAMMTransaction(tx *bt.Tx, previous []*bt.Output) (*TBCAMMValidationReport, error) {
	if tx == nil || tx.Version != 10 || len(tx.Inputs) == 0 || len(tx.Outputs) == 0 || len(previous) != len(tx.Inputs) {
		return nil, fmt.Errorf("AMM validation requires version 10 and trusted previous outputs")
	}
	// Interpreter attaches prevout metadata internally; isolate it from caller state.
	for _, in := range tx.Inputs {
		if in == nil {
			return nil, fmt.Errorf("missing input")
		}
	}
	for _, out := range tx.Outputs {
		if out == nil || out.LockingScript == nil {
			return nil, fmt.Errorf("missing output")
		}
	}
	copyTx, err := bt.NewTxFromString(tx.String())
	if err != nil {
		return nil, err
	}
	tx = copyTx
	seen := map[string]bool{}
	inputSat := new(big.Int)
	outputSat := new(big.Int)
	report := &TBCAMMValidationReport{Inputs: make([]TBCAMMInputValidation, 0, len(previous))}
	flags := scriptflag.VerifyStrictEncoding | scriptflag.EnableSighashForkID | scriptflag.VerifyLowS | scriptflag.VerifyNullFail | scriptflag.VerifyDERSignatures | scriptflag.VerifyMinimalData | scriptflag.StrictMultiSig | scriptflag.DiscourageUpgradableNops | scriptflag.VerifyCheckLockTimeVerify | scriptflag.VerifyCheckSequenceVerify
	for vin, in := range tx.Inputs {
		if in == nil || previous[vin] == nil || previous[vin].LockingScript == nil {
			return nil, fmt.Errorf("missing input or previous output")
		}
		outpoint := fmt.Sprintf("%x:%d", in.PreviousTxID(), in.PreviousTxOutIndex)
		if seen[outpoint] {
			return nil, fmt.Errorf("duplicate input")
		}
		seen[outpoint] = true
		if previous[vin].Satoshis > 9007199254740991 {
			return nil, fmt.Errorf("unsafe previous output value")
		}
		inputSat.Add(inputSat, new(big.Int).SetUint64(previous[vin].Satoshis))

		main, alt, err := interpreter.ExecuteDetailed(interpreter.WithTx(tx, vin, previous[vin]), interpreter.WithAfterGenesis(), interpreter.WithFlags(flags|scriptflag.VerifyCleanStack))
		r := TBCAMMInputValidation{InputIndex: vin, Success: err == nil, StackDepth: main, AltStackDepth: alt}

		if err != nil {
			r.Error = err.Error()
		}
		report.Inputs = append(report.Inputs, r)
	}
	for _, out := range tx.Outputs {
		if out == nil {
			return nil, fmt.Errorf("missing output")
		}
		if out.Satoshis > 9007199254740991 {
			return nil, fmt.Errorf("unsafe output value")
		}
		outputSat.Add(outputSat, new(big.Int).SetUint64(out.Satoshis))
	}
	report.ValueConserved = inputSat.Cmp(outputSat) >= 0
	report.Success = report.ValueConserved
	for _, in := range report.Inputs {
		report.Success = report.Success && in.Success
	}
	return report, nil
}
