# 测试场景：多签（MultiSig）

**规范对照**：[tbc-contract/docs/multiSIg.md](../../tbc-contract/docs/multiSIg.md)  
**Go 实现**：`lib/contract/multisig.go`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | `create` / `p2pk` 必填；`address` 不需要 | 付款方 WIF | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `MS_ACTION` | 否 | `address`：只算地址；`create`：创建多签钱包并广播；`p2pk`：向多签地址打 TBC | `address` |
| `MS_PUBKEYS` | `address` / `create` 必填 | 压缩公钥 hex，**逗号分隔**（无空格） | — |
| `MS_SIGNATURE_COUNT` | 否 | M | `2` |
| `MS_PUBLIC_KEY_COUNT` | 否 | N | `3` |
| `MS_TBC_AMOUNT` | `create` / `p2pk` | 打入多签的 TBC（float） | `0.001` |
| `MS_MULTISIG_ADDRESS` | `p2pk` 必填 | 目标多签地址 | — |

---

## 最小可执行脚本

```bash
export MS_ACTION=address
export MS_PUBKEYS='02....,03....,02....'
export MS_SIGNATURE_COUNT=2
export MS_PUBLIC_KEY_COUNT=3
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

func nw() string { return envOrDefault("TBC_NETWORK", "testnet") }

func pubKeys() []string {
	raw := strings.TrimSpace(os.Getenv("MS_PUBKEYS"))
	if raw == "" {
		panic("MS_PUBKEYS 必填")
	}
	return strings.Split(raw, ",")
}

func sigCount() int {
	a, _ := strconv.Atoi(envOrDefault("MS_SIGNATURE_COUNT", "2"))
	return a
}

func pubCount() int {
	b, _ := strconv.Atoi(envOrDefault("MS_PUBLIC_KEY_COUNT", "3"))
	return b
}

// tbcToSat converts a TBC float string to satoshis (1 TBC = 1_000_000 sat).
func tbcToSat(tbcStr string) uint64 {
	f, _ := strconv.ParseFloat(tbcStr, 64)
	return uint64(f * 1_000_000)
}

func runAddress() error {
	addr, err := contract.GetMultiSigAddress(pubKeys(), sigCount(), pubCount())
	if err != nil {
		return err
	}
	fmt.Println("multisig address:", addr)
	return nil
}

func runCreate(priv *bec.PrivateKey) error {
	from, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	amtSat := tbcToSat(envOrDefault("MS_TBC_AMOUNT", "0.001"))
	utxos, err := api.GetUTXOs(from.AddressString, float64(amtSat)/1_000_000+0.05, nw())
	if err != nil {
		return err
	}
	sl := make([]*bt.UTXO, len(utxos))
	for i := range utxos {
		sl[i] = utxos[i]
	}
	raw, err := contract.CreateMultiSigWallet(from.AddressString, pubKeys(), sigCount(), pubCount(), amtSat, sl, priv)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("create wallet txid:", txid)
	return err
}

func runP2PK(priv *bec.PrivateKey) error {
	to := strings.TrimSpace(os.Getenv("MS_MULTISIG_ADDRESS"))
	if to == "" {
		return fmt.Errorf("p2pk 需要 MS_MULTISIG_ADDRESS")
	}
	from, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	amtSat := tbcToSat(envOrDefault("MS_TBC_AMOUNT", "0.001"))
	utxos, err := api.GetUTXOs(from.AddressString, float64(amtSat)/1_000_000+0.05, nw())
	if err != nil {
		return err
	}
	sl := make([]*bt.UTXO, len(utxos))
	for i := range utxos {
		sl[i] = utxos[i]
	}
	raw, err := contract.P2PKHToMultiSigSendTBC(from.AddressString, to, amtSat, sl, priv)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("p2pk send txid:", txid)
	return err
}

func main() {
	switch strings.ToLower(strings.TrimSpace(envOrDefault("MS_ACTION", "address"))) {
	case "create":
		if err := runCreate(mustPriv()); err != nil {
			panic(err)
		}
	case "p2pk":
		if err := runP2PK(mustPriv()); err != nil {
			panic(err)
		}
	default:
		if err := runAddress(); err != nil {
			panic(err)
		}
	}
}
```

`MS_ACTION=address` 时**无需**设置 `TBC_WIF`。

## 预期

- `address` 不访问链上，仅调用 **`GetMultiSigAddress`**。
- `create` / `p2pk` 会广播；多签内部 **Build / Sign / Finish** 见 `multisig.go` 与 TS 文档进阶章节。
