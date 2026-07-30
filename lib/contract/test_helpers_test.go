package contract

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestTrackedTestsContainNoWIFLiterals(t *testing.T) {
	root := filepath.Clean("../..")
	wifPattern := regexp.MustCompile(`\b[KL][1-9A-HJ-NP-Za-km-z]{50,51}\b`)
	err := filepath.Walk(filepath.Join(root, "test"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if wifPattern.Match(body) {
			t.Errorf("tracked WIF literal in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustTestPrivateKey(t *testing.T, seed byte) *bec.PrivateKey {
	t.Helper()
	raw := bytes.Repeat([]byte{seed}, 32)
	key, _ := bec.PrivKeyFromBytes(bec.S256(), raw)
	if key == nil {
		t.Fatal("failed to derive deterministic test key")
	}
	return key
}

func mustTx(t *testing.T, raw string) *bt.Tx {
	t.Helper()
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func mustFixtureScript(t *testing.T, name string) *bscript.Script {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "js-1.6.5", name))
	if err != nil {
		t.Fatal(err)
	}
	script, err := bscript.NewFromHexString(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func assertOutputKind(t *testing.T, tx *bt.Tx, index int, want string) {
	t.Helper()
	if index < 0 || index >= len(tx.Outputs) {
		t.Fatalf("output %d outside transaction with %d outputs", index, len(tx.Outputs))
	}
	script := tx.Outputs[index].LockingScript
	var got string
	switch {
	case script == nil:
		got = "nil"
	case script.IsP2PKH():
		got = "p2pkh"
	case script.IsSafeDataOut():
		got = "safe-data"
	case script.IsDataOut():
		got = "data"
	default:
		got = "contract"
	}
	if got != want {
		t.Fatalf("output %d kind = %s, want %s", index, got, want)
	}
}

func assertSameTransactionStructureAndScripts(t *testing.T, leftRaw, rightRaw string) {
	t.Helper()
	left := mustTx(t, leftRaw)
	right := mustTx(t, rightRaw)
	if left.Version != right.Version || left.LockTime != right.LockTime {
		t.Fatalf("header differs: version/locktime %d/%d vs %d/%d",
			left.Version, left.LockTime, right.Version, right.LockTime)
	}
	if len(left.Inputs) != len(right.Inputs) || len(left.Outputs) != len(right.Outputs) {
		t.Fatalf("shape differs: %d inputs/%d outputs vs %d inputs/%d outputs",
			len(left.Inputs), len(left.Outputs), len(right.Inputs), len(right.Outputs))
	}
	for i := range left.Inputs {
		l, r := left.Inputs[i], right.Inputs[i]
		if l.PreviousTxIDStr() != r.PreviousTxIDStr() ||
			l.PreviousTxOutIndex != r.PreviousTxOutIndex ||
			l.SequenceNumber != r.SequenceNumber ||
			!bytes.Equal(l.UnlockingScript.Bytes(), r.UnlockingScript.Bytes()) {
			t.Fatalf("input %d differs", i)
		}
	}
	for i := range left.Outputs {
		l, r := left.Outputs[i], right.Outputs[i]
		if l.Satoshis != r.Satoshis ||
			hex.EncodeToString(l.LockingScript.Bytes()) != hex.EncodeToString(r.LockingScript.Bytes()) {
			t.Fatalf("output %d differs", i)
		}
	}
}
