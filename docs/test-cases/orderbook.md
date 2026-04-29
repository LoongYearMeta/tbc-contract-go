# OrderBook 测试场景（Go）

参照 TS：[`../tbc-contract/docs/orderBook.md`](../../tbc-contract/docs/orderBook.md)。覆盖创建卖单、撤销卖单、创建买单（用 token 买 tbc）、撤销买单、撮合交易五条主路径。

> 数值约定：八字节小端，参数类型统一 `uint64`，精度（除 FT 外）固定 6 位。
> 工作流：先用 `Build*` 构造未签名 raw → 钱包对每个输入签名 → `FillSigs*` 注入签名 → 广播。
> 由于撤销 / 撮合路径需要链上多份 UTXO + 前置交易，本示例只完整演示「创建卖单」「创建买单」「撮合」三段；「撤销」段给出参数表与方法名，结构与 TS 文档一一对应。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF` | 是 | 操作者 WIF（演示一方完整流程） |
| `TBC_NETWORK` | 否 | 默认 `"testnet"` |
| `FT_CONTRACT_TXID` | 是 | FT 合约 txid |
| `OB_TAX_ADDRESS` | 是 | 撮合手续费地址 |
| `OB_FT_FEE_ADDR` / `OB_TBC_FEE_ADDR` | 撮合时必填 | 撮合者收取的 FT / TBC 手续费地址 |

## 最小可执行脚本

```go
package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	network := env("TBC_NETWORK", "testnet")
	wifStr := strings.TrimSpace(os.Getenv("TBC_WIF"))
	if wifStr == "" {
		fmt.Println("请设置 TBC_WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	must(err)
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	must(err)
	holdAddress := addr.AddressString
	pubKey := hex.EncodeToString(priv.PubKey().SerialiseCompressed())

	ftContractTxid := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID"))
	taxAddress := strings.TrimSpace(os.Getenv("OB_TAX_ADDRESS"))
	if ftContractTxid == "" || taxAddress == "" {
		fmt.Println("请设置 FT_CONTRACT_TXID 与 OB_TAX_ADDRESS")
		os.Exit(1)
	}

	mainnet := strings.EqualFold(network, "mainnet") || network == ""
	const saleVolume uint64 = 10_000_000  // 卖出 10 TBC（精度 6）
	const unitPrice uint64 = 10_100_000   // 每 TBC 10.1 token（精度 6）
	const feeRate uint64 = 100            // 万分之一
	requiredAmount := new(big.Int).SetUint64((saleVolume * unitPrice) / 1_000_000)

	order := contract.NewOrderBook()

	// ============ 创建卖单（卖 TBC） =============
	{
		utxos, err := api.GetUTXOs(holdAddress, float64(saleVolume)/1e6+0.001, network)
		must(err)
		ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
		must(err)
		sellNoSigs, err := order.BuildSellOrderTX(
			holdAddress, taxAddress,
			saleVolume, unitPrice, feeRate,
			ftContractTxid, ftInfo.CodeScript,
			utxos,
		)
		must(err)
		// 钱包对每个输入做 SIGHASH_ALL|FORKID 签名后填入
		sigs := make([]string, len(utxos))
		raw, err := order.FillSigsSellOrder(sellNoSigs, sigs, pubKey, "make")
		must(err)
		_, _ = api.BroadcastTXRaw(raw, network)
	}

	// ============ 撤销卖单 =============
	//   var sellUTXO *bt.UTXO   // 已知卖单 UTXO（locking script 来自链上索引）
	//   utxos, _ := api.GetUTXOs(holdAddress, 0.001, network)
	//   raw, _   := order.BuildCancelSellOrderTX(sellUTXO, utxos, mainnet)
	//   final, _ := order.FillSigsSellOrder(raw, sigs, pubKey, "cancel")  // sigs[0] 为卖单输入

	// ============ 创建买单（用 token 买 tbc） =============
	{
		ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
		must(err)
		ftCode, err := contract.BuildFTtransferCode(ftInfo.CodeScript, holdAddress)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOs(ftContractTxid, holdAddress, ftCodeHex, network, requiredAmount)
		must(err)

		utxos, err := api.GetUTXOs(holdAddress, 0.001, network)
		must(err)
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxData := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxData[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}

		buyNoSigs, err := order.BuildBuyOrderTX(
			holdAddress, taxAddress,
			saleVolume, unitPrice, feeRate,
			ftContractTxid,
			utxos, ftutxos, preTXs,
		)
		must(err)
		// FT 输入由前 len(ftutxos) 个 sig 解锁；其余是 P2PKH 普通 utxo 签名
		sigs := make([]string, len(ftutxos)+len(utxos))
		isCoin := contract.IsCoinScript(ftInfo.CodeScript)
		raw, err := order.FillSigsMakeBuyOrder(buyNoSigs, sigs, pubKey, preTXs, prepreTxData, isCoin)
		must(err)
		_, _ = api.BroadcastTXRaw(raw, network)
	}

	// ============ 撤销买单 =============
	//   var buyUTXO *bt.UTXO          // 链上买单 UTXO
	//   var ftUTXO  *util.FtUTXO      // 买单控制的单个 FT UTXO
	//   ftPreTX,    _ := api.FetchTXRaw(hex.EncodeToString(ftUTXO.TxID), network)
	//   ftPrePre,   _ := api.FetchFtPrePreTxData(ftPreTX, int(ftUTXO.Vout), network)
	//   buyPreTX,   _ := api.FetchTXRaw(hex.EncodeToString(buyUTXO.TxID), network)
	//   raw,        _ := order.BuildCancelBuyOrderTX(buyUTXO, ftUTXO, ftPreTX, utxos, mainnet)
	//   final,      _ := order.FillSigsCancelBuyOrder(raw, sigs, pubKey, buyPreTX, ftPreTX, ftPrePre, isCoin)

	// ============ 撮合交易（撮合者私钥一次性签名） =============
	//   raw, _ := order.MatchOrder(
	//       priv,
	//       buyUTXO, buyPreTX,
	//       ftUTXO, ftPreTX, ftPrePre,
	//       sellUTXO, sellPreTX,
	//       utxos,
	//       env("OB_FT_FEE_ADDR", ""), env("OB_TBC_FEE_ADDR", ""),
	//       mainnet,
	//   )

	_ = mainnet
}
```

> 要拿到「钱包签名」可以参考 `lib/contract/orderbook.go` 中的 `MakeSellOrderWithSign` —— 它内部用 `unlocker.Getter{PrivateKey: priv}` 走 `bt.Tx.FillAllInputs`，把每个输入的 SIGHASH_ALL|FORKID 签名串拼出 hex 字符串。
