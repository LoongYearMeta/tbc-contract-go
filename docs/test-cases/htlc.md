# 测试场景：HTLC

**规范对照**：[tbc-contract/docs/htlc.md](../../tbc-contract/docs/htlc.md)  
**Go 实现**：`lib/contract/htlc.go`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 部署方 / 接收方 / 退款方 WIF（按 `HTLC_ACTION`） | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `HTLC_ACTION` | 否 | `deploy` / `withdraw` / `refund` | `deploy` |
| `HTLC_RECEIVER` | `deploy` 时必填 | 接收方地址 | — |
| `HTLC_SECRET_HEX` | `deploy` / `withdraw` | 32 字节原像 hex（deploy 用于算 hash；withdraw 解锁） | — |
| `HTLC_TIMELOCK` | `deploy` / `refund` | Unix 时间戳（秒） | — |
| `HTLC_LOCK_TBC` | 否 | 锁定金额（TBC float） | `0.001` |
| `HTLC_FEE_TBC` | 否 | 部署时额外预留 UTXO 的 TBC | `0.001` |
| `HTLC_TXID` | `withdraw` / `refund` | 部署交易 txid（hex） | — |
| `HTLC_VOUT` | 否 | 合约输出索引 | `0` |

`deploy`：若未设 `HTLC_SECRET_HEX`，脚本内会 **随机生成** 32 字节并打印，请保存后再做 `withdraw`。

---

## 最小可执行脚本

```bash
export TBC_WIF='...'
export HTLC_ACTION=deploy
export HTLC_RECEIVER=1...
export HTLC_TIMELOCK=1774427165
# 可选：export HTLC_SECRET_HEX=64位hex
go run .
```

```go
package main

import (
	"crypto/rand"
	"crypto/sha256"
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

func hashLockFromSecret(secretHex string) (string, error) {
	b, err := hex.DecodeString(strings.TrimSpace(secretHex))
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("HTLC_SECRET_HEX 须为 64 位 hex")
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func utxoFromOutpoint(nw, txid string, vout uint32) (*bt.UTXO, error) {
	tx, err := api.FetchTXRaw(strings.TrimSpace(strings.ToLower(txid)), nw)
	if err != nil {
		return nil, err
	}
	if int(vout) >= len(tx.Outputs) {
		return nil, fmt.Errorf("vout out of range")
	}
	tid, err := hex.DecodeString(strings.TrimSpace(strings.ToLower(txid)))
	if err != nil || len(tid) != 32 {
		return nil, fmt.Errorf("txid")
	}
	out := tx.Outputs[vout]
	return &bt.UTXO{
		TxID: tid, Vout: vout, LockingScript: out.LockingScript, Satoshis: out.Satoshis,
	}, nil
}

func runDeploy(priv *bec.PrivateKey) error {
	sender, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	recv := strings.TrimSpace(os.Getenv("HTLC_RECEIVER"))
	if recv == "" {
		return fmt.Errorf("需要 HTLC_RECEIVER")
	}
	sec := strings.TrimSpace(os.Getenv("HTLC_SECRET_HEX"))
	if sec == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		sec = hex.EncodeToString(buf)
		fmt.Println("generated HTLC_SECRET_HEX (save for withdraw):", sec)
	}
	hl, err := hashLockFromSecret(sec)
	if err != nil {
		return err
	}
	tl64, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("HTLC_TIMELOCK")), 10, 32)
	if err != nil {
		return fmt.Errorf("HTLC_TIMELOCK: %w", err)
	}
	tl := uint32(tl64)
	// DeployHTLCWithSign takes satoshis (1 TBC = 1_000_000 sat)
	lockF, _ := strconv.ParseFloat(envOrDefault("HTLC_LOCK_TBC", "0.001"), 64)
	feeF, _ := strconv.ParseFloat(envOrDefault("HTLC_FEE_TBC", "0.001"), 64)
	lockSat := uint64(lockF * 1_000_000)
	utxo, err := api.FetchUTXO(sender.AddressString, lockF+feeF, nw())
	if err != nil {
		return err
	}
	raw, err := contract.DeployHTLCWithSign(sender.AddressString, recv, hl, tl, lockSat, utxo, priv)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("deploy txid:", txid)
	return err
}

func runWithdraw(priv *bec.PrivateKey) error {
	tid := strings.TrimSpace(os.Getenv("HTLC_TXID"))
	sec := strings.TrimSpace(os.Getenv("HTLC_SECRET_HEX"))
	if tid == "" || sec == "" {
		return fmt.Errorf("withdraw 需要 HTLC_TXID 与 HTLC_SECRET_HEX")
	}
	vo, _ := strconv.ParseUint(envOrDefault("HTLC_VOUT", "0"), 10, 32)
	u, err := utxoFromOutpoint(nw(), tid, uint32(vo))
	if err != nil {
		return err
	}
	recv, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	raw, err := contract.WithdrawWithSign(priv, recv.AddressString, u, sec)
	if err != nil {
		return err
	}
	out, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("withdraw txid:", out)
	return err
}

func runRefund(priv *bec.PrivateKey) error {
	tid := strings.TrimSpace(os.Getenv("HTLC_TXID"))
	if tid == "" {
		return fmt.Errorf("refund 需要 HTLC_TXID")
	}
	vo, _ := strconv.ParseUint(envOrDefault("HTLC_VOUT", "0"), 10, 32)
	u, err := utxoFromOutpoint(nw(), tid, uint32(vo))
	if err != nil {
		return err
	}
	sender, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	tl64, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("HTLC_TIMELOCK")), 10, 32)
	if err != nil {
		return fmt.Errorf("HTLC_TIMELOCK: %w", err)
	}
	raw, err := contract.RefundWithSign(sender.AddressString, u, priv, uint32(tl64))
	if err != nil {
		return err
	}
	out, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("refund txid:", out)
	return err
}

func main() {
	priv := mustPriv()
	switch strings.ToLower(strings.TrimSpace(envOrDefault("HTLC_ACTION", "deploy"))) {
	case "withdraw":
		if err := runWithdraw(priv); err != nil {
			panic(err)
		}
	case "refund":
		if err := runRefund(priv); err != nil {
			panic(err)
		}
	default:
		if err := runDeploy(priv); err != nil {
			panic(err)
		}
	}
}
```

## 预期

- `refund` 仅在链上时间 **大于** `HTLC_TIMELOCK` 后成功；`withdraw` 需原像正确且在锁定期内。
