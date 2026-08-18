package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestOrderBookV4ScriptsMatchJS166(t *testing.T) {
	o := NewOrderBook()
	o.HoldAddress = "1BitcoinEaterAddressDontSendf59kuE"
	o.SaleVolume = 123456
	o.FeeRate = 10000
	o.UnitPrice = 2000000
	o.FtPartialHash = strings.Repeat("22", 32)
	o.FtID = strings.Repeat("11", 32)
	fixtures := loadJS166Fixtures(t)

	for _, tc := range []struct {
		name, side string
	}{
		{name: "sellOrderV4", side: "sell"},
		{name: "buyOrderV4", side: "buy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, err := o.buildOrderCodeScript(tc.side, false, o.HoldAddress, "1c08")
			if err != nil {
				t.Fatal(err)
			}
			got := script.Bytes()
			hash := sha256.Sum256(got)
			want := fixtures[tc.name]
			if len(got) != want.Length || hex.EncodeToString(hash[:]) != want.SHA256 {
				t.Fatalf("length/hash = %d/%s, want %d/%s", len(got), hex.EncodeToString(hash[:]), want.Length, want.SHA256)
			}
		})
	}
}

func TestOrderBookInputLimitsJS166(t *testing.T) {
	for _, tc := range []struct {
		name      string
		utxos, ft int
		buy       bool
		wantError bool
	}{
		{name: "sell ten", utxos: 10},
		{name: "sell eleven", utxos: 11, wantError: true},
		{name: "buy five ft ten total", utxos: 5, ft: 5, buy: true},
		{name: "buy six ft", utxos: 1, ft: 6, buy: true, wantError: true},
		{name: "buy eleven total", utxos: 6, ft: 5, buy: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.buy {
				err = validateBuyOrderInputCount(tc.utxos, tc.ft)
			} else {
				err = validateSellOrderInputCount(tc.utxos)
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}
