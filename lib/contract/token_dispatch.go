package contract

import (
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"math/big"
)

type TokenAsset struct {
	Code, Tape *bscript.Script
	Modern     *ModernTokenCode
	Balance    *big.Int
	Amounts    [6]uint64
	LockTime   uint32
	Controller [21]byte
	Identity   []byte
}

func ReadTokenAsset(s ModernTokenSpend) (*TokenAsset, error) {
	u, e := modernUtxo(s.Parent, s.CodeVout)
	if e != nil {
		return nil, e
	}
	if s.CodeVout+1 >= len(s.Parent.Outputs) || u.Satoshis != 500 || s.Parent.Outputs[s.CodeVout+1].Satoshis != 0 {
		return nil, modernError("invalid token Code/Tape values")
	}
	return ParseTokenAsset(u.LockingScript, s.Parent.Outputs[s.CodeVout+1].LockingScript)
}
func ParseTokenAsset(code, tape *bscript.Script) (*TokenAsset, error) {
	if code == nil || tape == nil {
		return nil, modernError("missing Code/Tape")
	}
	if ValidateTBC20StandardCode(code, len(tape.Bytes())) == nil {
		t, e := ParseTBC20StandardTape(tape)
		if e != nil {
			return nil, e
		}
		owner, e := util.GetTBC20StandardController(code)
		if e != nil {
			return nil, e
		}
		id, e := util.GetTBC20StandardCodeIdentity(code)
		return &TokenAsset{Code: code, Tape: tape, Balance: t.Balance, Amounts: t.Amounts, Controller: owner, Identity: id}, e
	}
	c, e := ParseModernTokenCode(code)
	if e != nil {
		return nil, e
	}
	t, e := ParseModernTokenTape(tape, c)
	if e != nil {
		return nil, e
	}
	id, e := c.Identity()
	return &TokenAsset{Code: code, Tape: tape, Modern: c, Balance: t.Balance(), Amounts: t.Amounts, LockTime: t.LockTime, Controller: c.Controller, Identity: id}, e
}
func (a TokenAsset) HasLock() bool { return a.Modern != nil && a.Modern.Kind != ModernLP }
func (a TokenAsset) ReplaceController(owner [21]byte) (*bscript.Script, error) {
	if a.Modern == nil {
		return ReplaceTBC20StandardController(a.Code, owner)
	}
	c := *a.Modern
	c.Controller = owner
	return c.Script()
}
func (a TokenAsset) ReplaceAmounts(amounts [6]uint64) (*bscript.Script, error) {
	if a.Modern == nil {
		return util.ReplaceTBC20StandardTapeAmounts(a.Tape, amounts)
	}
	t, e := ParseModernTokenTape(a.Tape, a.Modern)
	if e != nil {
		return nil, e
	}
	t.Amounts = amounts
	return t.Script(a.Modern.Kind)
}
func (a TokenAsset) Unlock(o util.TBC20StandardUnlockOptions) (*bscript.Script, error) {
	if a.Modern == nil {
		return util.BuildTBC20StandardUnlockScriptWithSignature(o)
	}
	if a.Modern.Kind == ModernStablecoin {
		return BuildTBC20StablecoinUnlockWithSignature(o)
	}
	return BuildTBC20LPUnlockWithSignature(o)
}
