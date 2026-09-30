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
	"reflect"
	"sort"
)

type TBCAMM struct {
	genesis     *bt.Tx
	plan        uint8
	controllers [][20]byte
	lpLocked    bool
}
type TBCAMMPoolSpend struct{ Parent, Ancestor *bt.Tx }
type TBCAMMOperation struct {
	Pool                    TBCAMMPoolSpend
	PoolFT                  ModernTokenSpend
	User                    *ModernTokenSpend
	Key, FeeKey, Controller TransactionSigner
	Fee                     *bt.UTXO
	FeeParent               *bt.Tx
	Recipient               string
	// 1 AddLP, 2 RemoveLP, 3 SwapFT, 4 SwapTBC. Values are atomic units.
	Option          int
	Amount, Minimum uint64
	UseTBC          bool
	FirstFT         *uint64
	LPLockTime      uint32
}
type TBCAMMBuildResult struct {
	Transaction *bt.Tx
	Quote       *TBCAMMQuote
	Layout      TBCAMMOutputLayout
}

func NewTBCAMM(genesis *bt.Tx, plan uint8, controllers [][20]byte, lpLocked bool) (*TBCAMM, error) {
	if _, _, _, e := TBCAMMFeePolicy(plan); e != nil {
		return nil, e
	}
	if len(controllers) > 0 {
		if _, e := ammMembership(controllers); e != nil {
			return nil, e
		}
	}
	if genesis == nil || len(genesis.Inputs) != 1 || len(genesis.Outputs) < 2 || len(genesis.Outputs) > 3 || genesis.Outputs[0].Satoshis != 500 || genesis.Outputs[1].Satoshis != 0 {
		return nil, modernError("expected canonical Standard genesis")
	}
	t, e := ParseTBC20StandardTape(genesis.Outputs[1].LockingScript)
	if e != nil {
		return nil, e
	}
	if t.Amounts[0] == 0 || lpLocked && t.Size < 66 {
		return nil, modernError("invalid genesis supply or LP Tape size")
	}
	for _, n := range t.Amounts[1:] {
		if n != 0 {
			return nil, modernError("invalid genesis allocation")
		}
	}
	owner, e := util.GetTBC20StandardController(genesis.Outputs[0].LockingScript)
	if e != nil {
		return nil, e
	}
	in := genesis.Inputs[0]
	code, e := InstantiateTBC20StandardCode(TBC20StandardOutpoint{TxID: hex.EncodeToString(in.PreviousTxID()), OutputIndex: in.PreviousTxOutIndex}, owner, t.Size)
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(code.Bytes(), genesis.Outputs[0].LockingScript.Bytes()) {
		return nil, modernError("reference is not FT genesis")
	}
	copyTx, e := bt.NewTxFromString(genesis.String())
	if e != nil {
		return nil, e
	}
	hashes := append([][20]byte{}, controllers...)
	sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
	return &TBCAMM{copyTx, plan, hashes, lpLocked}, nil
}
func (a *TBCAMM) tapeSize() int { return len(a.genesis.Outputs[1].LockingScript.Bytes()) }
func (a *TBCAMM) feeScript() (*bscript.Script, error) {
	_, _, address, e := TBCAMMFeePolicy(a.plan)
	if e != nil {
		return nil, e
	}
	return bscript.NewP2PKHFromAddress(address)
}
func (a *TBCAMM) lpCode(hash []byte, owner [21]byte) *ModernTokenCode {
	kind := ModernLP
	if a.lpLocked {
		kind = ModernTimelockedLP
	}
	c := &ModernTokenCode{Kind: kind, TapeSize: a.tapeSize(), Controller: owner}
	copy(c.IssuerHash[:], hash)
	return c
}
func (a *TBCAMM) lpTape(amounts [6]uint64, lock uint32) (*bscript.Script, error) {
	kind := ModernLP
	if a.lpLocked {
		kind = ModernTimelockedLP
	}
	return (ModernTokenTape{Amounts: amounts, LockTime: lock, TapeSize: a.tapeSize()}).Script(kind)
}
func (a *TBCAMM) ReadState(parent *bt.Tx) (*TBCAMMCode, *TBCAMMTape, TBCAMMMathState, error) {
	var empty TBCAMMMathState
	if parent == nil || len(parent.Outputs) < 2 || parent.Outputs[1].Satoshis != 0 {
		return nil, nil, empty, modernError("missing Pool Code/Tape")
	}
	c, e := ParseTBCAMMCode(parent.Outputs[0].LockingScript)
	if e != nil {
		return nil, nil, empty, e
	}
	t, e := ParseTBCAMMTape(parent.Outputs[1].LockingScript)
	if e != nil {
		return nil, nil, empty, e
	}
	fee, e := a.feeScript()
	if e != nil {
		return nil, nil, empty, e
	}
	rate, _, _, _ := TBCAMMFeePolicy(a.plan)
	if !reflect.DeepEqual(c.Controllers, a.controllers) && !(len(c.Controllers) == 0 && len(a.controllers) == 0) || c.TapeSize != a.tapeSize() || !bytes.Equal(c.FeeScriptHash[:], crypto.Sha256(fee.Bytes())) || t.FTGenesis != a.genesis.TxID() || t.LPPlan != a.plan || t.FeeRate != rate || t.Controlled != (len(a.controllers) > 0) || t.LPLocked != a.lpLocked {
		return nil, nil, empty, modernError("Pool configuration differs from trusted definition")
	}
	fi, e := util.GetTBC20StandardCodeIdentity(a.genesis.Outputs[0].LockingScript)
	if e != nil {
		return nil, nil, empty, e
	}
	lp := a.lpCode(crypto.Sha256(parent.Outputs[0].LockingScript.Bytes()), [21]byte{})
	li, e := lp.Identity()
	if e != nil {
		return nil, nil, empty, e
	}
	lc, _ := lp.Script()
	if !bytes.Equal(t.FTHash[:], fi[:32]) || int(t.FTSize) != len(a.genesis.Outputs[0].LockingScript.Bytes()) || !bytes.Equal(t.LPHash[:], li[:32]) || int(t.LPSize) != len(lc.Bytes()) {
		return nil, nil, empty, modernError("Pool token identity mismatch")
	}
	state, e := t.State(parent.Outputs[0].Satoshis)
	return c, t, state, e
}
func (a *TBCAMM) validateFT(s ModernTokenSpend, key TransactionSigner, pool *bt.Tx) (*bscript.Script, *bscript.Script, uint64, error) {
	u, e := modernUtxo(s.Parent, s.CodeVout)
	if e != nil {
		return nil, nil, 0, e
	}
	if s.CodeVout+1 >= len(s.Parent.Outputs) || u.Satoshis != 500 || s.Parent.Outputs[s.CodeVout+1].Satoshis != 0 {
		return nil, nil, 0, modernError("invalid FT pair")
	}
	code := u.LockingScript
	tape := s.Parent.Outputs[s.CodeVout+1].LockingScript
	if e = ValidateTBC20StandardCode(code, a.tapeSize()); e != nil {
		return nil, nil, 0, e
	}
	t, e := ParseTBC20StandardTape(tape)
	if e != nil {
		return nil, nil, 0, e
	}
	identity, e := util.GetTBC20StandardCodeIdentity(code)
	if e != nil {
		return nil, nil, 0, e
	}
	want, _ := util.GetTBC20StandardCodeIdentity(a.genesis.Outputs[0].LockingScript)
	gt, _ := ParseTBC20StandardTape(a.genesis.Outputs[1].LockingScript)
	if !bytes.Equal(identity, want) || !bytes.Equal(t.ExtensionData, gt.ExtensionData) || !t.Balance.IsUint64() {
		return nil, nil, 0, modernError("FT identity, metadata or balance mismatch")
	}
	owner, e := util.GetTBC20StandardController(code)
	if e != nil {
		return nil, nil, 0, e
	}
	var expected [21]byte
	if pool != nil {
		_, _, state, e := a.ReadState(pool)
		if e != nil {
			return nil, nil, 0, e
		}
		copy(expected[:20], crypto.Hash160(crypto.Sha256(pool.Outputs[0].LockingScript.Bytes())))
		expected[20] = 1
		if t.Balance.Uint64() != state.FT {
			return nil, nil, 0, modernError("pool FT balance mismatch")
		}
	} else {
		copy(expected[:20], crypto.Hash160(key.PubKey().SerialiseCompressed()))
	}
	if owner != expected {
		return nil, nil, 0, modernError("FT owner mismatch")
	}
	return code, tape, t.Balance.Uint64(), nil
}
func (a *TBCAMM) Mint(feeKey TransactionSigner, fee *bt.UTXO) ([]*bt.Tx, error) {
	source, e := modernFundMinimum(feeKey, fee, nil, nil, 0, 10, func(*bt.Tx) error { return nil })
	if e != nil {
		return nil, e
	}
	funding, e := modernUtxo(source, 0)
	if e != nil {
		return nil, e
	}
	tx, e := a.MintFromRoot(feeKey, funding)
	if e != nil {
		return nil, e
	}
	return []*bt.Tx{source, tx}, nil
}

