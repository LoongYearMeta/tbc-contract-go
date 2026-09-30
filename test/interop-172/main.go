// JSON/stdin transaction worker. No network access and no key logging.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"math/big"
	"os"
	"strings"
)

type request struct {
	TokenDecimal   uint8     `json:"token_decimal"`
	ControllerWIF  string    `json:"controller_wif"`
	Recipient      string    `json:"recipient"`
	OrderSpecs     []lopSpec `json:"order_specs"`
	Genesis        string    `json:"genesis"`
	Controlled     bool      `json:"controlled"`
	LPLocked       bool      `json:"lp_locked"`
	LPPlan         *uint8    `json:"lp_plan"`
	Controllers    *[]string `json:"controllers"`
	LPLockTime     uint32    `json:"lp_lock_time"`
	OutputLockTime *uint32   `json:"output_lock_time"`
	UseTBC         *bool     `json:"use_tbc"`
	FirstFT        *uint64   `json:"first_ft"`
	Minimum        *uint64   `json:"minimum"`
	WIF            string    `json:"wif"`
	Operation      string    `json:"operation"`
	Funding        string    `json:"funding"`
	FundingVout    uint32    `json:"funding_vout"`
	Parents        []string  `json:"parents"`
	Vouts          []int     `json:"vouts"`
	Ancestors      []string  `json:"ancestors"`
	Amount         string    `json:"amount"`
}
type lopTokenSpec struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Tape string `json:"tape"`
}
type lopSpec struct {
	Owner      string        `json:"owner"`
	TaxAddress string        `json:"tax_address"`
	Side       string        `json:"side"`
	Volume     string        `json:"volume"`
	Price      string        `json:"price"`
	FeeRate    string        `json:"fee_rate"`
	TokenA     lopTokenSpec  `json:"token_a"`
	TokenB     *lopTokenSpec `json:"token_b"`
}

func (s lopSpec) build(address string) (c.TBCLOP, error) {
	token := func(s lopTokenSpec) (c.LOPToken, error) {
		var t c.LOPToken
		id, e := hex.DecodeString(s.ID)
		if e != nil || len(id) != 32 {
			return t, fmt.Errorf("invalid token id")
		}
		copy(t.ID[:], id)
		code, e := bscript.NewFromHexString(s.Code)
		if e != nil {
			return t, e
		}
		tape, e := bscript.NewFromHexString(s.Tape)
		if e != nil {
			return t, e
		}
		t.Asset, e = c.ParseTokenAsset(code, tape)
		return t, e
	}
	var o c.TBCLOP
	var e error
	o.Side = c.LOPBuy
	if s.Side == "sell" {
		o.Side = c.LOPSell
	}
	o.Owner = s.Owner
	o.TaxAddress = s.TaxAddress
	for _, v := range []struct {
		s string
		n *uint64
	}{{s.Volume, &o.Volume}, {s.Price, &o.Price}, {s.FeeRate, &o.FeeRate}} {
		n, ok := new(big.Int).SetString(v.s, 10)
		if !ok || !n.IsUint64() {
			return o, fmt.Errorf("invalid order amount")
		}
		*v.n = n.Uint64()
	}
	o.TokenA, e = token(s.TokenA)
	if e != nil {
		return o, e
	}
	if s.TokenB != nil {
		t, e := token(*s.TokenB)
		if e != nil {
			return o, e
		}
		o.TokenB = &t
	}
	return o, nil
}

type resolver map[string]*bt.Tx

