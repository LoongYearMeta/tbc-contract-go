package main

import (
	"encoding/json"
	"fmt"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	q "github.com/LoongYearMeta/tbc-contract-go/lib/contractquery"
	"os"
)

func run() error {
	var r struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Stable  bool   `json:"stable"`
	}
	if e := json.NewDecoder(os.Stdin).Decode(&r); e != nil {
		return e
	}
	api := q.Client{Network: "testnet"}
	info, e := api.FetchTokenInfo(r.ID, r.Stable)
	if e != nil {
		return e
	}
	owner, e := c.TBC20StandardAddressController(r.Address)
	if e != nil {
		return e
	}
	utxos, e := api.FetchTokenUTXOs(r.ID, info.Asset, owner)
	if e != nil {
		return e
	}
	records := []map[string]interface{}{}
	for _, u := range utxos {
		parents, e := api.FetchTokenAncestors(u.Parent, u.Vout)
		if e != nil {
			return e
		}
		ids := []string{}
		for _, p := range parents {
			ids = append(ids, p.TxID())
		}
		records = append(records, map[string]interface{}{"txid": u.Parent.TxID(), "vout": u.Vout, "balance": u.Asset.Balance.String(), "lockTime": u.Asset.LockTime, "ancestors": ids})
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"name": info.Name, "symbol": info.Symbol, "decimal": info.Decimal, "supply": info.TotalSupply.String(), "utxos": records})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
