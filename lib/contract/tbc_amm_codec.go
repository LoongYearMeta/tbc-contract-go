package contract

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"sort"
	"strings"
)

//go:embed asm/tbc_amm_pool.hex
var ammPublicTemplate string

//go:embed asm/tbc_amm_pool_hash_lock.hex
var ammControlledTemplate string

//go:embed asm/tbc_amm_pool_172.hex
var ammPublicTemplate172 string

//go:embed asm/tbc_amm_pool_hash_lock_172.hex
var ammControlledTemplate172 string

const ammEnd172 = "ad516a09504f4f4c434f444532"
const ammTail172 = "76a9<self.Controller20>88" + ammEnd172

type TBCAMMVersion uint8

const (
	TBCAMMV173 TBCAMMVersion = iota
	TBCAMMV172
)
const ammReturn = "ad516a11ffffffffffffffffffffffffffffffffff"
const ammControllerTail = "76a9<self.Controller20>88" + ammReturn + "<self.ServiceProviderTag>09504f4f4c434f444532"
const ammCodeEnd = ammReturn + "<self.ServiceProviderTag>09504f4f4c434f444532"

func ammTagPush(tag []byte) []byte {
	n := len(tag)
	var b []byte
	switch {
	case n <= 75:
		b = []byte{byte(n)}
	case n <= 255:
		b = []byte{0x4c, byte(n)}
	case n <= 65535:
		b = []byte{0x4d, byte(n), byte(n >> 8)}
	default:
		b = []byte{0x4e, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	}
	return append(b, tag...)
}

// Walk instructions rather than searching inside arbitrary tag bytes.
func ammParseTag(b []byte) (int, []byte, error) {
	type chunk struct {
		start int
		data  []byte
		push  bool
	}
	var chunks []chunk
	for pos := 0; pos < len(b); {
		start := pos
		op := b[pos]
		pos++
		n := -1
		if op <= 75 {
			n = int(op)
		} else if op >= 0x4c && op <= 0x4e {
			width := 1 << uint(op-0x4c)
			if len(b)-pos < width {
				return 0, nil, modernError("truncated push")
			}
			var size uint64
			for i := 0; i < width; i++ {
				size |= uint64(b[pos+i]) << uint(i*8)
			}
			pos += width
			if size > uint64(len(b)-pos) {
				return 0, nil, modernError("truncated push")
			}
			n = int(size)
		}
		c := chunk{start: start, push: n >= 0}
		if n >= 0 {
			if n > len(b)-pos {
				return 0, nil, modernError("truncated push")
			}
			c.data = b[pos : pos+n]
			pos += n
		}
		chunks = append(chunks, c)
	}
	if len(chunks) < 2 {
		return 0, nil, modernError("missing tag")
	}
	c := chunks[len(chunks)-2]
	tail := append(ammTagPush(c.data), ammTagPush([]byte("POOLCODE2"))...)
	if !c.push || !bytes.Equal(b[c.start:], tail) {
		return 0, nil, modernError("noncanonical tag or marker")
	}
	return c.start, append([]byte{}, c.data...), nil
}

type TBCAMMCode struct {
	Version            TBCAMMVersion
	OriginalUTXO       [36]byte
	FeeScriptHash      [32]byte
	TapeSize           int
	Controllers        [][20]byte
	ServiceProviderTag []byte
}

func ammMembership(hashes [][20]byte) ([]byte, error) {
	if len(hashes) < 1 || len(hashes) > 5 {
		return nil, modernError("controller whitelist requires 1..5 hashes")
	}
	sorted := append([][20]byte{}, hashes...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i][:], sorted[j][:]) < 0 })
	b := []byte{}
	for i, h := range sorted {
		if i > 0 && h == sorted[i-1] {
			return nil, modernError("duplicate controller")
		}
		if i < len(sorted)-1 {
			b = append(b, 0x76, 0x14)
			b = append(b, h[:]...)
			b = append(b, 0x87, 0x63, 0x75, 0x67)
		} else {
			b = append(b, 0x14)
			b = append(b, h[:]...)
			b = append(b, 0x88)
		}
	}
	b = append(b, bytes.Repeat([]byte{0x68}, len(sorted)-1)...)
	return b, nil
}
func parseAMMMembership(b []byte) ([][20]byte, error) {
	hashes := [][20]byte{}
	pos := 0
	for pos < len(b) && b[pos] == 0x76 {
		if len(hashes) >= 4 || len(b)-pos < 26 || b[pos+1] != 0x14 || !bytes.Equal(b[pos+22:pos+26], []byte{0x87, 0x63, 0x75, 0x67}) {
			return nil, modernError("noncanonical controller branch")
		}
		var h [20]byte
		copy(h[:], b[pos+2:pos+22])
		hashes = append(hashes, h)
		pos += 26
	}
	if len(b)-pos < 22 || b[pos] != 0x14 || b[pos+21] != 0x88 {
		return nil, modernError("missing terminal controller")
	}
	var h [20]byte
	copy(h[:], b[pos+1:pos+21])
	hashes = append(hashes, h)
	want, e := ammMembership(hashes)
	if e != nil || !bytes.Equal(want, b) {
		return nil, modernError("noncanonical controller whitelist")
	}
	return hashes, nil
}
func (c TBCAMMCode) template() (string, error) {
	public, controlled, tail, end := ammPublicTemplate, ammControlledTemplate, ammControllerTail, ammCodeEnd
	switch c.Version {
	case TBCAMMV172:
		if len(c.ServiceProviderTag) != 0 {
			return "", modernError("1.7.2 Pool cannot contain a tag")
		}
		public, controlled, tail, end = ammPublicTemplate172, ammControlledTemplate172, ammTail172, ammEnd172
	case TBCAMMV173:
	default:
		return "", modernError("unknown Pool version")
	}
	if len(c.Controllers) == 0 {
		return public, nil
	}
	b, e := ammMembership(c.Controllers)
	if e != nil {
		return "", e
	}
	base := strings.TrimSpace(controlled)
	if !strings.HasSuffix(base, tail) {
		return "", modernError("controller artifact tail changed")
	}
	return strings.TrimSuffix(base, tail) + "76a9" + hex.EncodeToString(b) + end, nil
}
func (c TBCAMMCode) Script() (*bscript.Script, error) {
	if c.TapeSize < 61 || c.TapeSize > 127 {
		return nil, modernError("invalid FT tape size")
	}
	template, e := c.template()
	if e != nil {
		return nil, e
	}
	tag := c.ServiceProviderTag
	if tag == nil {
		tag = []byte("NULL")
	}
	template = strings.ReplaceAll(template, "<self.ServiceProviderTag>", hex.EncodeToString(ammTagPush(tag)))
	return instantiateArtifact(template, map[string][]byte{"self.OriginalUTXO36": c.OriginalUTXO[:], "self.TbcFeeScriptHash32": c.FeeScriptHash[:], "self.FtTapeSize1": {byte(c.TapeSize)}})
}
func ParseTBCAMMCode(script *bscript.Script) (*TBCAMMCode, error) {
	if script == nil {
		return nil, modernError("missing Pool Code")
	}
	for _, version := range []TBCAMMVersion{TBCAMMV173, TBCAMMV172} {
		b := script.Bytes()
		tag := []byte{}
		end, _ := hex.DecodeString(ammEnd172)
		if version == TBCAMMV173 {
			start, value, err := ammParseTag(b)
			if err != nil {
				continue
			}
			b, tag = b[:start], value
			end, _ = hex.DecodeString(ammReturn)
		}
		profiles := []TBCAMMCode{{Version: version}}
		if bytes.HasSuffix(b, end) {
			for n := 1; n <= 5; n++ {
				size := 22 + 27*(n-1)
				if len(b) < size+len(end) {
					continue
				}
				hashes, e := parseAMMMembership(b[len(b)-len(end)-size : len(b)-len(end)])
				if e == nil {
					profiles = append(profiles, TBCAMMCode{Version: version, Controllers: hashes})
				}
			}
		}
		for _, c := range profiles {
			c.ServiceProviderTag = tag
			template, e := c.template()
			if e != nil {
				continue
			}
			template = strings.ReplaceAll(template, "<self.ServiceProviderTag>", hex.EncodeToString(ammTagPush(tag)))
			f, e := parseArtifact(template, script)
			if e != nil {
				continue
			}
			copy(c.OriginalUTXO[:], f["self.OriginalUTXO36"])
			copy(c.FeeScriptHash[:], f["self.TbcFeeScriptHash32"])
			c.TapeSize = int(f["self.FtTapeSize1"][0])
			encoded, e := c.Script()
			if e != nil {
				return nil, e
			}
			if bytes.Equal(encoded.Bytes(), script.Bytes()) {
				return &c, nil
			}
		}
	}
	return nil, modernError("unsupported or modified Pool Code")
}

