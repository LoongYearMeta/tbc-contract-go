package api

import (
	"errors"
	"math"
	"testing"
)

func TestSelectUTXOAtLeast(t *testing.T) {
	utxos := []utxoListItem{
		{Value: 99},
		{Value: 100},
		{Value: 101},
	}

	got, err := selectUTXOAtLeast(utxos, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != 100 {
		t.Fatalf("selected %d, want 100", got.Value)
	}

	_, err = selectUTXOAtLeast(utxos, 102)
	if !errors.Is(err, ErrNoSufficientUTXO) {
		t.Fatalf("got %v, want %v", err, ErrNoSufficientUTXO)
	}
}

func TestTBCAmountToSatoshisStrictConversion(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		want    uint64
		wantErr bool
	}{
		{name: "zero", amount: 0, want: 0},
		{name: "six decimals", amount: 1.000001, want: 1_000_001},
		{name: "floating point noise", amount: 0.1 * 3, want: 300_000},
		{name: "large six-decimal amount", amount: 21_000_000.123456, want: 21_000_000_123_456},
		{name: "negative", amount: -1, wantErr: true},
		{name: "nan", amount: math.NaN(), wantErr: true},
		{name: "positive infinity", amount: math.Inf(1), wantErr: true},
		{name: "more than six decimals", amount: 0.0000001, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tbcAmountToSatoshis(test.amount)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidTBCAmount) {
					t.Fatalf("got %v, want %v", err, ErrInvalidTBCAmount)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %d, want %d", got, test.want)
			}
		})
	}
}