func (r resolver) ResolveTBC20StandardAncestor(id string) (*bt.Tx, bool) {
	tx, ok := r[id]
	return tx, ok
}
func utxo(tx *bt.Tx, vout uint32) *bt.UTXO {
	id, _ := hex.DecodeString(tx.TxID())
	o := tx.Outputs[vout]
	return &bt.UTXO{TxID: id, Vout: vout, Satoshis: o.Satoshis, LockingScript: o.LockingScript}
}
func finish(p *c.PreparedTransaction, key c.TransactionSigner, controller c.TransactionSigner) ([]string, error) {
	sigs := [][]byte{}
	for _, r := range p.SigningRequests() {
		k := key
		if !bytes.Equal(r.PublicKey[:], key.PubKey().SerialiseCompressed()) {
			k = controller
		}
		if k == nil {
			return nil, fmt.Errorf("missing signer")
		}
		s, e := k.Sign(r.Sighash[:])
		if e != nil {
			return nil, e
		}
		sigs = append(sigs, append(s.Serialise(), 0x41))
	}
	tx, e := p.Finalize(sigs)
	if e != nil {
		return nil, e
	}
	return []string{tx.String()}, nil
}
func run(r request) ([]string, error) {
	decoded, err := wif.DecodeWIF(r.WIF)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime key")
	}
	key := decoded.PrivKey
	address, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		return nil, err
	}
	funding, err := bt.NewTxFromString(r.Funding)
	if err != nil {
		return nil, err
	}
	if int(r.FundingVout) >= len(funding.Outputs) {
		return nil, fmt.Errorf("funding vout out of range")
	}
	fee := utxo(funding, r.FundingVout)
	prepared := os.Getenv("TBC_INTEROP_PREPARED") != ""
	public := key.PubKey()
	parents := []*bt.Tx{}
	for _, raw := range r.Parents {
		tx, e := bt.NewTxFromString(raw)
		if e != nil {
			return nil, e
		}
		parents = append(parents, tx)
	}
	ancestors := resolver{}
	for _, raw := range r.Ancestors {
		tx, e := bt.NewTxFromString(raw)
		if e != nil {
			return nil, e
		}
		ancestors[tx.TxID()] = tx
	}
	if strings.HasPrefix(r.Operation, "legacy_stable.") {
		coin, e := c.NewStableCoin(&c.FtParams{Name: "InteropLegacy172", Symbol: "IL172", Amount: 1000, Decimal: 1})
		if e != nil {
			return nil, e
		}
		finalize := func(p *c.AdminPrepared) ([]string, error) {
			sk, _ := btcec.PrivKeyFromBytes(key.Serialise())
			sigs := [][]byte{}
			for _, req := range p.Sighashes {
				s, e := schnorr.Sign(sk, req.Sighash)
				if e != nil {
					return nil, e
				}
				sigs = append(sigs, s.Serialize())
			}
			return p.Finalize(sigs)
		}
		public := key.PubKey().SerialiseCompressed()[1:]
		if r.Operation == "legacy_stable.create" {
			p, e := coin.PrepareCreateCoin(public, key, address.AddressString, fee, funding, "")
			if e != nil {
				return nil, e
			}
			return finalize(p)
		}
		v := r.Vouts[0]
		coin.CodeScript = parents[0].Outputs[v].LockingScript.String()
		coin.TapeScript = parents[0].Outputs[v+1].LockingScript.String()
		tokens := []*util.FtUTXO{}
		proofs := []string{}
		anc := []*bt.Tx{}
		for _, p := range ancestors {
			anc = append(anc, p)
		}
		for i, p := range parents {
			v := r.Vouts[i]
			u := utxo(p, uint32(v))
			balance, e := util.GetFtBalanceFromTape(p.Outputs[v+1].LockingScript.String())
			if e != nil {
				return nil, e
			}
			tokens = append(tokens, &util.FtUTXO{TxID: u.TxID, Vout: u.Vout, LockingScript: u.LockingScript, Satoshis: u.Satoshis, FtBalance: balance})
			proof, e := util.BuildFtPrePreTxData(p, v, anc)
			if e != nil {
				return nil, e
			}
			proofs = append(proofs, proof)
		}
		var p *c.AdminPrepared
		if r.Operation == "legacy_stable.freeze" {
			p, e = coin.PrepareFreezeCoinUTXO(public, key, 2000000000, tokens, fee, parents, proofs)
		} else {
			p, e = coin.PrepareUnfreezeCoinUTXO(public, key, tokens, fee, parents, proofs)
		}
		if e != nil {
			return nil, e
		}
		return finalize(p)
	}
	if strings.HasPrefix(r.Operation, "lop.") {
		orders := []c.TBCLOP{}
		for _, s := range r.OrderSpecs {
			o, e := s.build(address.AddressString)
			if e != nil {
				return nil, e
			}
			orders = append(orders, o)
		}
		if len(orders) == 0 {
			return nil, fmt.Errorf("missing orders")
		}
		var tx *bt.Tx
		var e error
		if r.Operation == "lop.make" {
			spends := []c.ModernTokenSpend{}
			for i, p := range parents {
				spends = append(spends, c.ModernTokenSpend{Parent: p, CodeVout: r.Vouts[i], Ancestors: ancestors})
			}
			if prepared {
				p, e := orders[0].PrepareMake(public, public, spends, fee)
				if e != nil {
					return nil, e
				}
				return finish(p, key, nil)
			}
			tx, e = orders[0].Make(key, key, spends, fee)
		} else {
			spends := []c.LOPSpend{}
			for i, o := range orders {
				sp := c.LOPSpend{Order: o, Parent: parents[i], Vout: r.Vouts[i]}
				if o.Side == c.LOPBuy || o.TokenB != nil {
					sp.Token = &c.ModernTokenSpend{Parent: parents[i], CodeVout: r.Vouts[i] + 1, Ancestors: ancestors}
				}
				spends = append(spends, sp)
			}
			if r.Operation == "lop.cancel" {
				if prepared {
					p, e := spends[0].PrepareCancel(public, public, fee)
					if e != nil {
						return nil, e
					}
					return finish(p, key, nil)
				}
				tx, e = spends[0].Cancel(key, key, fee)
			} else {
				if len(spends) != 2 {
					return nil, fmt.Errorf("match requires two orders")
				}
				if prepared {
					p, e := c.PrepareMatchLOPOrders(public, public, spends[0], spends[1], fee)
					if e != nil {
						return nil, e
					}
					return finish(p, key, nil)
				}
				tx, e = c.MatchLOPOrders(key, key, spends[0], spends[1], fee)
			}
		}
		if e != nil {
			return nil, e
		}
		return []string{tx.String()}, nil
	}
	if strings.HasPrefix(r.Operation, "htlc.") {
		secret := []byte("interop172 secret")
		h := c.TBCHTLC{Sender: address.AddressString, Receiver: address.AddressString, Timelock: 1}
		copy(h.Hashlock[:], crypto.Sha256(secret))
		if r.Operation == "htlc.deploy" {
			if prepared {
				p, e := h.PrepareDeploy(public, fee, 10000)
				if e != nil {
					return nil, e
				}
				return finish(p, key, nil)
			}
			raw, e := h.Deploy(key, fee, 10000)
			return []string{raw}, e
		}
		if r.Operation == "htlc.withdraw" || r.Operation == "htlc.refund" {
			var preimage []byte
			if r.Operation == "htlc.withdraw" {
				preimage = secret
			}
			if prepared {
				p, e := h.PrepareSpend(public, parents[0], 0, preimage)
				if e != nil {
					return nil, e
				}
				return finish(p, key, nil)
			}
			raw, e := h.Spend(key, parents[0], 0, preimage)
			return []string{raw}, e
		}
		spends := []c.ModernTokenSpend{}
		if len(parents) != len(r.Vouts) {
			return nil, fmt.Errorf("invalid token parents")
		}
		for i, p := range parents {
			spends = append(spends, c.ModernTokenSpend{Parent: p, CodeVout: r.Vouts[i], Ancestors: ancestors})
		}
		var tx *bt.Tx
		var e error
		if r.Operation == "htlc.token_deploy" {
			n, ok := new(big.Int).SetString(r.Amount, 10)
			if !ok || !n.IsUint64() {
				return nil, fmt.Errorf("invalid amount")
			}
			if prepared {
				p, e := h.PrepareDeployToken(public, public, spends, fee, n.Uint64())
				if e != nil {
					return nil, e
				}
				return finish(p, key, nil)
			}
			tx, e = h.DeployToken(key, key, spends, fee, n.Uint64())
		} else {
			var preimage []byte
			if r.Operation == "htlc.token_withdraw" {
				preimage = secret
			}
			if prepared {
				p, e := h.PrepareSpendToken(public, public, spends[0], fee, preimage)
				if e != nil {
					return nil, e
				}
				return finish(p, key, nil)
			}
			tx, e = h.SpendToken(key, key, spends[0], fee, preimage)
		}
		if e != nil {
			return nil, e
		}
		return []string{tx.String()}, nil
	}
	if strings.HasPrefix(r.Operation, "timelock.") {
		var raw string
		var e error
		if r.Operation == "timelock.freeze" {
			raw, e = c.FreezeTBCWithSign(key, 10000, 1, []*bt.UTXO{fee})
		} else {
			n, ok := new(big.Int).SetString(r.Amount, 10)
			if !ok || !n.IsUint64() {
				return nil, fmt.Errorf("invalid lock time")
			}
			raw, e = c.UnfreezeTBCWithSign(key, []*bt.UTXO{utxo(parents[0], 0)}, uint32(n.Uint64()))
		}
		return []string{raw}, e
	}
	if strings.HasPrefix(r.Operation, "amm.") {
		genesis, e := bt.NewTxFromString(r.Genesis)
		if e != nil {
			return nil, e
		}
		var controllers [][20]byte
		if r.Controllers != nil {
			for _, value := range *r.Controllers {
				raw, err := hex.DecodeString(value)
				if err != nil || len(raw) != 20 {
					return nil, fmt.Errorf("controller hash")
				}
				var h [20]byte
				copy(h[:], raw)
				controllers = append(controllers, h)
			}
		} else if r.Controlled {
			var h [20]byte
			copy(h[:], crypto.Hash160(key.PubKey().SerialiseCompressed()))
			controllers = append(controllers, h)
		}
		plan := uint8(6)
		if r.LPPlan != nil {
			plan = *r.LPPlan
		}
		amm, e := c.NewTBCAMM(genesis, plan, controllers, r.LPLocked)
		if e != nil {
			return nil, e
		}
		if r.Operation == "amm.mint" {
			txs, e := amm.Mint(key, fee)
			if e != nil {
				return nil, e
			}
			if prepared {
				p, e := amm.PrepareMintTbcAmm(public, utxo(txs[0], 0))
				if e != nil {
					return nil, e
				}
				raw, e := finish(p.PreparedTransaction, key, nil)
				if e != nil {
					return nil, e
				}
				return append([]string{txs[0].String()}, raw...), nil
			}
			raws := []string{}
			for _, tx := range txs {
				raws = append(raws, tx.String())
			}
			return raws, nil
		}
		if r.Operation == "amm.lp_transfer" {
			if len(parents) != len(r.Vouts) {
				return nil, fmt.Errorf("invalid LP parents")
			}
			spends := []c.ModernTokenSpend{}
			for i, p := range parents {
				spends = append(spends, c.ModernTokenSpend{Parent: p, CodeVout: r.Vouts[i], Ancestors: ancestors})
			}
			amount, ok := new(big.Int).SetString(r.Amount, 10)
			if !ok || !amount.IsUint64() {
				return nil, fmt.Errorf("invalid amount")
			}
			if prepared {
				p, e := amm.PrepareTransferLP(public, public, fee, spends, address.AddressString, amount.Uint64(), r.OutputLockTime)
				if e != nil {
					return nil, e
				}
				return finish(p.PreparedTransaction, key, nil)
			}
			tx, e := amm.TransferLP(key, key, fee, spends, address.AddressString, amount.Uint64(), r.OutputLockTime)
			if e != nil {
				return nil, e
			}
			return []string{tx.String()}, nil
		}
		if len(parents) < 2 || len(r.Vouts) != len(parents) {
			return nil, fmt.Errorf("invalid AMM parents")
		}
		ancestor, ok := ancestors[hex.EncodeToString(parents[0].Inputs[0].PreviousTxID())]
		if !ok {
			return nil, fmt.Errorf("missing Pool ancestor")
		}
		n, ok := new(big.Int).SetString(r.Amount, 10)
		if !ok || !n.IsUint64() {
			return nil, fmt.Errorf("invalid amount")
		}
		o := c.TBCAMMOperation{Pool: c.TBCAMMPoolSpend{Parent: parents[0], Ancestor: ancestor}, PoolFT: c.ModernTokenSpend{Parent: parents[1], CodeVout: r.Vouts[1], Ancestors: ancestors}, Key: key, FeeKey: key, Fee: fee, FeeParent: funding, Recipient: address.AddressString, Amount: n.Uint64(), UseTBC: true}
		o.LPLockTime = r.LPLockTime
		if r.UseTBC != nil {
			o.UseTBC = *r.UseTBC
		}
		if r.Controlled {
			o.Controller = key
			if r.ControllerWIF != "" {
				ck, err := wif.DecodeWIF(r.ControllerWIF)
				if err != nil {
					return nil, err
				}
				o.Controller = ck.PrivKey
			}
		}
		if len(parents) > 2 {
			o.User = &c.ModernTokenSpend{Parent: parents[2], CodeVout: r.Vouts[2], Ancestors: ancestors}
		}
		switch r.Operation {
		case "amm.add":
			o.Option = 1
			_, _, state, e := amm.ReadState(parents[0])
			if e != nil {
				return nil, e
			}
			if state.LP == 0 {
				first := uint64(500)
				if r.FirstFT != nil {
					first = *r.FirstFT
				}
				o.FirstFT = &first
			}
		case "amm.remove":
			o.Option = 2
		case "amm.swap_ft":
			o.Option = 3
			o.Minimum = 1
			if r.Minimum != nil {
				o.Minimum = *r.Minimum
			}
		case "amm.swap_tbc":
			o.Option = 4
			o.Minimum = 10
			if r.Minimum != nil {
				o.Minimum = *r.Minimum
			}
		default:
			return nil, fmt.Errorf("unknown AMM operation")
		}
		if prepared {
			var cp *bec.PublicKey
			if o.Controller != nil {
				cp = o.Controller.PubKey()
			}
			p, e := amm.PrepareOperation(o, public, public, cp)
			if e != nil {
				return nil, e
			}
			return finish(p.PreparedTransaction, key, o.Controller)
		}
		result, e := amm.Operate(o)
		if e != nil {
			return nil, e
		}
		return []string{result.Transaction.String()}, nil
	}
	definition := &c.TBC20StandardDefinition{Name: "Interop172", Symbol: "I172", Supply: "1000", Decimal: r.TokenDecimal}
	switch r.Operation {
	case "stable.mint":
		coin := c.TBC20Stablecoin{Name: "Interop172", Symbol: "I172", Decimal: 0}
		code, e := c.ParseModernTokenCode(parents[0].Outputs[3].LockingScript)
		if e != nil {
			return nil, e
		}
		tape, e := c.ParseModernTokenTape(parents[0].Outputs[4].LockingScript, code)
		if e != nil {
			return nil, e
		}
		var ancestor *bt.Tx
		for _, p := range ancestors {
			if p.TxID() == parents[0].Inputs[0].PreviousTxIDStr() {
				ancestor = p
			}
		}
		amount, ok := new(big.Int).SetString(r.Amount, 10)
		if !ok || !amount.IsUint64() {
			return nil, fmt.Errorf("invalid mint amount")
		}
		tx, e := coin.Mint(key, key, address.AddressString, amount.Uint64(), fee, parents[0], ancestor, code, tape)
		if e != nil {
			return nil, e
		}
		return []string{tx.String()}, nil
	case "stable.create":
		txs, e := (c.TBC20Stablecoin{Name: "Interop172", Symbol: "I172", Decimal: 0}).Create(key, key, address.AddressString, 1000, fee, funding)
		if e != nil {
			return nil, e
		}
		raws := []string{}
		for _, tx := range txs {
			raws = append(raws, tx.String())
		}
		return raws, nil
	case "stable.transfer", "stable.merge", "stable.freeze", "stable.unfreeze":
		if len(parents) != len(r.Vouts) {
			return nil, fmt.Errorf("invalid parents")
		}
		spends := []c.ModernTokenSpend{}
		for i, v := range r.Vouts {
			spends = append(spends, c.ModernTokenSpend{Parent: parents[i], CodeVout: v, Ancestors: ancestors})
		}
		coin := c.TBC20Stablecoin{}
		var tx *bt.Tx
		var e error
		if r.Operation == "stable.freeze" || r.Operation == "stable.unfreeze" {
			var lock uint32
			if r.Operation == "stable.freeze" {
				lock = 2000000000
			}
			tx, e = coin.SetLockTime(key, key, spends, fee, lock)
		} else {
			amount, ok := new(big.Int).SetString(r.Amount, 10)
			if !ok {
				return nil, fmt.Errorf("invalid amount")
			}
			recipient := r.Recipient
			if recipient == "" {
				recipient = address.AddressString
			}
			controller, err := c.TBC20StandardAddressController(recipient)
			if err != nil {
				return nil, err
			}
			tx, e = coin.Transfer(key, key, spends, fee, []c.StablecoinRecipient{{Controller: controller, AmountRaw: amount}})
		}
		if e != nil {
			return nil, e
		}
		return []string{tx.String()}, nil
	case "standard.mint":
		token, e := c.NewTBC20Standard(c.TBC20StandardConfig{Definition: definition})
		if e != nil {
			return nil, e
		}
		result, e := token.Mint(key, address.AddressString, fee, nil)
		if e != nil {
			return nil, e
		}
		return []string{result.SourceTxRaw, result.TxRaw}, nil
	case "standard.transfer", "standard.merge":
		if len(parents) == 0 || len(parents) != len(r.Vouts) {
			return nil, fmt.Errorf("invalid token parents")
		}
		vout := r.Vouts[0]
		if vout < 0 || vout+1 >= len(parents[0].Outputs) {
			return nil, fmt.Errorf("invalid vout")
		}
		token, e := c.NewTBC20Standard(c.TBC20StandardConfig{Definition: definition, CodeScript: parents[0].Outputs[vout].LockingScript, TapeScript: parents[0].Outputs[vout+1].LockingScript})
		if e != nil {
			return nil, e
		}
		tokens := []*bt.UTXO{}
		proofs := []c.TBC20StandardAncestorResolver{}
		for i, v := range r.Vouts {
			tokens = append(tokens, utxo(parents[i], uint32(v)))
			proofs = append(proofs, ancestors)
		}
		var result *c.TBC20StandardBuildResult
		if r.Operation == "standard.merge" {
			result, e = token.Merge(key, tokens, fee, parents, proofs, nil)
		} else {
			result, e = token.Transfer(key, address.AddressString, r.Amount, tokens, fee, parents, proofs, nil)
		}
		if e != nil {
			return nil, e
		}
		return []string{result.TxRaw}, nil
	case "nft.collection":
		raw, e := (c.TBC721Standard{}).CreateCollection(address.AddressString, key, &c.CollectionData{CollectionName: "Interop172", Description: "Three language testnet verification", Supply: 3, File: ""}, []*bt.UTXO{fee})
		return []string{raw}, e
	case "nft.mint":
		if len(parents) != 1 || len(r.Vouts) != 1 {
			return nil, fmt.Errorf("invalid NFT parent")
		}
		raw, e := (c.TBC721Standard{}).CreateNFT(parents[0].TxID(), address.AddressString, key, &c.NFTData{NftName: "Interop172", Symbol: "I172", Description: "Three language testnet verification", Attributes: ""}, []*bt.UTXO{fee}, utxo(parents[0], uint32(r.Vouts[0])))
		return []string{raw}, e
	case "nft.transfer":
		if len(parents) != 1 || len(r.Ancestors) != 1 {
			return nil, fmt.Errorf("invalid NFT ancestry")
		}
		ancestor, e := bt.NewTxFromString(r.Ancestors[0])
		if e != nil {
			return nil, e
		}
		raw, e := (c.TBC721Standard{}).Transfer(key, address.AddressString, []*bt.UTXO{fee}, parents[0], ancestor, "", 0)
		return []string{raw}, e
	default:
		return nil, fmt.Errorf("unknown operation")
	}
}
func main() {
	var r request
	if err := json.NewDecoder(os.Stdin).Decode(&r); err != nil {
		fmt.Fprintln(os.Stderr, "invalid request")
		os.Exit(1)
	}
	if r.Operation == "validate" {
		tx, e := bt.NewTxFromString(r.Funding)
		if e != nil {
			panic(e)
		}
		parents := map[string]*bt.Tx{}
		for _, raw := range r.Parents {
			p, e := bt.NewTxFromString(raw)
			if e != nil {
				panic(e)
			}
			parents[p.TxID()] = p
		}
		previous := []*bt.Output{}
		for _, in := range tx.Inputs {
			p := parents[hex.EncodeToString(in.PreviousTxID())]
			if p == nil || int(in.PreviousTxOutIndex) >= len(p.Outputs) {
				panic("missing validation parent")
			}
			previous = append(previous, p.Outputs[in.PreviousTxOutIndex])
		}
		report, e := c.ValidateTBCAMMTransaction(tx, previous)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	raws, err := run(r)
	if err != nil {
		fmt.Fprintln(os.Stderr, "interop worker failed:", err)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"raws": raws})
}
