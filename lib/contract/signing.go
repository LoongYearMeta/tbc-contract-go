package contract

import (
	"bytes"
	"encoding/binary"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"math/big"
	"reflect"
	"sync"
)

// TransactionSigner is implemented by bec.PrivateKey and may be backed by an
// external signing device. Public-key-only preparation uses an internal signer.
type TransactionSigner interface {
	PubKey() *bec.PublicKey
	Sign([]byte) (*bec.Signature, error)
}

func signerMissing(s TransactionSigner) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	return (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) && v.IsNil()
}

type externalSigner struct{ public *bec.PublicKey }

func (s externalSigner) PubKey() *bec.PublicKey { return s.public }
func (s externalSigner) Sign([]byte) (*bec.Signature, error) {
	return &bec.Signature{R: new(big.Int).Lsh(big.NewInt(1), 255), S: new(big.Int).Lsh(big.NewInt(1), 254)}, nil
}
func external(public *bec.PublicKey) (TransactionSigner, error) {
	if public == nil {
		return nil, modernError("missing signing public key")
	}
	p, e := bec.ParsePubKey(public.SerialiseCompressed(), bec.S256())
	if e != nil {
		return nil, e
	}
	return externalSigner{p}, nil
}
func placeholderSignature() []byte {
	s, _ := (externalSigner{}).Sign(nil)
	return append(s.Serialise(), 0x41)
}

type TransactionSigningRequest struct {
	InputIndex       int
	PublicKey        [33]byte
	Sighash          [32]byte
	PreviousScript   string
	PreviousSatoshis uint64
}
type PreparedTransaction struct {
	mu        sync.Mutex
	tx        *bt.Tx
	requests  []TransactionSigningRequest
	slots     [][2]int
	fee       uint64
	finalized bool
}

func scriptPushes(b []byte) ([][3]int, error) {
	var out [][3]int
	for i := 0; i < len(b); {
		start := i
		op := b[i]
		i++
		n := uint64(0)
		switch {
		case op <= 75:
			n = uint64(op)
		case op == 0x4c:
			if i+1 > len(b) {
				return nil, modernError("truncated push")
			}
			n = uint64(b[i])
			i++
		case op == 0x4d:
			if i+2 > len(b) {
				return nil, modernError("truncated push")
			}
			n = uint64(binary.LittleEndian.Uint16(b[i:]))
			i += 2
		case op == 0x4e:
			if i+4 > len(b) {
				return nil, modernError("truncated push")
			}
			n = uint64(binary.LittleEndian.Uint32(b[i:]))
			i += 4
		default:
			continue
		}
		if n > uint64(len(b)-i) {
			return nil, modernError("truncated push data")
		}
		out = append(out, [3]int{start, i, i + int(n)})
		i += int(n)
	}
	return out, nil
}
func newPreparedTransaction(tx *bt.Tx) (*PreparedTransaction, error) {
	copyTx, e := bt.NewTxFromString(tx.String())
	if e != nil {
		return nil, e
	}
	p := &PreparedTransaction{tx: copyTx}
	total := uint64(0)
	for vin, in := range tx.Inputs {
		if in.PreviousTxScript == nil {
			return nil, modernError("missing trusted previous output")
		}
		if ^uint64(0)-total < in.PreviousTxSatoshis {
			return nil, modernError("input overflow")
		}
		total += in.PreviousTxSatoshis
		copyTx.Inputs[vin].PreviousTxScript = bscript.NewFromBytes(append([]byte{}, in.PreviousTxScript.Bytes()...))
		copyTx.Inputs[vin].PreviousTxSatoshis = in.PreviousTxSatoshis
		if in.UnlockingScript == nil {
			continue
		}
		b := in.UnlockingScript.Bytes()
		chunks, e := scriptPushes(b)
		if e != nil {
			return nil, e
		}
		found := false
		for n, c := range chunks {
			if !bytes.Equal(b[c[1]:c[2]], placeholderSignature()) {
				continue
			}
			if found || n+1 >= len(chunks) {
				return nil, modernError("ambiguous signature placeholder")
			}
			found = true
			next := chunks[n+1]
			public, e := bec.ParsePubKey(b[next[1]:next[2]], bec.S256())
			if e != nil {
				return nil, e
			}
			digest, e := tx.CalcInputSignatureHash(uint32(vin), sighash.AllForkID)
			if e != nil {
				return nil, e
			}
			r := TransactionSigningRequest{InputIndex: vin, PreviousScript: in.PreviousTxScript.ToHex(), PreviousSatoshis: in.PreviousTxSatoshis}
			copy(r.PublicKey[:], public.SerialiseCompressed())
			copy(r.Sighash[:], digest)
			p.requests = append(p.requests, r)
			p.slots = append(p.slots, [2]int{c[0], c[2]})
		}
	}
	for _, o := range tx.Outputs {
		if o.Satoshis > total {
			return nil, modernError("outputs exceed inputs")
		}
		total -= o.Satoshis
	}
	p.fee = total
	if len(p.requests) == 0 {
		return nil, modernError("no external signatures requested")
	}
	return p, nil
}
func (p *PreparedTransaction) TransactionHex() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tx.String()
}
func (p *PreparedTransaction) SigningRequests() []TransactionSigningRequest {
	return append([]TransactionSigningRequest{}, p.requests...)
}
func (p *PreparedTransaction) FeeSat() uint64 { return p.fee }

// Finalize accepts canonical low-S DER signatures including the 0x41 sighash
// byte, in request order. It verifies every signature before changing any input.
func (p *PreparedTransaction) Finalize(signatures [][]byte) (*bt.Tx, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return nil, modernError("prepared transaction already finalized")
	}
	if len(signatures) != len(p.requests) {
		return nil, modernError("signature count mismatch")
	}
	for i, r := range p.requests {
		sig := signatures[i]
		if len(sig) < 2 || len(sig) > 72 || sig[len(sig)-1] != 0x41 {
			return nil, modernError("invalid signature encoding or sighash type")
		}
		parsed, e := bec.ParseDERSignature(sig[:len(sig)-1], bec.S256())
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(parsed.Serialise(), sig[:len(sig)-1]) {
			return nil, modernError("noncanonical signature")
		}
		public, e := bec.ParsePubKey(r.PublicKey[:], bec.S256())
		if e != nil {
			return nil, e
		}
		if !parsed.Verify(r.Sighash[:], public) {
			return nil, modernError("signature does not match fixed transaction")
		}
	}
	for i, r := range p.requests {
		b := p.tx.Inputs[r.InputIndex].UnlockingScript.Bytes()
		slot := p.slots[i]
		push := bscript.NewFromBytes(nil)
		if e := push.AppendPushData(signatures[i]); e != nil {
			return nil, e
		}
		next := append([]byte{}, b[:slot[0]]...)
		next = append(next, push.Bytes()...)
		next = append(next, b[slot[1]:]...)
		p.tx.Inputs[r.InputIndex].UnlockingScript = bscript.NewFromBytes(next)
	}
	p.finalized = true
	return p.tx, nil
}
