# 测试场景：FT（同质化代币）

**规范对照**：[tbc-contract/docs/ft.md](../../tbc-contract/docs/ft.md)  
**Go 实现**：`lib/contract/ft.go`；索引与广播：`lib/api`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 付款方 WIF | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `FT_ACTION` | 否 | `mint`：新铸；`transfer`：已存在合约转账 | `mint` |
| `FT_CONTRACT_TXID` | `transfer` 时必填 | 已部署 FT 合约（mint 第二笔）txid | — |
| `FT_TO` | `transfer` 时必填 | 收款地址 | — |
| `FT_AMOUNT` | 否 | 转账数量（十进制字符串，与 JS 一致） | `1000` |
| `FT_NAME` | 否 | 新铸名称 | `test` |
| `FT_SYMBOL` | 否 | 新铸符号 | `test` |
| `FT_DECIMAL` | 否 | 精度 | `6` |
| `FT_SUPPLY` | 否 | 总供应（人类可读整数，非最小单位） | `100000000` |

---

## 最小可执行脚本

将下列内容保存为业务模块中的 **`main.go`**（与 `go.mod` 中 `require` / `replace` 已指向本库及 `tbc-lib-go`），然后执行：

```bash
export TBC_WIF='你的WIF'
export FT_ACTION=mint          # 或 transfer
# transfer 时：
# export FT_CONTRACT_TXID=...
# export FT_TO=1xxx...
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
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func mustPriv() *bec.PrivateKey {
	dec, err := wif.DecodeWIF(os.Getenv("TBC_WIF"))
	if err != nil {
		panic("TBC_WIF: " + err.Error())
	}
	return dec.PrivKey
}

func network() string {
	return envOrDefault("TBC_NETWORK", "testnet")
}

func ftInfoFromAPI(info *api.FtInfo) *contract.FtInfo {
	ts := strings.TrimSpace(info.TotalSupply)
	total, _ := strconv.ParseInt(ts, 10, 64)
	return &contract.FtInfo{
		Name: info.Name, Symbol: info.Symbol,
		Decimal: int(info.Decimal), TotalSupply: total,
		CodeScript: info.CodeScript, TapeScript: info.TapeScript,
	}
}

func loadFT(nw, contractTxid string, priv *bec.PrivateKey) (*contract.FT, error) {
	info, err := api.FetchFtInfo(contractTxid, nw)
	if err != nil {
		return nil, err
	}
	ft, err := contract.NewFT(contractTxid)
	if err != nil {
		return nil, err
	}
	ft.Initialize(ftInfoFromAPI(info))
	return ft, nil
}

func runMint(priv *bec.PrivateKey, nw string) error {
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	from := addr.AddressString
	dec, _ := strconv.Atoi(envOrDefault("FT_DECIMAL", "6"))
	sup, _ := strconv.ParseInt(envOrDefault("FT_SUPPLY", "100000000"), 10, 64)
	ft, err := contract.NewFT(&contract.FtParams{
		Name: envOrDefault("FT_NAME", "test"), Symbol: envOrDefault("FT_SYMBOL", "test"),
		Amount: sup, Decimal: dec,
	})
	if err != nil {
		return err
	}
	utxo, err := api.FetchUTXO(from, 0.02, nw)
	if err != nil {
		return err
	}
	raws, err := ft.MintFT(priv, from, utxo)
	if err != nil {
		return err
	}
	t0, err := api.BroadcastTXRaw(raws[0], nw)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	t1, err := api.BroadcastTXRaw(raws[1], nw)
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	fmt.Println("mint source txid:", t0)
	fmt.Println("mint txid (常用作合约 id):", t1)
	return nil
}

func runTransfer(priv *bec.PrivateKey, nw string) error {
	cid := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID"))
	to := strings.TrimSpace(os.Getenv("FT_TO"))
	if cid == "" || to == "" {
		return fmt.Errorf("transfer 需要 FT_CONTRACT_TXID 与 FT_TO")
	}
	ft, err := loadFT(nw, cid, priv)
	if err != nil {
		return err
	}
	fromAddr, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	from := fromAddr.AddressString
	amt := envOrDefault("FT_AMOUNT", "1000")

	cs, err := contract.BuildFTtransferCode(ft.CodeScript, from)
	if err != nil {
		return err
	}
	codeHex := hex.EncodeToString(cs.Bytes())
	need := util.ParseDecimalToBigInt(amt, ft.Decimal)
	ftutxos, err := api.FetchFtUTXOs(cid, from, codeHex, nw, need)
	if err != nil {
		return err
	}
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepre := make([]string, len(ftutxos))
	for i, fu := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(fu.TxID, nw)
		if err != nil {
			return err
		}
		prepre[i], err = api.FetchFtPrePreTxData(preTXs[i], int(fu.Vout), nw)
		if err != nil {
			return err
		}
	}
	feeUTXO, err := api.FetchUTXO(from, 0.02, nw)
	if err != nil {
		return err
	}
	raw, err := ft.TransferDecimalString(priv, to, amt, ftutxos, feeUTXO, preTXs, prepre, 0)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw)
	if err != nil {
		return err
	}
	fmt.Println("transfer txid:", txid)
	return nil
}

func main() {
	priv := mustPriv()
	nw := network()
	switch strings.ToLower(strings.TrimSpace(envOrDefault("FT_ACTION", "mint"))) {
	case "transfer":
		if err := runTransfer(priv, nw); err != nil {
			panic(err)
		}
	default:
		if err := runMint(priv, nw); err != nil {
			panic(err)
		}
	}
}
```

## 预期

- `mint`：先广播 `raws[0]` 再 `raws[1]`；第二笔 txid 常作为后续 `FT_CONTRACT_TXID`。
- `transfer`：`TransferDecimalString` 与 TS `parseDecimalToBigInt` 路径一致；`TotalSupply` 极大时 `ftInfoFromAPI` 的 `ParseInt` 可能溢出，请在生产代码中改为安全解析（见库内其它用法）。