type TBCAMMTape struct {
	// 1..6 preserves the 1.7.2 format; zero selects 1.7.3.
	LegacyPlan           uint8
	LPHash               [32]byte
	LPSize               uint16
	FTHash               [32]byte
	FTSize               uint16
	LP, FT, TBC          uint64
	FTGenesis            string
	Fees                 TBCAMMFeeRates
	FeeAddress           [20]byte
	Controlled, LPLocked bool
}

func (t TBCAMMTape) Version() TBCAMMVersion {
	if t.LegacyPlan != 0 {
		return TBCAMMV172
	}
	return TBCAMMV173
}
func (t TBCAMMTape) Script() (*bscript.Script, error) {
	if t.LPSize < 128 || t.LPSize > 32767 || t.FTSize < 128 || t.FTSize > 32767 {
		return nil, modernError("invalid code size")
	}
	for _, n := range []uint64{t.LP, t.FT, t.TBC} {
		if e := ammAmount(n); e != nil {
			return nil, e
		}
	}
	id, e := hex.DecodeString(t.FTGenesis)
	if e != nil || len(id) != 32 {
		return nil, modernError("invalid FT genesis txid")
	}
	size, payload, flags := 168, byte(155), 156
	if t.LegacyPlan != 0 {
		size, payload, flags = 143, 130, 131
	}
	b := make([]byte, size)
	copy(b, []byte{0, 0x6a, 0x4c, payload})
	copy(b[4:], t.LPHash[:])
	binary.LittleEndian.PutUint16(b[36:], t.LPSize)
	copy(b[38:], t.FTHash[:])
	binary.LittleEndian.PutUint16(b[70:], t.FTSize)
	for i, n := range []uint64{t.LP, t.FT, t.TBC} {
		binary.LittleEndian.PutUint64(b[72+i*8:], n)
	}
	copy(b[96:], id)
	if t.LegacyPlan != 0 {
		rates, e := TBCAMMRatesFromPlan(t.LegacyPlan)
		if e != nil {
			return nil, e
		}
		total, _, address, _ := TBCAMMFeePolicy(t.LegacyPlan)
		fee, e := ammFeeScript(address)
		if e != nil {
			return nil, e
		}
		if rates != t.Fees || !bytes.Equal(t.FeeAddress[:], fee.Bytes()[3:23]) {
			return nil, modernError("1.7.2 fees or recipient differ from plan")
		}
		binary.LittleEndian.PutUint16(b[128:], uint16(total))
		b[130] = t.LegacyPlan
	} else {
		for i, rate := range []uint16{t.Fees.LP, t.Fees.Tax, t.Fees.ServiceProvider, t.Fees.Protocol} {
			binary.LittleEndian.PutUint16(b[128+i*2:], rate)
		}
		copy(b[136:156], t.FeeAddress[:])
	}
	if t.Controlled {
		b[flags] = 1
		b[flags+2] = 1
	}
	if t.LPLocked {
		b[flags+1] = 1
	}
	copy(b[flags+3:], append([]byte{8}, []byte("POOLTAPE")...))
	return bscript.NewFromBytes(b), nil
}
func ParseTBCAMMTape(script *bscript.Script) (*TBCAMMTape, error) {
	if script == nil {
		return nil, modernError("missing Pool Tape")
	}
	b := script.Bytes()
	legacy := len(b) == 143
	flags, payload := 156, byte(155)
	if legacy {
		flags, payload = 131, 130
	}
	if (!legacy && len(b) != 168) || !bytes.HasPrefix(b, []byte{0, 0x6a, 0x4c, payload}) || !bytes.HasSuffix(b, []byte("POOLTAPE")) || (legacy && b[134] != 8) || b[flags] > 1 || b[flags+1] > 1 || b[flags+2] != b[flags] {
		return nil, modernError("invalid Pool Tape envelope")
	}
	t := &TBCAMMTape{LPSize: binary.LittleEndian.Uint16(b[36:]), FTSize: binary.LittleEndian.Uint16(b[70:]), LP: binary.LittleEndian.Uint64(b[72:]), FT: binary.LittleEndian.Uint64(b[80:]), TBC: binary.LittleEndian.Uint64(b[88:]), FTGenesis: hex.EncodeToString(b[96:128]), Controlled: b[flags] != 0, LPLocked: b[flags+1] != 0}
	if legacy {
		t.LegacyPlan = b[130]
		rates, e := TBCAMMRatesFromPlan(t.LegacyPlan)
		if e != nil {
			return nil, e
		}
		total, _, address, _ := TBCAMMFeePolicy(t.LegacyPlan)
		if binary.LittleEndian.Uint16(b[128:]) != total {
			return nil, modernError("1.7.2 total fee differs from plan")
		}
		t.Fees = rates
		fee, e := ammFeeScript(address)
		if e != nil {
			return nil, e
		}
		copy(t.FeeAddress[:], fee.Bytes()[3:23])
	} else {
		t.Fees = TBCAMMFeeRates{binary.LittleEndian.Uint16(b[128:]), binary.LittleEndian.Uint16(b[130:]), binary.LittleEndian.Uint16(b[132:]), binary.LittleEndian.Uint16(b[134:])}
		copy(t.FeeAddress[:], b[136:156])
	}
	copy(t.LPHash[:], b[4:36])
	copy(t.FTHash[:], b[38:70])
	if _, e := t.Script(); e != nil {
		return nil, e
	}
	return t, nil
}
func (t TBCAMMTape) State(value uint64) (TBCAMMMathState, error) {
	s := TBCAMMMathState{t.LP, t.FT, t.TBC, value}
	return s, s.Validate(false)
}
func (t TBCAMMTape) WithState(s TBCAMMMathState) (*bscript.Script, error) {
	t.LP = s.LP
	t.FT = s.FT
	t.TBC = s.TBC
	return t.Script()
}
