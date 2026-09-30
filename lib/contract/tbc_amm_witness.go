package contract

import (
	"bytes"
	"encoding/hex"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
)

// Optional output indexes use -1.
type TBCAMMOutputLayout struct{ PoolFT, UserFT, LP, LPBurn, TokenChange, TBC, Fee, Change int }

func TBCAMMLayout(count, option int) (TBCAMMOutputLayout, error) {
	l := TBCAMMOutputLayout{-1, -1, -1, -1, -1, -1, -1, -1}
	if option == 3 {
		if count != 7 && count != 8 {
			return l, modernError("SwapFT requires 7 or 8 outputs")
		}
		l.PoolFT = 5
		l.UserFT = 2
		l.Fee = 4
		if count == 8 {
			l.Change = 7
		}
		return l, nil
	}
	base := 6
	if option == 2 {
		base = 9
	} else if option != 1 && option != 4 {
		return l, modernError("invalid Pool option")
	}
	if count < base || count > base+3 {
		return l, modernError("invalid operation output count")
	}
	if count-base >= 2 {
		l.TokenChange = base
	}
	if (count-base)%2 == 1 {
		l.Change = count - 1
	}
	switch option {
	case 1:
		l.PoolFT = 2
		l.LP = 4
	case 2:
		l.PoolFT = 7
		l.UserFT = 3
		l.LPBurn = 5
		l.TBC = 2
	case 4:
		l.PoolFT = 4
		l.TBC = 2
		l.Fee = 3
	}
	return l, nil
}
func ammOutputLeaves(tx *bt.Tx, vout int) ([][]byte, error) {
	if vout < 0 {
		return make([][]byte, 4), nil
	}
	if vout >= len(tx.Outputs) {
		return nil, modernError("output vout")
	}
	o := tx.Outputs[vout]
	p, e := util.GetTBC20StandardPartialScriptData(o.LockingScript)
	if e != nil {
		return nil, e
	}
	return [][]byte{nft721Value(o), p.SuffixData, p.PartialHash, p.Size}, nil
}
func ammPairLeaves(tx *bt.Tx, vout int) ([][]byte, error) {
	if vout < 0 {
		return make([][]byte, 6), nil
	}
	leaves, e := ammOutputLeaves(tx, vout)
	if e != nil {
		return nil, e
	}
	if vout+1 >= len(tx.Outputs) {
		return nil, modernError("paired Tape missing")
	}
	o := tx.Outputs[vout+1]
	return append(leaves, nft721Value(o), o.LockingScript.Bytes()), nil
}
func ammInputHashes(tx *bt.Tx) []byte {
	return append(crypto.Sha256(nft721Inputs(tx)), nft721UnlockHash(tx)...)
}
func ammLinked(tx *bt.Tx, vin int, parent *bt.Tx) (int, error) {
	if vin < 0 || vin >= len(tx.Inputs) {
		return 0, modernError("input index")
	}
	i := tx.Inputs[vin]
	if hex.EncodeToString(i.PreviousTxID()) != parent.TxID() || int(i.PreviousTxOutIndex) >= len(parent.Outputs) {
		return 0, modernError("parent outpoint mismatch")
	}
	return int(i.PreviousTxOutIndex), nil
}
func BuildTBCAMMUnlock(tx, parent, ancestor *bt.Tx, auxiliary []*bt.Tx, option int, signature, publicKey []byte) (*bscript.Script, error) {
	count := 3
	if option == 3 {
		count = 2
	}
	if tx == nil || len(tx.Inputs) != count+1 || len(auxiliary) != count {
		return nil, modernError("wrong Pool input count")
	}
	for _, t := range append([]*bt.Tx{tx, parent, ancestor}, auxiliary...) {
		if t == nil || t.Version != 10 || len(t.Inputs) == 0 || len(t.Outputs) == 0 {
			return nil, modernError("invalid version 10 transaction")
		}
	}
	seen := map[string]bool{}
	for _, i := range tx.Inputs {
		p := hex.EncodeToString(i.PreviousTxID()) + hex.EncodeToString(util.EncodeTBC20StandardUInt64LE(uint64(i.PreviousTxOutIndex)))
		if seen[p] {
			return nil, modernError("duplicate input")
		}
		seen[p] = true
	}
	v, e := ammLinked(tx, 0, parent)
	if e != nil || v != 0 || !bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), parent.Outputs[0].LockingScript.Bytes()) {
		return nil, modernError("Pool Code must be preserved")
	}
	code, e := ParseTBCAMMCode(parent.Outputs[0].LockingScript)
	if e != nil {
		return nil, e
	}
	av, e := ammLinked(parent, 0, ancestor)
	if e != nil {
		return nil, e
	}
	l, e := TBCAMMLayout(len(tx.Outputs), option)
	if e != nil {
		return nil, e
	}
	leaves := [][]byte{}
	if len(signature) > 0 {
		member := false
		for _, h := range code.Controllers {
			if bytes.Equal(h[:], crypto.Hash160(publicKey)) {
				member = true
			}
		}
		if !member || len(publicKey) != 33 || len(signature) > 72 || len(signature) == 65 || signature[len(signature)-1] != 0x41 {
			return nil, modernError("invalid Pool Controller authorization")
		}
		leaves = append(leaves, signature, publicKey)
	} else if len(code.Controllers) > 0 || len(publicKey) > 0 {
		return nil, modernError("Controller signature required")
	}
	if len(parent.Outputs) < 2 || len(tx.Outputs) < 2 {
		return nil, modernError("missing Pool Tape")
	}
	leaves = append(leaves, nft721Value(tx.Outputs[0]), crypto.Sha256(tx.Outputs[0].LockingScript.Bytes()), nft721Value(tx.Outputs[1]), tx.Outputs[1].LockingScript.Bytes())
	type group struct {
		v    int
		pair bool
	}
	groups := []group{}
	switch option {
	case 1:
		groups = []group{{l.PoolFT, true}, {l.LP, true}, {l.TokenChange, true}}
	case 2:
		groups = []group{{l.TBC, false}, {l.UserFT, true}, {l.LPBurn, true}, {l.PoolFT, true}, {l.TokenChange, true}}
	case 3:
		groups = []group{{l.UserFT, true}, {l.Fee, false}, {l.PoolFT, true}}
	case 4:
		groups = []group{{l.TBC, false}, {l.Fee, false}, {l.PoolFT, true}, {l.TokenChange, true}}
	}
	groups = append(groups, group{l.Change, false})
	for _, g := range groups {
		var items [][]byte
		if g.pair {
			items, e = ammPairLeaves(tx, g.v)
		} else {
			items, e = ammOutputLeaves(tx, g.v)
		}
		if e != nil {
			return nil, e
		}
		leaves = append(leaves, items...)
	}
	for i, p := range auxiliary {
		v, e := ammLinked(tx, i+1, p)
		if e != nil {
			return nil, e
		}
		leaves = append(leaves, nft721Header(p), ammInputHashes(p), nft721Records(p.Outputs[:v]))
		items, e := ammOutputLeaves(p, v)
		if e != nil {
			return nil, e
		}
		leaves = append(leaves, items...)
		leaves = append(leaves, nft721Records(p.Outputs[v+1:]))
	}
	leaves = append(leaves, nft721Inputs(tx), util.EncodeTBC20StandardUnsignedLE(uint64(option)), nft721Header(ancestor), ammInputHashes(ancestor), nft721Records(ancestor.Outputs[:av]), nft721Value(ancestor.Outputs[av]), crypto.Sha256(ancestor.Outputs[av].LockingScript.Bytes()), nft721Records(ancestor.Outputs[av+1:]))
	leaves = append(leaves, nft721Header(parent), nft721Inputs(parent), nft721UnlockHash(parent), nft721Value(parent.Outputs[0]), crypto.Sha256(parent.Outputs[0].LockingScript.Bytes()), nft721Value(parent.Outputs[1]), parent.Outputs[1].LockingScript.Bytes(), nft721Records(parent.Outputs[2:]))
	want := []int{0, 66, 76, 56, 68}[option]
	if len(signature) > 0 {
		want += 2
	}
	if len(leaves) != want {
		return nil, modernError("internal Pool ABI leaf count")
	}
	script := bscript.NewFromBytes(nil)
	for _, leaf := range leaves {
		if len(leaf) == 1 && (leaf[0] >= 1 && leaf[0] <= 16 || leaf[0] == 0x81) {
			op := byte(0x4f)
			if leaf[0] != 0x81 {
				op = 0x50 + leaf[0]
			}
			if e = script.AppendOpcodes(op); e != nil {
				return nil, e
			}
		} else if e = script.AppendPushData(leaf); e != nil {
			return nil, e
		}
	}
	return script, nil
}
