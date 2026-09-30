package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"math/big"
)

// TBC20Stablecoin uses the 1.7.2 generation. All amounts below are atomic units.
type TBC20Stablecoin struct {
	Name, Symbol string
	Decimal      uint8
}
type ModernTokenSpend struct {
	Parent    *bt.Tx
	CodeVout  int
	Ancestors util.TBC20StandardAncestorResolver
}
type StablecoinRecipient struct {
	Controller [21]byte
	AmountRaw  *big.Int
}
type modernAllocation struct {
	controller [21]byte
	amounts    [6]uint64
	lock       uint32
}

func modernXOnly(key *bec.PrivateKey) []byte { return key.PubKey().SerialiseCompressed()[1:] }
func modernSign(key TransactionSigner, digest []byte, xonly bool) ([]byte, error) {
	if signerMissing(key) {
		return nil, modernError("missing signer")
	}
	if xonly {
		private, ok := key.(*bec.PrivateKey)
		if !ok {
			return nil, modernError("BIP340 requires external prepared-admin signing")
		}
		k, _ := btcec.PrivKeyFromBytes(private.Serialise())
		sig, e := schnorr.Sign(k, digest, schnorr.CustomNonce([32]byte{}))
		if e != nil {
			return nil, e
		}
		return append(sig.Serialize(), 0x41), nil
	}
	sig, e := key.Sign(digest)
	if e != nil {
		return nil, e
	}
	return append(sig.Serialise(), 0x41), nil
}
func modernUtxo(parent *bt.Tx, vout int) (*bt.UTXO, error) {
	if parent == nil || vout < 0 || vout >= len(parent.Outputs) {
		return nil, modernError("parent vout out of range")
	}
	id, _ := hex.DecodeString(parent.TxID())
	o := parent.Outputs[vout]
	return &bt.UTXO{TxID: id, Vout: uint32(vout), Satoshis: o.Satoshis, LockingScript: o.LockingScript}, nil
}
func modernFund(key TransactionSigner, fee *bt.UTXO, parents []*bt.UTXO, outputs []*bt.Output, lock uint32, sign func(*bt.Tx) error) (*bt.Tx, error) {
	return modernFundMinimum(key, fee, parents, outputs, lock, 24, sign)
}
func modernFundMinimum(key TransactionSigner, fee *bt.UTXO, parents []*bt.UTXO, outputs []*bt.Output, lock uint32, minimum uint64, sign func(*bt.Tx) error) (*bt.Tx, error) {
	if e := tbc20StandardAssertP2PKHOwner(fee, key, "fee UTXO"); e != nil {
		return nil, e
	}
	tx := newFTTx()
	seen := map[string]bool{}
	for _, u := range append(append([]*bt.UTXO{}, parents...), fee) {
		if u == nil {
			return nil, modernError("missing input")
		}
		point := hex.EncodeToString(u.TxID) + ":" + new(big.Int).SetUint64(uint64(u.Vout)).String()
		if seen[point] {
			return nil, modernError("duplicate input")
		}
		seen[point] = true
		if e := tx.FromUTXOs(u); e != nil {
			return nil, e
		}
	}
	for i := range parents {
		tx.Inputs[i].SequenceNumber = 0xfffffffe
	}
	tx.LockTime = lock
	for _, o := range outputs {
		tx.AddOutput(o)
	}
	change, _ := bscript.NewFromHexString(fee.LockingScript.String())
	tx.AddOutput(&bt.Output{Satoshis: 24, LockingScript: change})
	err := finalizeSignedFeeMinimum(tx, len(tx.Outputs)-1, minimum, func() error {
		if e := sign(tx); e != nil {
			return e
		}
		return signP2PKHAtIdx(tx, key, uint32(len(parents)))
	})
	if err != nil {
		return nil, err
	}
	return tx, nil
}
func modernFundOptional(key TransactionSigner, fee *bt.UTXO, parents []*bt.UTXO, outputs []*bt.Output, lock uint32, minimum uint64, sign func(*bt.Tx) error) (*bt.Tx, error) {
	if e := tbc20StandardAssertP2PKHOwner(fee, key, "fee UTXO"); e != nil {
		return nil, e
	}
	tx := newFTTx()
	seen := map[string]bool{}
	for _, u := range append(append([]*bt.UTXO{}, parents...), fee) {
		if u == nil {
			return nil, modernError("missing input")
		}
		point := hex.EncodeToString(u.TxID) + ":" + new(big.Int).SetUint64(uint64(u.Vout)).String()
		if seen[point] {
			return nil, modernError("duplicate input")
		}
		seen[point] = true
		if e := tx.FromUTXOs(u); e != nil {
			return nil, e
		}
	}
	for i := range parents {
		tx.Inputs[i].SequenceNumber = 0xfffffffe
	}
	tx.LockTime = lock
	for _, o := range outputs {
		tx.AddOutput(o)
	}
	change, _ := bscript.NewFromHexString(fee.LockingScript.String())
	tx.AddOutput(&bt.Output{Satoshis: 24, LockingScript: change})
	err := finalizeSignedFeeOptional(tx, len(tx.Outputs)-1, minimum, func() error {
		if e := sign(tx); e != nil {
			return e
		}
		return signP2PKHAtIdx(tx, key, uint32(len(parents)))
	})
	if err != nil {
		return nil, err
	}
	return tx, nil
}
func (c TBC20Stablecoin) Create(admin, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, funding *bt.Tx) ([]*bt.Tx, error) {
	return c.createSigned(modernAdminSigner{key: admin}, feeKey, recipient, amount, fee, funding, "")
}
func (c TBC20Stablecoin) createSigned(admin modernAdminSigner, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, funding *bt.Tx, message string) ([]*bt.Tx, error) {
	if c.Decimal > 18 || !admin.valid() || fee == nil {
		return nil, modernError("invalid coin definition or administrator")
	}
	actual, e := modernUtxo(funding, int(fee.Vout))
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(actual.TxID, fee.TxID) || actual.Satoshis != fee.Satoshis || !bytes.Equal(actual.LockingScript.Bytes(), fee.LockingScript.Bytes()) {
		return nil, modernError("funding parent mismatch")
	}
	data := &CoinNftData{NftName: c.Name + " NFT", NftSymbol: c.Symbol + " NFT", Description: "The issuance certificate for the stablecoin, recording cumulative supply and issuance history.", CoinDecimal: int(c.Decimal), CoinTotalSupply: "0"}
	code, e := BuildTBC721StandardCode(hex.EncodeToString(fee.TxID), fee.Vout)
	if e != nil {
		return nil, e
	}
	hold, e := GetCoinNftHoldScriptFromHash(hex.EncodeToString(crypto.Hash160(admin.xonly())), data.NftName)
	if e != nil {
		return nil, e
	}
	tape, e := GetCoinNftTapeScript(data)
	if e != nil {
		return nil, e
	}
	source, e := modernFund(feeKey, fee, nil, BuildCoinNftOutput(code, hold, tape), 0, func(*bt.Tx) error { return nil })
	if e != nil {
		return nil, e
	}
	next, e := modernUtxo(source, 3)
	if e != nil {
		return nil, e
	}
	mint, e := c.mintSigned(admin, feeKey, recipient, amount, next, source, funding, nil, nil, message)
	if e != nil {
		return nil, e
	}
	return []*bt.Tx{source, mint}, nil
}
func (c TBC20Stablecoin) Mint(admin, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, parent, ancestor *bt.Tx, previousCode *ModernTokenCode, previousTape *ModernTokenTape) (*bt.Tx, error) {
	return c.mintSigned(modernAdminSigner{key: admin}, feeKey, recipient, amount, fee, parent, ancestor, previousCode, previousTape, "")
}
func (c TBC20Stablecoin) mintSigned(admin modernAdminSigner, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, parent, ancestor *bt.Tx, previousCode *ModernTokenCode, previousTape *ModernTokenTape, message string) (*bt.Tx, error) {
	if c.Decimal > 18 || !admin.valid() || amount == 0 || amount > 1<<63-1 || parent == nil || len(parent.Outputs) < 3 {
		return nil, modernError("invalid issuance")
	}
	if _, _, e := ParseTBC721StandardCode(parent.Outputs[0].LockingScript); e != nil {
		return nil, e
	}
	dataMap, e := DecodeCoinNftTapeScript(parent.Outputs[2].LockingScript)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(dataMap)
	if e != nil {
		return nil, e
	}
	var data CoinNftData
	if e = json.Unmarshal(raw, &data); e != nil {
		return nil, e
	}
	adminHash := crypto.Hash160(admin.xonly())
	hold, e := GetCoinNftHoldScriptFromHash(hex.EncodeToString(adminHash), data.NftName)
	if e != nil {
		return nil, e
	}
	if data.CoinDecimal != int(c.Decimal) || parent.Outputs[0].Satoshis != 200 || parent.Outputs[1].Satoshis != 100 || parent.Outputs[2].Satoshis != 0 || !bytes.Equal(hold.Bytes(), parent.Outputs[1].LockingScript.Bytes()) {
		return nil, modernError("certificate owner or layout mismatch")
	}
	supply, ok := new(big.Int).SetString(data.CoinTotalSupply, 10)
	if !ok || supply.Sign() < 0 {
		return nil, modernError("invalid cumulative supply")
	}
	data.CoinTotalSupply = supply.Add(supply, new(big.Int).SetUint64(amount)).String()
	cert, e := GetCoinNftTapeScript(&data)
	if e != nil {
		return nil, e
	}
	metadata := bscript.NewFromBytes(nil)
	for _, b := range [][]byte{{c.Decimal}, []byte(c.Name), []byte(c.Symbol)} {
		if e = metadata.AppendPushData(b); e != nil {
			return nil, e
		}
	}
	t := ModernTokenTape{Amounts: [6]uint64{amount}, TapeSize: 66 + len(metadata.Bytes()), Metadata: metadata.Bytes()}
	controller, e := TBC20StandardAddressController(recipient)
	if e != nil {
		return nil, e
	}
	code := ModernTokenCode{Kind: ModernStablecoin, AdminHash: adminHash, TapeSize: t.TapeSize, Controller: controller}
	copy(code.IssuerHash[:], crypto.Sha256(parent.Outputs[0].LockingScript.Bytes()))
	if previousCode != nil {
		if previousTape == nil {
			return nil, modernError("missing previous Tape")
		}
		code.TapeSize = previousCode.TapeSize
		old, _ := previousCode.Identity()
		next, e := code.Identity()
		if e != nil || !bytes.Equal(old, next) {
			return nil, modernError("certificate differs from coin")
		}
		t = *previousTape
		t.Amounts = [6]uint64{amount}
		t.LockTime = 0
	}
	cs, e := code.Script()
	if e != nil {
		return nil, e
	}
	ts, e := t.Script(ModernStablecoin)
	if e != nil {
		return nil, e
	}
	outputs := append(BuildCoinNftOutput(parent.Outputs[0].LockingScript, hold, cert), &bt.Output{Satoshis: 500, LockingScript: cs}, &bt.Output{Satoshis: 0, LockingScript: ts})
	if message != "" {
		script := bscript.NewFromBytes([]byte{0, 0x6a})
		if e := script.AppendPushData([]byte(message)); e != nil {
			return nil, e
		}
		outputs = append(outputs, &bt.Output{Satoshis: 0, LockingScript: script})
	}
	u0, _ := modernUtxo(parent, 0)
	u1, _ := modernUtxo(parent, 1)
	return modernFund(feeKey, fee, []*bt.UTXO{u0, u1}, outputs, 0, func(tx *bt.Tx) error {
		// Issuance certificates use final sequences in the published JS builder.
		for vin := 0; vin < 2; vin++ {
			tx.Inputs[vin].SequenceNumber = 0xffffffff
		}
		for vin := 0; vin < 2; vin++ {
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e := admin.sign(digest, true)
			if e != nil {
				return e
			}
			var unlock *bscript.Script
			if vin == 0 {
				unlock, e = BuildTBC721StandardUnlockWithSignature(sig, admin.xonly(), tx, parent, ancestor)
			} else {
				unlock, e = buildSchnorrP2PKHLikeUnlock(sig[:64], admin.xonly())
			}
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = unlock
		}
		return nil
	})
}
func modernReadSpend(s ModernTokenSpend) (*ModernTokenCode, *ModernTokenTape, error) {
	if s.Parent == nil || s.CodeVout < 0 || s.CodeVout+1 >= len(s.Parent.Outputs) {
		return nil, nil, modernError("invalid coin parent")
	}
	c, e := ParseModernTokenCode(s.Parent.Outputs[s.CodeVout].LockingScript)
	if e != nil {
		return nil, nil, e
	}
	t, e := ParseModernTokenTape(s.Parent.Outputs[s.CodeVout+1].LockingScript, c)
	return c, t, e
}
func (TBC20Stablecoin) Transfer(key, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, recipients []StablecoinRecipient) (*bt.Tx, error) {
	return (TBC20Stablecoin{}).TransferWithExtras(key, feeKey, spends, fee, recipients, nil)
}

