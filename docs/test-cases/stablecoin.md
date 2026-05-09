# StableCoin 测试场景（Go）

参照 TS：[`../tbc-contract/docs/stableCoin.md`](../../tbc-contract/docs/stableCoin.md)。覆盖 Owner-signed（`Transfer` / `BatchTransfer` / `MergeCoin`）和 Admin-MuSig2（`PrepareCreateCoin` / `PrepareMintCoin` / `PrepareFreezeCoinUTXO` / `PrepareUnfreezeCoinUTXO`）两类入口。

> **关键约束**：tbc-lib-go **不内置 BIP327 / MuSig2 / Schnorr** 签名原语。Go 端的 admin 路径只负责把交易构造好、把 32 字节 SHA256d sighash 暴露给调用方；64 字节 BIP340 Schnorr 签名要在外部仪式（推荐 `tbc-lib-js` 的 `tbc.crypto.MuSig2`、或自带 Schnorr 的钱包）里完成，再通过 `(*AdminPrepared).Finalize(sigs)` 回填。Go 侧自身没法独立完成首铸 / 增发 / 冻结 / 解冻。
>
> Owner 路径完全 ECDSA，可以在 Go 内闭环完成；下方可执行脚本只演示这一类。

## 接口速览

| 路径 | 签名方 | 返回 |
|------|--------|------|
| `Transfer(priv, addrTo, amount, ftutxos, feeUTXO, preTXs, prepreTxDatas, tbcSat)` | 持币者 ECDSA | `string` raw |
| `BatchTransfer(priv, []AddressAmount, ftutxos, feeUTXO, preTXs, prepreTxDatas)` | 持币者 ECDSA | `[]string` 链式 raw（≤5 收款人/笔） |
| `MergeCoin(priv, ftutxos, feeUTXO, preTXs, prepreTxDatas, localTXs)` | 持币者 ECDSA | `[]string` 合并 raw 链 |
| `PrepareCreateCoin(aggPubkey32, feePriv, addrTo, utxo, utxoTX, mintMessage)` | Admin MuSig2 + ECDSA fee | `*AdminPrepared`，`Finalize(sigs)` → `[coinNftRaw, mintRaw]` |
| `PrepareMintCoin(aggPubkey32, feePriv, addrTo, mintAmount, utxo, nftPreTX, nftPrePreTX, mintMessage)` | Admin MuSig2 + ECDSA fee | `*AdminPrepared`，`Finalize(sigs)` → `[mintRaw]` |
| `PrepareFreezeCoinUTXO(aggPubkey32, feePriv, lockTime, ftutxos, feeUTXO, preTXs, prepreTxDatas)` | Admin MuSig2 ×N + ECDSA fee | `*AdminPrepared`，`Finalize(sigs)` → `[freezeRaw]` |
| `PrepareUnfreezeCoinUTXO(aggPubkey32, feePriv, ftutxos, feeUTXO, preTXs, prepreTxDatas)` | Admin MuSig2 ×N + ECDSA fee | `*AdminPrepared`，`Finalize(sigs)` → `[unfreezeRaw]` |

## Admin 仪式数据流（参考）

```
Go 侧：
    prepared, err := sc.PrepareCreateCoin(aggPubkey32, feePriv, ...)
    // prepared.Sighashes = [{InputIndex: 0, Sighash: 32B}, {InputIndex: 1, Sighash: 32B}]
    // prepared.Tx 已经按 dummy 64B sig 把字节布局锁住，fee 用 ECDSA 两遍签好

调用方：
    把每个 32B sighash 交给 BIP327 MuSig2 仪式：
      - n 个管理员各自 nonceGen → 聚合 → partialSign → partialSigAgg → 64B sig
      - 推荐用 tbc-lib-js 的 tbc.crypto.MuSig2，本仓不重复造轮子

Go 侧：
    raws, err := prepared.Finalize([][]byte{sig0, sig1, ...})
    // 64B sig 同长度替换 dummy → 不重算 fee；返回 [coinNftRaw, mintRaw] 之类的 raw 列表
    api.BroadcastTXsRaw(raws, network)  // 顺序广播
```

约束：

- `aggPubkey32` 必须是 BIP327 keyAgg 后的 **32 字节 x-only** 聚合公钥；签名仪式使用的 sighash 必须是 `prepared.Sighashes[i].Sighash`（已经是 BIP143 preimage 的 SHA256d）。
- `Finalize(sigs)` 中 `sigs` 顺序必须与 `Sighashes` 一致；长度必须严格匹配（`PrepareCreateCoin` / `PrepareMintCoin` 期望 2，`PrepareFreezeCoinUTXO` / `PrepareUnfreezeCoinUTXO` 期望 `len(ftutxos)`）。
- `PrepareCreateCoin.Finalize(sigs)` 返回 `[coinNftRaw, mintRaw]`，**broadcasts 必须按顺序**：先发 coinNftRaw，等其落账后再发 mintRaw（mintRaw 的输入 0/1/3 来自 coinNftRaw 的 outputs[0/1/3]）。本仓的 `api.BroadcastTXsRaw` 会一次性提交两个 raw，节点端会自己按依赖顺序处理。
- 冻结路径里 `lockTime` 必须 ≥ 500_000_000（Unix 秒）。解冻路径将 lockTime 写回 0。

