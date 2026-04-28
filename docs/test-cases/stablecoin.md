# 测试场景：稳定币（StableCoin）

**规范对照**：[tbc-contract/docs/stableCoin.md](../../tbc-contract/docs/stableCoin.md)  
**Go 实现**：`lib/contract/stablecoin.go`；索引：`lib/api`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 管理员或付款方 WIF | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `STABLE_ACTION` | 否 | `create` / `mint` / `transfer` | `create` |
| `COIN_CONTRACT_TXID` | `mint`、`transfer` 时必填 | 稳定币合约 id（多为 **CreateCoin 首笔 coin NFT** 的 txid） | — |
| `STABLE_MINT_MESSAGE` | 否 | 跨链/审计说明，可空 | 空 |
| `STABLE_MINT_AMOUNT` | `mint` 时建议设 | 增发数量（字符串） | `50000` |
| `STABLE_MINT_TO` | 否 | 增发接收地址，空则用管理员地址 | 空 |
| `STABLE_TO` | `transfer` 时必填 | 收款地址 | — |
| `STABLE_AMOUNT` | `transfer` 时建议设 | 转账数量（字符串） | `1000` |
| `COIN_NAME` / `COIN_SYMBOL` / `COIN_DECIMAL` / `COIN_SUPPLY` | 否 | CreateCoin 元数据 | `USD Test` / `USDT` / `6` / `100000000` |

---

## 最小可执行脚本

