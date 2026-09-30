package contract

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
	"math/big"
	"strings"
)

//go:embed asm/tbc_lop_sell.hex
var lopSellTemplate string

//go:embed asm/tbc_lop_buy.hex
var lopBuyTemplate string

//go:embed asm/tbc_lop_token_sell.hex
var lopTokenSellTemplate string

//go:embed asm/tbc_lop_token_buy.hex
var lopTokenBuyTemplate string

type LOPSide uint8

const (
	LOPBuy LOPSide = iota
	LOPSell
)

type LOPToken struct {
	ID    [32]byte
	Asset *TokenAsset
}

// TBCLOP prices and fee rates have denominator 1,000,000; volumes are raw integers.
type TBCLOP struct {
	Side                   LOPSide
	Owner, TaxAddress      string
	Volume, Price, FeeRate uint64
	TokenA                 LOPToken
	TokenB                 *LOPToken
}
type LOPSpend struct {
	Order  TBCLOP
	Parent *bt.Tx
	Vout   int
	Token  *ModernTokenSpend
}

func lopMul(a, b uint64) (uint64, error) {
	n := new(big.Int).Mul(new(big.Int).SetUint64(a), new(big.Int).SetUint64(b))
	n.Quo(n, big.NewInt(1000000))
	return lopBound(n)
}
func lopBound(n *big.Int) (uint64, error) {
	if n.Sign() < 0 || n.BitLen() > 63 {
		return 0, modernError("LOP signed 63-bit amount overflow")
	}
	return n.Uint64(), nil
}
func lopLE(n uint64, width int) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, n)
	return b[:width]
}
func lopPush(buf *[]byte, b []byte) {
	n := len(b)
	if n < 76 {
		*buf = append(*buf, byte(n))
	} else if n < 256 {
		*buf = append(*buf, 0x4c, byte(n))
	} else {
		*buf = append(*buf, 0x4d, byte(n), byte(n>>8))
	}
	*buf = append(*buf, b...)
}
func lopController(s *bscript.Script) [21]byte {
	var c [21]byte
	copy(c[:20], crypto.Hash160(crypto.Sha256(s.Bytes())))
	c[20] = 1
	return c
}
func lopPartial(a *TokenAsset) []byte {
	b := a.Code.Bytes()
	h, _ := hex.DecodeString(partialsha256.CalculatePartialHash(b[:len(b)/64*64]))
	return h
}
func (o TBCLOP) Script() (*bscript.Script, error) {
	if o.Volume == 0 || o.Volume > uint64(1<<63-1) || o.Price == 0 || o.Price > uint64(1<<63-1) || o.FeeRate >= 1000000 || o.Side > LOPSell || o.TokenA.Asset == nil {
		return nil, modernError("invalid LOP definition")
	}
	if n := len(o.TokenA.Asset.Code.Bytes()); n != 2657 && n != 2981 {
		return nil, modernError("LOP requires Standard or Stablecoin")
	}
	if o.TokenB != nil && o.TokenB.Asset != nil {
		if n := len(o.TokenB.Asset.Code.Bytes()); n != 2657 && n != 2981 {
			return nil, modernError("LOP requires Standard or Stablecoin")
		}
	}
	if _, e := ParseTokenAsset(o.TokenA.Asset.Code, o.TokenA.Asset.Tape); e != nil {
		return nil, e
	}
	if o.TokenB != nil {
		if o.TokenB.Asset == nil {
			return nil, modernError("missing pair asset")
		}
		if _, e := ParseTokenAsset(o.TokenB.Asset.Code, o.TokenB.Asset.Tape); e != nil {
			return nil, e
		}
		if bytes.Equal(o.TokenA.Asset.Identity, o.TokenB.Asset.Identity) {
			return nil, modernError("pair identities must differ")
		}
	}
	owner, e := TBC20StandardAddressController(o.Owner)
	if e != nil {
		return nil, e
	}
	tax, e := TBC20StandardAddressController(o.TaxAddress)
	if e != nil {
		return nil, e
	}
	template := lopBuyTemplate
	if o.Side == LOPSell {
		template = lopSellTemplate
	}
	if o.TokenB != nil {
		template = lopTokenBuyTemplate
		if o.Side == LOPSell {
			template = lopTokenSellTemplate
		}
	}
	values := map[string]string{"address": "14" + hex.EncodeToString(owner[:20]), "taxAddressHex": "14" + hex.EncodeToString(tax[:20]), "ftCodeSize": hex.EncodeToString(lopLE(uint64(len(o.TokenA.Asset.Code.Bytes())), 2)), "tapeMarker": "3054415045", "a": "3054415045", "b": "3054415045", "buyCodeSize": "023405", "sellCodeSize": "023405"}
	for k, v := range values {
		template = strings.ReplaceAll(template, "{"+k+"}", v)
	}
	buf, e := hex.DecodeString(strings.TrimSpace(template))
	if e != nil {
		return nil, e
	}
	lopPush(&buf, owner[:20])
	lopPush(&buf, lopLE(o.Volume, 8))
	lopPush(&buf, lopPartial(o.TokenA.Asset))
	if o.TokenB != nil {
		lopPush(&buf, lopPartial(o.TokenB.Asset))
	}
	lopPush(&buf, lopLE(o.FeeRate, 8))
	lopPush(&buf, lopLE(o.Price, 8))
	lopPush(&buf, o.TokenA.ID[:])
	if o.TokenB != nil {
		lopPush(&buf, o.TokenB.ID[:])
	}
	return bscript.NewFromBytes(buf), nil
}
func (o TBCLOP) locked() *LOPToken {
	if o.Side == LOPBuy {
		if o.TokenB != nil {
			return o.TokenB
		}
		return &o.TokenA
	}
	if o.TokenB != nil {
		return &o.TokenA
	}
	return nil
}

