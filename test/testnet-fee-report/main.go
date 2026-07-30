package main

import (
	"fmt"
	"math/bits"
	"os"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
)

func checkedAdd(total, value uint64) (uint64, error) {
	next, carry := bits.Add64(total, value, 0)
	if carry != 0 {
		return 0, fmt.Errorf("satoshi sum overflow")
	}
	return next, nil
}

func feeMetrics(txid string) error {
	tx, err := api.FetchTXRaw(txid, "testnet")
	if err != nil {
		return err
	}

	var inputSum uint64
	for i, input := range tx.Inputs {
		parent, err := api.FetchTXRaw(input.PreviousTxIDStr(), "testnet")
		if err != nil {
			return fmt.Errorf("input %d parent: %w", i, err)
		}
		vout := int(input.PreviousTxOutIndex)
		if vout < 0 || vout >= len(parent.Outputs) {
			return fmt.Errorf("input %d parent vout %d out of range", i, vout)
		}
		inputSum, err = checkedAdd(inputSum, parent.Outputs[vout].Satoshis)
		if err != nil {
			return err
		}
	}

	var outputSum uint64
	for _, output := range tx.Outputs {
		outputSum, err = checkedAdd(outputSum, output.Satoshis)
		if err != nil {
			return err
		}
	}
	if inputSum < outputSum {
		return fmt.Errorf("outputs %d exceed inputs %d", outputSum, inputSum)
	}

	sizeBytes := len(tx.Bytes())
	paidFee := inputSum - outputSum
	targetFee := (uint64(sizeBytes)*80 + 999) / 1000
	if targetFee < 80 {
		targetFee = 80
	}
	rate := float64(paidFee) * 1000 / float64(sizeBytes)
	status := "ok"
	if paidFee < targetFee {
		status = "UNDERPAID"
	}
	fmt.Printf("%s bytes=%d paid_sat=%d target_sat=%d rate_sat_kb=%.2f status=%s\n",
		txid, sizeBytes, paidFee, targetFee, rate, status)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: testnet-fee-report <txid>...")
		os.Exit(2)
	}
	for _, txid := range os.Args[1:] {
		if err := feeMetrics(txid); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", txid, err)
			os.Exit(1)
		}
	}
}
