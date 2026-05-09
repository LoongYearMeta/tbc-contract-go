# OrderBook 测试场景（Go）

参照 TS：[`../tbc-contract/docs/orderBook.md`](../../tbc-contract/docs/orderBook.md)。覆盖创建卖单、撤销卖单、创建买单（用 token 买 TBC）、撤销买单、撮合五条主路径。

> 数值约定：八字节小端，参数类型统一 `uint64`，精度（除 FT 外）固定 6 位。
> 每个金额 / 单价 / 数量字段都按 `value × 10^6` 编码。

| 角色 | 职责 | 起始资源 |
|------|------|---------|
| `wifA` | 撮合者（pay match-tx miner fee） | TBC（足够付撮合手续费 + 流转给 B/C） |
| `wifB` | 买家（用 token 买 TBC） | token + 少量 TBC（付买单 tx 手续费） |
| `wifC` | 卖家（用 TBC 换 token） | TBC（够挂单 + 手续费） |

如果只想验证「创建/撤销」，单 WIF 即可，撮合必须 3 钱包。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `wifA` / `wifB` / `wifC` | 撮合时必填 | 三个角色的 WIF（地址必须各不相同） |
| `network` | 否 | 默认 `"testnet"` |
| `ftContractTxid` | 是 | FT 合约 txid |
| `taxAddress` | 是 | 写入订单数据的 tax 地址（订单创建后不可改） |
| `ftFeeAddress` / `tbcFeeAddress` | 撮合必填 | 撮合者实际收取的 FT / TBC 手续费地址（必须等于订单 baked-in 的 taxAddress pkh） |
| `saleVolume` / `unitPrice` / `feeRate` | 是 | `uint64` 精度 6；保证 `tbcTax = matchedTBC × feeRate / 1e6 ≥ 10` 否则触达 dust 限制 |

## 推荐 API（带签名一步到位）

| 路径 | 方法 | 备注 |
|------|------|------|
| 创建卖单 | `(*OrderBook).MakeSellOrderWithSign(priv, taxAddress, saleVolume, unitPrice, feeRate, ftID, ftCodeScript, utxos)` | 内部走 `BuildSellOrderTX → FillAllInputs`，返回签好的 raw |
| 撤销卖单 | `(*OrderBook).CancelSellOrderWithSign(priv, sellUTXO, utxos, mainnet)` | 两遍签名 + actual-bytes 矿工费 |
| 创建买单 | `BuildBuyOrderTX → FillSigsMakeBuyOrder`（手动签名 FT 输入） | 见示例 `signBuyOrderInputs` |
| 撤销买单 | `(*OrderBook).CancelBuyOrderWithSign(priv, buyUTXO, ftUTXO, buyPreTX, ftPrePreTxData, utxos, mainnet)` | 内部 in-place 签名 |
| 撮合 | `(*OrderBook).MatchOrder(priv, buyUTXO, buyPreTX, ftUTXO, ftPreTX, ftPrePreTxData, sellUTXO, sellPreTX, utxos, ftFeeAddress, tbcFeeAddress, mainnet)` | priv 是撮合者私钥；buyer/seller/matcher 三地址必须各不相同 |

> 完整可跑的循环测试在 `test/orderbook/main.go` —— 双开关 `doFundB` / `doFundC` 给买家、卖家分别打初始资金，然后 `doRoundLoop` 跑 10 轮 sell+buy+match（每轮还会用部分成交的找零订单做二次撮合）。

## 最小可执行脚本

