# PiggyBank 测试场景（Go）

PiggyBank 是「定时锁 TBC」合约：把若干 satoshi 锁到一个 P2SH-like 脚本，到 `lockTime`（unix 秒或区块高度）后再由原地址赎回。TS 侧没有独立大文档，对照源码 `tbc-contract/lib/contract/piggyBank.ts`。

> Go 端有两组 API：**带签名** 的 `FreezeTBCWithSign` / `UnfreezeTBCWithSign`（直接得到广播 raw），与 **未签名** 的 `FreezeTBC` / `UnfreezeTBC`（前端 / 钱包侧再签）。
> 解锁端的 `currentBlockHeight` 必须从 `api.FetchBlockHeaders` 读到，传入交易的 nLockTime；这一步建议放服务侧而不是钱包，避免端机时差导致的解锁失败。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF` | 是 | 操作者 WIF |
| `TBC_NETWORK` | 否 | 默认 `"testnet"` |
| `PIGGY_AMOUNT_SAT` | 否 | 冻结金额（satoshi），默认 `200000`（0.2 TBC） |
| `PIGGY_LOCKTIME` | 否 | 解锁阈值（unix 秒 / 区块高度，自适应），默认 `1774410989` |

## 最小可执行脚本

```go
package main

import (
	"fmt"
	"os"
	"strconv"
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
	address := addr.AddressString

	amountSat64, _ := strconv.ParseUint(env("PIGGY_AMOUNT_SAT", "200000"), 10, 64)
	amountSat := amountSat64
	lt64, _ := strconv.ParseUint(env("PIGGY_LOCKTIME", "1774410989"), 10, 32)
	lockTime := uint32(lt64)

	// ============ Freeze（冻结 TBC，至 lockTime） =============
	{
		// 至少 amountSat + 一些找零
		utxos, err := api.GetUTXOs(address, float64(amountSat)/1e6+0.001, network)
		must(err)
		raw, err := contract.FreezeTBCWithSign(priv, amountSat, lockTime, utxos)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Freeze:", txid)
	}

	// ============ Unfreeze（lockTime 到期后赎回） =============
	{
		// 拉取已冻结但已过期的 UTXO 列表
		frozen, err := api.FetchUnfrozenUTXOList(address, network)
		must(err)
		if len(frozen) == 0 {
			fmt.Println("没有可解锁的 PiggyBank UTXO")
			return
		}

		utxos := make([]*bt.UTXO, 0, len(frozen))
		for _, f := range frozen {
			u, err := api.FrozenToBTUTXO(f)
			must(err)
			utxos = append(utxos, u)
		}

		headers, err := api.FetchBlockHeaders(0, 1, network)
		must(err)
		currentBlockHeight := uint32(headers[0].Height)

		raw, err := contract.UnfreezeTBCWithSign(priv, utxos, currentBlockHeight)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Unfreeze:", txid)
	}
}
```

> **未签名版本**：用 `contract.FreezeTBC(address, amountSat, lockTime, utxos)` 与 `contract.UnfreezeTBC(address, utxos, currentBlockHeight)` 拿到 raw，再让钱包对每个 P2PKH 输入做 SIGHASH_ALL|FORKID 签名后填回；具体填回方式参考 `lib/contract/piggybank.go` 中 `*WithSign` 内部实现。