## 参数定义（Owner 路径运行示例）

| 名称 | 必填 | 说明 |
|------|------|------|
| `wifA` | 是 | 持币方 WIF |
| `addressB` | Transfer/BatchTransfer 必填 | 收款人 P2PKH |
| `coinContractTxid` | 是 | 已部署的 stableCoin mint txid（`PrepareCreateCoin.Finalize` 返回的 mintRaw 的 TxID） |
| `network` | 否 | `"testnet"` / `"mainnet"`，默认 `"testnet"` |

## 可执行脚本（仅 owner 路径）

把下列代码贴成业务仓的 `main.go`，配置 `replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go`，填好顶部 const 后 `go run .`。Admin 路径请见上方 "Admin 仪式数据流" 示意，自带 Schnorr/MuSig 实现的钱包按相同接口调用即可。

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
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

const (
	network          = "testnet"
	wifA             = "" // 持币方 WIF
	addressB         = "" // Transfer 收款方
	coinContractTxid = "" // 已部署 stableCoin mint txid
)

func must(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func coinFTInfoFromAPI(txid string, info *api.CoinInfoResponse) *contract.FtInfo {
	return &contract.FtInfo{
		ContractTxid: txid,
		Name:         info.Name,
		Symbol:       info.Symbol,
		Decimal:      info.Decimal,
		TotalSupply:  info.TotalSupply,
		CodeScript:   info.CodeScript,
		TapeScript:   info.TapeScript,
	}
}

func main() {
	if strings.TrimSpace(wifA) == "" || strings.TrimSpace(coinContractTxid) == "" {
		fmt.Println("请填好 wifA / coinContractTxid")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifA)
	must(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	must(err, "Addr")
	addressA := addr.AddressString

	// 加载合约元数据
	sc, err := contract.NewStableCoin(coinContractTxid)
	must(err, "NewStableCoin")
	info, err := api.FetchCoinInfo(coinContractTxid, network)
	must(err, "FetchCoinInfo")
	sc.Initialize(coinFTInfoFromAPI(coinContractTxid, info))

	// 查余额（与 GetFTBalance 同语义，但走 stablecoin 索引器路径）
	bal, err := api.GetCoinBalance(coinContractTxid, addressA, network)
	must(err, "GetCoinBalance")
	fmt.Println("balance:", bal.String())

	// === Transfer 1.0 stableCoin ===
	amount, err := util.ParseDecimalToBigInt("1", sc.Decimal)
	must(err, "ParseDecimalToBigInt")
	ftCode, err := contract.BuildFTtransferCode(sc.CodeScript, addressA)
	must(err, "BuildFTtransferCode")
	ftutxos, err := api.FetchCoinUTXOs(sc.ContractTxid, addressA,
		hex.EncodeToString(ftCode.Bytes()), network, amount, 5)
	must(err, "FetchCoinUTXOs")
	feeUTXO, err := api.FetchUTXO(addressA, 0.01, network)
	must(err, "FetchUTXO")

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i, u := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
		must(err, "FetchTXRaw")
		prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
		must(err, "FetchFtPrePreTxData")
	}
	raw, err := sc.Transfer(priv, addressB, amount, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
	must(err, "Transfer")
	txid, err := api.BroadcastTXRaw(raw, network)
	must(err, "broadcast")
	fmt.Println("transfer tx:", txid)

	// === BatchTransfer / MergeCoin 用法相同：见 test/stablecoin/main.go ===
	_ = big.NewInt
}
```

## 已知限制与排错

- **Schnorr 签名仪式不内置**：admin 路径在 Go 端无法独立完成。要么调用方自带 BIP327 实现，要么用 TS 仓的 `runMuSigCeremony` 拿到 sigs 后再回调 Go 的 Finalize。
- **管理员 PKH 必须 = HASH160(aggPubkey32)**：`PrepareFreezeCoinUTXO` / `PrepareUnfreezeCoinUTXO` 的 hold script 是 P2PKH-on-xonly，节点端 `OP_CHECKSIG` 用 sig 长度 64 + pubkey 长度 32 走 Schnorr 分支；HASH160(xonly 32B) 必须和合约里嵌入的 admin pkh 完全相等。
- **冻结/解冻最多一笔 5 个 FT UTXO**：超过会报 `too many FT UTXOs (max 5)`。多余 UTXO 先 `MergeCoin` 合并。
- **`BatchTransfer` 链式费率**：第二个及之后批次的 fee 输入来自上一笔的 P2PKH 找零（vout = `prevBatchSize*2 + 2`）。如果上一笔的找零被 mempool 仍未确认，节点会以 `Missing inputs` 拒绝；此时按 orderbook 测试经验，`time.Sleep(15 * time.Second)` 后重试一次。
- **lockTime ≥ 500_000_000**：冻结时低于这个值会被 Go 端 `SetLockTimeInTape` 直接报错（与 TS `setLockTimeInTape` 同语义）。
