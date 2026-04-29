# HTLC 测试场景（Go）

参照 TS：[`../tbc-contract/docs/htlc.md`](../../tbc-contract/docs/htlc.md)。覆盖 Deploy / Withdraw / Refund 三条主路径，分别给出 **带私钥签名** 与 **不带私钥签名（前端构建 + 钱包签名）** 两种用法。

> Go 端 HTLC 数量参数是 `uint64` satoshi（不是 TBC float64）。`amount + fee` 的 UTXO 仍以 TBC 调用 `api.FetchUTXO`。
> `ASM` 中分支选择使用 `OP_TRUE` / `OP_FALSE`（不是 TS 的 `"1"` / `"0"` 字面量）—— `bscript.NewFromASM` 不接受奇数长度十六进制字符串。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF_SENDER` | 是 | 发送方 WIF |
| `TBC_WIF_RECEIVER` | 是 | 接收方 WIF |
| `TBC_NETWORK` | 否 | 默认 `"testnet"` |
| `HTLC_AMOUNT_TBC` | 否 | 锁定金额（TBC），默认 `0.001` |
| `HTLC_TIMELOCK` | 否 | 退款时间锁（unix 秒），默认 `1774427165` |
| `HTLC_TXID` | Withdraw/Refund 必填 | DeployHTLC 后获得的 txid |
| `HTLC_SECRET_HEX` | Withdraw 必填 | 32 字节随机数（hex） |

## 最小可执行脚本

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

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
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

func loadKey(envName string) (*bec.PrivateKey, string, string) {
	w := strings.TrimSpace(os.Getenv(envName))
	if w == "" {
		panic("missing " + envName)
	}
	dec, err := wif.DecodeWIF(w)
	must(err)
	addr, err := bscript.NewAddressFromPublicKey(dec.PrivKey.PubKey(), true)
	must(err)
	pub := hex.EncodeToString(dec.PrivKey.PubKey().SerialiseCompressed())
	return dec.PrivKey, addr.AddressString, pub
}

func main() {
	network := env("TBC_NETWORK", "testnet")
	privSender, addrSender, pubSender := loadKey("TBC_WIF_SENDER")
	privReceiver, addrReceiver, pubReceiver := loadKey("TBC_WIF_RECEIVER")

	amountTBC, _ := strconv.ParseFloat(env("HTLC_AMOUNT_TBC", "0.001"), 64)
	amountSat := uint64(amountTBC * 1e6)
	feeTBC := 0.001

	timelock64, _ := strconv.ParseUint(env("HTLC_TIMELOCK", "1774427165"), 10, 32)
	timelock := uint32(timelock64)

	// 生成 secret / hashlock（仅 Deploy 路径需要；Withdraw/Refund 用环境变量传入）
	var secret, hashlock string
	if v := strings.TrimSpace(os.Getenv("HTLC_SECRET_HEX")); v != "" {
		secret = v
		raw, err := hex.DecodeString(secret)
		must(err)
		sum := sha256.Sum256(raw)
		hashlock = hex.EncodeToString(sum[:])
	} else {
		buf := make([]byte, 32)
		_, err := rand.Read(buf)
		must(err)
		secret = hex.EncodeToString(buf)
		sum := sha256.Sum256(buf)
		hashlock = hex.EncodeToString(sum[:])
		fmt.Println("generated secret:", secret)
		fmt.Println("hashlock:", hashlock)
	}

	htlcTxid := strings.TrimSpace(os.Getenv("HTLC_TXID"))

	// =========================================================
	// 带私钥签名
	// =========================================================

	// DeployHTLC（发送方部署）
	{
		utxo, err := api.FetchUTXO(addrSender, amountTBC+feeTBC, network)
		must(err)
		raw, err := contract.DeployHTLCWithSign(addrSender, addrReceiver, hashlock, timelock, amountSat, utxo, privSender)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Deploy:", txid)
		htlcTxid = txid // 记录给后续步骤
	}

	// Withdraw（接收方在 timelock 之前用 secret 提取）
	{
		const outputIndex uint32 = 0
		htlcTX, err := api.FetchTXRaw(htlcTxid, network)
		must(err)
		txidBytes, err := hex.DecodeString(htlcTxid)
		must(err)
		htlcUTXO := &bt.UTXO{
			TxID:          txidBytes,
			Vout:          outputIndex,
			LockingScript: htlcTX.Outputs[outputIndex].LockingScript,
			Satoshis:      htlcTX.Outputs[outputIndex].Satoshis,
		}
		raw, err := contract.WithdrawWithSign(privReceiver, addrReceiver, htlcUTXO, secret)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Withdraw:", txid)
	}

	// Refund（timelock 过期后由发送方退款）
	{
		const outputIndex uint32 = 0
		htlcTX, err := api.FetchTXRaw(htlcTxid, network)
		must(err)
		txidBytes, err := hex.DecodeString(htlcTxid)
		must(err)
		htlcUTXO := &bt.UTXO{
			TxID:          txidBytes,
			Vout:          outputIndex,
			LockingScript: htlcTX.Outputs[outputIndex].LockingScript,
			Satoshis:      htlcTX.Outputs[outputIndex].Satoshis,
		}
		raw, err := contract.RefundWithSign(addrSender, htlcUTXO, privSender, timelock)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Refund:", txid)
	}

	// =========================================================
	// 不带私钥签名（前端构造 + 钱包签名场景）
	// =========================================================

	// Deploy
	{
		utxo, err := api.FetchUTXO(addrSender, amountTBC+feeTBC, network)
		must(err)
		raw, err := contract.DeployHTLC(addrSender, addrReceiver, hashlock, timelock, amountSat, utxo)
		must(err)
		// raw 中输入 0 是 P2PKH，需要钱包对其签名后填入
		sig := "" // 来自钱包
		signed, err := contract.FillSigDeploy(raw, sig, pubSender)
		must(err)
		_, _ = api.BroadcastTXRaw(signed, network)
	}

	// Withdraw
	{
		const outputIndex uint32 = 0
		htlcTX, err := api.FetchTXRaw(htlcTxid, network)
		must(err)
		txidBytes, _ := hex.DecodeString(htlcTxid)
		htlcUTXO := &bt.UTXO{
			TxID:          txidBytes,
			Vout:          outputIndex,
			LockingScript: htlcTX.Outputs[outputIndex].LockingScript,
			Satoshis:      htlcTX.Outputs[outputIndex].Satoshis,
		}
		raw, err := contract.Withdraw(addrReceiver, htlcUTXO)
		must(err)
		sig := "" // 钱包对 raw 输入 0 签名
		signed, err := contract.FillSigWithdraw(raw, secret, sig, pubReceiver)
		must(err)
		_, _ = api.BroadcastTXRaw(signed, network)
	}

	// Refund
	{
		const outputIndex uint32 = 0
		htlcTX, err := api.FetchTXRaw(htlcTxid, network)
		must(err)
		txidBytes, _ := hex.DecodeString(htlcTxid)
		htlcUTXO := &bt.UTXO{
			TxID:          txidBytes,
			Vout:          outputIndex,
			LockingScript: htlcTX.Outputs[outputIndex].LockingScript,
			Satoshis:      htlcTX.Outputs[outputIndex].Satoshis,
		}
		raw, err := contract.Refund(addrSender, htlcUTXO, timelock)
		must(err)
		sig := "" // 钱包对 raw 输入 0 签名
		signed, err := contract.FillSigRefund(raw, sig, pubSender)
		must(err)
		_, _ = api.BroadcastTXRaw(signed, network)
	}
}
```