type lopInput struct {
	order         *LOPSpend
	match         bool
	token         *ModernTokenSpend
	asset         *TokenAsset
	controller    *bt.Tx
	controllerVin int
}

func lopPair(outputs *[]*bt.Output, a *TokenAsset, owner [21]byte, amounts [6]uint64) error {
	code, e := a.ReplaceController(owner)
	if e != nil {
		return e
	}
	tape, e := a.ReplaceAmounts(amounts)
	if e != nil {
		return e
	}
	*outputs = append(*outputs, &bt.Output{Satoshis: 500, LockingScript: code}, &bt.Output{LockingScript: tape})
	return nil
}
func (o TBCLOP) Make(key, feeKey TransactionSigner, spends []ModernTokenSpend, fee *bt.UTXO) (*bt.Tx, error) {
	address, e := privKeyToAddress(key)
	if e != nil {
		return nil, e
	}
	if address != o.Owner {
		return nil, modernError("LOP owner mismatch")
	}
	owner, e := TBC20StandardAddressController(o.Owner)
	if e != nil {
		return nil, e
	}
	script, e := o.Script()
	if e != nil {
		return nil, e
	}
	value := o.Volume
	if o.locked() != nil {
		value = 300
	}
	outputs := []*bt.Output{{Satoshis: value, LockingScript: script}}
	inputs := []lopInput{}
	if token := o.locked(); token != nil {
		if len(spends) < 1 || len(spends) > 5 {
			return nil, modernError("LOP requires 1..5 token inputs")
		}
		amount := o.Volume
		if o.Side == LOPBuy {
			amount, e = lopMul(o.Volume, o.Price)
			if e != nil {
				return nil, e
			}
		}
		if amount == 0 {
			return nil, modernError("zero token order")
		}
		remaining := new(big.Int).SetUint64(amount)
		total := new(big.Int)
		locked, change := [6]uint64{}, [6]uint64{}
		for i := range spends {
			s := &spends[i]
			a, e := ReadTokenAsset(*s)
			if e != nil {
				return nil, e
			}
			if a.Controller != owner || !bytes.Equal(a.Identity, token.Asset.Identity) {
				return nil, modernError("LOP token identity/owner mismatch")
			}
			n := new(big.Int).Set(remaining)
			if n.Cmp(a.Balance) > 0 {
				n.Set(a.Balance)
			}
			locked[i], e = lopBound(n)
			if e != nil {
				return nil, e
			}
			change[i], e = lopBound(new(big.Int).Sub(a.Balance, n))
			if e != nil {
				return nil, e
			}
			remaining.Sub(remaining, n)
			total.Add(total, a.Balance)
			inputs = append(inputs, lopInput{token: s, asset: a})
		}
		if remaining.Sign() != 0 {
			return nil, modernError("insufficient token balance")
		}
		if e = lopPair(&outputs, token.Asset, lopController(script), locked); e != nil {
			return nil, e
		}
		if total.Cmp(new(big.Int).SetUint64(amount)) > 0 {
			if e = lopPair(&outputs, token.Asset, owner, change); e != nil {
				return nil, e
			}
		}
	} else if len(spends) > 0 {
		return nil, modernError("native sell does not take tokens")
	}
	change := len(outputs)
	outputs = append(outputs, &bt.Output{LockingScript: fee.LockingScript})
	return lopFund(key, feeKey, fee, inputs, outputs, change)
}
func (s LOPSpend) validate() error {
	script, e := s.Order.Script()
	if e != nil {
		return e
	}
	u, e := modernUtxo(s.Parent, s.Vout)
	if e != nil {
		return e
	}
	value := s.Order.Volume
	if s.Order.locked() != nil {
		value = 300
	}
	if value != u.Satoshis || !bytes.Equal(script.Bytes(), u.LockingScript.Bytes()) {
		return modernError("LOP parent mismatch")
	}
	if t := s.Order.locked(); t != nil {
		if s.Token == nil || s.Token.Parent == nil || s.Token.Parent.TxID() != s.Parent.TxID() || s.Token.CodeVout != s.Vout+1 {
			return modernError("LOP companion token mismatch")
		}
		a, e := ReadTokenAsset(*s.Token)
		if e != nil {
			return e
		}
		if !bytes.Equal(a.Identity, t.Asset.Identity) || a.Controller != lopController(script) {
			return modernError("LOP controller mismatch")
		}
	} else if s.Token != nil {
		return modernError("unexpected locked token")
	}
	return nil
}
func (s LOPSpend) Cancel(key, feeKey TransactionSigner, fee *bt.UTXO) (*bt.Tx, error) {
	if e := s.validate(); e != nil {
		return nil, e
	}
	address, e := privKeyToAddress(key)
	if e != nil {
		return nil, e
	}
	if address != s.Order.Owner {
		return nil, modernError("LOP cancel owner mismatch")
	}
	owner, e := TBC20StandardAddressController(address)
	if e != nil {
		return nil, e
	}
	inputs := []lopInput{{order: &s}}
	outputs := []*bt.Output{}
	if s.Token != nil {
		a, e := ReadTokenAsset(*s.Token)
		if e != nil {
			return nil, e
		}
		amount, e := lopBound(a.Balance)
		if e != nil {
			return nil, e
		}
		if e = lopPair(&outputs, a, owner, [6]uint64{0, amount}); e != nil {
			return nil, e
		}
		inputs = append(inputs, lopInput{token: s.Token, asset: a, controller: s.Parent})
	} else {
		script, e := bscript.NewP2PKHFromAddress(address)
		if e != nil {
			return nil, e
		}
		outputs = append(outputs, &bt.Output{Satoshis: s.Order.Volume, LockingScript: script})
	}
	change := len(outputs)
	outputs = append(outputs, &bt.Output{LockingScript: fee.LockingScript})
	return lopFund(key, feeKey, fee, inputs, outputs, change)
}
func MatchLOPOrders(key, feeKey TransactionSigner, buy, sell LOPSpend, fee *bt.UTXO) (*bt.Tx, error) {
	if e := buy.validate(); e != nil {
		return nil, e
	}
	if e := sell.validate(); e != nil {
		return nil, e
	}
	b, s := buy.Order, sell.Order
	if b.Side != LOPBuy || s.Side != LOPSell || b.Price != s.Price || b.TokenA.ID != s.TokenA.ID || !bytes.Equal(b.TokenA.Asset.Identity, s.TokenA.Asset.Identity) || (b.TokenB == nil) != (s.TokenB == nil) {
		return nil, modernError("LOP pair/price/direction mismatch")
	}
	if b.TokenB != nil && (b.TokenB.ID != s.TokenB.ID || !bytes.Equal(b.TokenB.Asset.Identity, s.TokenB.Asset.Identity)) {
		return nil, modernError("LOP pair identity mismatch")
	}
	feeAddress, err := privKeyToAddress(feeKey)
	if err != nil {
		return nil, err
	}
	if b.Owner == s.Owner || feeAddress == b.Owner || feeAddress == s.Owner || b.TaxAddress == b.Owner || s.TaxAddress == s.Owner {
		return nil, modernError("LOP distinct-address covenant")
	}
	n := b.Volume
	if s.Volume < n {
		n = s.Volume
	}
	taxA, e := lopMul(n, b.FeeRate)
	if e != nil {
		return nil, e
	}
	receiveA := n - taxA
	payB, e := lopMul(n, s.Price)
	if e != nil {
		return nil, e
	}
	taxB, e := lopMul(payB, s.FeeRate)
	if e != nil {
		return nil, e
	}
	receiveB := payB - taxB
	if receiveA == 0 || receiveB == 0 {
		return nil, modernError("match rounds to zero")
	}
	ba, e := ReadTokenAsset(*buy.Token)
	if e != nil {
		return nil, e
	}
	if ba.Balance.Cmp(new(big.Int).SetUint64(payB)) < 0 {
		return nil, modernError("buy balance insufficient")
	}
	inputs := []lopInput{{order: &buy, match: true}, {token: buy.Token, asset: ba, controller: buy.Parent}, {order: &sell, match: true}}
	outputs := []*bt.Output{}
	pair := func(a *TokenAsset, address string, amount uint64, vin int) error {
		c, e := TBC20StandardAddressController(address)
		if e != nil {
			return e
		}
		var slots [6]uint64
		slots[vin] = amount
		return lopPair(&outputs, a, c, slots)
	}
	if e = pair(ba, s.Owner, receiveB, 1); e != nil {
		return nil, e
	}
	if e = pair(ba, s.TaxAddress, taxB, 1); e != nil {
		return nil, e
	}
	var sa *TokenAsset
	if b.TokenB != nil {
		sa, e = ReadTokenAsset(*sell.Token)
		if e != nil {
			return nil, e
		}
		if sa.Balance.Cmp(new(big.Int).SetUint64(n)) < 0 {
			return nil, modernError("sell balance insufficient")
		}
		if e = pair(sa, b.Owner, receiveA, 3); e != nil {
			return nil, e
		}
		if e = pair(sa, b.TaxAddress, taxA, 3); e != nil {
			return nil, e
		}
		inputs = append(inputs, lopInput{token: sell.Token, asset: sa, controller: sell.Parent, controllerVin: 2})
	} else {
		if receiveA < 24 || taxA < 10 && b.FeeRate != 0 {
			return nil, modernError("native payout below dust")
		}
		owner, e := bscript.NewP2PKHFromAddress(b.Owner)
		if e != nil {
			return nil, e
		}
		tax, e := bscript.NewP2PKHFromAddress(b.TaxAddress)
		if e != nil {
			return nil, e
		}
		if taxA == 0 {
			tax = bscript.NewFromBytes(append([]byte{0, 0x6a, 22}, bytes.Repeat([]byte{0xff}, 22)...))
		}
		outputs = append(outputs, &bt.Output{Satoshis: receiveA, LockingScript: owner}, &bt.Output{Satoshis: taxA, LockingScript: tax})
	}
	change := len(outputs)
	outputs = append(outputs, &bt.Output{LockingScript: fee.LockingScript})
	if s.Volume > n {
		next := s
		next.Volume -= n
		script, e := next.Script()
		if e != nil {
			return nil, e
		}
		value := next.Volume
		if sa != nil {
			value = 300
		}
		outputs = append(outputs, &bt.Output{Satoshis: value, LockingScript: script})
		if sa != nil {
			amount, e := lopBound(new(big.Int).Sub(sa.Balance, new(big.Int).SetUint64(n)))
			if e != nil {
				return nil, e
			}
			if e = lopPair(&outputs, sa, lopController(script), [6]uint64{0, 0, 0, amount}); e != nil {
				return nil, e
			}
		}
	} else if b.Volume > n {
		next := b
		next.Volume -= n
		script, e := next.Script()
		if e != nil {
			return nil, e
		}
		amount, e := lopBound(new(big.Int).Sub(ba.Balance, new(big.Int).SetUint64(payB)))
		if e != nil {
			return nil, e
		}
		if amount == 0 {
			return nil, modernError("remaining buy order has zero balance")
		}
		outputs = append(outputs, &bt.Output{Satoshis: 300, LockingScript: script})
		if e = lopPair(&outputs, ba, lopController(script), [6]uint64{0, amount}); e != nil {
			return nil, e
		}
	}
	if b.Volume == n && ba.Balance.Cmp(new(big.Int).SetUint64(payB)) != 0 || s.Volume == n && sa != nil && sa.Balance.Cmp(new(big.Int).SetUint64(n)) != 0 {
		return nil, modernError("fully filled order contains surplus tokens")
	}
	return lopFund(key, feeKey, fee, inputs, outputs, change)
}
func lopFund(key, feeKey TransactionSigner, fee *bt.UTXO, inputs []lopInput, outputs []*bt.Output, change int) (*bt.Tx, error) {
	if signerMissing(key) || len(inputs)+1 > 6 {
		return nil, modernError("invalid LOP signer/input count")
	}
	if e := tbc20StandardAssertP2PKHOwner(fee, feeKey, "fee"); e != nil {
		return nil, e
	}
	tx := newFTTx()
	seen := map[string]bool{}
	locks := []uint32{}
	for _, item := range inputs {
		var u *bt.UTXO
		var e error
		if item.order != nil {
			u, e = modernUtxo(item.order.Parent, item.order.Vout)
		} else {
			u, e = modernUtxo(item.token.Parent, item.token.CodeVout)
			locks = append(locks, item.asset.LockTime)
		}
		if e != nil {
			return nil, e
		}
		point := fmt.Sprintf("%x:%d", u.TxID, u.Vout)
		if seen[point] {
			return nil, modernError("duplicate LOP input")
		}
		seen[point] = true
		if e = tx.FromUTXOs(u); e != nil {
			return nil, e
		}
	}
	if seen[fmt.Sprintf("%x:%d", fee.TxID, fee.Vout)] {
		return nil, modernError("duplicate fee")
	}
	if e := tx.FromUTXOs(fee); e != nil {
		return nil, e
	}
	lock, e := RequiredModernLockTime(locks)
	if e != nil {
		return nil, e
	}
	tx.LockTime = lock
	tx.Outputs = outputs
	groups := []util.TBC20StandardOutputGroup{}
	for i := 0; i < len(outputs); i++ {
		g := util.TBC20StandardOutputGroup{CodeVout: i}
		if i+1 < len(outputs) {
			if _, e := ParseTokenAsset(outputs[i].LockingScript, outputs[i+1].LockingScript); e == nil {
				v := i + 1
				g.TapeVout = &v
				i++
			}
		}
		groups = append(groups, g)
	}
	e = finalizeSignedFeeMinimum(tx, change, 24, func() error {
		for i, item := range inputs {
			tx.Inputs[i].SequenceNumber = 0xffffffff
			if item.asset != nil && item.asset.HasLock() {
				tx.Inputs[i].SequenceNumber = 0xfffffffe
			}
		}
		for vin, item := range inputs {
			var script *bscript.Script
			var e error
			if item.order != nil && item.match {
				script, e = BuildLOPOrderUnlock(tx, item.order.Parent, item.order.Vout, item.order.Order.TokenB != nil)
			} else {
				digest, err := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
				if err != nil {
					return err
				}
				sig, err := modernSign(key, digest, false)
				if err != nil {
					return err
				}
				pub := key.PubKey().SerialiseCompressed()
				if item.order != nil {
					buf := []byte{}
					lopPush(&buf, sig)
					lopPush(&buf, pub)
					buf = append(buf, 0x52)
					script = bscript.NewFromBytes(buf)
				} else {
					opts := util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: vin, PreTx: item.token.Parent, PreTxVout: item.token.CodeVout, AncestorTransactions: item.token.Ancestors, OutputGroups: groups, Signature: sig, PublicKey: pub}
					if item.controller != nil {
						opts.ContractController = &util.TBC20StandardContractControllerWitness{Transaction: item.controller, CurrentInputIndex: item.controllerVin}
					}
					script, e = item.asset.Unlock(opts)
				}
			}
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = script
		}
		return signP2PKHAtIdx(tx, feeKey, uint32(len(inputs)))
	})
	return tx, e
}
func lopOffset(n int) int {
	switch n {
	case 946:
		return 832
	case 1074:
		return 960
	case 1332:
		return 1152
	}
	return n / 64 * 64
}
func lopRecord(buf *[]byte, o *bt.Output, offset int) {
	b := o.LockingScript.Bytes()
	lopPush(buf, lopLE(o.Satoshis, 8))
	lopPush(buf, b[offset:])
	var h []byte
	if offset > 0 {
		h, _ = hex.DecodeString(partialsha256.CalculatePartialHash(b[:offset]))
	}
	lopPush(buf, h)
	width := 1
	if len(b) >= 256 {
		width = 2
	}
	lopPush(buf, lopLE(uint64(len(b)), width))
}
func BuildLOPOrderUnlock(tx, parent *bt.Tx, vout int, pair bool) (*bscript.Script, error) {
	slots := 10
	if pair {
		slots = 12
	}
	if tx == nil || parent == nil || tx.Version != 10 || parent.Version != 10 || len(parent.Inputs) > 10 || len(parent.Outputs) > slots || len(tx.Outputs) > slots || vout < 0 || vout >= len(parent.Outputs) {
		return nil, modernError("LOP witness dimensions")
	}
	buf := []byte{}
	for i := 0; i < len(tx.Outputs); i++ {
		o := tx.Outputs[i]
		lopRecord(&buf, o, lopOffset(len(o.LockingScript.Bytes())))
		if i+1 < len(tx.Outputs) {
			if _, e := ParseTokenAsset(o.LockingScript, tx.Outputs[i+1].LockingScript); e == nil {
				i++
				lopPush(&buf, lopLE(tx.Outputs[i].Satoshis, 8))
				lopPush(&buf, tx.Outputs[i].LockingScript.Bytes())
			}
		}
	}
	padding := (slots-len(tx.Outputs))*4 - 2
	if padding > 0 {
		buf = append(buf, make([]byte, padding)...)
	}
	header := []byte{}
	for _, v := range []uint64{10, uint64(parent.LockTime), uint64(len(parent.Inputs)), uint64(len(parent.Outputs))} {
		header = append(header, lopLE(v, 4)...)
	}
	lopPush(&buf, header)
	hashes := []byte{}
	for _, in := range parent.Inputs {
		b := []byte{}
		id := in.PreviousTxID()
		for i := len(id) - 1; i >= 0; i-- {
			b = append(b, id[i])
		}
		b = append(b, lopLE(uint64(in.PreviousTxOutIndex), 4)...)
		b = append(b, lopLE(uint64(in.SequenceNumber), 4)...)
		lopPush(&buf, b)
		hashes = append(hashes, crypto.Sha256(in.UnlockingScript.Bytes())...)
	}
	buf = append(buf, make([]byte, 10-len(parent.Inputs))...)
	lopPush(&buf, crypto.Sha256(hashes))
	for i, o := range parent.Outputs {
		offset := len(o.LockingScript.Bytes()) / 64 * 64
		if i == vout {
			offset = lopOffset(len(o.LockingScript.Bytes()))
		}
		lopRecord(&buf, o, offset)
	}
	buf = append(buf, make([]byte, 4*(slots-len(parent.Outputs)))...)
	buf = append(buf, 0x51)
	return bscript.NewFromBytes(buf), nil
}
