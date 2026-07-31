package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestTargetFee80PerKBWithFloor(t *testing.T) {
	tests := []struct {
		size int
		want uint64
	}{
		{size: 1, want: 80},
		{size: 999, want: 80},
		{size: 1000, want: 80},
		{size: 1001, want: 81},
		{size: 2500, want: 200},
	}
	for _, test := range tests {
		if got := targetFee80(test.size); got != test.want {
			t.Fatalf("size=%d got=%d want=%d", test.size, got, test.want)
		}
	}
}

func TestCheckedAddSatoshisRejectsOverflow(t *testing.T) {
	if _, err := checkedAddSatoshis(math.MaxUint64, 1); err == nil {
		t.Fatal("expected satoshi overflow error")
	}
}

func TestEvidenceJSONContainsOnlyPublicFields(t *testing.T) {
	result := txEvidence{
		Stage:     "ft-transfer",
		TxID:      strings.Repeat("1", 64),
		Bytes:     250,
		PaidFee:   80,
		TargetFee: 80,
		Refetched: true,
		Invariant: "ft amount conserved",
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"wif", "private", "secret", "raw", "signature"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("public evidence leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func evidenceTestTransactions(t *testing.T) (*bt.Tx, *bt.Tx) {
	t.Helper()
	lockingScript, err := bscript.NewFromHexString("51")
	if err != nil {
		t.Fatal(err)
	}
	parent := bt.NewTx()
	parent.AddOutput(&bt.Output{Satoshis: 1000, LockingScript: lockingScript})
	parentUTXO, err := outputUTXO(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	child := bt.NewTx()
	if err := child.FromUTXOs(parentUTXO); err != nil {
		t.Fatal(err)
	}
	child.AddOutput(&bt.Output{Satoshis: 900, LockingScript: lockingScript})
	return parent, child
}

func TestFeeFromParentsUsesReferencedOutputs(t *testing.T) {
	parent, child := evidenceTestTransactions(t)
	fee, err := feeFromParents(child, func(txid string) (*bt.Tx, error) {
		if txid != parent.TxID() {
			return nil, fmt.Errorf("unexpected txid %s", txid)
		}
		return parent, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fee != 100 {
		t.Fatalf("fee=%d want=100", fee)
	}
}

func TestCollectBroadcastEvidenceBroadcastsOnceAndRetriesOnlyReads(t *testing.T) {
	parent, child := evidenceTestTransactions(t)
	var output bytes.Buffer
	broadcastCalls := 0
	childFetchCalls := 0
	deps := evidenceDependencies{
		Broadcast: func(raw, network string) (string, error) {
			broadcastCalls++
			if network != "testnet" || raw != child.String() {
				return "", fmt.Errorf("unexpected broadcast arguments")
			}
			return child.TxID(), nil
		},
		Fetch: func(txid, network string) (*bt.Tx, error) {
			if network != "testnet" {
				return nil, fmt.Errorf("unexpected network %s", network)
			}
			switch txid {
			case child.TxID():
				childFetchCalls++
				if childFetchCalls < 3 {
					return nil, errors.New("index pending")
				}
				return child, nil
			case parent.TxID():
				return parent, nil
			default:
				return nil, fmt.Errorf("unexpected txid %s", txid)
			}
		},
		Sleep:  func() {},
		Output: io.Writer(&output),
	}
	accepted, evidence, err := collectBroadcastEvidence(
		deps, "plain", child.String(), "testnet", "shape-ok",
		func(tx *bt.Tx) error {
			if tx.TxID() != child.TxID() {
				return fmt.Errorf("wrong callback transaction")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.TxID() != child.TxID() || evidence.TxID != child.TxID() {
		t.Fatalf("unexpected accepted transaction or evidence")
	}
	if broadcastCalls != 1 {
		t.Fatalf("broadcast calls=%d want=1", broadcastCalls)
	}
	if childFetchCalls != 3 {
		t.Fatalf("child fetch calls=%d want=3", childFetchCalls)
	}
	if !strings.HasPrefix(output.String(), "RESULT ") {
		t.Fatalf("missing machine-readable result: %q", output.String())
	}
}

func TestCollectBroadcastEvidenceValidatesBeforeBroadcast(t *testing.T) {
	_, child := evidenceTestTransactions(t)
	broadcastCalls := 0
	deps := evidenceDependencies{
		Broadcast: func(string, string) (string, error) {
			broadcastCalls++
			return "", nil
		},
		Fetch:  func(string, string) (*bt.Tx, error) { return nil, errors.New("unused") },
		Sleep:  func() {},
		Output: io.Discard,
	}
	_, _, err := collectBroadcastEvidence(
		deps, "invalid", child.String(), "testnet", "must-fail",
		func(*bt.Tx) error { return errors.New("invalid transaction shape") },
	)
	if err == nil || !strings.Contains(err.Error(), "validate before broadcast") {
		t.Fatalf("unexpected error: %v", err)
	}
	if broadcastCalls != 0 {
		t.Fatalf("broadcast called %d times", broadcastCalls)
	}
}
