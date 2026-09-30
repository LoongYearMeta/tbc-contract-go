package contract

import (
	"bytes"
	"encoding/hex"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"math/big"
)

// TBCHTLC preserves the native HTLC template and adds authenticated new-token dispatch.
type TBCHTLC struct {
	Sender, Receiver string
	Hashlock         [32]byte
	Timelock         uint32
}

func (h TBCHTLC) Script() (*bscript.Script, error) {
	s, e := htlcAddressToPKH(h.Sender)
	if e != nil {
		return nil, e
	}
	r, e := htlcAddressToPKH(h.Receiver)
	if e != nil {
		return nil, e
	}
	return GetHTLCCode(s, r, hex.EncodeToString(h.Hashlock[:]), h.Timelock)
}
func (h TBCHTLC) owner(key TransactionSigner, secret []byte) (string, error) {
	if signerMissing(key) {
		return "", modernError("missing HTLC signer")
	}
	address, e := privKeyToAddress(key)
	if e != nil {
		return "", e
	}
	want := h.Sender
	if secret != nil {
		want = h.Receiver
		if !bytes.Equal(crypto.Sha256(secret), h.Hashlock[:]) {
			return "", modernError("secret does not satisfy hash lock")
		}
	}
	if address != want {
		return "", modernError("HTLC branch owner mismatch")
	}
	return address, nil
}
func (h TBCHTLC) unlock(key TransactionSigner, tx *bt.Tx, vin int, secret []byte) (*bscript.Script, error) {
	if _, e := h.owner(key, secret); e != nil {
		return nil, e
	}
	digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
	if e != nil {
		return nil, e
	}
	sig, e := modernSign(key, digest, false)
	if e != nil {
		return nil, e
	}
	script := bscript.NewFromBytes(nil)
	for _, b := range [][]byte{sig, key.PubKey().SerialiseCompressed()} {
		if e = script.AppendPushData(b); e != nil {
			return nil, e
		}
	}
	if secret != nil {
		if e = script.AppendPushData(secret); e != nil {
			return nil, e
		}
		e = script.AppendOpcodes(0x51)
	} else {
		e = script.AppendOpcodes(0)
	}
	return script, e
}
func (h TBCHTLC) Deploy(key TransactionSigner, fee *bt.UTXO, amount uint64) (string, error) {
	if amount < 42 {
		return "", modernError("native HTLC amount below dust")
	}
	if e := tbc20StandardAssertP2PKHOwner(fee, key, "fee"); e != nil {
		return "", e
	}
	return DeployHTLCWithSign(h.Sender, h.Receiver, hex.EncodeToString(h.Hashlock[:]), h.Timelock, amount, fee, key)
}
func (h TBCHTLC) Spend(key TransactionSigner, parent *bt.Tx, vout int, secret []byte) (string, error) {
	address, e := h.owner(key, secret)
	if e != nil {
		return "", e
	}
	u, e := modernUtxo(parent, vout)
	if e != nil {
		return "", e
	}
	script, e := h.Script()
	if e != nil {
		return "", e
	}
	if !bytes.Equal(script.Bytes(), u.LockingScript.Bytes()) {
		return "", modernError("parent is a different HTLC")
	}
	if secret != nil {
		return WithdrawWithSign(key, address, u, hex.EncodeToString(secret))
	}
	return RefundWithSign(address, u, key, h.Timelock)
}
func (h TBCHTLC) DeployToken(key, feeKey TransactionSigner, spends []ModernTokenSpend, fee *bt.UTXO, amount uint64) (*bt.Tx, error) {
	if len(spends) < 1 || len(spends) > 5 || amount == 0 {
		return nil, modernError("invalid HTLC token inputs")
	}
	sender, e := privKeyToAddress(key)
	if e != nil {
		return nil, e
	}
	if sender != h.Sender {
		return nil, modernError("sender mismatch")
	}
	owner, e := TBC20StandardAddressController(sender)
	if e != nil {
		return nil, e
	}
	assets := []*TokenAsset{}
	total := new(big.Int)
	for _, s := range spends {
		a, e := ReadTokenAsset(s)
		if e != nil {
			return nil, e
		}
		if a.Controller != owner || len(assets) > 0 && !bytes.Equal(a.Identity, assets[0].Identity) {
			return nil, modernError("mixed token identity or owner")
		}
		assets = append(assets, a)
		total.Add(total, a.Balance)
	}
	remaining := new(big.Int).SetUint64(amount)
	if remaining.Cmp(total) > 0 {
		return nil, modernError("insufficient token balance")
	}
	locked, change := [6]uint64{}, [6]uint64{}
	for i, a := range assets {
		take := new(big.Int).Set(remaining)
		if take.Cmp(a.Balance) > 0 {
			take.Set(a.Balance)
		}
		rest := new(big.Int).Sub(a.Balance, take)
		if !take.IsUint64() || !rest.IsUint64() {
			return nil, modernError("slot overflow")
		}
		locked[i] = take.Uint64()
		change[i] = rest.Uint64()
		remaining.Sub(remaining, take)
	}
	script, e := h.Script()
	if e != nil {
		return nil, e
	}
	controller := [21]byte{}
	copy(controller[:20], crypto.Hash160(crypto.Sha256(script.Bytes())))
	controller[20] = 1
	code, e := assets[0].ReplaceController(controller)
	if e != nil {
		return nil, e
	}
	tape, e := assets[0].ReplaceAmounts(locked)
	if e != nil {
		return nil, e
	}
	outputs := []*bt.Output{{Satoshis: 100, LockingScript: script}, {Satoshis: 500, LockingScript: code}, {Satoshis: 0, LockingScript: tape}}
	two := 2
	groups := []util.TBC20StandardOutputGroup{{CodeVout: 0}, {CodeVout: 1, TapeVout: &two}}
	if total.Cmp(new(big.Int).SetUint64(amount)) > 0 {
		tape, e := assets[0].ReplaceAmounts(change)
		if e != nil {
			return nil, e
		}
		four := 4
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: 3, TapeVout: &four})
		outputs = append(outputs, &bt.Output{Satoshis: 500, LockingScript: assets[0].Code}, &bt.Output{Satoshis: 0, LockingScript: tape})
	}
	return h.buildToken(key, feeKey, spends, assets, fee, outputs, groups, nil, nil)
}
func (h TBCHTLC) SpendToken(key, feeKey TransactionSigner, spend ModernTokenSpend, fee *bt.UTXO, secret []byte) (*bt.Tx, error) {
	address, e := h.owner(key, secret)
	if e != nil {
		return nil, e
	}
	script, e := h.Script()
	if e != nil {
		return nil, e
	}
	if spend.CodeVout != 1 || spend.Parent == nil || len(spend.Parent.Outputs) < 3 || spend.Parent.Outputs[0].Satoshis != 100 || !bytes.Equal(script.Bytes(), spend.Parent.Outputs[0].LockingScript.Bytes()) {
		return nil, modernError("token must be bound to HTLC at vout0")
	}
	a, e := ReadTokenAsset(spend)
	if e != nil {
		return nil, e
	}
	controller := [21]byte{}
	copy(controller[:20], crypto.Hash160(crypto.Sha256(script.Bytes())))
	controller[20] = 1
	if a.Controller != controller || a.Balance.Sign() <= 0 || !a.Balance.IsUint64() {
		return nil, modernError("token balance or controller mismatch")
	}
	owner, e := TBC20StandardAddressController(address)
	if e != nil {
		return nil, e
	}
	code, e := a.ReplaceController(owner)
	if e != nil {
		return nil, e
	}
	tape, e := a.ReplaceAmounts([6]uint64{0, a.Balance.Uint64()})
	if e != nil {
		return nil, e
	}
	one := 1
	return h.buildToken(key, feeKey, []ModernTokenSpend{spend}, []*TokenAsset{a}, fee, []*bt.Output{{Satoshis: 500, LockingScript: code}, {Satoshis: 0, LockingScript: tape}}, []util.TBC20StandardOutputGroup{{CodeVout: 0, TapeVout: &one}}, spend.Parent, secret)
}
func (h TBCHTLC) buildToken(key, feeKey TransactionSigner, spends []ModernTokenSpend, assets []*TokenAsset, fee *bt.UTXO, outputs []*bt.Output, groups []util.TBC20StandardOutputGroup, controller *bt.Tx, secret []byte) (*bt.Tx, error) {
	inputs := []*bt.UTXO{}
	offset := 0
	if controller != nil {
		u, e := modernUtxo(controller, 0)
		if e != nil {
			return nil, e
		}
		inputs = append(inputs, u)
		offset = 1
	}
	locks := []uint32{}
	for i, s := range spends {
		u, e := modernUtxo(s.Parent, s.CodeVout)
		if e != nil {
			return nil, e
		}
		inputs = append(inputs, u)
		locks = append(locks, assets[i].LockTime)
	}
	if controller != nil && secret == nil {
		locks = append(locks, h.Timelock)
	}
	lock, e := RequiredModernLockTime(locks)
	if e != nil {
		return nil, e
	}
	groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
	return modernFund(feeKey, fee, inputs, outputs, lock, func(tx *bt.Tx) error {
		for i, a := range assets {
			tx.Inputs[i+offset].SequenceNumber = 0xffffffff
			if a.HasLock() {
				tx.Inputs[i+offset].SequenceNumber = 0xfffffffe
			}
		}
		if controller != nil {
			tx.Inputs[0].SequenceNumber = 0xffffffff
			if secret == nil {
				tx.Inputs[0].SequenceNumber = 0xfffffffe
			}
			unlock, e := h.unlock(key, tx, 0, secret)
			if e != nil {
				return e
			}
			tx.Inputs[0].UnlockingScript = unlock
		}
		for i, s := range spends {
			vin := i + offset
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e := modernSign(key, digest, false)
			if e != nil {
				return e
			}
			opts := util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: vin, PreTx: s.Parent, PreTxVout: s.CodeVout, AncestorTransactions: s.Ancestors, OutputGroups: groups, Signature: sig, PublicKey: key.PubKey().SerialiseCompressed()}
			if controller != nil {
				opts.ContractController = &util.TBC20StandardContractControllerWitness{Transaction: controller, CurrentInputIndex: 0}
			}
			unlock, e := assets[i].Unlock(opts)
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = unlock
		}
		return nil
	})
}

// The published rename preserves PiggyBank's native locking script.
func GetTBCTimelockCode(address string, lockTime uint32) (*bscript.Script, error) {
	return GetPiggyBankCode(address, lockTime)
}
