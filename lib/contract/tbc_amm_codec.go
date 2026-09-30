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

const ammControllerTail = "76a9<self.Controller20>88ad516a09504f4f4c434f444532"
const ammCodeEnd = "ad516a09504f4f4c434f444532"

type TBCAMMCode struct {
	OriginalUTXO  [36]byte
	FeeScriptHash [32]byte
	TapeSize      int
	Controllers   [][20]byte
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
	if len(c.Controllers) == 0 {
		return ammPublicTemplate, nil
	}
	b, e := ammMembership(c.Controllers)
	if e != nil {
		return "", e
	}
	base := strings.TrimSpace(ammControlledTemplate)
	if !strings.HasSuffix(base, ammControllerTail) {
		return "", modernError("controller artifact tail changed")
	}
	return strings.TrimSuffix(base, ammControllerTail) + "76a9" + hex.EncodeToString(b) + ammCodeEnd, nil
}
func (c TBCAMMCode) Script() (*bscript.Script, error) {
	if c.TapeSize < 61 || c.TapeSize > 127 {
		return nil, modernError("invalid FT tape size")
	}
	template, e := c.template()
	if e != nil {
		return nil, e
	}
	return instantiateArtifact(template, map[string][]byte{"self.OriginalUTXO36": c.OriginalUTXO[:], "self.TbcFeeScriptHash32": c.FeeScriptHash[:], "self.FtTapeSize1": {byte(c.TapeSize)}})
}
func ParseTBCAMMCode(script *bscript.Script) (*TBCAMMCode, error) {
	if script == nil {
		return nil, modernError("missing Pool Code")
	}
	profiles := []TBCAMMCode{{}}
	end, _ := hex.DecodeString(ammCodeEnd)
	b := script.Bytes()
	if bytes.HasSuffix(b, end) {
		for n := 1; n <= 5; n++ {
			size := 22 + 27*(n-1)
			if len(b) < size+len(end) {
				continue
			}
			hashes, e := parseAMMMembership(b[len(b)-len(end)-size : len(b)-len(end)])
			if e == nil {
				profiles = append(profiles, TBCAMMCode{Controllers: hashes})
			}
		}
	}
	for _, c := range profiles {
		template, e := c.template()
		if e != nil {
			continue
		}
		f, e := parseArtifact(template, script)
		if e != nil {
			continue
		}
		copy(c.OriginalUTXO[:], f["self.OriginalUTXO36"])
		copy(c.FeeScriptHash[:], f["self.TbcFeeScriptHash32"])
		c.TapeSize = int(f["self.FtTapeSize1"][0])
		if _, e = c.Script(); e != nil {
			return nil, e
		}
		return &c, nil
	}
	return nil, modernError("unsupported or modified Pool Code")
}

type TBCAMMTape struct {
	LPHash               [32]byte
	LPSize               uint16
	FTHash               [32]byte
	FTSize               uint16
	LP, FT, TBC          uint64
	FTGenesis            string
	FeeRate              uint16
	LPPlan               uint8
	Controlled, LPLocked bool
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
	b := make([]byte, 143)
	copy(b, []byte{0, 0x6a, 0x4c, 130})
	copy(b[4:], t.LPHash[:])
	binary.LittleEndian.PutUint16(b[36:], t.LPSize)
	copy(b[38:], t.FTHash[:])
	binary.LittleEndian.PutUint16(b[70:], t.FTSize)
	for i, n := range []uint64{t.LP, t.FT, t.TBC} {
		binary.LittleEndian.PutUint64(b[72+i*8:], n)
	}
	copy(b[96:], id)
	binary.LittleEndian.PutUint16(b[128:], t.FeeRate)
	b[130] = t.LPPlan
	if t.Controlled {
		b[131] = 1
		b[133] = 1
	}
	if t.LPLocked {
		b[132] = 1
	}
	copy(b[134:], append([]byte{8}, []byte("POOLTAPE")...))
	return bscript.NewFromBytes(b), nil
}
func ParseTBCAMMTape(script *bscript.Script) (*TBCAMMTape, error) {
	if script == nil {
		return nil, modernError("missing Pool Tape")
	}
	b := script.Bytes()
	if len(b) != 143 || !bytes.HasPrefix(b, []byte{0, 0x6a, 0x4c, 130}) || !bytes.HasSuffix(b, append([]byte{8}, []byte("POOLTAPE")...)) || b[131] > 1 || b[132] > 1 || b[133] != b[131] {
		return nil, modernError("invalid Pool Tape envelope")
	}
	t := &TBCAMMTape{LPSize: binary.LittleEndian.Uint16(b[36:]), FTSize: binary.LittleEndian.Uint16(b[70:]), LP: binary.LittleEndian.Uint64(b[72:]), FT: binary.LittleEndian.Uint64(b[80:]), TBC: binary.LittleEndian.Uint64(b[88:]), FTGenesis: hex.EncodeToString(b[96:128]), FeeRate: binary.LittleEndian.Uint16(b[128:]), LPPlan: b[130], Controlled: b[131] != 0, LPLocked: b[132] != 0}
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