// MintFromRoot creates only the Pool transaction using an explicitly chosen root.
func (a *TBCAMM) MintFromRoot(feeKey TransactionSigner, fee *bt.UTXO) (*bt.Tx, error) {
	if e := tbc20StandardAssertP2PKHOwner(fee, feeKey, "mint root"); e != nil {
		return nil, e
	}
	root, e := EncodeTBC20StandardOriginalUTXO(TBC20StandardOutpoint{TxID: hex.EncodeToString(fee.TxID), OutputIndex: fee.Vout})
	if e != nil {
		return nil, e
	}
	fs, e := a.feeScript()
	if e != nil {
		return nil, e
	}
	c := TBCAMMCode{TapeSize: a.tapeSize(), Controllers: a.controllers}
	copy(c.OriginalUTXO[:], root)
	copy(c.FeeScriptHash[:], crypto.Sha256(fs.Bytes()))
	code, e := c.Script()
	if e != nil {
		return nil, e
	}
	ph := crypto.Sha256(code.Bytes())
	lp := a.lpCode(ph, [21]byte{})
	li, e := lp.Identity()
	if e != nil {
		return nil, e
	}
	lc, _ := lp.Script()
	fi, _ := util.GetTBC20StandardCodeIdentity(a.genesis.Outputs[0].LockingScript)
	rate, _, _, _ := TBCAMMFeePolicy(a.plan)
	t := TBCAMMTape{LPSize: uint16(len(lc.Bytes())), FTSize: uint16(len(a.genesis.Outputs[0].LockingScript.Bytes())), FTGenesis: a.genesis.TxID(), FeeRate: rate, LPPlan: a.plan, Controlled: len(a.controllers) > 0, LPLocked: a.lpLocked}
	copy(t.LPHash[:], li[:32])
	copy(t.FTHash[:], fi[:32])
	tape, e := t.Script()
	if e != nil {
		return nil, e
	}
	owner := [21]byte{}
	copy(owner[:20], crypto.Hash160(ph))
	owner[20] = 1
	ftc, e := ReplaceTBC20StandardController(a.genesis.Outputs[0].LockingScript, owner)
	if e != nil {
		return nil, e
	}
	ftt, e := util.ReplaceTBC20StandardTapeAmounts(a.genesis.Outputs[1].LockingScript, [6]uint64{})
	if e != nil {
		return nil, e
	}
	outputs := []*bt.Output{{Satoshis: 1500, LockingScript: code}, {Satoshis: 0, LockingScript: tape}, {Satoshis: 500, LockingScript: ftc}, {Satoshis: 0, LockingScript: ftt}}
	tx, e := modernFundOptional(feeKey, fee, nil, outputs, 0, 10, func(*bt.Tx) error { return nil })
	if e != nil {
		return nil, e
	}
	if _, _, _, e = a.ReadState(tx); e != nil {
		return nil, e
	}
	return tx, nil
}
func ammSlots(entries map[int]uint64) [6]uint64 {
	var s [6]uint64
	for i, n := range entries {
		s[i] = n
	}
	return s
}
func (a *TBCAMM) Operate(o TBCAMMOperation) (*TBCAMMBuildResult, error) {
	if signerMissing(o.Key) || signerMissing(o.FeeKey) || o.Option < 1 || o.Option > 4 {
		return nil, modernError("invalid operation arguments")
	}
	c, t, state, e := a.ReadState(o.Pool.Parent)
	if e != nil {
		return nil, e
	}
	pc, pt, _, e := a.validateFT(o.PoolFT, o.Key, o.Pool.Parent)
	if e != nil {
		return nil, e
	}
	owner, e := TBC20StandardAddressController(o.Recipient)
	if e != nil {
		return nil, e
	}
	pay, e := bscript.NewP2PKHFromAddress(o.Recipient)
	if e != nil {
		return nil, e
	}
	var quote *TBCAMMQuote
	switch o.Option {
	case 1:
		quote, e = state.Add(o.Amount, o.UseTBC, o.FirstFT)
	case 2:
		quote, e = state.Remove(o.Amount)
	case 3:
		quote, e = state.SwapFT(o.Amount, a.plan, o.Minimum)
	case 4:
		quote, e = state.SwapTBC(o.Amount, a.plan, o.Minimum)
	}
	if e != nil {
		return nil, e
	}
	tape, e := t.WithState(quote.Next)
	if e != nil {
		return nil, e
	}
	outputs := []*bt.Output{{Satoshis: quote.Next.Value, LockingScript: o.Pool.Parent.Outputs[0].LockingScript}, {Satoshis: 0, LockingScript: tape}}
	one := 1
	groups := []util.TBC20StandardOutputGroup{{CodeVout: 0, TapeVout: &one}}
	var buildErr error
	pair := func(code, tape *bscript.Script) {
		if code == nil || tape == nil {
			buildErr = modernError("missing asset output")
			return
		}
		v := len(outputs) + 1
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs), TapeVout: &v})
		outputs = append(outputs, &bt.Output{Satoshis: 500, LockingScript: code}, &bt.Output{Satoshis: 0, LockingScript: tape})
	}
	single := func(value uint64, script *bscript.Script) {
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
		outputs = append(outputs, &bt.Output{Satoshis: value, LockingScript: script})
	}
	replace := func(t *bscript.Script, s [6]uint64) *bscript.Script {
		r, e := util.ReplaceTBC20StandardTapeAmounts(t, s)
		if e != nil {
			buildErr = e
		}
		return r
	}
	changeOwner := func(code *bscript.Script, owner [21]byte) *bscript.Script {
		r, e := ReplaceTBC20StandardController(code, owner)
		if e != nil {
			buildErr = e
		}
		return r
	}
	lpTape := func(s [6]uint64, lock uint32) *bscript.Script {
		r, e := a.lpTape(s, lock)
		if e != nil {
			buildErr = e
		}
		return r
	}
	var lock uint32
	switch o.Option {
	case 1, 4:
		if o.User == nil {
			return nil, modernError("missing user FT")
		}
		uc, ut, balance, e := a.validateFT(*o.User, o.Key, nil)
		if e != nil {
			return nil, e
		}
		amount := quote.FTDelta
		if balance < amount {
			return nil, modernError("insufficient user FT")
		}
		if o.Option == 4 {
			single(quote.Fees.Net, pay)
			fs, e := a.feeScript()
			if e != nil {
				return nil, e
			}
			if quote.Fees.ServicePaid == 0 {
				fs = bscript.NewFromBytes([]byte{0, 0x6a})
			}
			single(quote.Fees.ServicePaid, fs)
		}
		pair(pc, replace(pt, ammSlots(map[int]uint64{1: amount, 2: state.FT})))
		if o.Option == 1 {
			lc, e := a.lpCode(crypto.Sha256(o.Pool.Parent.Outputs[0].LockingScript.Bytes()), owner).Script()
			if e != nil {
				return nil, e
			}
			pair(lc, lpTape(ammSlots(map[int]uint64{0: quote.LPDelta}), o.LPLockTime))
		}
		if balance > amount {
			pair(uc, replace(ut, ammSlots(map[int]uint64{1: balance - amount})))
		}
	case 3:
		pair(changeOwner(pc, owner), replace(pt, ammSlots(map[int]uint64{2: quote.FTDelta})))
		fs, e := a.feeScript()
		if e != nil {
			return nil, e
		}
		if quote.Fees.ServicePaid == 0 {
			fs = bscript.NewFromBytes([]byte{0, 0x6a})
		}
		single(quote.Fees.ServicePaid, fs)
		pair(pc, replace(pt, ammSlots(map[int]uint64{2: quote.Next.FT})))
	case 2:
		if o.User == nil {
			return nil, modernError("missing user LP")
		}
		lc, lt, e := modernReadSpend(*o.User)
		if e != nil {
			return nil, e
		}
		identity, e := lc.Identity()
		if e != nil {
			return nil, e
		}
		expected, e := a.lpCode(crypto.Sha256(o.Pool.Parent.Outputs[0].LockingScript.Bytes()), [21]byte{}).Identity()
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(identity, expected) || !bytes.Equal(lc.Controller[:20], crypto.Hash160(o.Key.PubKey().SerialiseCompressed())) || lc.Controller[20] != 0 || !lt.Balance().IsUint64() || lt.Balance().Uint64() < o.Amount {
			return nil, modernError("LP identity, owner or balance mismatch")
		}
		lock = lt.LockTime
		single(quote.ValueDelta, pay)
		pair(changeOwner(pc, owner), replace(pt, ammSlots(map[int]uint64{2: quote.FTDelta})))
		burnHex, _ := hex.DecodeString("759d6677091e973b9e9d99f19c68fbf43e3f05f900")
		burn := *lc
		copy(burn.Controller[:], burnHex)
		burnCode, e := burn.Script()
		if e != nil {
			return nil, e
		}
		pair(burnCode, lpTape(ammSlots(map[int]uint64{1: o.Amount}), 0))
		pair(pc, replace(pt, ammSlots(map[int]uint64{2: quote.Next.FT})))
		if lt.Balance().Uint64() > o.Amount {
			code, e := lc.Script()
			if e != nil {
				return nil, e
			}
			pair(code, lpTape(ammSlots(map[int]uint64{1: lt.Balance().Uint64() - o.Amount}), lock))
		}
	}
	if buildErr != nil {
		return nil, buildErr
	}
	if (len(c.Controllers) == 0) != (signerMissing(o.Controller)) {
		return nil, modernError("Controller signer presence differs from profile")
	}
	if e = tbc20StandardAssertP2PKHOwner(o.Fee, o.FeeKey, "fee"); e != nil {
		return nil, e
	}
	actual, e := modernUtxo(o.FeeParent, int(o.Fee.Vout))
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(actual.TxID, o.Fee.TxID) || actual.Satoshis != o.Fee.Satoshis || !bytes.Equal(actual.LockingScript.Bytes(), o.Fee.LockingScript.Bytes()) {
		return nil, modernError("fee parent mismatch")
	}
	p0, _ := modernUtxo(o.Pool.Parent, 0)
	p2, e := modernUtxo(o.PoolFT.Parent, o.PoolFT.CodeVout)
	if e != nil {
		return nil, e
	}
	refs := []*bt.UTXO{p0}
	aux := []*bt.Tx{}
	if o.Option == 3 {
		refs = append(refs, o.Fee)
		aux = append(aux, o.FeeParent)
	} else {
		u, e := modernUtxo(o.User.Parent, o.User.CodeVout)
		if e != nil {
			return nil, e
		}
		refs = append(refs, u)
		aux = append(aux, o.User.Parent)
	}
	refs = append(refs, p2)
	aux = append(aux, o.PoolFT.Parent)
	if o.Option != 3 {
		refs = append(refs, o.Fee)
		aux = append(aux, o.FeeParent)
	}
	tx := newFTTx()
	seen := map[string]bool{}
	for _, u := range refs {
		id := hex.EncodeToString(u.TxID) + hex.EncodeToString(util.EncodeTBC20StandardUInt64LE(uint64(u.Vout)))
		if seen[id] {
			return nil, modernError("duplicate input")
		}
		seen[id] = true
		if e = tx.FromUTXOs(u); e != nil {
			return nil, e
		}
	}
	tx.LockTime = lock
	if o.Option == 2 && a.lpLocked {
		tx.Inputs[1].SequenceNumber = 0xfffffffe
	}
	for _, out := range outputs {
		tx.AddOutput(out)
	}
	tx.AddOutput(&bt.Output{Satoshis: 10, LockingScript: o.Fee.LockingScript})
	e = finalizeSignedFeeOptional(tx, len(tx.Outputs)-1, 10, func() error {
		groups := append([]util.TBC20StandardOutputGroup{}, groups...)
		if len(tx.Outputs) > len(outputs) {
			groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
		}
		var sig, pub []byte
		if !signerMissing(o.Controller) {
			digest, e := tx.CalcInputSignatureHash(0, sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e = modernSign(o.Controller, digest, false)
			if e != nil {
				return e
			}
			pub = o.Controller.PubKey().SerialiseCompressed()
		}
		unlock, e := BuildTBCAMMUnlock(tx, o.Pool.Parent, o.Pool.Ancestor, aux, o.Option, sig, pub)
		if e != nil {
			return e
		}
		tx.Inputs[0].UnlockingScript = unlock
		for _, vin := range []int{2, 1} {
			if vin == 1 && o.Option == 3 {
				continue
			}
			spend := o.PoolFT
			if vin == 1 {
				spend = *o.User
			}
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e := modernSign(o.Key, digest, false)
			if e != nil {
				return e
			}
			opts := util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: vin, PreTx: spend.Parent, PreTxVout: spend.CodeVout, OutputGroups: groups, AncestorTransactions: spend.Ancestors, Signature: sig, PublicKey: o.Key.PubKey().SerialiseCompressed()}
			if vin == 2 {
				opts.ContractController = &util.TBC20StandardContractControllerWitness{Transaction: o.Pool.Parent, CurrentInputIndex: 0}
			}
			if vin == 1 && o.Option == 2 {
				unlock, e = BuildTBC20LPUnlockWithSignature(opts)
			} else {
				unlock, e = util.BuildTBC20StandardUnlockScriptWithSignature(opts)
			}
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = unlock
		}
		feeVin := 3
		if o.Option == 3 {
			feeVin = 1
		}
		return signP2PKHAtIdx(tx, o.FeeKey, uint32(feeVin))
	})
	if e != nil {
		return nil, e
	}
	if _, _, _, e = a.ReadState(tx); e != nil {
		return nil, e
	}
	layout, e := TBCAMMLayout(len(tx.Outputs), o.Option)
	if e != nil {
		return nil, e
	}
	return &TBCAMMBuildResult{tx, quote, layout}, nil
}

