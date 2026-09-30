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
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

//go:embed asm/tbc20_stablecoin.hex
var stablecoin172Template string

//go:embed asm/tbc20_lp.hex
var lp172Template string

//go:embed asm/tbc20_lp_locktime.hex
var lpLock172Template string

type ModernTokenKind string

const (
	ModernStablecoin   ModernTokenKind = "stablecoin"
	ModernLP           ModernTokenKind = "lp"
	ModernTimelockedLP ModernTokenKind = "lp-locktime"
)

type ModernTokenCode struct {
	Kind       ModernTokenKind
	IssuerHash [32]byte
	AdminHash  []byte
	TapeSize   int
	Controller [21]byte
}
type ModernTokenTape struct {
	Amounts  [6]uint64
	LockTime uint32
	Metadata []byte
	TapeSize int
}

var artifactField = regexp.MustCompile(`<([^>]*?([0-9]+))>`)

func modernError(message string) error { return fmt.Errorf("modern TBC20: %s", message) }
func modernTemplate(kind ModernTokenKind) string {
	switch kind {
	case ModernStablecoin:
		return stablecoin172Template
	case ModernLP:
		return lp172Template
	case ModernTimelockedLP:
		return lpLock172Template
	}
	return ""
}
func instantiateArtifact(template string, fields map[string][]byte) (*bscript.Script, error) {
	if template == "" {
		return nil, modernError("unknown artifact")
	}
	var failed bool
	raw := artifactField.ReplaceAllStringFunc(strings.TrimSpace(template), func(s string) string {
		match := artifactField.FindStringSubmatch(s)
		size, _ := strconv.Atoi(match[2])
		v, ok := fields[match[1]]
		if !ok || len(v) != size || size < 1 || size > 75 {
			failed = true
			return ""
		}
		return hex.EncodeToString(append([]byte{byte(size)}, v...))
	})
	if failed {
		return nil, modernError("constructor parameter width mismatch")
	}
	return bscript.NewFromHexString(raw)
}
func parseArtifact(template string, script *bscript.Script) (map[string][]byte, error) {
	if script == nil || template == "" {
		return nil, modernError("missing Code")
	}
	template = strings.TrimSpace(template)
	data := script.Bytes()
	offset := 0
	last := 0
	fields := map[string][]byte{}
	for _, m := range artifactField.FindAllStringSubmatchIndex(template, -1) {
		fixed, e := hex.DecodeString(template[last:m[0]])
		if e != nil || len(data)-offset < len(fixed) || !bytes.Equal(data[offset:offset+len(fixed)], fixed) {
			return nil, modernError("modified Code template")
		}
		offset += len(fixed)
		width, _ := strconv.Atoi(template[m[4]:m[5]])
		name := template[m[2]:m[3]]
		if width < 1 || width > 75 || len(data)-offset < width+1 || data[offset] != byte(width) {
			return nil, modernError("invalid constructor push")
		}
		v := append([]byte(nil), data[offset+1:offset+1+width]...)
		if old, ok := fields[name]; ok && !bytes.Equal(old, v) {
			return nil, modernError("inconsistent repeated constructor")
		}
		fields[name] = v
		offset += width + 1
		last = m[1]
	}
	suffix, e := hex.DecodeString(template[last:])
	if e != nil || !bytes.Equal(data[offset:], suffix) {
		return nil, modernError("modified Code suffix")
	}
	return fields, nil
}
func (c ModernTokenCode) Script() (*bscript.Script, error) {
	min := 66
	if c.Kind == ModernLP {
		min = 61
	}
	if c.TapeSize < min || c.TapeSize > 127 || c.Controller[20] > 1 {
		return nil, modernError("invalid tape size or controller")
	}
	f := map[string][]byte{"self.ConstTapeSize1": {byte(c.TapeSize)}, "self.Controller21": c.Controller[:]}
	if c.Kind == ModernStablecoin {
		if len(c.AdminHash) != 20 {
			return nil, modernError("missing administrator")
		}
		f["self.AdminPubKeyHash20"] = c.AdminHash
		f["self.CoinNftCodeHash32"] = c.IssuerHash[:]
	} else {
		if len(c.AdminHash) != 0 {
			return nil, modernError("LP cannot have administrator")
		}
		f["self.PoolCodeHash32"] = c.IssuerHash[:]
	}
	return instantiateArtifact(modernTemplate(c.Kind), f)
}
func ParseModernTokenCode(script *bscript.Script) (*ModernTokenCode, error) {
	for _, kind := range []ModernTokenKind{ModernStablecoin, ModernLP, ModernTimelockedLP} {
		f, e := parseArtifact(modernTemplate(kind), script)
		if e != nil {
			continue
		}
		c := &ModernTokenCode{Kind: kind, TapeSize: int(f["self.ConstTapeSize1"][0]), AdminHash: f["self.AdminPubKeyHash20"]}
		copy(c.Controller[:], f["self.Controller21"])
		name := "self.PoolCodeHash32"
		if kind == ModernStablecoin {
			name = "self.CoinNftCodeHash32"
		}
		copy(c.IssuerHash[:], f[name])
		if _, e = c.Script(); e != nil {
			return nil, e
		}
		return c, nil
	}
	return nil, modernError("unsupported or modified Code")
}
func (c ModernTokenCode) Identity() ([]byte, error) {
	s, e := c.Script()
	if e != nil {
		return nil, e
	}
	p, e := util.GetTBC20StandardPartialScriptData(s)
	return append(p.PartialHash, p.Size...), e
}
func (t ModernTokenTape) Balance() *big.Int {
	sum := new(big.Int)
	for _, v := range t.Amounts {
		sum.Add(sum, new(big.Int).SetUint64(v))
	}
	return sum
}
func validateModernMetadata(b []byte) error {
	for i := 0; i < len(b); {
		op := b[i]
		i++
		n := uint64(0)
		switch {
		case op == 0 || op == 0x4f || op >= 0x51 && op <= 0x60:
		case op <= 75:
			n = uint64(op)
		case op == 0x4c:
			if i >= len(b) {
				return modernError("truncated metadata")
			}
			n = uint64(b[i])
			i++
		case op == 0x4d:
			if i+2 > len(b) {
				return modernError("truncated metadata")
			}
			n = uint64(binary.LittleEndian.Uint16(b[i:]))
			i += 2
		case op == 0x4e:
			if i+4 > len(b) {
				return modernError("truncated metadata")
			}
			n = uint64(binary.LittleEndian.Uint32(b[i:]))
			i += 4
		default:
			return modernError("metadata must be push-only")
		}
		if n > uint64(len(b)-i) {
			return modernError("truncated metadata")
		}
		i += int(n)
	}
	return nil
}
func (t ModernTokenTape) Script(kind ModernTokenKind) (*bscript.Script, error) {
	base := 66
	if kind == ModernLP {
		base = 61
	}
	if modernTemplate(kind) == "" || t.TapeSize < base || t.TapeSize > 127 || len(t.Metadata) > t.TapeSize-base {
		return nil, modernError("invalid Tape size")
	}
	if e := validateModernMetadata(t.Metadata); e != nil {
		return nil, e
	}
	if kind != ModernStablecoin {
		for _, v := range t.Metadata {
			if v != 0 {
				return nil, modernError("LP padding must be zero")
			}
		}
	}
	if kind == ModernLP && t.LockTime != 0 {
		return nil, modernError("plain LP cannot have lock")
	}
	b := make([]byte, t.TapeSize)
	copy(b, []byte{0, 0x6a, 0x30})
	for i, v := range t.Amounts {
		if v > 1<<63-1 {
			return nil, modernError("slot overflow")
		}
		binary.LittleEndian.PutUint64(b[3+8*i:], v)
	}
	copy(b[51:], t.Metadata)
	if kind != ModernLP {
		offset := 51
		if kind == ModernStablecoin {
			offset = t.TapeSize - 15
		}
		b[offset] = 4
		binary.LittleEndian.PutUint32(b[offset+1:], t.LockTime)
	}
	copy(b[len(b)-10:], append([]byte{9}, []byte("TBC20TAPE")...))
	return bscript.NewFromBytes(b), nil
}
func ParseModernTokenTape(script *bscript.Script, c *ModernTokenCode) (*ModernTokenTape, error) {
	if script == nil || c == nil {
		return nil, modernError("missing Tape")
	}
	if _, e := c.Script(); e != nil {
		return nil, e
	}
	b := script.Bytes()
	base := 66
	if c.Kind == ModernLP {
		base = 61
	}
	if len(b) != c.TapeSize || len(b) < base || !bytes.HasPrefix(b, []byte{0, 0x6a, 0x30}) || !bytes.HasSuffix(b, append([]byte{9}, []byte("TBC20TAPE")...)) {
		return nil, modernError("invalid Tape envelope")
	}
	t := &ModernTokenTape{TapeSize: len(b)}
	for i := range t.Amounts {
		t.Amounts[i] = binary.LittleEndian.Uint64(b[3+8*i:])
	}
	switch c.Kind {
	case ModernStablecoin:
		offset := len(b) - 15
		if b[offset] != 4 {
			return nil, modernError("invalid lock field")
		}
		t.LockTime = binary.LittleEndian.Uint32(b[offset+1:])
		t.Metadata = append([]byte(nil), b[51:offset]...)
	case ModernTimelockedLP:
		if b[51] != 4 {
			return nil, modernError("invalid LP lock field")
		}
		t.LockTime = binary.LittleEndian.Uint32(b[52:])
		t.Metadata = append([]byte(nil), b[56:len(b)-10]...)
	case ModernLP:
		t.Metadata = append([]byte(nil), b[51:len(b)-10]...)
	}
	_, e := t.Script(c.Kind)
	return t, e
}
func RequiredModernLockTime(values []uint32) (uint32, error) {
	var max uint32
	for _, v := range values {
		if max > 0 && v > 0 && (max < 500000000) != (v < 500000000) {
			return 0, modernError("cannot mix height and timestamp locks")
		}
		if v > max {
			max = v
		}
	}
	return max, nil
}
func BuildTBC20StablecoinUnlockWithSignature(o util.TBC20StandardUnlockOptions) (*bscript.Script, error) {
	return buildModernTokenUnlock(o, true)
}
func BuildTBC20LPUnlockWithSignature(o util.TBC20StandardUnlockOptions) (*bscript.Script, error) {
	return buildModernTokenUnlock(o, false)
}
func buildModernTokenUnlock(o util.TBC20StandardUnlockOptions, stable bool) (*bscript.Script, error) {
	if o.CurrentTx == nil || o.PreTx == nil || o.InputIndex < 0 || o.InputIndex >= len(o.CurrentTx.Inputs) || len(o.CurrentTx.Inputs) > 6 || o.PreTxVout < 0 || o.PreTxVout+1 >= len(o.PreTx.Outputs) {
		return nil, modernError("invalid parent or input")
	}
	c, e := ParseModernTokenCode(o.PreTx.Outputs[o.PreTxVout].LockingScript)
	if e != nil {
		return nil, e
	}
	if (c.Kind == ModernStablecoin) != stable {
		return nil, modernError("wrong token family")
	}
	t, e := ParseModernTokenTape(o.PreTx.Outputs[o.PreTxVout+1].LockingScript, c)
	if e != nil {
		return nil, e
	}
	admin := bytes.Equal(crypto.Hash160(o.PublicKey), c.AdminHash)
	if len(o.Signature) < 2 || o.Signature[len(o.Signature)-1] != 0x41 || !((len(o.Signature) == 65 && len(o.PublicKey) == 32) || (len(o.Signature) <= 72 && len(o.PublicKey) == 33)) {
		return nil, modernError("invalid signature shape")
	}
	if !stable && (len(o.PublicKey) != 33 || len(o.Signature) == 65) {
		return nil, modernError("LP requires ECDSA compressed key")
	}
	if c.Kind != ModernLP && (o.CurrentTx.Inputs[o.InputIndex].SequenceNumber == 0xffffffff || !admin && (t.LockTime > o.CurrentTx.LockTime || t.LockTime > 0 && (t.LockTime < 500000000) != (o.CurrentTx.LockTime < 500000000))) {
		return nil, modernError("unsatisfied token lock")
	}
	identity, _ := c.Identity()
	for slot, v := range t.Amounts {
		if v == 0 {
			continue
		}
		if slot >= len(o.PreTx.Inputs) || o.AncestorTransactions == nil {
			return nil, modernError("missing ancestor input")
		}
		in := o.PreTx.Inputs[slot]
		id := hex.EncodeToString(in.PreviousTxID())
		ancestor, ok := o.AncestorTransactions.ResolveTBC20StandardAncestor(id)
		if !ok || ancestor == nil || ancestor.TxID() != id || int(in.PreviousTxOutIndex) >= len(ancestor.Outputs) {
			return nil, modernError("missing authenticated ancestor")
		}
		script := ancestor.Outputs[in.PreviousTxOutIndex].LockingScript
		ac, err := ParseModernTokenCode(script)
		same := false
		if err == nil {
			ai, _ := ac.Identity()
			same = bytes.Equal(ai, identity)
		}
		if !same && !(slot == 0 && in.PreviousTxOutIndex == 0 && bytes.Equal(crypto.Sha256(script.Bytes()), c.IssuerHash[:])) {
			return nil, modernError("ancestor identity mismatch")
		}
	}
	allocated := new(big.Int)
	for _, g := range o.OutputGroups {
		if g.CodeVout < 0 || g.CodeVout >= len(o.CurrentTx.Outputs) {
			return nil, modernError("output Code index")
		}
		out := o.CurrentTx.Outputs[g.CodeVout]
		if len(out.LockingScript.Bytes()) == c.TapeSize && bytes.HasSuffix(out.LockingScript.Bytes(), []byte("TBC20TAPE")) {
			return nil, modernError("hidden Tape in Code group")
		}
		if g.TapeVout == nil {
			continue
		}
		vout := *g.TapeVout
		if vout < 0 || vout >= len(o.CurrentTx.Outputs) {
			return nil, modernError("output Tape index")
		}
		tape := o.CurrentTx.Outputs[vout]
		if len(tape.LockingScript.Bytes()) != c.TapeSize {
			continue
		}
		amounts, e := util.ReadTBC20StandardTapeAmounts(tape.LockingScript)
		if e != nil {
			return nil, e
		}
		v := amounts[o.InputIndex]
		allocated.Add(allocated, new(big.Int).SetUint64(v))
		if v > 0 {
			oc, e := ParseModernTokenCode(out.LockingScript)
			if e != nil {
				return nil, e
			}
			oi, _ := oc.Identity()
			if !bytes.Equal(oi, identity) || out.Satoshis != 500 || tape.Satoshis != 0 {
				return nil, modernError("output identity or value mismatch")
			}
			if _, e = ParseModernTokenTape(tape.LockingScript, oc); e != nil {
				return nil, e
			}
		}
	}
	if allocated.Cmp(t.Balance()) != 0 {
		return nil, modernError("input allocation does not conserve balance")
	}
	controller := c.Controller
	if admin {
		if o.ContractController != nil {
			return nil, modernError("admin cannot supply controller proof")
		}
		copy(controller[:20], crypto.Hash160(o.PublicKey))
		controller[20] = 0
	}
	return util.SerializeTBC20TokenUnlock(o, controller)
}

// Compile-time check that transaction types used above are native Go types.
var _ *bt.Tx
