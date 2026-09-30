package contract

import (
	"bytes"
	"encoding/binary"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"sync"
)

type modernAdminSigner struct {
	key    *bec.PrivateKey
	public []byte
}

func (s modernAdminSigner) valid() bool { return s.key != nil || len(s.public) == 32 }
func (s modernAdminSigner) xonly() []byte {
	if s.key != nil {
		return modernXOnly(s.key)
	}
	return s.public
}
func (s modernAdminSigner) compressed() []byte {
	if s.key != nil {
		return s.key.PubKey().SerialiseCompressed()
	}
	return append([]byte{2}, s.public...)
}
func (s modernAdminSigner) sign(digest []byte, xonly bool) ([]byte, error) {
	if s.key != nil {
		return modernSign(s.key, digest, xonly)
	}
	if !xonly {
		return nil, modernError("external administrator must use BIP340")
	}
	return append(make([]byte, 64), 0x41), nil
}

// PreparedAdminSighash is an immutable-by-copy external signing request.
type PreparedAdminSighash struct {
	TransactionIndex, InputIndex int
	Sighash                      [32]byte
}
type PreparedAdmin struct {
	mu        sync.Mutex
	txs       []*bt.Tx
	requests  []PreparedAdminSighash
	public    [32]byte
	finalized bool
}

func newPreparedAdmin(txs []*bt.Tx, indices [][2]int, public [32]byte) (*PreparedAdmin, error) {
	if _, e := schnorr.ParsePubKey(public[:]); e != nil {
		return nil, e
	}
	p := &PreparedAdmin{txs: txs, public: public}
	for _, point := range indices {
		ti, vin := point[0], point[1]
		digest, e := txs[ti].CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
		if e != nil {
			return nil, e
		}
		r := PreparedAdminSighash{TransactionIndex: ti, InputIndex: vin}
		copy(r.Sighash[:], digest)
		p.requests = append(p.requests, r)
	}
	return p, nil
}
func (p *PreparedAdmin) Sighashes() []PreparedAdminSighash {
	return append([]PreparedAdminSighash{}, p.requests...)
}
func (p *PreparedAdmin) Transactions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, len(p.txs))
	for i, t := range p.txs {
		result[i] = t.String()
	}
	return result
}
func replaceAdminPlaceholder(script *bscript.Script, sig []byte) (*bscript.Script, error) {
	b := append([]byte{}, script.Bytes()...)
	i, count := 0, 0
	for i < len(b) {
		op := b[i]
		i++
		n := uint64(0)
		switch {
		case op >= 1 && op <= 75:
			n = uint64(op)
		case op == 0x4c:
			if i >= len(b) {
				return nil, modernError("truncated prepared push")
			}
			n = uint64(b[i])
			i++
		case op == 0x4d:
			if i+2 > len(b) {
				return nil, modernError("truncated prepared push")
			}
			n = uint64(binary.LittleEndian.Uint16(b[i : i+2]))
			i += 2
		case op == 0x4e:
			if i+4 > len(b) {
				return nil, modernError("truncated prepared push")
			}
			n = uint64(binary.LittleEndian.Uint32(b[i : i+4]))
			i += 4
		}
		if n > uint64(len(b)-i) {
			return nil, modernError("truncated prepared push")
		}
		if n == 65 && bytes.Equal(b[i:i+64], make([]byte, 64)) && b[i+64] == 0x41 {
			copy(b[i:i+64], sig)
			count++
		}
		i += int(n)
	}
	if count != 1 {
		return nil, modernError("prepared signature placeholder is not unique")
	}
	return bscript.NewFromBytes(b), nil
}

// Finalize verifies all external BIP340 signatures before replacing any witness.
func (p *PreparedAdmin) Finalize(signatures [][]byte) ([]*bt.Tx, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return nil, modernError("prepared transaction already finalized")
	}
	if len(signatures) != len(p.requests) {
		return nil, modernError("administrator signature count mismatch")
	}
	pk, e := schnorr.ParsePubKey(p.public[:])
	if e != nil {
		return nil, e
	}
	scripts := make([]*bscript.Script, len(p.requests))
	for i, r := range p.requests {
		sig, e := schnorr.ParseSignature(signatures[i])
		if e != nil || !sig.Verify(r.Sighash[:], pk) {
			return nil, modernError("invalid administrator signature")
		}
		scripts[i], e = replaceAdminPlaceholder(p.txs[r.TransactionIndex].Inputs[r.InputIndex].UnlockingScript, signatures[i])
		if e != nil {
			return nil, e
		}
	}
	for i, r := range p.requests {
		p.txs[r.TransactionIndex].Inputs[r.InputIndex].UnlockingScript = scripts[i]
	}
	result := make([]*bt.Tx, len(p.txs))
	for i, tx := range p.txs {
		result[i], e = bt.NewTxFromString(tx.String())
		if e != nil {
			return nil, e
		}
	}
	p.finalized = true
	return result, nil
}
func (c TBC20Stablecoin) PrepareCreate(public [32]byte, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, funding *bt.Tx, message string) (*PreparedAdmin, error) {
	txs, e := c.createSigned(modernAdminSigner{public: public[:]}, feeKey, recipient, amount, fee, funding, message)
	if e != nil {
		return nil, e
	}
	return newPreparedAdmin(txs, [][2]int{{1, 0}, {1, 1}}, public)
}
func (c TBC20Stablecoin) PrepareMint(public [32]byte, feeKey *bec.PrivateKey, recipient string, amount uint64, fee *bt.UTXO, parent, ancestor *bt.Tx, previousCode *ModernTokenCode, previousTape *ModernTokenTape, message string) (*PreparedAdmin, error) {
	tx, e := c.mintSigned(modernAdminSigner{public: public[:]}, feeKey, recipient, amount, fee, parent, ancestor, previousCode, previousTape, message)
	if e != nil {
		return nil, e
	}
	return newPreparedAdmin([]*bt.Tx{tx}, [][2]int{{0, 0}, {0, 1}}, public)
}
func (c TBC20Stablecoin) PrepareSetLockTime(public [32]byte, feeKey *bec.PrivateKey, spends []ModernTokenSpend, fee *bt.UTXO, lock uint32) (*PreparedAdmin, error) {
	tx, e := c.setLockTimeSigned(modernAdminSigner{public: public[:]}, feeKey, spends, fee, lock)
	if e != nil {
		return nil, e
	}
	indices := make([][2]int, len(spends))
	for i := range indices {
		indices[i] = [2]int{0, i}
	}
	return newPreparedAdmin([]*bt.Tx{tx}, indices, public)
}
