package contractquery

import (
	"encoding/json"
	"testing"
)

func TestLosslessIntegersAndConflictingAliases(t *testing.T) {
	n, e := integer(json.RawMessage(`"9007199254740993"`))
	if e != nil || n.String() != "9007199254740993" {
		t.Fatal(n, e)
	}
	for _, raw := range []string{`1.5`, `"-1"`, `"1e6"`} {
		if _, e = integer(json.RawMessage(raw)); e == nil {
			t.Fatal("accepted noninteger", raw)
		}
	}
	if _, e = alias(map[string]json.RawMessage{"index": json.RawMessage(`1`), "vout": json.RawMessage(`2`)}, "index", "vout"); e == nil {
		t.Fatal("accepted aliases")
	}
}
