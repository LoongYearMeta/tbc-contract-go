package contractquery

import (
	"errors"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"math/big"
	"testing"
)

func TestTokenSelection173LockDomains(t *testing.T) {
	item := func(n int64, lock uint32) TokenUTXO {
		return TokenUTXO{Asset: &c.TokenAsset{Balance: big.NewInt(n), LockTime: lock}}
	}
	list := []TokenUTXO{item(6, 100), item(7, 500000001), item(2, 0)}
	selected, e := SelectTokenUTXOs(list, big.NewInt(8), 2)
	if e != nil || len(selected) != 2 || selected[0].Asset.LockTime != 100 || selected[1].Asset.LockTime != 0 {
		t.Fatal(selected, e)
	}
	if _, e = SelectTokenUTXOs(list, big.NewInt(10), 3); !errors.Is(e, ErrTokenInputsIncompatible) {
		t.Fatal(e)
	}
	if _, e = SelectTokenUTXOs(list, big.NewInt(16), 3); e == nil || errors.Is(e, ErrTokenInputsIncompatible) {
		t.Fatal(e)
	}
	selected, e = SelectTokenUTXOs(list, nil, 1)
	if e != nil || selected[0].Asset.Balance.Int64() != 7 {
		t.Fatal(selected, e)
	}
}
