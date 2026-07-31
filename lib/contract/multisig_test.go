package contract

import (
	"strings"
	"testing"
)

func TestGetCombineHashMatchesJSAndRustWalletVector(t *testing.T) {
	const (
		address = "FP1tiQcNY7ggf4qqF8Gti9LLsASjGWoQyW"
		want    = "ed4eb345d392c4a971103aad53f1851d6316f13901"
	)
	got, err := GetCombineHash(address)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("combine hash=%s want=%s", got, want)
	}
}

func TestGetMultiSigAddressRejectsDeclaredKeyCountMismatch(t *testing.T) {
	pubKeys := []string{
		"02" + strings.Repeat("11", 32),
		"03" + strings.Repeat("22", 32),
		"02" + strings.Repeat("33", 32),
	}

	for _, publicKeyCount := range []int{4, 2} {
		if _, err := GetMultiSigAddress(pubKeys, 2, publicKeyCount); err == nil {
			t.Fatalf("publicKeyCount=%d: expected mismatch error", publicKeyCount)
		}
	}
}

func TestGetMultiSigAddressRejectsInvalidCompressedPublicKey(t *testing.T) {
	valid := []string{
		"02" + strings.Repeat("11", 32),
		"03" + strings.Repeat("22", 32),
		"02" + strings.Repeat("33", 32),
	}
	tests := []string{
		"04" + strings.Repeat("44", 32),
		"02" + strings.Repeat("55", 31),
		"not-hex",
	}
	for _, invalid := range tests {
		pubKeys := append([]string(nil), valid...)
		pubKeys[1] = invalid
		if _, err := GetMultiSigAddress(pubKeys, 2, len(pubKeys)); err == nil {
			t.Fatalf("public key %q: expected validation error", invalid)
		}
	}
}
