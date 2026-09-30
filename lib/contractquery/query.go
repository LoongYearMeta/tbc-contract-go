// Package contractquery authenticates new-generation index records against raw parents.
// It is separate from api to keep the existing contract -> api dependency acyclic.
package contractquery

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"math/big"
	"strconv"
)

type Client struct{ Network string }
type TokenInfo struct {
	ContractID, Name, Symbol string
	Decimal                  uint8
	TotalSupply              *big.Int
	Asset                    *c.TokenAsset
}
type TokenUTXO struct {
	Parent *bt.Tx
	Vout   int
	Asset  *c.TokenAsset
}

func integer(v json.RawMessage) (*big.Int, error) {
	var s string
	if len(v) > 0 && v[0] == '"' {
		if e := json.Unmarshal(v, &s); e != nil {
			return nil, e
		}
	} else {
		s = string(v)
	}
	if s == "" {
		return nil, fmt.Errorf("missing integer")
	}
	for _, b := range s {
		if b < '0' || b > '9' {
			return nil, fmt.Errorf("invalid integer")
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.BitLen() > 128 {
		return nil, fmt.Errorf("integer overflow")
	}
	return n, nil
}
func stringField(v map[string]json.RawMessage, k string) (string, error) {
	var s string
	e := json.Unmarshal(v[k], &s)
	return s, e
}
func alias(v map[string]json.RawMessage, a, b string) (*big.Int, error) {
	raw, ok := v[a]
	if !ok {
		raw = v[b]
	}
	n, e := integer(raw)
	if e != nil {
		return nil, e
	}
	if v[a] != nil && v[b] != nil {
		other, e := integer(v[b])
		if e != nil || other.Cmp(n) != 0 {
			return nil, fmt.Errorf("conflicting index aliases")
		}
	}
	return n, nil
}
func validID(id string) error {
	b, e := hex.DecodeString(id)
	if e != nil || len(b) != 32 {
		return fmt.Errorf("invalid transaction id")
	}
	return nil
}
func (q Client) data(path string) (map[string]json.RawMessage, error) {
	raw, e := api.FetchContractData(path, q.Network)
	if e != nil {
		return nil, e
	}
	var data map[string]json.RawMessage
	e = json.Unmarshal(raw, &data)
	return data, e
}
func (q Client) FetchTokenInfo(id string, stable bool) (*TokenInfo, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	route := "ft/info/contract/"
	field := "amount"
	if stable {
		route = "stablecoin/info/stablecoinid/"
		field = "supply"
	}
	d, e := q.data(route + id)
	if e != nil {
		return nil, e
	}
	code, e := stringField(d, "code_script")
	if e != nil {
		return nil, e
	}
	tape, e := stringField(d, "tape_script")
	if e != nil {
		return nil, e
	}
	cs, e := bscript.NewFromHexString(code)
	if e != nil {
		return nil, e
	}
	ts, e := bscript.NewFromHexString(tape)
	if e != nil {
		return nil, e
	}
	a, e := c.ParseTokenAsset(cs, ts)
	if e != nil {
		return nil, e
	}
	if stable && (a.Modern == nil || a.Modern.Kind != c.ModernStablecoin) || !stable && a.Modern != nil {
		return nil, fmt.Errorf("token family mismatch")
	}
	decimal, e := integer(d["decimal"])
	if e != nil || decimal.Cmp(big.NewInt(18)) > 0 {
		return nil, fmt.Errorf("invalid decimal")
	}
	supply, e := integer(d[field])
	if e != nil {
		return nil, e
	}
	name, e := stringField(d, "name")
	if e != nil {
		return nil, e
	}
	symbol, e := stringField(d, "symbol")
	if e != nil {
		return nil, e
	}
	return &TokenInfo{ContractID: id, Name: name, Symbol: symbol, Decimal: uint8(decimal.Uint64()), TotalSupply: supply, Asset: a}, nil
}
func (q Client) FetchTokenUTXOs(id string, a *c.TokenAsset, owner [21]byte) ([]TokenUTXO, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	if a == nil {
		return nil, fmt.Errorf("missing token")
	}
	code, e := a.ReplaceController(owner)
	if e != nil {
		return nil, e
	}
	route := "ft/utxo/combinescript/" + hex.EncodeToString(owner[:]) + "/contract/" + id
	lp := false
	if a.Modern != nil {
		if a.Modern.Kind == c.ModernStablecoin {
			route = "stablecoin/utxo/combinescript/" + hex.EncodeToString(owner[:]) + "/stablecoinid/" + id
		} else {
			lp = true
			h := crypto.Sha256(code.Bytes())
			for i, j := 0, len(h)-1; i < j; i, j = i+1, j-1 {
				h[i], h[j] = h[j], h[i]
			}
			route = "pool/lputxo/scriptpubkeyhash/" + hex.EncodeToString(h)
		}
	}
	d, e := q.data(route)
	if e != nil {
		return nil, e
	}
	var records []map[string]json.RawMessage
	if e = json.Unmarshal(d["utxos"], &records); e != nil {
		return nil, e
	}
	if records == nil {
		return nil, fmt.Errorf("missing UTXO array")
	}
	cache := map[string]*bt.Tx{}
	seen := map[string]bool{}
	result := []TokenUTXO{}
	for _, r := range records {
		id, e := stringField(r, "txid")
		if e != nil {
			return nil, e
		}
		if e = validID(id); e != nil {
			return nil, e
		}
		index, e := alias(r, "index", "vout")
		if e != nil || index.BitLen() > 32 {
			return nil, fmt.Errorf("invalid vout")
		}
		v := int(index.Uint64())
		point := id + ":" + strconv.Itoa(v)
		if seen[point] {
			return nil, fmt.Errorf("duplicate outpoint")
		}
		seen[point] = true
		p, ok := cache[id]
		if !ok {
			p, e = api.FetchTXRaw(id, q.Network)
			if e != nil {
				return nil, e
			}
			if p.TxID() != id {
				return nil, fmt.Errorf("raw txid mismatch")
			}
			cache[id] = p
		}
		asset, e := c.ReadTokenAsset(c.ModernTokenSpend{Parent: p, CodeVout: v})
		if e != nil {
			return nil, e
		}
		valueField, balanceField := "tbc_value", "ft_value"
		if lp {
			valueField, balanceField = "tbc_balance", "lp_balance"
		}
		value, e := alias(r, valueField, "value")
		if e != nil {
			return nil, e
		}
		balance, e := alias(r, balanceField, "ftBalance")
		if e != nil {
			return nil, e
		}
		if value.Cmp(big.NewInt(500)) != 0 || balance.Cmp(asset.Balance) != 0 || !bytes.Equal(asset.Code.Bytes(), code.Bytes()) {
			return nil, fmt.Errorf("index differs from raw Code/Tape")
		}
		if raw, ok := r["lock_time"]; ok {
			lock, e := integer(raw)
			if e != nil || lock.Cmp(new(big.Int).SetUint64(uint64(asset.LockTime))) != 0 {
				return nil, fmt.Errorf("indexed lock time mismatch")
			}
		}
		result = append(result, TokenUTXO{Parent: p, Vout: v, Asset: asset})
	}
	return result, nil
}
func (q Client) FetchTokenBalance(id string, a *c.TokenAsset, owner [21]byte) (*big.Int, error) {
	list, e := q.FetchTokenUTXOs(id, a, owner)
	if e != nil {
		return nil, e
	}
	n := new(big.Int)
	for _, u := range list {
		n.Add(n, u.Asset.Balance)
	}
	return n, nil
}
func (q Client) FetchTokenAncestors(parent *bt.Tx, vout int) ([]*bt.Tx, error) {
	a, e := c.ReadTokenAsset(c.ModernTokenSpend{Parent: parent, CodeVout: vout})
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	out := []*bt.Tx{}
	for i, n := range a.Amounts {
		if n == 0 {
			continue
		}
		if i >= len(parent.Inputs) {
			return nil, fmt.Errorf("Tape references missing input")
		}
		in := parent.Inputs[i]
		id := hex.EncodeToString(in.PreviousTxID())
		if seen[id] {
			continue
		}
		seen[id] = true
		p, e := api.FetchTXRaw(id, q.Network)
		if e != nil {
			return nil, e
		}
		if p.TxID() != id || int(in.PreviousTxOutIndex) >= len(p.Outputs) {
			return nil, fmt.Errorf("ancestor mismatch")
		}
		out = append(out, p)
	}
	return out, nil
}
func (q Client) FetchTBC721Output(id string, vout int) (*bt.Tx, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	p, e := api.FetchTXRaw(id, q.Network)
	if e != nil {
		return nil, e
	}
	if p.TxID() != id || vout < 0 || vout+2 >= len(p.Outputs) {
		return nil, fmt.Errorf("NFT parent/vout mismatch")
	}
	if _, _, e = c.ParseTBC721StandardCode(p.Outputs[vout].LockingScript); e != nil {
		return nil, e
	}
	if p.Outputs[vout].Satoshis != 200 || p.Outputs[vout+1].Satoshis < 100 || p.Outputs[vout+2].Satoshis != 0 || !bytes.HasPrefix(p.Outputs[vout+2].LockingScript.Bytes(), []byte{0, 0x6a}) {
		return nil, fmt.Errorf("NFT triplet layout")
	}
	return p, nil
}

func (q Client) FetchTBCAMMInput(id string, amm *c.TBCAMM) (c.TBCAMMPoolSpend, error) {
	var result c.TBCAMMPoolSpend
	if e := validID(id); e != nil {
		return result, e
	}
	if amm == nil {
		return result, fmt.Errorf("missing AMM definition")
	}
	d, e := q.data("pool/poolinfo/poolid/" + id)
	if e != nil {
		return result, e
	}
	current, e := stringField(d, "txid")
	if e != nil {
		return result, e
	}
	if e = validID(current); e != nil {
		return result, e
	}
	v, e := integer(d["vout"])
	if e != nil || v.Sign() != 0 {
		return result, fmt.Errorf("AMM vout must be zero")
	}
	p, e := api.FetchTXRaw(current, q.Network)
	if e != nil {
		return result, e
	}
	if p.TxID() != current {
		return result, fmt.Errorf("AMM parent id mismatch")
	}
	_, t, _, e := amm.ReadState(p)
	if e != nil {
		return result, e
	}
	for field, want := range map[string]uint64{"value": p.Outputs[0].Satoshis, "lp_balance": t.LP, "token_balance": t.FT, "tbc_balance": t.TBC, "service_fee_rate": uint64(t.FeeRate)} {
		n, e := integer(d[field])
		if e != nil || n.Cmp(new(big.Int).SetUint64(want)) != 0 {
			return result, fmt.Errorf("AMM indexed %s differs from raw state", field)
		}
	}
	for field, want := range map[string]string{"pool_code_script": p.Outputs[0].LockingScript.String(), "ft_lp_partial_hash": hex.EncodeToString(t.LPHash[:]), "ft_contract_id": t.FTGenesis} {
		got, e := stringField(d, field)
		if e != nil || got != want {
			return result, fmt.Errorf("AMM indexed %s mismatch", field)
		}
	}
	if len(p.Inputs) == 0 {
		return result, fmt.Errorf("missing AMM ancestor")
	}
	input := p.Inputs[0]
	ancestor, e := api.FetchTXRaw(input.PreviousTxIDStr(), q.Network)
	if e != nil {
		return result, e
	}
	if ancestor.TxID() != input.PreviousTxIDStr() || int(input.PreviousTxOutIndex) >= len(ancestor.Outputs) {
		return result, fmt.Errorf("AMM ancestor mismatch")
	}
	return c.TBCAMMPoolSpend{Parent: p, Ancestor: ancestor}, nil
}
