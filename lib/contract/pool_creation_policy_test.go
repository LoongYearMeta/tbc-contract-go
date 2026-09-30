package contract

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPoolV2CreationRejectsStablecoinForBothEntrypoints(t *testing.T) {
	fx, _ := stableFeeFixture(t)
	code, e := GetCoinMintCode(strings.Repeat("11", 20), fx.sender, strings.Repeat("22", 32), 91)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": "200", "data": map[string]interface{}{"code_script": code.ToHex(), "tape_script": fx.tape.ToHex(), "amount": "1000", "decimal": 6, "name": "Stable", "symbol": "ST"}})
	}))
	defer server.Close()
	for _, lockedLP := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			p := NewPoolNFT2(&PoolNFT2Config{Network: server.URL + "/"})
			if e = p.InitCreate(strings.Repeat("31", 32)); e != nil {
				t.Fatal(e)
			}
			var raws []string
			if controlled {
				raws, e = p.CreatePoolNFTWithLock(fx.senderKey, fx.feeUTXO, "reject", fx.sender, 0.000024, []string{hex.EncodeToString(fx.senderKey.PubKey().SerialiseCompressed())}, 35, 1, lockedLP)
			} else {
				raws, e = p.CreatePoolNFT(fx.senderKey, fx.feeUTXO, "reject", 35, 1, lockedLP)
			}
			if e == nil || !strings.Contains(e.Error(), "stablecoin pools are not supported") || len(raws) != 0 {
				t.Fatalf("controlled=%v lockedLP=%v raws=%d error=%v", controlled, lockedLP, len(raws), e)
			}
		}
	}
	p := NewPoolNFT2(&PoolNFT2Config{Network: server.URL + "/"})
	p.FtAContractTxID = strings.Repeat("31", 32)
	p.WithLockTime = true
	operations := map[string]func() (string, error){
		"init":      func() (string, error) { return p.InitPoolNFT(fx.senderKey, fx.sender, fx.feeUTXO, "1", "1", 0) },
		"swap":      func() (string, error) { return p.SwapToToken(fx.senderKey, fx.sender, fx.feeUTXO, "1", 1) },
		"unlock LP": func() (string, error) { return p.UnlockFTLP(fx.senderKey, fx.feeUTXO, nil) },
		"merge LP":  func() (string, error) { return p.MergeFTLP(fx.senderKey, fx.feeUTXO, nil) },
		"burn LP":   func() (string, error) { return p.BurnFTLP(fx.senderKey, fx.feeUTXO) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			raw, err := operation()
			if raw != "" || err == nil || !strings.Contains(err.Error(), "stablecoin pools are not supported") {
				t.Fatalf("raw=%s error=%v", raw, err)
			}
		})
	}
}

func TestAMMRejectsStablecoinGenesis(t *testing.T) {
	f := loadCompletion(t)
	mint := completionTx(t, f.Stable.Mint)
	for _, locked := range []bool{false, true} {
		if _, err := NewTBCAMM(mint, 1, nil, locked); err == nil {
			t.Fatal("accepted stablecoin AMM")
		}
	}
}
