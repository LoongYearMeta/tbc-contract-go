// OrderBook 测试程序 — 对应 docs/test-cases/orderbook.md。
//
// 仅演示「创建卖单（带签名）」一条最常被复现的路径。撤销 / 撮合 / 取消买单
// 需要预先存在的链上 UTXO 与一连串前置交易，只在文档里给参数表与方法名 stub。
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

// === 配置区（直接改这里） ===
const (
	network        = "testnet"
	wifStr         = "" // 操作者 WIF（必填）
	ftContractTxid = "" // FT 合约 txid（必填）
	taxAddress     = "" // 撮合手续费地址（必填）

	saleVolume = uint64(10_000_000) // 卖出 TBC 数量（精度 6 整数）
	unitPrice  = uint64(10_100_000) // 每单位 TBC 标价（精度 6 整数）
	feeRate    = uint64(100)        // 万分之一

	doSell = false
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
	holdAddr := addr.AddressString

	if ftContractTxid == "" || taxAddress == "" {
		fmt.Println("缺少 ftContractTxid 或 taxAddress")
		os.Exit(1)
	}

	if doSell {
		ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
		mustExit(err, "FetchFtInfo")

		utxos, err := api.GetUTXOs(holdAddr, float64(saleVolume)/1e6+0.001, network)
		mustExit(err, "GetUTXOs")

		order := contract.NewOrderBook()
		raw, err := order.MakeSellOrderWithSign(priv, taxAddress, saleVolume, unitPrice, feeRate, ftContractTxid, ftInfo.CodeScript, utxos)
		mustExit(err, "MakeSellOrderWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast sell")
		fmt.Println("sell order:", txid)
	}

	// 撤销卖单（CancelSellOrderWithSign）/ 创建买单（BuildBuyOrderTX +
	// FillSigsMakeBuyOrder）/ 撤销买单（BuildCancelBuyOrderTX +
	// FillSigsCancelBuyOrder）/ 撮合（MatchOrder）这几条路径都需要先准备好对应
	// 的链上 UTXO，请参考 docs/test-cases/orderbook.md 中的 stub 段。
}
