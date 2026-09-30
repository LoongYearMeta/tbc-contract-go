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
		ID          string   `json:"id"`
		Genesis     string   `json:"genesis"`
		Plan        uint8    `json:"plan"`
		Controllers []string `json:"controllers"`
		Locked      bool     `json:"locked"`
	}
	if e := json.NewDecoder(os.Stdin).Decode(&r); e != nil {
		return e
	}
	genesis, e := bt.NewTxFromString(r.Genesis)
	if e != nil {
		return e
	}
	var hashes [][20]byte
	for _, h := range r.Controllers {
		b, e := hex.DecodeString(h)
		if e != nil || len(b) != 20 {
			return fmt.Errorf("controller hash")
		}
		var v [20]byte
		copy(v[:], b)
		hashes = append(hashes, v)
	}
	amm, e := c.NewTBCAMM(genesis, r.Plan, hashes, r.Locked)
	if e != nil {
		return e
	}
	pool, e := (q.Client{Network: "testnet"}).FetchTBCAMMInput(r.ID, amm)
	if e != nil {
		return e
	}
	_, t, s, e := amm.ReadState(pool.Parent)
	if e != nil {
		return e
	}
	n := func(v uint64) string { return strconv.FormatUint(v, 10) }
	return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{"txid": pool.Parent.TxID(), "ancestor": pool.Ancestor.TxID(), "value": n(s.Value), "lp": n(s.LP), "ft": n(s.FT), "tbc": n(s.TBC), "plan": t.LPPlan, "feeRate": t.FeeRate})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
