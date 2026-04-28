# 测试场景：订单簿（OrderBook）

**规范对照**：[tbc-contract/docs/orderBook.md](../../tbc-contract/docs/orderBook.md)  
**Go 实现**：`lib/contract/orderbook.go`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 挂单账户 WIF | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `OB_ACTION` | 否 | 当前仅实现 `sell`（`MakeSellOrderWithSign`） | `sell` |
| `OB_TAX_ADDRESS` | 是 | 税费地址 | — |
| `OB_FT_CONTRACT_TXID` | 是 | FT 合约 id（hex） | — |
| `OB_FT_CODE_SCRIPT_HEX` | 是 | FT **完整** codeScript 十六进制 | — |
| `OB_SALE_VOLUME` | 否 | 卖单数量（uint64，与脚本编码一致） | `10000` |
| `OB_UNIT_PRICE` | 否 | 单价 | `100000` |
| `OB_FEE_RATE` | 否 | 手续费率 | `5000` |

---

## 最小可执行脚本

```bash
export TBC_WIF='...'
export OB_TAX_ADDRESS='1...'
export OB_FT_CONTRACT_TXID='...'
export OB_FT_CODE_SCRIPT_HEX='....长hex....'
go run .
```

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

func envOrDefault(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func mustPriv() *bec.PrivateKey {
	d, err := wif.DecodeWIF(os.Getenv("TBC_WIF"))
	if err != nil {
		panic(err)
	}
	return d.PrivKey
}

func u64(k, def string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(envOrDefault(k, def)), 10, 64)
	if err != nil {
		panic(k + ": " + err.Error())
	}
	return n
}

func main() {
	if strings.ToLower(strings.TrimSpace(envOrDefault("OB_ACTION", "sell"))) != "sell" {
		panic("当前示例仅支持 OB_ACTION=sell")
	}
	priv := mustPriv()
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		panic(err)
	}
	network := envOrDefault("TBC_NETWORK", "testnet")
	utxos, err := api.GetUTXOs(addr.AddressString, 0.08, network)
	if err != nil {
		panic(err)
	}
	sl := make([]*bt.UTXO, len(utxos))
	for i := range utxos {
		sl[i] = utxos[i]
	}
	tax := strings.TrimSpace(os.Getenv("OB_TAX_ADDRESS"))
	ftid := strings.TrimSpace(os.Getenv("OB_FT_CONTRACT_TXID"))
	code := strings.TrimSpace(os.Getenv("OB_FT_CODE_SCRIPT_HEX"))
	if tax == "" || ftid == "" || code == "" {
		panic("需要 OB_TAX_ADDRESS OB_FT_CONTRACT_TXID OB_FT_CODE_SCRIPT_HEX")
	}
	ob := contract.NewOrderBook()
	raw, err := ob.MakeSellOrderWithSign(priv, tax, u64("OB_SALE_VOLUME", "10000"), u64("OB_UNIT_PRICE", "100000"), u64("OB_FEE_RATE", "5000"), ftid, code, sl)
	if err != nil {
		panic(err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		panic(err)
	}
	fmt.Println("sell order txid:", txid)
}
```

## 说明

- **`MatchOrder`** 需买卖单与 FT UTXO 及多段父交易数据，变量较多；请在阅读 `orderBook.md` 与 `orderbook.go` 后于业务侧组装，本页仅覆盖 **卖单广播** 的最小路径。
