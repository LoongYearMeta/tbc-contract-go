package contract

import "testing"

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
