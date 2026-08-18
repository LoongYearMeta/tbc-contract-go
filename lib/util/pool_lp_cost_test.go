package util

import (
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestPoolLpCostUsesOpcodeContextJS166(t *testing.T) {
	const pkh = "759d6677091e973b9e9d99f19c68fbf43e3f05f9"
	script, err := bscript.NewFromASM(pkh + " OP_EQUALVERIFY OP_PARTIAL_HASH OP_OVER 40420f0000000000 OP_EQUALVERIFY")
	if err != nil {
		t.Fatal(err)
	}
	code := script.String()
	address, err := GetLpCostAddress(code)
	if err != nil {
		t.Fatal(err)
	}
	if address != "1BitcoinEaterAddressDontSendf59kuE" {
		t.Fatalf("address = %s", address)
	}
	amount, err := GetLpCostAmount(code)
	if err != nil {
		t.Fatal(err)
	}
	if amount != 1_000_000 {
		t.Fatalf("amount = %d", amount)
	}

	duplicate, err := bscript.NewFromHexString(code + code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetLpCostAmount(duplicate.String()); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("duplicate error = %v", err)
	}
}
