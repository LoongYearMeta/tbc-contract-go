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
	maxFeeFinalizeAttempts = 8
)

var (
	ErrFeeDidNotConverge       = errors.New("signed transaction fee did not converge")
	ErrInvalidChangeOutput     = errors.New("invalid fee change output")
	ErrInsufficientContractFee = errors.New("insufficient inputs for contract fee")
	ErrOrdinaryOutputDust      = errors.New("ordinary output is below SDK dust")
)

func contractTargetFee(sizeBytes int) (uint64, error) {
	return bt.CeilFeeForBytes(sizeBytes, contractSatoshisPerKB, contractMinimumFee)
}

func requireOrdinaryOutput(valueSat uint64, context string) error {
	if valueSat < bt.DustLimit {
		return fmt.Errorf("%w: %s is %d sat, need at least %d sat",
			ErrOrdinaryOutputDust, context, valueSat, bt.DustLimit)
	}
	return nil
}

func setChangeForTarget(tx *bt.Tx, changeIndex int, targetFee uint64) (bool, error) {
	if tx == nil || changeIndex < 0 || changeIndex >= len(tx.Outputs) {
		return false, ErrInvalidChangeOutput
	}

	inputSum, err := tx.TotalInputSatoshisChecked()
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
			return false, bt.ErrAmountOverflow
		}
		nonChangeSum = next
	}

	required, carry := bits.Add64(nonChangeSum, targetFee, 0)
	if carry != 0 || inputSum < required {
		return false, fmt.Errorf("%w: inputs=%d non-change=%d target=%d",
			ErrInsufficientContractFee, inputSum, nonChangeSum, targetFee)
	}

	change := inputSum - required
	if err := requireOrdinaryOutput(change, "fee change"); err != nil {
		return false, err
	}
	if tx.Outputs[changeIndex].Satoshis == change {
		return false, nil
	}
	tx.Outputs[changeIndex].Satoshis = change
	return true, nil
}

func verifyPaidFee(tx *bt.Tx, targetFee uint64) error {
	inputSum, err := tx.TotalInputSatoshisChecked()
	if err != nil {
		return err
	}
	outputSum, err := tx.TotalOutputSatoshisChecked()
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
	if tx == nil || changeIndex < 0 || changeIndex >= len(tx.Outputs) {
		return ErrInvalidChangeOutput
	}
	if sign == nil {
		return errors.New("nil transaction signer")
	}

	for attempt := 0; attempt < maxFeeFinalizeAttempts; attempt++ {
		if err := sign(); err != nil {
			return err
		}

		targetFee, err := contractTargetFee(len(tx.Bytes()))
		if err != nil {
			return err
		}
		changed, err := setChangeForTarget(tx, changeIndex, targetFee)
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
