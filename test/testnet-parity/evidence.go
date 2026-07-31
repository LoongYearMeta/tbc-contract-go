package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"os"
	"time"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	bt "github.com/LoongYearMeta/tbc-lib-go"
)

const evidenceRefetchAttempts = 10

type txEvidence struct {
	Stage     string `json:"stage"`
	TxID      string `json:"txid"`
	Bytes     int    `json:"bytes"`
	PaidFee   uint64 `json:"paid_fee_sat"`
	TargetFee uint64 `json:"target_fee_sat"`
	Refetched bool   `json:"refetched"`
	Invariant string `json:"invariant"`
}

type evidenceDependencies struct {
	Broadcast func(raw, network string) (string, error)
	Fetch     func(txid, network string) (*bt.Tx, error)
	Sleep     func()
	Output    io.Writer
}

func targetFee80(size int) uint64 {
	target := (uint64(size)*80 + 999) / 1000
	if target < 80 {
		return 80
	}
	return target
}

func checkedAddSatoshis(total, value uint64) (uint64, error) {
	next, carry := bits.Add64(total, value, 0)
	if carry != 0 {
		return 0, fmt.Errorf("satoshi sum overflow")
	}
	return next, nil
}

func feeFromParents(tx *bt.Tx, fetch func(string) (*bt.Tx, error)) (uint64, error) {
	var inputs uint64
	for i, input := range tx.Inputs {
		parent, err := fetch(input.PreviousTxIDStr())
		if err != nil {
			return 0, fmt.Errorf("input %d parent: %w", i, err)
		}
		vout := int(input.PreviousTxOutIndex)
		if vout < 0 || vout >= len(parent.Outputs) {
			return 0, fmt.Errorf("input %d parent vout %d out of range", i, vout)
		}
		inputs, err = checkedAddSatoshis(inputs, parent.Outputs[vout].Satoshis)
		if err != nil {
			return 0, err
		}
	}
	var outputs uint64
	for _, output := range tx.Outputs {
		var err error
		outputs, err = checkedAddSatoshis(outputs, output.Satoshis)
		if err != nil {
			return 0, err
		}
	}
	if inputs < outputs {
		return 0, fmt.Errorf("outputs %d exceed inputs %d", outputs, inputs)
	}
	return inputs - outputs, nil
}

func fetchEvidenceTransaction(
	deps evidenceDependencies,
	txid string,
	network string,
) (*bt.Tx, error) {
	var lastErr error
	for attempt := 1; attempt <= evidenceRefetchAttempts; attempt++ {
		tx, err := deps.Fetch(txid, network)
		if err == nil && tx != nil {
			if tx.TxID() != txid {
				err = fmt.Errorf("refetched txid %s, want %s", tx.TxID(), txid)
			} else {
				return tx, nil
			}
		}
		if err == nil {
			err = fmt.Errorf("refetch returned nil transaction")
		}
		lastErr = err
		if attempt < evidenceRefetchAttempts {
			deps.Sleep()
		}
	}
	return nil, lastErr
}

func collectBroadcastEvidence(
	deps evidenceDependencies,
	label string,
	raw string,
	network string,
	invariantName string,
	validate func(*bt.Tx) error,
) (*bt.Tx, txEvidence, error) {
	if network != "testnet" {
		return nil, txEvidence{}, fmt.Errorf("%s refuses non-testnet network %q", label, network)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, txEvidence{}, fmt.Errorf("%s parse: %w", label, err)
	}
	if validate != nil {
		if err := validate(tx); err != nil {
			return nil, txEvidence{}, fmt.Errorf("%s validate before broadcast: %w", label, err)
		}
	}
	txid, err := deps.Broadcast(raw, network)
	if err != nil {
		return nil, txEvidence{}, fmt.Errorf("%s broadcast: %w", label, err)
	}
	if txid != tx.TxID() {
		return nil, txEvidence{}, fmt.Errorf(
			"%s returned txid %s, want local %s", label, txid, tx.TxID(),
		)
	}
	refetched, err := fetchEvidenceTransaction(deps, txid, network)
	if err != nil {
		return nil, txEvidence{}, fmt.Errorf("%s refetch: %w", label, err)
	}
	if !bytes.Equal(refetched.Bytes(), tx.Bytes()) {
		return nil, txEvidence{}, fmt.Errorf("%s refetched raw transaction mismatch", label)
	}
	paidFee, err := feeFromParents(refetched, func(parentTxID string) (*bt.Tx, error) {
		return fetchEvidenceTransaction(deps, parentTxID, network)
	})
	if err != nil {
		return nil, txEvidence{}, fmt.Errorf("%s fee reconstruction: %w", label, err)
	}
	size := len(refetched.Bytes())
	targetFee := targetFee80(size)
	if paidFee < targetFee {
		return nil, txEvidence{}, fmt.Errorf(
			"%s fee underpaid: paid=%d target=%d bytes=%d",
			label, paidFee, targetFee, size,
		)
	}
	if invariantName == "" {
		invariantName = "transaction-valid"
	}
	evidence := txEvidence{
		Stage:     label,
		TxID:      txid,
		Bytes:     size,
		PaidFee:   paidFee,
		TargetFee: targetFee,
		Refetched: true,
		Invariant: invariantName,
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, txEvidence{}, fmt.Errorf("%s encode evidence: %w", label, err)
	}
	if deps.Output != nil {
		if _, err := fmt.Fprintf(deps.Output, "RESULT %s\n", encoded); err != nil {
			return nil, txEvidence{}, fmt.Errorf("%s write evidence: %w", label, err)
		}
	}
	return refetched, evidence, nil
}

func broadcastAndVerify(
	label string,
	raw string,
	network string,
	invariantName string,
	validate func(*bt.Tx) error,
) (*bt.Tx, txEvidence, error) {
	return collectBroadcastEvidence(
		evidenceDependencies{
			Broadcast: api.BroadcastTXRaw,
			Fetch:     api.FetchTXRaw,
			Sleep:     func() { time.Sleep(time.Second) },
			Output:    os.Stdout,
		},
		label,
		raw,
		network,
		invariantName,
		validate,
	)
}
