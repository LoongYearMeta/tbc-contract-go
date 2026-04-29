# NFT 测试场景（Go）

参照 TS：[`../tbc-contract/docs/nft.md`](../../tbc-contract/docs/nft.md)。覆盖创建合集（CreateCollection）、单个铸造（CreateNFT）、批量铸造（BatchCreateNFT）、单笔转移（TransferNFT）四条主路径。

> 图片以 `data:image/<png|jpeg>;base64,...` 的字符串塞进 `CollectionData.File` / `NFTData.File`。Mint 阶段的 NFT 来源 UTXO 走 `api.FetchNFTUTXO` 而不是 `FetchUTXO`。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF` | 是 | 操作者 WIF（推导出收款 / 铸造地址） |
| `TBC_NETWORK` | 否 | `"testnet"` / `"mainnet"`，默认 `"testnet"` |
| `NFT_FILE_PATH` | CreateCollection 必填 | 本地 PNG / JPG 路径，将编码为 base64 |
| `NFT_COLLECTION_ID` | CreateNFT / Transfer 必填 | CreateCollection 返回的 txid |
| `NFT_CONTRACT_ID` | Transfer 必填 | CreateNFT 返回的 txid |
| `NFT_TRANSFER_TO` | Transfer 必填 | 接收方地址 |

## 最小可执行脚本

```go
package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
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

func encodeByBase64(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	mime := "image/jpeg"
	if strings.EqualFold(filepath.Ext(p), ".png") {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func main() {
	network := env("TBC_NETWORK", "testnet")
	wifStr := strings.TrimSpace(os.Getenv("TBC_WIF"))
	if wifStr == "" {
		fmt.Println("请设置 TBC_WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	must(err)
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	must(err)
	address := addr.AddressString
	collectionID := strings.TrimSpace(os.Getenv("NFT_COLLECTION_ID"))
	contractID := strings.TrimSpace(os.Getenv("NFT_CONTRACT_ID"))

	// ============ CreateCollection（约每 100KB 图片 0.05 TBC） =============
	{
		filePath := strings.TrimSpace(os.Getenv("NFT_FILE_PATH"))
		if filePath == "" {
			fmt.Println("请设置 NFT_FILE_PATH 后再创建合集")
		} else {
			content, err := encodeByBase64(filePath)
			must(err)
			data := &contract.CollectionData{
				CollectionName: "demo",
				Description:    "demo collection",
				Supply:         10,
				File:           content,
			}
			utxos, err := api.GetUTXOs(address, 0.2, network)
			must(err)
			raw, err := contract.CreateCollection(address, priv, data, utxos)
			must(err)
			id, err := api.BroadcastTXRaw(raw, network)
			must(err)
			fmt.Println("Collection ID:", id)
			collectionID = id
		}
	}

	// ============ CreateNFT（合集下单只） =============
	{
		filePath := strings.TrimSpace(os.Getenv("NFT_FILE_PATH"))
		var fileContent string
		if filePath != "" {
			c, err := encodeByBase64(filePath)
			must(err)
			fileContent = c
		}
		data := &contract.NFTData{
			NftName:     "demo nft",
			Symbol:      "demo",
			Description: "first one",
			Attributes:  "{}",
			File:        fileContent, // 留空则复用合集图
		}
		var utxos []*bt.UTXO
		if data.File != "" {
			utxos, err = api.GetUTXOs(address, 0.2, network)
		} else {
			utxos, err = api.GetUTXOs(address, 0.001, network)
		}
		must(err)
		mintScript, err := contract.BuildMintScript(address)
		must(err)
		nftUTXO, err := api.FetchNFTUTXO(hex.EncodeToString(mintScript.Bytes()), collectionID, network)
		must(err)
		raw, err := contract.CreateNFT(collectionID, address, priv, data, utxos, nftUTXO)
		must(err)
		id, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("NFT contract ID:", id)
		contractID = id
	}

	// ============ BatchCreateNFT（合集图共享） =============
	{
		number := 10 // 演示用 10 个，TS 示例为 1000
		datas := make([]contract.NFTData, number)
		for i := 0; i < number; i++ {
			datas[i] = contract.NFTData{
				NftName:     fmt.Sprintf("demo nft #%d", i),
				Symbol:      "demo",
				Description: "batch",
				Attributes:  "{}",
			}
		}
		utxos, err := api.GetUTXOs(address, 0.001*float64(number), network)
		must(err)
		mintScript, err := contract.BuildMintScript(address)
		must(err)
		nftUTXOs, err := api.FetchNFTUTXOs(hex.EncodeToString(mintScript.Bytes()), collectionID, network)
		must(err)
		if len(nftUTXOs) > number {
			nftUTXOs = nftUTXOs[:number]
		}
		raws, err := contract.BatchCreateNFT(collectionID, address, priv, datas, utxos, nftUTXOs, network)
		must(err)
		_, err = api.BroadcastTXsRaw(raws, network)
		must(err)
		fmt.Println("BatchCreateNFT broadcast:", len(raws))
	}

	// ============ TransferNFT =============
	{
		addressTo := strings.TrimSpace(os.Getenv("NFT_TRANSFER_TO"))
		if addressTo == "" {
			fmt.Println("跳过 transfer：未设置 NFT_TRANSFER_TO")
			return
		}
		nft := contract.NewNFT(contractID)
		info, err := api.FetchNFTInfo(contractID, network)
		must(err)
		nft.Initialize(info)

		utxos, err := api.GetUTXOs(address, 0.01, network)
		must(err)
		code, err := contract.BuildCodeScript(info.CollectionID, uint32(info.CollectionIndex))
		must(err)
		nftUTXO, err := api.FetchNFTUTXO(hex.EncodeToString(code.Bytes()), "", network)
		must(err)
		preTx, err := api.FetchTXRaw(hex.EncodeToString(nftUTXO.TxID), network)
		must(err)
		// 上一笔转移交易的父交易：当 nft_transfer_count == 0 时取 collectionTx，否则取 preTx 的第一个输入的前置交易
		var prePreTx *bt.Tx
		if info.NFTTransferTimeCount == 0 {
			prePreTx, err = api.FetchTXRaw(info.CollectionID, network)
		} else {
			prevTxIDHex := hex.EncodeToString(preTx.Inputs[0].PreviousTxID())
			prePreTx, err = api.FetchTXRaw(prevTxIDHex, network)
		}
		must(err)

		raw, err := nft.TransferNFT(address, addressTo, priv, utxos, preTx, prePreTx, false)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Transfer:", txid)
	}
}
```