```bash
export TBC_WIF='...'
export STABLE_ACTION=create    # 或 mint / transfer
# mint / transfer:
# export COIN_CONTRACT_TXID=...
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
	d, err := wif.DecodeWIF(os.Getenv("TBC_WIF"))
	if err != nil {
		panic(err)
	}
	return d.PrivKey
}

func nw() string { return envOrDefault("TBC_NETWORK", "testnet") }

func initSCFromInfo(si *api.StableCoinInfoResult, cid string) (*contract.StableCoin, error) {
	sc, err := contract.NewStableCoin(cid)
	if err != nil {
		return nil, err
	}
	total, err := strconv.ParseInt(strings.TrimSpace(si.TotalSupply), 10, 64)
	if err != nil {
		return nil, err
	}
	sc.Initialize(&contract.FtInfo{
		Name: si.Name, Symbol: si.Symbol, Decimal: int(si.Decimal),
		TotalSupply: total, CodeScript: si.CodeScript, TapeScript: si.TapeScript,
	})
	return sc, nil
}

func runCreate(priv *bec.PrivateKey) error {
	admin, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	dec, _ := strconv.Atoi(envOrDefault("COIN_DECIMAL", "6"))
	sup, _ := strconv.ParseInt(envOrDefault("COIN_SUPPLY", "100000000"), 10, 64)
	sc, err := contract.NewStableCoin(&contract.FtParams{
		Name: envOrDefault("COIN_NAME", "USD Test"),
		Symbol: envOrDefault("COIN_SYMBOL", "USDT"),
		Amount: sup, Decimal: dec,
	})
	if err != nil {
		return err
	}
	utxo, err := api.FetchUTXO(admin.AddressString, 0.03, nw())
	if err != nil {
		return err
	}
	utxoTX, err := api.FetchTXRaw(hex.EncodeToString(utxo.TxID), nw())
	if err != nil {
		return err
	}
	txs, err := sc.CreateCoin(priv, admin.AddressString, utxo, utxoTX, envOrDefault("STABLE_MINT_MESSAGE", ""))
	if err != nil {
		return err
	}
	c0, err := api.BroadcastTXRaw(txs[0], nw())
	if err != nil {
		return err
	}
	c1, err := api.BroadcastTXRaw(txs[1], nw())
	if err != nil {
		return err
	}
	fmt.Println("coin nft txid (常用作 COIN_CONTRACT_TXID):", c0)
	fmt.Println("mint txid:", c1)
	return nil
}

func runMint(priv *bec.PrivateKey) error {
	cid := strings.TrimSpace(os.Getenv("COIN_CONTRACT_TXID"))
	if cid == "" {
		return fmt.Errorf("mint 需要 COIN_CONTRACT_TXID")
	}
	si, err := api.FetchStableCoinInfo(cid, nw())
	if err != nil {
		return err
	}
	sc, err := initSCFromInfo(si, cid)
	if err != nil {
		return err
	}
	admin, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	utxo, err := api.FetchUTXO(admin.AddressString, 0.03, nw())
	if err != nil {
		return err
	}
	nftPreTX, err := api.FetchTXRaw(si.NftTXID, nw())
	if err != nil {
		return err
	}
	prev := strings.TrimSpace(strings.ToLower(hex.EncodeToString(nftPreTX.Inputs[0].PreviousTxID())))
	nftPrePre, err := api.FetchTXRaw(prev, nw())
	if err != nil {
		return err
	}
	to := strings.TrimSpace(envOrDefault("STABLE_MINT_TO", admin.AddressString))
	raw, err := sc.MintCoin(priv, to, envOrDefault("STABLE_MINT_AMOUNT", "50000"), utxo, nftPreTX, nftPrePre, envOrDefault("STABLE_MINT_MESSAGE", ""))
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("mint coin txid:", txid)
	return err
}

func runTransfer(priv *bec.PrivateKey) error {
	cid := strings.TrimSpace(os.Getenv("COIN_CONTRACT_TXID"))
	to := strings.TrimSpace(os.Getenv("STABLE_TO"))
	if cid == "" || to == "" {
		return fmt.Errorf("transfer 需要 COIN_CONTRACT_TXID 与 STABLE_TO")
	}
	si, err := api.FetchStableCoinInfo(cid, nw())
	if err != nil {
		return err
	}
	sc, err := initSCFromInfo(si, cid)
	if err != nil {
		return err
	}
	fromAddr, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	from := fromAddr.AddressString
	amt := envOrDefault("STABLE_AMOUNT", "1000")

	cs, err := contract.BuildFTtransferCode(sc.CodeScript, from)
	if err != nil {
		return err
	}
	codeHex := hex.EncodeToString(cs.Bytes())
	need := util.ParseDecimalToBigInt(amt, sc.Decimal)
	ftutxos, err := api.FetchFtUTXOs(cid, from, codeHex, nw(), need)
	if err != nil {
		return err
	}
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepre := make([]string, len(ftutxos))
	var err2 error
	for i, fu := range ftutxos {
		preTXs[i], err2 = api.FetchTXRaw(fu.TxID, nw())
		if err2 != nil {
			return err2
		}
		prepre[i], err2 = api.FetchFtPrePreTxData(preTXs[i], int(fu.Vout), nw())
		if err2 != nil {
			return err2
		}
	}
	feeUTXO, err := api.FetchUTXO(from, 0.03, nw())
	if err != nil {
		return err
	}
	raw, err := sc.TransferCoin(priv, to, amt, ftutxos, feeUTXO, preTXs, prepre, 0)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("transfer txid:", txid)
	return err
}

func main() {
	priv := mustPriv()
	switch strings.ToLower(strings.TrimSpace(envOrDefault("STABLE_ACTION", "create"))) {
	case "mint":
		if err := runMint(priv); err != nil {
			panic(err)
		}
	case "transfer":
		if err := runTransfer(priv); err != nil {
			panic(err)
		}
	default:
		if err := runCreate(priv); err != nil {
			panic(err)
		}
	}
}
```

## 预期

- `create` 广播顺序与 `stableCoin.md` 一致；`COIN_CONTRACT_TXID` 一般填 **首笔 coin NFT** 的 txid。
- `mint` 依赖 `FetchStableCoinInfo` 的 **`NftTXID`**；索引 id 与 mint 不一致时参见 `api.StableCoinIndexerIDFromMintContractTx`。
