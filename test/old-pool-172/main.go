// PoolNFT v2 test worker. Reads testnet state; never broadcasts.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
	"os"
)

func run() ([]string, error) {
	var r struct {
		WIF         string   `json:"wif"`
		Operation   string   `json:"operation"`
		Funding     string   `json:"funding"`
		FundingVout uint32   `json:"funding_vout"`
		Genesis     string   `json:"genesis"`
		PoolID      string   `json:"pool_id"`
		Plan        int      `json:"plan"`
		Rate        int      `json:"rate"`
		Keys        []string `json:"keys"`
		LockedLP    bool     `json:"locked_lp"`
		LPCost      uint64   `json:"lp_cost"`
		LockTime    uint32   `json:"lock_time"`
		Amount      string   `json:"amount"`
	}
	if e := json.NewDecoder(os.Stdin).Decode(&r); e != nil {
		return nil, e
	}
	k, e := wif.DecodeWIF(r.WIF)
	if e != nil {
		return nil, e
	}
	key := k.PrivKey
	address, e := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if e != nil {
		return nil, e
	}
	funding, e := bt.NewTxFromString(r.Funding)
	if e != nil {
		return nil, e
	}
	id, e := hex.DecodeString(funding.TxID())
	if e != nil {
		return nil, e
	}
	o := funding.Outputs[r.FundingVout]
	fee := &bt.UTXO{TxID: id, Vout: r.FundingVout, Satoshis: o.Satoshis, LockingScript: o.LockingScript}
	if r.Operation == "ft.mint" {
		ft, e := c.NewFT(&c.FtParams{Name: "OldPool172", Symbol: "OP172", Amount: 1000000, Decimal: 6})
		if e != nil {
			return nil, e
		}
		return ft.MintFT(key, address.AddressString, fee)
	}
	pool := c.NewPoolNFT2(&c.PoolNFT2Config{ContractTxID: r.PoolID, Network: "testnet"})
	if r.Operation == "pool.create" {
		g, e := bt.NewTxFromString(r.Genesis)
		if e != nil {
			return nil, e
		}
		if e = pool.InitCreate(g.TxID()); e != nil {
			return nil, e
		}
		if len(r.Keys) == 0 {
			return pool.CreatePoolNFT(key, fee, "Old172", r.Rate, r.Plan, r.LockedLP)
		}
		return pool.CreatePoolNFTWithLock(key, fee, "Old172", address.AddressString, float64(r.LPCost)/1000000, r.Keys, r.Rate, r.Plan, r.LockedLP)
	}
	if e = pool.InitFromContractID(); e != nil {
		return nil, e
	}
	var raw string
	var lock *uint32
	if r.LockedLP {
		lock = &r.LockTime
	}
	switch r.Operation {
	case "pool.init":
		raw, e = pool.InitPoolNFT(key, address.AddressString, fee, r.Amount, "100", r.LockTime)
	case "pool.add":
		raw, e = pool.IncreaseLP(key, address.AddressString, fee, r.Amount, r.LockTime)
	case "pool.swap_ft":
		raw, e = pool.SwapToToken(key, address.AddressString, fee, r.Amount, r.Plan)
	case "pool.swap_tbc":
		raw, e = pool.SwapToTBC(key, address.AddressString, fee, r.Amount, r.Plan)
	case "pool.remove":
		return pool.ConsumeLP(key, address.AddressString, fee, r.Amount, lock)
	case "pool.merge_lp":
		raw, e = pool.MergeFTLP(key, fee, lock)
	case "pool.burn_lp":
		raw, e = pool.BurnFTLP(key, fee)
	case "pool.merge_ft":
		return pool.MergeFTinPool(key, fee, 1)
	default:
		return nil, fmt.Errorf("unknown operation")
	}
	if e != nil {
		return nil, e
	}
	if raw == "" {
		return []string{}, nil
	}
	return []string{raw}, nil
}
func main() {
	raws, e := run()
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"raws": raws})
}
