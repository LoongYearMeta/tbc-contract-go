# Pool NFT 2.0 测试场景（Go）

参照 TS：[`../tbc-contract/docs/poolNFT2.0.md`](../../tbc-contract/docs/poolNFT2.0.md)。覆盖建池（普通 / multisig 锁定）、初始资金注入、增减 LP、双向 swap、合并/销毁 LP、合并池中 FT 等路径。

> 仅实现 **Pool NFT 2.0（线性池）**；旧版 `poolNFT` v1 与 TS 中已弃用的 `*WithLockTime` 三件套（`initPoolNFTWithLockTime` / `increaseLpWithLockTime` / `consumeLpWithLockTime`）不在本仓范围内。
> with-lock-time 模式下的 `ConsumeLP` 返回 `[]string`：长度 1 时为 `[consumeRaw]`；长度 2 时为 `[unlockRaw, consumeRaw]`，需按顺序广播（先 unlock 上链，再 consume）。
> Pool 端所有数量参数（`tbcAmount` / `ftAmount` / `amountLP`）都是十进制 **字符串**，由库内部用 `util.ParseDecimalToBigInt` 转 `*big.Int`，避免 float 精度损失。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF_A` | 是 | 操作者 WIF（建池/swap/LP 都用它） |
| `TBC_NETWORK` | 否 | 默认 `"testnet"` |
| `FT_CONTRACT_TXID` | 是 | 标的 FT 合约 txid |
| `POOL_CONTRACT_TXID` | 后续步骤必填 | CreatePoolNFT 完成后的池子 txid |
| `POOL_TAG` | 否 | 池子标签（区分创建者），默认 `"tbc"` |
| `POOL_SERVICE_RATE` | 否 | swap 手续费率（默认 35 即千分之三点五） |
| `POOL_LP_PLAN` | 否 | LP 方案（1 或 2，默认 2） |
| `POOL_WITH_LOCK_TIME` | 否 | `"true"` 启用锁仓功能 |

## 最小可执行脚本

```go
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	wifStr := strings.TrimSpace(os.Getenv("TBC_WIF_A"))
	if wifStr == "" {
		fmt.Println("请设置 TBC_WIF_A")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	must(err)
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	must(err)
	addressA := addr.AddressString
	ftContractTxid := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID"))
	poolContractTxid := strings.TrimSpace(os.Getenv("POOL_CONTRACT_TXID"))
	tag := env("POOL_TAG", "tbc")
	serviceRate, _ := strconv.Atoi(env("POOL_SERVICE_RATE", "35"))
	lpPlan, _ := strconv.Atoi(env("POOL_LP_PLAN", "2"))
	withLockTime := strings.EqualFold(env("POOL_WITH_LOCK_TIME", "false"), "true")
	const fee = 0.01

	// ============ Step 1: 创建 poolNFT =============
	{
		pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: network})
		must(pool.InitCreate(ftContractTxid))

		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)

		// (a) 普通建池
		txs, err := pool.CreatePoolNFT(priv, utxo, tag, serviceRate, lpPlan, withLockTime)
		must(err)
		// (b) 或：multisig 锁定建池（最多十个公钥）
		//   pubKeyLock := []string{"pubkey1", "pubkey2"}
		//   lpCostAddr := ""    // LP 成本扣款地址
		//   lpCostTBC  := 5.0   // LP 成本 TBC
		//   txs, err := pool.CreatePoolNFTWithLock(priv, utxo, tag, lpCostAddr, lpCostTBC, pubKeyLock,
		//       serviceRate, lpPlan, withLockTime)

		_, err = api.BroadcastTXRaw(txs[0], network)
		must(err)
		fmt.Println("poolNFT Contract ID:")
		id, err := api.BroadcastTXRaw(txs[1], network)
		must(err)
		fmt.Println(id)
		poolContractTxid = id
	}

	// ============ Step 2: 用已创建的 poolNFT =============
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{ContractTxID: poolContractTxid, Network: network})
	must(pool.InitFromContractID())

	// Step 2.1: 注入初始资金
	{
		const tbcAmount = "30"
		const ftAmount = "1000" // 大数请用字符串
		utxo, err := api.FetchUTXO(addressA, mustFloat(tbcAmount)+fee, network)
		must(err)
		// lockTime=0 不锁仓；非 0 则锁至该 unix 时间或区块高度
		raw, err := pool.InitPoolNFT(priv, addressA, utxo, tbcAmount, ftAmount, 0)
		must(err)
		_, err = api.BroadcastTXRaw(raw, network)
		must(err)
	}

	// Step 2.2: 添加流动性
	{
		const tbcAmount = "0.1"
		utxo, err := api.FetchUTXO(addressA, mustFloat(tbcAmount)+fee, network)
		must(err)
		raw, err := pool.IncreaseLP(priv, addressA, utxo, tbcAmount, 0) // lockTime=0 不锁仓
		must(err)
		_, err = api.BroadcastTXRaw(raw, network)
		must(err)
	}

	// Step 2.3: 花费 LP（若为 with-lock-time 池，会自动 unlock 一次）
	{
		const lpAmount = "13"
		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)
		// lockTime *uint32：传 nil 时由库自动选 (currentHeight - 2) 或 (now - 30min)
		raws, err := pool.ConsumeLP(priv, addressA, utxo, lpAmount, nil)
		must(err)
		// raws 长度 1 → [consumeRaw]；长度 2 → [unlockRaw, consumeRaw]，按顺序广播
		_, err = api.BroadcastTXsRaw(raws, network)
		must(err)
	}

	// Step 2.4: 用 TBC 兑换 Token
	{
		const tbcAmount = "0.1"
		utxo, err := api.FetchUTXO(addressA, mustFloat(tbcAmount)+fee, network)
		must(err)
		raw, err := pool.SwapToToken(priv, addressA, utxo, tbcAmount, lpPlan)
		must(err)
		_, err = api.BroadcastTXRaw(raw, network)
		must(err)
	}

	// Step 2.5: 用 Token 兑换 TBC
	{
		const ftAmount = "100"
		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)
		raw, err := pool.SwapToTBC(priv, addressA, utxo, ftAmount, lpPlan)
		must(err)
		_, err = api.BroadcastTXRaw(raw, network)
		must(err)
	}

	// 池子查询
	{
		poolInfo, err := api.FetchPoolNFTInfo(poolContractTxid, network)
		must(err)
		fmt.Printf("pool: ftA=%s ftLp=%s tbc=%s\n", poolInfo.FtAAmount, poolInfo.FtLpAmount, poolInfo.TBCAmount)

		ftlpBalance, err := pool.FetchFtlpBalance(addressA)
		must(err)
		fmt.Println("ftlp balance:", ftlpBalance.String())

		ftlpLockTime, err := pool.FetchFtlpLockTime(addressA)
		must(err)
		fmt.Println("ftlp lock-time entries:", len(ftlpLockTime))
	}

	// 合并 FT-LP（每次最多 5 合 1）
	{
		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)
		// 第三个参数 lockTime *uint32：nil 时库自动派生
		raw, err := pool.MergeFTLP(priv, utxo, nil)
		must(err)
		if raw != "" {
			_, err = api.BroadcastTXRaw(raw, network)
			must(err)
		} else {
			fmt.Println("Merge success")
		}
	}

	// 销毁 FT-LP
	{
		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)
		raw, err := pool.BurnFTLP(priv, utxo)
		must(err)
		_, err = api.BroadcastTXRaw(raw, network)
		must(err)
	}

	// 合并池子内的 FT-A（每次最多 4 合 1，传 times 决定执行多少轮）
	{
		const times = 10
		mergeFee := 0.005 * float64(times)
		utxo, err := api.FetchUTXO(addressA, mergeFee, network)
		must(err)
		raws, err := pool.MergeFTinPool(priv, utxo, times)
		must(err)
		if len(raws) > 0 {
			_, err = api.BroadcastTXsRaw(raws, network)
			must(err)
		} else {
			fmt.Println("Merge success")
		}
	}

	// with-lock-time 池：手动调用 UnlockFTLP 提前消化锁定 LP
	if pool.WithLockTime {
		utxo, err := api.FetchUTXO(addressA, fee, network)
		must(err)
		raw, err := pool.UnlockFTLP(priv, utxo, nil)
		must(err)
		if raw != "" {
			_, err = api.BroadcastTXRaw(raw, network)
			must(err)
		}
	}

	_ = hex.EncodeToString // 备用
}

func mustFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	must(err)
	return f
}
```

> Pool 内部状态字段（`p.FtAAmount` / `p.FtLpAmount` / `p.TbcAmount` 等）在每次 `InitFromContractID` 时由链上 UTXO + tape 校准；`SwapToToken` / `SwapToTBC` / `IncreaseLP` / `ConsumeLP` 在中途任一 API 调用失败时，会自动通过 `defer` 把这些字段回滚到调用前的快照，避免状态被部分更新污染。
