# 测试场景：Pool NFT 2.0

**规范对照**：[tbc-contract/docs/poolNFT2.0.md](../../tbc-contract/docs/poolNFT2.0.md)  
**Go 实现**：`lib/contract/poolnft2.go`；索引：`lib/api.FetchPoolNFTInfo` 等。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `POOL_ACTION` | 否 | `fetch`：仅索引；`load`：`InitFromContractID`；`initft`：`InitCreate` | `fetch` |
| `POOL_CONTRACT_TXID` | `fetch` / `load` 必填 | 池合约 txid（64 hex） | — |
| `POOL_FT_CONTRACT_TXID` | `initft` 必填 | 池子底层 FT 合约 txid | — |

---

## 最小可执行脚本

```bash
export POOL_ACTION=fetch
export POOL_CONTRACT_TXID='...'
go run .
```

```go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

func envOrDefault(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func nw() string { return envOrDefault("TBC_NETWORK", "testnet") }

func runFetch() error {
	id := strings.TrimSpace(os.Getenv("POOL_CONTRACT_TXID"))
	if id == "" {
		return fmt.Errorf("需要 POOL_CONTRACT_TXID")
	}
	info, err := api.FetchPoolNFTInfo(id, nw())
	if err != nil {
		return err
	}
	fmt.Printf("pool: ft_a=%s tbc=%s version=%d ft_contract=%s\n",
		info.FtAAmount, info.TBCAmount, info.PoolVersion, info.FtAContractTxID)
	return nil
}

func runLoad() error {
	id := strings.TrimSpace(os.Getenv("POOL_CONTRACT_TXID"))
	if id == "" {
		return fmt.Errorf("需要 POOL_CONTRACT_TXID")
	}
	p := contract.NewPoolNFT2(&contract.PoolNFT2Config{ContractTxID: id, Network: nw()})
	if err := p.InitFromContractID(); err != nil {
		return err
	}
	fmt.Printf("loaded: FtAAmount=%s TbcAmount=%s ContractTxID=%s\n", p.FtAAmount.String(), p.TbcAmount.String(), p.ContractTxID)
	return nil
}

func runInitFT() error {
	ft := strings.TrimSpace(os.Getenv("POOL_FT_CONTRACT_TXID"))
	if ft == "" {
		return fmt.Errorf("需要 POOL_FT_CONTRACT_TXID")
	}
	p := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: nw()})
	if err := p.InitCreate(ft); err != nil {
		return err
	}
	fmt.Println("InitCreate ok, FtAContractTxID:", p.FtAContractTxID)
	return nil
}

func main() {
	var err error
	switch strings.ToLower(strings.TrimSpace(envOrDefault("POOL_ACTION", "fetch"))) {
	case "load":
		err = runLoad()
	case "initft":
		err = runInitFT()
	default:
		err = runFetch()
	}
	if err != nil {
		panic(err)
	}
}
```

## 预期

- `fetch` 仅调用 **`api.FetchPoolNFTInfo`**，不修改链上状态。
- `load` 会请求索引与 **`FetchTXRaw`** 解析 tape；`initft` 只做本地 **`InitCreate`** 校验与赋值。