// TransferLP also merges up to five LP outputs; all amounts are atomic units.
func (a *TBCAMM) TransferLP(key, feeKey TransactionSigner, fee *bt.UTXO, spends []ModernTokenSpend, recipient string, amount uint64, outputLock *uint32) (*bt.Tx, error) {
	if signerMissing(key) || len(spends) < 1 || len(spends) > 5 || amount == 0 {
		return nil, modernError("invalid LP transfer inputs")
	}
	kind := ModernLP
	if a.lpLocked {
		kind = ModernTimelockedLP
	} else if outputLock != nil {
		return nil, modernError("plain LP has no lock")
	}
	codes := []*ModernTokenCode{}
	tapes := []*ModernTokenTape{}
	inputs := []*bt.UTXO{}
	locks := []uint32{}
	total := new(big.Int)
	var identity []byte
	for _, spend := range spends {
		c, t, e := modernReadSpend(spend)
		if e != nil {
			return nil, e
		}
		u, e := modernUtxo(spend.Parent, spend.CodeVout)
		if e != nil {
			return nil, e
		}
		if u.Satoshis != 500 || spend.Parent.Outputs[spend.CodeVout+1].Satoshis != 0 || c.Kind != kind || c.TapeSize != a.tapeSize() || c.Controller[20] != 0 || !bytes.Equal(c.Controller[:20], crypto.Hash160(key.PubKey().SerialiseCompressed())) {
			return nil, modernError("LP identity or owner mismatch")
		}
		id, e := c.Identity()
		if e != nil {
			return nil, e
		}
		if identity == nil {
			identity = id
		} else if !bytes.Equal(identity, id) {
			return nil, modernError("mixed LP identities")
		}
		codes = append(codes, c)
		tapes = append(tapes, t)
		inputs = append(inputs, u)
		locks = append(locks, t.LockTime)
		total.Add(total, t.Balance())
	}
	remaining := new(big.Int).SetUint64(amount)
	if total.Cmp(remaining) < 0 {
		return nil, modernError("insufficient LP")
	}
	lock, e := RequiredModernLockTime(locks)
	if e != nil {
		return nil, e
	}
	receive, change := [6]uint64{}, [6]uint64{}
	for i, t := range tapes {
		take := new(big.Int).Set(remaining)
		balance := t.Balance()
		if take.Cmp(balance) > 0 {
			take.Set(balance)
		}
		rest := new(big.Int).Sub(balance, take)
		if !take.IsUint64() || !rest.IsUint64() || take.Uint64() > 1<<63-1 || rest.Uint64() > 1<<63-1 {
			return nil, modernError("LP slot overflow")
		}
		receive[i] = take.Uint64()
		change[i] = rest.Uint64()
		remaining.Sub(remaining, take)
	}
	owner, e := TBC20StandardAddressController(recipient)
	if e != nil {
		return nil, e
	}
	receiver := *codes[0]
	receiver.Controller = owner
	code, e := receiver.Script()
	if e != nil {
		return nil, e
	}
	outLock := lock
	if outputLock != nil {
		outLock = *outputLock
	}
	tape, e := a.lpTape(receive, outLock)
	if e != nil {
		return nil, e
	}
	outputs := []*bt.Output{{Satoshis: 500, LockingScript: code}, {Satoshis: 0, LockingScript: tape}}
	one := 1
	groups := []util.TBC20StandardOutputGroup{{CodeVout: 0, TapeVout: &one}}
	if total.Cmp(new(big.Int).SetUint64(amount)) > 0 {
		code, e := codes[0].Script()
		if e != nil {
			return nil, e
		}
		tape, e := a.lpTape(change, lock)
		if e != nil {
			return nil, e
		}
		three := 3
		groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: 2, TapeVout: &three})
		outputs = append(outputs, &bt.Output{Satoshis: 500, LockingScript: code}, &bt.Output{Satoshis: 0, LockingScript: tape})
	}
	return modernFundOptional(feeKey, fee, inputs, outputs, lock, 10, func(tx *bt.Tx) error {
		groups := append([]util.TBC20StandardOutputGroup{}, groups...)
		if len(tx.Outputs) > len(outputs) {
			groups = append(groups, util.TBC20StandardOutputGroup{CodeVout: len(outputs)})
		}
		for vin := range spends {
			if !a.lpLocked {
				tx.Inputs[vin].SequenceNumber = 0xffffffff
			}
		}
		for vin, spend := range spends {
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return e
			}
			sig, e := modernSign(key, digest, false)
			if e != nil {
				return e
			}
			unlock, e := BuildTBC20LPUnlockWithSignature(util.TBC20StandardUnlockOptions{CurrentTx: tx, InputIndex: vin, PreTx: spend.Parent, PreTxVout: spend.CodeVout, AncestorTransactions: spend.Ancestors, OutputGroups: groups, Signature: sig, PublicKey: key.PubKey().SerialiseCompressed()})
			if e != nil {
				return e
			}
			tx.Inputs[vin].UnlockingScript = unlock
		}
		return nil
	})
}