```go
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/transaction/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

const (
	network        = "testnet"
	wifA           = "..." // 撮合者
	wifB           = "..." // 买家（A 提前转 token + 一点 TBC 过来）
	wifC           = "..." // 卖家（A 提前转 TBC 过来）
	ftContractTxid = "..."
	taxAddress     = "..." // 写入订单的 tax pkh
	ftFeeAddress   = taxAddress
	tbcFeeAddress  = taxAddress

	saleVolume = uint64(1_000_000) // 1 TBC
	unitPrice  = uint64(1_500_000) // 1.5 token / TBC
	feeRate    = uint64(1000)      // 0.001 = 0.1%
)

func decodeWif(s string) (*bec.PrivateKey, string) {
	dec, _ := wif.DecodeWIF(s)
	priv := dec.PrivKey
	addr, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	return priv, addr.AddressString
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if strings.TrimSpace(wifA) == "" {
		fmt.Println("请填三个 WIF")
		os.Exit(1)
	}
	privA, _ := decodeWif(wifA)
	privB, addrB := decodeWif(wifB)
	privC, addrC := decodeWif(wifC)
	mainnet := network == "mainnet" || network == ""

	// ============ C 创建卖单（用 TBC 换 token） ============
	sellID := createSell(privC, addrC)
	fmt.Println("sell:", sellID)

	// ============ B 创建买单（用 token 买 TBC） ============
	buyID := createBuy(privB, addrB)
	fmt.Println("buy: ", buyID)

	// ============ A 撮合 ============
	matchID := matchOrder(privA, sellID, buyID, mainnet)
	fmt.Println("match:", matchID)

	// ============ 撤销卖单（C 自己撤）============
	//   sellTX, _ := api.FetchTXRaw(sellID, network)
	//   sellTxIDBytes, _ := hex.DecodeString(sellID)
	//   sellUTXO := &bt.UTXO{TxID: sellTxIDBytes, Vout: 0,
	//       LockingScript: sellTX.Outputs[0].LockingScript,
	//       Satoshis:      sellTX.Outputs[0].Satoshis}
	//   utxos, _ := api.GetUTXOs(addrC, 0.001, network)
	//   order := contract.NewOrderBook()
	//   raw, _ := order.CancelSellOrderWithSign(privC, sellUTXO, utxos, mainnet)
	//   api.BroadcastTXRaw(raw, network)

	// ============ 撤销买单（B 自己撤）============
	//   buyTX, _ := api.FetchTXRaw(buyID, network)
	//   buyTxIDBytes, _ := hex.DecodeString(buyID)
	//   buyUTXO := &bt.UTXO{TxID: buyTxIDBytes, Vout: 0,
	//       LockingScript: buyTX.Outputs[0].LockingScript,
	//       Satoshis:      buyTX.Outputs[0].Satoshis}
	//   ftUTXO, _ := util.BuildUTXO(buyTX, 1, true)
	//   ftPrePreTxData, _ := api.FetchFtPrePreTxData(buyTX, int(ftUTXO.Vout), network)
	//   utxos, _ := api.GetUTXOs(addrB, 0.001, network)
	//   order := contract.NewOrderBook()
	//   raw, _ := order.CancelBuyOrderWithSign(privB, buyUTXO, ftUTXO, buyTX, ftPrePreTxData, utxos, mainnet)
	//   api.BroadcastTXRaw(raw, network)

	_ = privA
}

func createSell(priv *bec.PrivateKey, addr string) string {
	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	must(err)
	utxos, err := api.GetUTXOs(addr, float64(saleVolume)/1e6+0.001, network)
	must(err)
	order := contract.NewOrderBook()
	raw, err := order.MakeSellOrderWithSign(priv, taxAddress, saleVolume, unitPrice, feeRate, ftContractTxid, ftInfo.CodeScript, utxos)
	must(err)
	txid, err := api.BroadcastTXRaw(raw, network)
	must(err)
	return txid
}

func createBuy(priv *bec.PrivateKey, addr string) string {
	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	must(err)
	ftCode, err := contract.BuildFTtransferCode(ftInfo.CodeScript, addr)
	must(err)
	requiredAmount := new(big.Int).SetUint64((saleVolume * unitPrice) / 1_000_000)
	ftutxos, err := api.FetchFtUTXOs(ftContractTxid, addr, hex.EncodeToString(ftCode.Bytes()), network, requiredAmount)
	must(err)
	utxos, err := api.GetUTXOs(addr, 0.002, network)
	must(err)
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxData := make([]string, len(ftutxos))
	for i, u := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
		must(err)
		prepreTxData[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
		must(err)
	}
	order := contract.NewOrderBook()
	buyNoSigs, err := order.BuildBuyOrderTX(addr, taxAddress, saleVolume, unitPrice, feeRate, ftContractTxid, utxos, ftutxos, preTXs)
	must(err)
	raw, err := signBuyOrderInputs(buyNoSigs, priv, ftutxos, utxos, preTXs, prepreTxData, ftInfo.CodeScript)
	must(err)
	txid, err := api.BroadcastTXRaw(raw, network)
	must(err)
	return txid
}

func matchOrder(priv *bec.PrivateKey, sellID, buyID string, mainnet bool) string {
	matcherAddr := func() string {
		addr, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
		return addr.AddressString
	}()
	sellTX, err := api.FetchTXRaw(sellID, network)
	must(err)
	buyTX, err := api.FetchTXRaw(buyID, network)
	must(err)
	sellTxIDBytes, _ := hex.DecodeString(sellID)
	buyTxIDBytes, _ := hex.DecodeString(buyID)
	sellUTXO := &bt.UTXO{TxID: sellTxIDBytes, Vout: 0, LockingScript: sellTX.Outputs[0].LockingScript, Satoshis: sellTX.Outputs[0].Satoshis}
	buyUTXO := &bt.UTXO{TxID: buyTxIDBytes, Vout: 0, LockingScript: buyTX.Outputs[0].LockingScript, Satoshis: buyTX.Outputs[0].Satoshis}
	ftUTXO, err := util.BuildUTXO(buyTX, 1, true)
	must(err)
	ftPrePreTxData, err := api.FetchFtPrePreTxData(buyTX, int(ftUTXO.Vout), network)
	must(err)
	utxos, err := api.GetUTXOs(matcherAddr, 0.005, network)
	must(err)
	order := contract.NewOrderBook()
	raw, err := order.MatchOrder(priv, buyUTXO, buyTX, ftUTXO, buyTX, ftPrePreTxData, sellUTXO, sellTX, utxos, ftFeeAddress, tbcFeeAddress, mainnet)
	must(err)
	txid, err := api.BroadcastTXRaw(raw, network)
	must(err)
	return txid
}

// signBuyOrderInputs 解决 BuildBuyOrderTX → tx.Bytes()(hex) → bt.NewTxFromBytes 的
// round-trip 把 PreviousTxScript / PreviousTxSatoshis 丢掉的问题：reparse 之后用调
// 用方传进来的 utxo / ftutxo 把 prev metadata 填回去，再算 BIP143 sighash 签名。
func signBuyOrderInputs(rawHex string, priv *bec.PrivateKey, ftutxos []*util.FtUTXO, utxos []*bt.UTXO, preTXs []*bt.Tx, prepreTxData []string, ftCodeScript string) (string, error) {
	rawBytes, err := hex.DecodeString(rawHex)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	if len(tx.Inputs) != len(ftutxos)+len(utxos) {
		return "", fmt.Errorf("input count %d != ftutxos %d + utxos %d", len(tx.Inputs), len(ftutxos), len(utxos))
	}
	for i, fu := range ftutxos {
		tx.Inputs[i].PreviousTxScript = fu.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = fu.Satoshis
	}
	for i, u := range utxos {
		tx.Inputs[len(ftutxos)+i].PreviousTxScript = u.LockingScript
		tx.Inputs[len(ftutxos)+i].PreviousTxSatoshis = u.Satoshis
	}
	pubKey := hex.EncodeToString(priv.PubKey().SerialiseCompressed())
	sigs := make([]string, len(tx.Inputs))
	for i := range tx.Inputs {
		sh, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return "", fmt.Errorf("sighash input %d: %w", i, err)
		}
		sig, err := priv.Sign(sh)
		if err != nil {
			return "", fmt.Errorf("sign input %d: %w", i, err)
		}
		sigs[i] = hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
	}
	order := contract.NewOrderBook()
	isCoin := contract.IsCoinScript(ftCodeScript)
	return order.FillSigsMakeBuyOrder(rawHex, sigs, pubKey, preTXs, prepreTxData, isCoin)
}

// 仅为屏蔽未使用 import / pkg 的占位
var _ = context.Background
var _ = unlocker.Simple{}
```