// StablecoinTransferExtras adds the JS native-payment or OP_RETURN output.
type StablecoinTransferExtras struct {
	PaymentAddress string
	PaymentSat     uint64
	AdditionalInfo []byte // nil omits the output; a non-nil empty slice includes it.
}

func (TBC20Stablecoin) TransferWithExtras(key, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, recipients []StablecoinRecipient, extra *StablecoinTransferExtras) (*bt.Tx, error) {
	var extraOutputs []*bt.Output
	if extra != nil {
		if extra.PaymentSat > 0 {
			if extra.PaymentSat < 24 {
				return nil, modernError("attached TBC below 24 sat")
			}
			script, e := bscript.NewP2PKHFromAddress(extra.PaymentAddress)
			if e != nil {
				return nil, e
			}
			extraOutputs = append(extraOutputs, &bt.Output{Satoshis: extra.PaymentSat, LockingScript: script})
		}
		if extra.AdditionalInfo != nil {
			script := bscript.NewFromBytes([]byte{0, 0x6a})
			if e := script.AppendPushData(extra.AdditionalInfo); e != nil {
				return nil, e
			}
			extraOutputs = append(extraOutputs, &bt.Output{Satoshis: 0, LockingScript: script})
		}
	}

	if len(spends) < 1 || len(spends) > 5 || len(recipients) < 1 {
		return nil, modernError("invalid input/output count")
	}
	remaining := []*big.Int{}
	total := new(big.Int)
	for _, s := range spends {
		_, t, e := modernReadSpend(s)
		if e != nil {
			return nil, e
		}
		remaining = append(remaining, t.Balance())
		total.Add(total, t.Balance())
	}
	wanted := new(big.Int)
	for _, r := range recipients {
		if r.AmountRaw == nil || r.AmountRaw.Sign() <= 0 {
			return nil, modernError("recipient amount must be positive")
		}
		wanted.Add(wanted, r.AmountRaw)
	}
	if wanted.Cmp(total) > 0 {
		return nil, modernError("insufficient balance")
	}
	targets := append([]StablecoinRecipient{}, recipients...)
	if wanted.Cmp(total) < 0 {
		address, e := tbc20StandardKeyAddress(key)
		if e != nil {
			return nil, e
		}
		controller, e := TBC20StandardAddressController(address.AddressString)
		if e != nil {
			return nil, e
		}
		targets = append(targets, StablecoinRecipient{controller, new(big.Int).Sub(total, wanted)})
	}
	allocations := []modernAllocation{}
	for _, r := range targets {
		need := new(big.Int).Set(r.AmountRaw)
		a := modernAllocation{controller: r.Controller}
		for i, b := range remaining {
			n := new(big.Int).Set(need)
			if b.Cmp(n) < 0 {
				n.Set(b)
			}
			if !n.IsUint64() || n.Uint64() > 1<<63-1 {
				return nil, modernError("slot overflow")
			}
			a.amounts[i] = n.Uint64()
			b.Sub(b, n)
			need.Sub(need, n)
		}
		allocations = append(allocations, a)
	}
	return modernStableBuild(key, feeKey, spends, fee, allocations, false, extraOutputs...)
}
func (c TBC20Stablecoin) SetLockTime(admin, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, lock uint32) (*bt.Tx, error) {
	return c.setLockTimeSigned(modernAdminSigner{key: admin}, feeKey, spends, fee, lock)
}
func (c TBC20Stablecoin) setLockTimeSigned(admin modernAdminSigner, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, lock uint32) (*bt.Tx, error) {
	if len(spends) < 1 || len(spends) > 5 {
		return nil, modernError("invalid coin count")
	}
	allocations := []modernAllocation{}
	for vin, s := range spends {
		c, t, e := modernReadSpend(s)
		if e != nil {
			return nil, e
		}
		b := t.Balance()
		if !b.IsUint64() || b.Uint64() > 1<<63-1 {
			return nil, modernError("balance overflow")
		}
		index := -1
		for i, a := range allocations {
			if a.controller == c.Controller {
				index = i
				break
			}
		}
		if index < 0 {
			allocations = append(allocations, modernAllocation{controller: c.Controller, lock: lock})
			index = len(allocations) - 1
		}
		allocations[index].amounts[vin] = b.Uint64()
	}
	return modernStableBuildSigner(admin, feeKey, spends, fee, allocations, true)
}
func modernStableBuild(key, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, allocations []modernAllocation, admin bool, extras ...*bt.Output) (*bt.Tx, error) {
	return modernStableBuildSigner(modernAdminSigner{key: key}, feeKey, spends, fee, allocations, admin, extras...)
}
func modernStableBuildSigner(key modernAdminSigner, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, allocations []modernAllocation, admin bool, extras ...*bt.Output) (*bt.Tx, error) {
	if !key.valid() || len(spends) < 1 || len(spends) > 5 || len(allocations) < 1 || len(allocations)+len(extras) > 7 {
		return nil, modernError("invalid builder arguments")
	}
	codes := []*ModernTokenCode{}
	tapes := []*ModernTokenTape{}
	locks := []uint32{}
	xonly := []bool{}
	inputs := []*bt.UTXO{}
	var identity []byte
	for _, s := range spends {
		c, t, e := modernReadSpend(s)
		if e != nil {
			return nil, e
		}
		u, e := modernUtxo(s.Parent, s.CodeVout)
		if e != nil {
			return nil, e
		}
		if c.Kind != ModernStablecoin || u.Satoshis != 500 || s.Parent.Outputs[s.CodeVout+1].Satoshis != 0 || t.Balance().Sign() == 0 {
			return nil, modernError("invalid Stablecoin input")
		}
		isAdmin := bytes.Equal(c.AdminHash, crypto.Hash160(key.xonly())) || bytes.Equal(c.AdminHash, crypto.Hash160(key.compressed()))
		if admin && !isAdmin {
			return nil, modernError("wrong administrator")
		}
		xonly = append(xonly, admin || bytes.Equal(c.AdminHash, crypto.Hash160(key.xonly())) || bytes.Equal(c.Controller[:20], crypto.Hash160(key.xonly())))
		lock := t.LockTime
		if isAdmin {
			lock = 0
		}
		locks = append(locks, lock)
		id, e := c.Identity()
		if e != nil {
			return nil, e
		}
		if identity == nil {
			identity = id
		} else if !bytes.Equal(identity, id) {
			return nil, modernError("mixed coin identities")
		}
		codes = append(codes, c)
		tapes = append(tapes, t)
		inputs = append(inputs, u)
	}
	lock, e := RequiredModernLockTime(locks)
	if admin {
		lock = 0
		e = nil
	}
	if e != nil {
		return nil, e
	}
	outputs := []*bt.Output{}
	groups := []util.TBC20StandardOutputGroup{}
	for _, a := range allocations {
		t := *tapes[0]
		if admin {
			for i, c := range codes {
				if c.Controller == a.controller {
					t = *tapes[i]
					break
				}
			}
		}
		t.Amounts = a.amounts
		t.LockTime = a.lock
		code := *codes[0]
		code.Controller = a.controller
		cs, e := code.Script()
		if e != nil {
			return nil, e
		}
		ts, e := t.Script(ModernStablecoin)
		if e != nil {
			return nil, e
		}
		vout := len(outputs) + 1
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs), TapeVout: &vout})
		outputs = append(outputs, &bt.Output{Satoshis: 500, LockingScript: cs}, &bt.Output{Satoshis: 0, LockingScript: ts})
	}
	for _, output := range extras {
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
		outputs = append(outputs, output)
	}
	groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
	return modernFund(feeKey, fee, inputs, outputs, lock, func(tx *bt.Tx) error {
		for vin, s := range spends {
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e := key.sign(digest, xonly[vin])
			if e != nil {
				return e
			}
			pub := key.compressed()
			if xonly[vin] {
				pub = key.xonly()
			}
			unlock, e := BuildTBC20StablecoinUnlockWithSignature(util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: vin, PreTx: s.Parent, PreTxVout: s.CodeVout, OutputGroups: groups, AncestorTransactions: s.Ancestors, Signature: sig, PublicKey: pub})
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = unlock
		}
		return nil
	})
}
