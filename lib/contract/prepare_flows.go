package contract

import (
	"bytes"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type PreparedTBCAMMOperation struct {
	*PreparedTransaction
	Quote  *TBCAMMQuote
	Layout *TBCAMMOutputLayout
}

// PrepareMintTbcAmm uses an explicitly chosen root; it does not create a Source.
func (a *TBCAMM) PrepareMintTbcAmm(public *bec.PublicKey, fee *bt.UTXO) (*PreparedTBCAMMOperation, error) {
	signer, e := external(public)
	if e != nil {
		return nil, e
	}
	tx, e := a.MintFromRoot(signer, fee)
	if e != nil {
		return nil, e
	}
	p, e := newPreparedTransaction(tx)
	if e != nil {
		return nil, e
	}
	return &PreparedTBCAMMOperation{PreparedTransaction: p}, nil
}

// PrepareOperation ignores local signers in o. All required identities are
// supplied explicitly as public keys; returned requests commit to a snapshot.
func (a *TBCAMM) PrepareOperation(o TBCAMMOperation, key, feeKey, controller *bec.PublicKey) (*PreparedTBCAMMOperation, error) {
	var e error
	o.Key, e = external(key)
	if e != nil {
		return nil, e
	}
	o.FeeKey, e = external(feeKey)
	if e != nil {
		return nil, e
	}
	o.Controller = nil
	if controller != nil {
		o.Controller, e = external(controller)
		if e != nil {
			return nil, e
		}
	}
	built, e := a.Operate(o)
	if e != nil {
		return nil, e
	}
	p, e := newPreparedTransaction(built.Transaction)
	if e != nil {
		return nil, e
	}
	return &PreparedTBCAMMOperation{p, built.Quote, &built.Layout}, nil
}
func (a *TBCAMM) PrepareAddLP(o TBCAMMOperation, key, feeKey, controller *bec.PublicKey) (*PreparedTBCAMMOperation, error) {
	o.Option = 1
	return a.PrepareOperation(o, key, feeKey, controller)
}
func (a *TBCAMM) PrepareRemoveLP(o TBCAMMOperation, key, feeKey, controller *bec.PublicKey) (*PreparedTBCAMMOperation, error) {
	o.Option = 2
	return a.PrepareOperation(o, key, feeKey, controller)
}
func (a *TBCAMM) PrepareSwapFT(o TBCAMMOperation, key, feeKey, controller *bec.PublicKey) (*PreparedTBCAMMOperation, error) {
	o.Option = 3
	return a.PrepareOperation(o, key, feeKey, controller)
}
func (a *TBCAMM) PrepareSwapTBC(o TBCAMMOperation, key, feeKey, controller *bec.PublicKey) (*PreparedTBCAMMOperation, error) {
	o.Option = 4
	return a.PrepareOperation(o, key, feeKey, controller)
}
func (a *TBCAMM) PrepareTransferLP(key, feeKey *bec.PublicKey, fee *bt.UTXO, spends []ModernTokenSpend, recipient string, amount uint64, outputLock *uint32) (*PreparedTBCAMMOperation, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := a.TransferLP(k, f, fee, spends, recipient, amount, outputLock)
	if e != nil {
		return nil, e
	}
	p, e := newPreparedTransaction(tx)
	if e != nil {
		return nil, e
	}
	return &PreparedTBCAMMOperation{PreparedTransaction: p}, nil
}
func (a *TBCAMM) PrepareUnlockLP(key, feeKey *bec.PublicKey, fee *bt.UTXO, spends []ModernTokenSpend, recipient string, amount uint64) (*PreparedTBCAMMOperation, error) {
	zero := uint32(0)
	return a.PrepareTransferLP(key, feeKey, fee, spends, recipient, amount, &zero)
}

func preparedRaw(raw string, previous []*bt.UTXO) (*PreparedTransaction, error) {
	tx, e := bt.NewTxFromString(raw)
	if e != nil {
		return nil, e
	}
	for _, in := range tx.Inputs {
		found := false
		for _, u := range previous {
			if u != nil && bytes.Equal(u.TxID, in.PreviousTxID()) && u.Vout == in.PreviousTxOutIndex {
				in.PreviousTxScript = bscript.NewFromBytes(append([]byte{}, u.LockingScript.Bytes()...))
				in.PreviousTxSatoshis = u.Satoshis
				found = true
				break
			}
		}
		if !found {
			return nil, modernError("missing trusted previous output")
		}
	}
	return newPreparedTransaction(tx)
}
func (h TBCHTLC) PrepareDeploy(public *bec.PublicKey, fee *bt.UTXO, amount uint64) (*PreparedTransaction, error) {
	k, e := external(public)
	if e != nil {
		return nil, e
	}
	raw, e := h.Deploy(k, fee, amount)
	if e != nil {
		return nil, e
	}
	return preparedRaw(raw, []*bt.UTXO{fee})
}
func (h TBCHTLC) PrepareSpend(public *bec.PublicKey, parent *bt.Tx, vout int, secret []byte) (*PreparedTransaction, error) {
	k, e := external(public)
	if e != nil {
		return nil, e
	}
	raw, e := h.Spend(k, parent, vout, secret)
	if e != nil {
		return nil, e
	}
	u, e := modernUtxo(parent, vout)
	if e != nil {
		return nil, e
	}
	return preparedRaw(raw, []*bt.UTXO{u})
}
func (h TBCHTLC) PrepareDeployToken(key, feeKey *bec.PublicKey, spends []ModernTokenSpend, fee *bt.UTXO, amount uint64) (*PreparedTransaction, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := h.DeployToken(k, f, spends, fee, amount)
	if e != nil {
		return nil, e
	}
	return newPreparedTransaction(tx)
}
func (h TBCHTLC) PrepareSpendToken(key, feeKey *bec.PublicKey, spend ModernTokenSpend, fee *bt.UTXO, secret []byte) (*PreparedTransaction, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := h.SpendToken(k, f, spend, fee, secret)
	if e != nil {
		return nil, e
	}
	return newPreparedTransaction(tx)
}
func (o TBCLOP) PrepareMake(key, feeKey *bec.PublicKey, spends []ModernTokenSpend, fee *bt.UTXO) (*PreparedTransaction, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := o.Make(k, f, spends, fee)
	if e != nil {
		return nil, e
	}
	return newPreparedTransaction(tx)
}
func (s LOPSpend) PrepareCancel(key, feeKey *bec.PublicKey, fee *bt.UTXO) (*PreparedTransaction, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := s.Cancel(k, f, fee)
	if e != nil {
		return nil, e
	}
	return newPreparedTransaction(tx)
}
func PrepareMatchLOPOrders(key, feeKey *bec.PublicKey, buy, sell LOPSpend, fee *bt.UTXO) (*PreparedTransaction, error) {
	k, e := external(key)
	if e != nil {
		return nil, e
	}
	f, e := external(feeKey)
	if e != nil {
		return nil, e
	}
	tx, e := MatchLOPOrders(k, f, buy, sell, fee)
	if e != nil {
		return nil, e
	}
	return newPreparedTransaction(tx)
}
