# 测试场景：存钱罐（定时锁 TBC）

**规范对照**：TS `lib/contract/piggyBank.ts`  
**Go 实现**：`lib/contract/piggybank.go`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 操作私钥 | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `PIGGY_ACTION` | 否 | `freeze` / `unfreeze` | `freeze` |
| `PIGGY_TBC` | `freeze` | 锁定 TBC 数量（float） | `0.001` |
| `PIGGY_LOCKTIME` | `freeze` | `nLockTime`（uint32，语义见源码） | `500000` |
| `PIGGY_FROZEN_TXID` | `unfreeze` | 冻结交易 txid | — |
| `PIGGY_FROZEN_VOUT` | 否 | 冻结输出索引 | `0` |

---

## 最小可执行脚本

```bash
export TBC_WIF='...'
export PIGGY_ACTION=freeze
export PIGGY_TBC=0.001
export PIGGY_LOCKTIME=500000
go run .
```

```go
package main

import (
	"encoding/hex"
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

func runFreeze(priv *bec.PrivateKey) error {
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	tbcF, _ := strconv.ParseFloat(envOrDefault("PIGGY_TBC", "0.001"), 64)
	// FreezeTBCWithSign takes satoshis (1 TBC = 1_000_000 sat)
	tbcSat := uint64(tbcF * 1_000_000)
	lt64, _ := strconv.ParseUint(strings.TrimSpace(envOrDefault("PIGGY_LOCKTIME", "500000")), 10, 32)
	utxo, err := api.FetchUTXO(addr.AddressString, tbcF+0.03, nw())
	if err != nil {
		return err
	}
	raw, err := contract.FreezeTBCWithSign(priv, tbcSat, uint32(lt64), []*bt.UTXO{utxo})
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("freeze txid:", txid)
	return err
}

func frozenUtxo() (*bt.UTXO, error) {
	txid := strings.TrimSpace(os.Getenv("PIGGY_FROZEN_TXID"))
	if txid == "" {
		return nil, fmt.Errorf("unfreeze 需要 PIGGY_FROZEN_TXID")
	}
	vo, _ := strconv.ParseUint(envOrDefault("PIGGY_FROZEN_VOUT", "0"), 10, 32)
	tx, err := api.FetchTXRaw(txid, nw())
	if err != nil {
		return nil, err
	}
	if int(vo) >= len(tx.Outputs) {
		return nil, fmt.Errorf("vout")
	}
	tb, err := hex.DecodeString(strings.TrimSpace(strings.ToLower(txid)))
	if err != nil || len(tb) != 32 {
		return nil, fmt.Errorf("txid")
	}
	out := tx.Outputs[vo]
	return &bt.UTXO{TxID: tb, Vout: uint32(vo), LockingScript: out.LockingScript, Satoshis: out.Satoshis}, nil
}

func runUnfreeze(priv *bec.PrivateKey) error {
	u, err := frozenUtxo()
	if err != nil {
		return err
	}
	// UnfreezeTBCWithSign needs the current block height (not a network string).
	// Use api.FetchTBCLockTime to retrieve it.
	blockHeight, err := api.FetchTBCLockTime(nw())
	if err != nil {
		return err
	}
	raw, err := contract.UnfreezeTBCWithSign(priv, []*bt.UTXO{u}, blockHeight)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("unfreeze txid:", txid)
	return err
}

func main() {
	priv := mustPriv()
	var err error
	if strings.ToLower(strings.TrimSpace(envOrDefault("PIGGY_ACTION", "freeze"))) == "unfreeze" {
		err = runUnfreeze(priv)
	} else {
		err = runFreeze(priv)
	}
	if err != nil {
		panic(err)
	}
}
```

## 预期

- `freeze` 广播后可用索引 **`FetchFrozenUTXOList`** 等核对；`unfreeze` 需在锁定期过后且输入为可花费冻结 UTXO（与 TS 一致）。
