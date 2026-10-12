// Read-only AMM index/raw-state comparison worker.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	c "github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	q "github.com/LoongYearMeta/tbc-contract-go/lib/contractquery"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"os"
	"strconv"
)

func run() error {
	var r struct {
		ID      string `json:"id"`
		Genesis string `json:"genesis"`
		Pool    string `json:"pool"`
	}
	if e := json.NewDecoder(os.Stdin).Decode(&r); e != nil {
		return e
	}
	genesis, e := bt.NewTxFromString(r.Genesis)
	if e != nil {
		return e
	}
	initial, e := bt.NewTxFromString(r.Pool)
	if e != nil {
		return e
	}
	amm, e := c.TBCAMMFromPool(initial, genesis, "")
	if e != nil {
		return e
	}
	pool, e := (q.Client{Network: "testnet"}).FetchTBCAMMInput(r.ID, amm)
	if e != nil {
		return e
	}
	code, t, s, e := amm.ReadState(pool.Parent)
	if e != nil {
		return e
	}
	n := func(v uint64) string { return strconv.FormatUint(v, 10) }
	return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"txid": pool.Parent.TxID(), "ancestor": pool.Ancestor.TxID(), "value": n(s.Value), "lp": n(s.LP), "ft": n(s.FT), "tbc": n(s.TBC), "feeRates": []uint16{t.Fees.LP, t.Fees.Tax, t.Fees.ServiceProvider, t.Fees.Protocol}, "feeAddressHash": hex.EncodeToString(t.FeeAddress[:]), "tag": hex.EncodeToString(code.ServiceProviderTag)})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
