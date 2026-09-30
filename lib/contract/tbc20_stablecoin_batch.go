package contract

import (
	"bytes"
	"fmt"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"math/big"
)

func stableBatchBalance(s ModernTokenSpend) (*big.Int, error) {
	c, t, e := modernReadSpend(s)
	if e != nil {
		return nil, e
	}
	if c.Kind != ModernStablecoin {
		return nil, modernError("expected Stablecoin")
	}
	return t.Balance(), nil
}
func stableBatchUnique(spends []ModernTokenSpend, fee *bt.UTXO) error {
	if fee == nil {
		return modernError("missing fee UTXO")
	}
	seen := map[string]bool{fmt.Sprintf("%s:%d", fee.TxIDStr(), fee.Vout): true}
	for _, s := range spends {
		if s.Parent == nil {
			return modernError("missing parent")
		}
		p := fmt.Sprintf("%s:%d", s.Parent.TxID(), s.CodeVout)
		if seen[p] {
			return modernError("duplicate batch/merge input")
		}
		seen[p] = true
	}
	return nil
}
func stableBatchFee(tx *bt.Tx, key *bec.PrivateKey) (*bt.UTXO, error) {
	v := len(tx.Outputs) - 1
	if v < 0 {
		return nil, modernError("missing fee change")
	}
	a, e := tbc20StandardKeyAddress(key)
	if e != nil {
		return nil, e
	}
	expected, e := bscript.NewP2PKHFromAddress(a.AddressString)
	if e != nil {
		return nil, e
	}
	u, e := util.BuildUTXO(tx, v, false)
	if e != nil {
		return nil, e
	}
	if u.Satoshis < 24 || !bytes.Equal(u.LockingScript.Bytes(), expected.Bytes()) {
		return nil, modernError("insufficient batch fee change")
	}
	return util.FtUTXOToUTXO(u), nil
}
func stableBatchChild(tx *bt.Tx, vout int, group []ModernTokenSpend) ModernTokenSpend {
	parents := ModernTokenAncestors{}
	for _, s := range group {
		parents[s.Parent.TxID()] = s.Parent
	}
	return ModernTokenSpend{Parent: tx, CodeVout: vout, Ancestors: parents}
}

// BatchTransfer chains at most five recipients per transaction, as JS 1.7.2 does.
func (coin TBC20Stablecoin) BatchTransfer(key, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, recipients []StablecoinRecipient) ([]*bt.Tx, error) {
	if len(spends) < 1 || len(spends) > 5 || len(recipients) == 0 {
		return nil, modernError("invalid batch inputs/recipients")
	}
	if e := stableBatchUnique(spends, fee); e != nil {
		return nil, e
	}
	total := new(big.Int)
	for _, s := range spends {
		n, e := stableBatchBalance(s)
		if e != nil {
			return nil, e
		}
		total.Add(total, n)
	}
	required := new(big.Int)
	for _, r := range recipients {
		if r.AmountRaw == nil || r.AmountRaw.Sign() <= 0 {
			return nil, modernError("recipient amount must be positive")
		}
		required.Add(required, r.AmountRaw)
	}
	if required.Cmp(total) > 0 {
		return nil, modernError("insufficient batch balance")
	}
	inputs := append([]ModernTokenSpend{}, spends...)
	funding := fee
	result := []*bt.Tx{}
	for start := 0; start < len(recipients); start += 5 {
		end := start + 5
		if end > len(recipients) {
			end = len(recipients)
		}
		targets := recipients[start:end]
		tx, e := coin.Transfer(key, feeKey, inputs, funding, targets)
		if e != nil {
			return nil, e
		}
		if end < len(recipients) {
			child := stableBatchChild(tx, len(targets)*2, inputs)
			n, e := stableBatchBalance(child)
			if e != nil {
				return nil, e
			}
			if n.Sign() == 0 {
				return nil, modernError("missing token change for next batch")
			}
			inputs = []ModernTokenSpend{child}
			funding, e = stableBatchFee(tx, feeKey)
			if e != nil {
				return nil, e
			}
		}
		result = append(result, tx)
	}
	return result, nil
}

// MergeCoin recursively merges the first five queued inputs, prepending the result.
func (coin TBC20Stablecoin) MergeCoin(key, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO) ([]*bt.Tx, error) {
	if e := stableBatchUnique(spends, fee); e != nil {
		return nil, e
	}
	for _, s := range spends {
		if _, e := stableBatchBalance(s); e != nil {
			return nil, e
		}
	}
	owner, e := tbc20StandardKeyAddress(key)
	if e != nil {
		return nil, e
	}
	controller, e := TBC20StandardAddressController(owner.AddressString)
	if e != nil {
		return nil, e
	}
	pending := append([]ModernTokenSpend{}, spends...)
	funding := fee
	result := []*bt.Tx{}
	for len(pending) > 1 {
		n := len(pending)
		if n > 5 {
			n = 5
		}
		group := append([]ModernTokenSpend{}, pending[:n]...)
		pending = pending[n:]
		sum := new(big.Int)
		for _, s := range group {
			n, e := stableBatchBalance(s)
			if e != nil {
				return nil, e
			}
			sum.Add(sum, n)
		}
		tx, e := coin.Transfer(key, feeKey, group, funding, []StablecoinRecipient{{Controller: controller, AmountRaw: sum}})
		if e != nil {
			return nil, e
		}
		pending = append([]ModernTokenSpend{stableBatchChild(tx, 0, group)}, pending...)
		funding, e = stableBatchFee(tx, feeKey)
		if e != nil {
			return nil, e
		}
		result = append(result, tx)
	}
	return result, nil
}

// ModernTokenAncestors resolves unbroadcast parents in local batch chains.
type ModernTokenAncestors map[string]*bt.Tx

func (m ModernTokenAncestors) ResolveTBC20StandardAncestor(id string) (*bt.Tx, bool) {
	t, ok := m[id]
	return t, ok
}
