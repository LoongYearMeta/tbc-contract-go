# FT 测试场景（Go）

参照 TS：[`../tbc-contract/docs/ft.md`](../../tbc-contract/docs/ft.md)。覆盖铸造（Mint）、单笔转账（Transfer）、批量转账（BatchTransfer）、合并 UTXO（MergeFT）四条主路径。

> Go 端 FT 金额统一为 **`*big.Int`**：调用方若手里是十进制字符串（如 `"1000"`），先用 `util.ParseDecimalToBigInt(amount, decimal)` 转换；FT 元数据 `*api.FtInfoResponse` 与 `*contract.FtInfo` 字段一致但是不同结构体，需要手工拷贝（见示例中的 `ftInfoFromAPI`）。
> Go 端 `Transfer` 的 `tbcAmount` 参数是 `uint64` satoshis，与 TS 的 number TBC 不同；只转 FT 时填 0。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF_A` | 是 | 发送方 WIF（推导出 `addressA`） |
| `TBC_ADDRESS_B` | 是 | 接收方 P2PKH 地址 |
| `TBC_NETWORK` | 否 | `"testnet"` / `"mainnet"`，默认 `"testnet"` |
| `FT_NAME` / `FT_SYMBOL` | Mint 时必填 | FT 名称 / 符号 |
| `FT_DECIMAL` | Mint 时必填 | 小数位（建议 6） |
| `FT_AMOUNT` | Mint 时必填 | 总供应量；精度 6 上限 1 万亿 |
| `FT_CONTRACT_TXID` | Transfer/Merge 必填 | Mint 后获得的合约 txid |

## 最小可执行脚本

业务模块需先 `require github.com/LoongYearMeta/tbc-contract-go` 并配置 `replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go`，将下列文件存为 `main.go` 后 `go run .`。

```go
package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
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

func ftInfoFromAPI(txid string, info *api.FtInfoResponse) *contract.FtInfo {
	return &contract.FtInfo{
		ContractTxid: txid,
		Name:         info.Name,
		Symbol:       info.Symbol,
		Decimal:      info.Decimal,
		TotalSupply:  info.TotalSupply,
		CodeScript:   info.CodeScript,
		TapeScript:   info.TapeScript,
	}
}

func main() {
	network := env("TBC_NETWORK", "testnet")
	wifStr := strings.TrimSpace(os.Getenv("TBC_WIF_A"))
	addressB := strings.TrimSpace(os.Getenv("TBC_ADDRESS_B"))
	if wifStr == "" || addressB == "" {
		fmt.Println("请设置 TBC_WIF_A 和 TBC_ADDRESS_B")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	must(err)
	privA := dec.PrivKey
	addrA, err := bscript.NewAddressFromPublicKey(privA.PubKey(), true)
	must(err)
	addressA := addrA.AddressString
	ftContractTxid := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID"))

	// ============ Mint =============
	{
		newToken, err := contract.NewFT(&contract.FtParams{
			Name:    env("FT_NAME", "test"),
			Symbol:  env("FT_SYMBOL", "test"),
			Amount:  100000000, // 精度 6，上限 1 万亿
			Decimal: 6,
		})
		must(err)
		utxo, err := api.FetchUTXO(addressA, 0.01, network)
		must(err)
		mintTX, err := newToken.MintFT(privA, addressA, utxo)
		must(err)
		_, err = api.BroadcastTXRaw(mintTX[0], network)
		must(err)
		fmt.Println("FT Contract ID:")
		txid, err := api.BroadcastTXRaw(mintTX[1], network)
		must(err)
		fmt.Println(txid)
	}

	// ============ Transfer =============
	{
		token, err := contract.NewFT(ftContractTxid)
		must(err)
		info, err := api.FetchFtInfo(token.ContractTxid, network)
		must(err)
		token.Initialize(ftInfoFromAPI(token.ContractTxid, info))

		var tbcAmount float64 = 0 // 需要同时转 TBC + FT 时设置（单位 TBC）
		utxo, err := api.FetchUTXO(addressA, tbcAmount+0.01, network)
		must(err)

		amountBN, err := util.ParseDecimalToBigInt("1000", token.Decimal)
		must(err)

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addressA, ftCodeHex, network, amountBN)
		must(err)

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}

		// 同时转 TBC：用 uint64(tbcAmount * 1e6) 替换 0
		var tbcAmountSat uint64 = 0
		raw, err := token.Transfer(privA, addressB, amountBN, ftutxos, utxo, preTXs, prepreTxDatas, tbcAmountSat)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Transfer:", txid)
	}

	// ============ BatchTransfer（最多每 5 人一笔，自动链式拆分） =============
	{
		token, err := contract.NewFT(ftContractTxid)
		must(err)
		info, err := api.FetchFtInfo(token.ContractTxid, network)
		must(err)
		token.Initialize(ftInfoFromAPI(token.ContractTxid, info))

		mustBN := func(s string) *big.Int {
			n, e := util.ParseDecimalToBigInt(s, token.Decimal)
			must(e)
			return n
		}
		receivers := []contract.AddressAmount{
			{Address: addressA, Amount: mustBN("500")},
			{Address: addressB, Amount: mustBN("700")},
			// ... 任意条数；每 5 条切成一笔
		}
		batchCount := (len(receivers) + 4) / 5
		transferFee := 0.005 * float64(batchCount)
		utxo, err := api.FetchUTXO(addressA, transferFee, network)
		must(err)

		// 给 FetchFtUTXOs 用的总额
		total := new(big.Int)
		for _, r := range receivers {
			total.Add(total, r.Amount)
		}

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addressA, ftCodeHex, network, total)
		must(err)
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}
		raws, err := token.BatchTransfer(privA, receivers, ftutxos, utxo, preTXs, prepreTxDatas)
		must(err)
		if len(raws) > 0 {
			_, err = api.BroadcastTXsRaw(raws, network)
			must(err)
			fmt.Println("BatchTransfer broadcast:", len(raws))
		} else {
			fmt.Println("BatchTransfer failed")
		}
	}

	// ============ MergeFT（要求所有 ftutxo 已上链） =============
	{
		token, err := contract.NewFT(ftContractTxid)
		must(err)
		info, err := api.FetchFtInfo(token.ContractTxid, network)
		must(err)
		token.Initialize(ftInfoFromAPI(token.ContractTxid, info))

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOList(token.ContractTxid, addressA, ftCodeHex, network)
		must(err)

		mergeFee := 0.005 * float64(len(ftutxos))
		utxo, err := api.FetchUTXO(addressA, mergeFee, network)
		must(err)

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}
		// localTXs 通常传 nil；当 utxo 不来自链上时可注入本地交易
		raws, err := token.MergeFT(privA, ftutxos, utxo, preTXs, prepreTxDatas, nil)
		must(err)
		if len(raws) > 0 {
			_, err = api.BroadcastTXsRaw(raws, network)
			must(err)
			fmt.Println("MergeFT chained txs:", len(raws))
		} else {
			fmt.Println("Merge success (already merged)")
		}
	}
}
```