## 撮合 tx 输出布局

撮合 tx 的输出顺序（由 `MatchOrder` 构造）：

| vout | 内容 | 数量 |
|------|------|------|
| 0 | FT 给卖家（按 ftSeller 数额） | `ftutxo.satoshis` |
| 1 | FT 卖家 tape | 0 |
| 2 | FT 给 ftFeeAddress（按 ftTax 数额） | `ftutxo.satoshis` |
| 3 | FT tax tape | 0 |
| 4 | TBC 给买家（`tbcBuyer`） | `matchedTBC - tbcTax` |
| 5 | TBC 给 tbcFeeAddress（或 placeholder if `feeRate==0 && tbcTax==0`） | `tbcTax` |
| 6 | 撮合者找零 P2PKH | `inputsFee - fee - 1300` |
| 7+（可选） | 部分成交的找零订单：8 outputs ⇒ sell-change，10 outputs ⇒ buy-change（含 FT 找零 code+tape） | — |

### 部分成交的二次撮合

`tx.Outputs[7]` 是新一轮订单：
- 8 个输出 ⇒ 找零是**卖单**（vol=`oldSell - matched`）。新建一笔买单跟它撮合。
- 10 个输出 ⇒ 找零是**买单**（vol=`oldBuy - matched`），且它在 vout=8/9 还带着剩余 FT。新建一笔卖单跟它撮合。

完整的二次撮合代码见 `test/orderbook/main.go:secondaryMatch`。

## 已知限制

1. **三地址约束**（见上文）。
2. **TBC tax 不能落入 [1, 10) 区间**：合约要求 `tbcTax == 0`（走 placeholder 分支）或 `tbcTax >= 10`。当 `matchedTBC × feeRate / 1e6` 落在 1..9 时，`MatchOrder` 会直接 `errorf("TBC tax amount below dust limit")` —— 调高 feeRate 或扩大订单成交量即可避免。这种情况在二次撮合的极小残单上常出现。
3. **mempool 抢 UTXO race**：连续广播多笔 tx 时索引器可能还没更新，下一笔 `FetchUTXO` 会拿到刚刚被花掉的 UTXO 触发 `Missing inputs`。解决：每笔之间 sleep ≥ 8 秒（block 时间）或捕获错误重试。
