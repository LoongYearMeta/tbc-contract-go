package contract

import (
	"errors"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

const (
	feeTestAddress = "mwV3YgnowbJJB3LcyCuqiKpdivvNNFiK7M"
	feeTestScript  = "76a914af2590a45ae401651fdbdf59a76ad43d1862534088ac"
)

func TestContractTargetFeeBoundaries(t *testing.T) {
	tests := map[int]uint64{
		0:    80,
		999:  80,
		1000: 80,
		1001: 81,
	}

	for sizeBytes, expected := range tests {
		got, err := contractTargetFee(sizeBytes)
		if err != nil {
			t.Fatalf("size %d: %v", sizeBytes, err)
		}
		if got != expected {
			t.Fatalf("size %d: got %d, want %d", sizeBytes, got, expected)
		}
	}

	_, err := contractTargetFee(-1)
	if !errors.Is(err, ErrInvalidContractFee) {
		t.Fatalf("negative size: got %v, want %v", err, ErrInvalidContractFee)
	}
}

func feeFinalizerTx(t *testing.T, inputValue, paymentValue, provisionalChange uint64) *bt.Tx {
	t.Helper()

	tx := bt.NewTx()
	if err := tx.From(
		"07912972e42095fe58daaf09161c5a5da57be47c2054dc2aaa52b30fefa1940b",
		0,
		feeTestScript,
		inputValue,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.PayToAddress(feeTestAddress, paymentValue); err != nil {
		t.Fatal(err)
	}
	if err := tx.PayToAddress(feeTestAddress, provisionalChange); err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestFinalizeSignedFeeRejectsInsufficientFunding(t *testing.T) {
	tx := feeFinalizerTx(t, 121, 42, 42)

	err := finalizeSignedFee(tx, 1, func() error { return nil })

	if !errors.Is(err, ErrInsufficientContractFee) {
		t.Fatalf("got %v, want %v", err, ErrInsufficientContractFee)
	}
}

func TestFinalizeSignedFeeRejectsDustChange(t *testing.T) {
	tx := feeFinalizerTx(t, 221, 100, 42)

	err := finalizeSignedFee(tx, 1, func() error { return nil })

	if !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("got %v, want %v", err, ErrOrdinaryOutputDust)
	}
}

func TestFinalizeSignedFeeResignsAndPaysForActualBytes(t *testing.T) {
	tx := feeFinalizerTx(t, 5000, 100, 4000)
	signCalls := 0
	sign := func() error {
		signCalls++
		return tx.InsertInputUnlockingScript(
			0,
			bscript.NewFromBytes(make([]byte, 1200)),
		)
	}

	if err := finalizeSignedFee(tx, 1, sign); err != nil {
		t.Fatal(err)
	}

	if signCalls < 2 {
		t.Fatalf("sign calls = %d, want at least 2", signCalls)
	}
	paid := tx.TotalInputSatoshis() - tx.TotalOutputSatoshis()
	target, err := contractTargetFee(len(tx.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if paid < target {
		t.Fatalf("paid fee %d below target %d", paid, target)
	}
	if tx.Outputs[1].Satoshis < 42 {
		t.Fatalf("change %d below 42", tx.Outputs[1].Satoshis)
	}
}

func TestFinalizeSignedFeePropagatesSignerError(t *testing.T) {
	tx := feeFinalizerTx(t, 1000, 100, 800)
	want := errors.New("sign failed")

	err := finalizeSignedFee(tx, 1, func() error { return want })

	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestRequireOrdinaryOutputBoundaries(t *testing.T) {
	if err := requireOrdinaryOutput(0, "change"); !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("zero output: got %v, want %v", err, ErrOrdinaryOutputDust)
	}
	if err := requireOrdinaryOutput(41, "change"); !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("41 sat output: got %v, want %v", err, ErrOrdinaryOutputDust)
	}
	if err := requireOrdinaryOutput(42, "change"); err != nil {
		t.Fatalf("42 sat output: %v", err)
	}
}

func TestAdjustFeeFromActualSizeRejectsDustChange(t *testing.T) {
	tx := feeFinalizerTx(t, 221, 100, 42)

	err := adjustFeeFromActualSize(tx, 80)

	if !errors.Is(err, ErrOrdinaryOutputDust) {
		t.Fatalf("got %v, want %v", err, ErrOrdinaryOutputDust)
	}
}
