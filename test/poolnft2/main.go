// Pool NFT 2.0 测试程序 — 对应 docs/test-cases/poolnft2.md。
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

// === 配置区（直接改这里） ===
const (
	network              = "testnet"
	wifA                 = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"             // 操作者 WIF（必填）
	ftContractTxid       = "62ac8fb58fc18d7c0bbcc7e4fa11c704ef62faa17be078e3c530d5dec9cc231f" // 标的 FT 合约 txid
	poolContractTxidInit = "a18f147d27beddafc4a92e05e40286295b839979446c57fd3ba205d4baf2cc32" // 已有池子 txid；CreatePoolNFT 之后会被覆盖

	tag          = "tbcgo"
	serviceRate  = 35 // 千分之三点五
	lpPlan       = 2
	withLockTime = false

	feeTBC = 0.01

	// 各阶段开关
	doCreate    = false
	doInit      = false
	doIncrease  = false
	doConsume   = false
	doSwapToFT  = false
	doSwapToTBC = false
	doMergeLP   = false
	doBurnLP    = true
	doMergeFT   = false
	doUnlockLP  = false

	// 数量参数
	initTBCAmount = "10"
	initFTAmount  = "1000"
	increaseTBC   = "1"
	consumeLP     = "11"
	swapTBC       = "0.1"
	swapFT        = "100"
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func mustFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	mustExit(err, "ParseFloat "+s)
	return f
}

func main() {
	if strings.TrimSpace(wifA) == "" {
		fmt.Println("请在源码顶部把 wifA 设成你的 WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifA)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	addressA := addr.AddressString
	poolContractTxid := poolContractTxidInit

	if doCreate {
		if ftContractTxid == "" {
			fmt.Println("ftContractTxid 未设置")
			os.Exit(1)
		}
		pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: network})
		mustExit(pool.InitCreate(ftContractTxid), "InitCreate")
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		txs, err := pool.CreatePoolNFT(priv, utxo, tag, serviceRate, lpPlan, withLockTime)
		mustExit(err, "CreatePoolNFT")
		_, err = api.BroadcastTXRaw(txs[0], network)
		mustExit(err, "broadcast source")
		id, err := api.BroadcastTXRaw(txs[1], network)
		mustExit(err, "broadcast mint")
		fmt.Println("pool contract id:", id)
		poolContractTxid = id
	}

	loadPool := func() *contract.PoolNFT2 {
		if poolContractTxid == "" {
			fmt.Println("poolContractTxid 未设置")
			os.Exit(1)
		}
		pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{ContractTxID: poolContractTxid, Network: network})
		mustExit(pool.InitFromContractID(), "InitFromContractID")
		return pool
	}

	if doInit {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, mustFloat(initTBCAmount)+feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.InitPoolNFT(priv, addressA, utxo, initTBCAmount, initFTAmount, 0)
		mustExit(err, "InitPoolNFT")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast init")
		fmt.Println("init pool ok")
	}

	if doIncrease {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, mustFloat(increaseTBC)+feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.IncreaseLP(priv, addressA, utxo, increaseTBC, 0)
		mustExit(err, "IncreaseLP")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast increase")
		fmt.Println("increase lp ok")
	}

	if doConsume {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		raws, err := pool.ConsumeLP(priv, addressA, utxo, consumeLP, nil)
		mustExit(err, "ConsumeLP")
		_, err = api.BroadcastTXsRaw(raws, network)
		mustExit(err, "broadcast consume")
		fmt.Println("consume lp txs:", len(raws))
	}

	if doSwapToFT {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, mustFloat(swapTBC)+feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.SwapToToken(priv, addressA, utxo, swapTBC, lpPlan)
		mustExit(err, "SwapToToken")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast swap to ft")
		fmt.Println("swap → token ok")
	}

	if doSwapToTBC {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.SwapToTBC(priv, addressA, utxo, swapFT, lpPlan)
		mustExit(err, "SwapToTBC")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast swap to tbc")
		fmt.Println("swap → tbc ok")
	}

	if doMergeLP {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.MergeFTLP(priv, utxo, nil)
		mustExit(err, "MergeFTLP")
		if raw == "" {
			fmt.Println("MergeFTLP: nothing to merge")
		} else {
			_, err = api.BroadcastTXRaw(raw, network)
			mustExit(err, "broadcast merge lp")
			fmt.Println("merge lp ok")
		}
	}

	if doBurnLP {
		pool := loadPool()
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.BurnFTLP(priv, utxo)
		mustExit(err, "BurnFTLP")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast burn lp")
		fmt.Println("burn lp ok")
	}

	if doMergeFT {
		pool := loadPool()
		const times = 10
		mergeFee := 0.005 * float64(times)
		utxo, err := api.FetchUTXO(addressA, mergeFee, network)
		mustExit(err, "FetchUTXO")
		raws, err := pool.MergeFTinPool(priv, utxo, times)
		mustExit(err, "MergeFTinPool")
		if len(raws) == 0 {
			fmt.Println("MergeFTinPool: nothing to merge")
		} else {
			_, err = api.BroadcastTXsRaw(raws, network)
			mustExit(err, "broadcast merge ft")
			fmt.Println("merge ft chained txs:", len(raws))
		}
	}

	if doUnlockLP {
		pool := loadPool()
		if !pool.WithLockTime {
			fmt.Println("pool 不是 with-lock-time，无需 UnlockFTLP")
			return
		}
		utxo, err := api.FetchUTXO(addressA, feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := pool.UnlockFTLP(priv, utxo, nil)
		mustExit(err, "UnlockFTLP")
		if raw == "" {
			fmt.Println("UnlockFTLP: 已解锁")
		} else {
			_, err = api.BroadcastTXRaw(raw, network)
			mustExit(err, "broadcast unlock")
			fmt.Println("unlock lp ok")
		}
	}
}
