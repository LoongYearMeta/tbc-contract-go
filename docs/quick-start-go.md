# Go 快速开始（tbc-contract-go）

与 [tbc-contract/docs/快速开始.md](../tbc-contract/docs/快速开始.md) 对应：先查余额、再取一枚 UTXO，演示 **`lib/api`** 与 TS `API.getTBCbalance` / `fetchUTXO` 一致的最小路径。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 用于推导查询地址的 WIF（本示例不广播、不花费） | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `TBC_FETCH_AMOUNT` | 否 | `FetchUTXO` 请求的 TBC 金额（含手续费余量） | `0.02` |

---

## 最小可执行脚本

在已 **`require github.com/LoongYearMeta/tbc-contract-go`** 且 **`replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go`** 的业务模块中，保存为 **`main.go`**：

```bash
export TBC_WIF='你的测试网WIF'
go run .
```

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LoongYearMeta/tbc-lib-go/wif"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
)

func envOrDefault(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func main() {
	network := envOrDefault("TBC_NETWORK", "testnet")
	wifStr := strings.TrimSpace(os.Getenv("TBC_WIF"))
	if wifStr == "" {
		fmt.Println("请设置 TBC_WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		panic(err)
	}
	addr, err := bscript.NewAddressFromPublicKey(dec.PrivKey.PubKey(), true)
	if err != nil {
		panic(err)
	}
	from := addr.AddressString

	bal, err := api.GetTBCBalance(from, network)
	if err != nil {
		panic(err)
	}
	fmt.Println("GetTBCBalance (sat):", bal)

	amt, _ := strconv.ParseFloat(envOrDefault("TBC_FETCH_AMOUNT", "0.02"), 64)
	utxo, err := api.FetchUTXO(from, amt, network)
	if err != nil {
		panic(err)
	}
	fmt.Printf("FetchUTXO: txid=%x vout=%d sat=%d\n", utxo.TxID, utxo.Vout, utxo.Satoshis)
}
```

---

## 下一步（合约与广播）

- **FT / 稳定币 / NFT 等**：见 [test-cases/README.md](./test-cases/README.md)，各篇在开头提供 **参数表** 与 **单文件 `go run` 示例**。
- **模块总览**：见 [合约库说明.md](./合约库说明.md)。
