// PiggyBank 测试程序 — 对应 docs/test-cases/piggybank.md。
package main

import (
	"fmt"
	"os"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

// === 配置区（直接改这里） ===
const (
	network   = "testnet"
	wifStr    = "" // 操作者 WIF（必填）
	amountSat = uint64(200_000)         // 冻结金额（satoshi）
	lockTime  = uint32(1_774_410_989)   // 解锁阈值（unix 秒 / 区块高度）

	doFreeze   = false
	doUnfreeze = false
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func main() {
	if strings.TrimSpace(wifStr) == "" {
		fmt.Println("请在源码顶部把 wifStr 设成你的 WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	address := addr.AddressString

	if doFreeze {
		utxos, err := api.GetUTXOs(address, float64(amountSat)/1e6+0.001, network)
		mustExit(err, "GetUTXOs")
		raw, err := contract.FreezeTBCWithSign(priv, amountSat, lockTime, utxos)
		mustExit(err, "FreezeTBCWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast freeze")
		fmt.Println("freeze:", txid)
	}

	if doUnfreeze {
		frozen, err := api.FetchUnfrozenUTXOList(address, network)
		mustExit(err, "FetchUnfrozenUTXOList")
		if len(frozen) == 0 {
			fmt.Println("没有可解锁的 PiggyBank UTXO")
			return
		}
		utxos := make([]*bt.UTXO, 0, len(frozen))
		for _, f := range frozen {
			u, err := api.FrozenToBTUTXO(f)
			mustExit(err, "FrozenToBTUTXO")
			utxos = append(utxos, u)
		}
		headers, err := api.FetchBlockHeaders(0, 1, network)
		mustExit(err, "FetchBlockHeaders")
		if len(headers) == 0 {
			fmt.Println("FetchBlockHeaders: empty")
			os.Exit(1)
		}
		raw, err := contract.UnfreezeTBCWithSign(priv, utxos, uint32(headers[0].Height))
		mustExit(err, "UnfreezeTBCWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast unfreeze")
		fmt.Println("unfreeze:", txid)
	}
}
