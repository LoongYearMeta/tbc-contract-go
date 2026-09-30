package contract

import (
	"errors"
	"fmt"
	"math/bits"

	bt "github.com/LoongYearMeta/tbc-lib-go"
)

const (
	contractSatoshisPerKB  = uint64(80)
	contractMinimumFee     = uint64(80)
	sdkDustLimit           = uint64(42)
	maxFeeFinalizeAttempts = 8
)

var (
	ErrFeeDidNotConverge       = errors.New("signed transaction fee did not converge")
	ErrInvalidChangeOutput     = errors.New("invalid fee change output")
	ErrInvalidContractFee      = errors.New("invalid contract fee")
	ErrContractFeeOverflow     = errors.New("contract fee calculation overflow")
	ErrContractAmountOverflow  = errors.New("contract amount sum overflow")
	ErrInsufficientContractFee = errors.New("insufficient inputs for contract fee")
	ErrOrdinaryOutputDust      = errors.New("ordinary output is below SDK dust")
)

func contractTargetFee(sizeBytes int) (uint64, error) {
	if sizeBytes < 0 {
		return 0, ErrInvalidContractFee
	}
	hi, lo := bits.Mul64(uint64(sizeBytes), contractSatoshisPerKB)
	if hi != 0 || lo > ^uint64(0)-999 {
		return 0, ErrContractFeeOverflow
	}
	fee := (lo + 999) / 1000
	if fee < contractMinimumFee {
		fee = contractMinimumFee
	}
	return fee, nil
}

func requireOrdinaryOutput(valueSat uint64, context string) error {
	if valueSat < sdkDustLimit {
		return fmt.Errorf("%w: %s is %d sat, need at least %d sat",
			ErrOrdinaryOutputDust, context, valueSat, sdkDustLimit)
	}
	return nil
}

func checkedInputSatoshis(tx *bt.Tx) (uint64, error) {
	var total uint64
	for _, input := range tx.Inputs {
		next, carry := bits.Add64(total, input.PreviousTxSatoshis, 0)
		if carry != 0 {
			return 0, ErrContractAmountOverflow
		}
		total = next
	}
	return total, nil
}

func checkedOutputSatoshis(tx *bt.Tx) (uint64, error) {
	var total uint64
	for _, output := range tx.Outputs {
		next, carry := bits.Add64(total, output.Satoshis, 0)
		if carry != 0 {
			return 0, ErrContractAmountOverflow
		}
		total = next
	}
	return total, nil
}

func setChangeForTarget(tx *bt.Tx, changeIndex int, targetFee uint64) (bool, error) {
	return setChangeForTargetMinimum(tx, changeIndex, targetFee, sdkDustLimit)
}
func setChangeForTargetMinimum(tx *bt.Tx, changeIndex int, targetFee, minimum uint64) (bool, error) {
	if tx == nil || changeIndex < 0 || changeIndex >= len(tx.Outputs) {
		return false, ErrInvalidChangeOutput
	}

	inputSum, err := checkedInputSatoshis(tx)
	if err != nil {
		return false, err
	}

	var nonChangeSum uint64
	for index, output := range tx.Outputs {
		if index == changeIndex {
			continue
		}
		next, carry := bits.Add64(nonChangeSum, output.Satoshis, 0)
		if carry != 0 {
			return false, ErrContractAmountOverflow
		}
		nonChangeSum = next
	}

	required, carry := bits.Add64(nonChangeSum, targetFee, 0)
	if carry != 0 || inputSum < required {
		return false, fmt.Errorf("%w: inputs=%d non-change=%d target=%d",
			ErrInsufficientContractFee, inputSum, nonChangeSum, targetFee)
	}

	change := inputSum - required
	if change < minimum {
		return false, fmt.Errorf("%w: fee change %d below %d", ErrOrdinaryOutputDust, change, minimum)
	}
	if tx.Outputs[changeIndex].Satoshis == change {
		return false, nil
	}
	tx.Outputs[changeIndex].Satoshis = change
	return true, nil
}

func verifyPaidFee(tx *bt.Tx, targetFee uint64) error {
	inputSum, err := checkedInputSatoshis(tx)
	if err != nil {
		return err
	}
	outputSum, err := checkedOutputSatoshis(tx)
	if err != nil {
		return err
	}
	if inputSum < outputSum {
		return fmt.Errorf("%w: outputs=%d exceed inputs=%d",
			ErrInsufficientContractFee, outputSum, inputSum)
	}
	paidFee := inputSum - outputSum
	if paidFee < targetFee {
		return fmt.Errorf("%w: paid=%d target=%d",
			ErrInsufficientContractFee, paidFee, targetFee)
	}
	return nil
}

func finalizeSignedFee(tx *bt.Tx, changeIndex int, sign func() error) error {
	return finalizeSignedFeeMinimum(tx, changeIndex, sdkDustLimit, sign)
}
func finalizeSignedFeeMinimum(tx *bt.Tx, changeIndex int, minimum uint64, sign func() error) error {
	if tx == nil || changeIndex < 0 || changeIndex >= len(tx.Outputs) {
		return ErrInvalidChangeOutput
	}
	if sign == nil {
		return errors.New("nil transaction signer")
	}

	var targetFloor uint64
	for attempt := 0; attempt < maxFeeFinalizeAttempts; attempt++ {
		if err := sign(); err != nil {
			return err
		}

		observedTarget, err := contractTargetFee(len(tx.Bytes()))
		if err != nil {
			return err
		}
		if observedTarget > targetFloor {
			targetFloor = observedTarget
		}
		changed, err := setChangeForTargetMinimum(tx, changeIndex, targetFloor, minimum)
		if err != nil {
			return err
		}
		if changed {
			continue
		}

		finalTarget, err := contractTargetFee(len(tx.Bytes()))
		if err != nil {
			return err
		}
		return verifyPaidFee(tx, finalTarget)
	}

	return ErrFeeDidNotConverge
}

// Optional trailing AMM change. Rebuild every witness after removing change;
// the smaller transaction must still pay its own minimum fee.
func finalizeSignedFeeOptional(tx *bt.Tx, changeIndex int, minimum uint64, sign func() error) error {
	if tx == nil || changeIndex != len(tx.Outputs)-1 || changeIndex < 0 || sign == nil {
		return ErrInvalidChangeOutput
	}
	err := finalizeSignedFeeMinimum(tx, changeIndex, minimum, sign)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrOrdinaryOutputDust) && !errors.Is(err, ErrInsufficientContractFee) {
		return err
	}
	tx.Outputs = tx.Outputs[:changeIndex]
	if err = sign(); err != nil {
		return err
	}
	target, err := contractTargetFee(len(tx.Bytes()))
	if err != nil {
		return err
	}
	return verifyPaidFee(tx, target)
}
