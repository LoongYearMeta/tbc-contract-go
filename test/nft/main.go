// NFT 测试程序 — 对应 docs/test-cases/nft.md。
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

// === 配置区（直接改这里） ===
const (
	network        = "testnet"
	wifStr         = "" // 操作者 WIF（必填）
	filePath       = "" // PNG/JPG 路径，base64 编码后塞进合集 / NFT
	collectionName = "demo"
	nftName        = "demo nft"
	nftSymbol      = "demo"
	transferTo     = ""

	// 已有 id 时填入；CreateCollection / CreateNFT 后会被覆盖
	collectionIDInit = ""
	contractIDInit   = ""

	doCollection = false
	doMint       = false
	doBatch      = false
	doTransfer   = false
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func encodeByBase64(p string) string {
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	mustExit(err, "ReadFile")
	mime := "image/jpeg"
	if strings.EqualFold(filepath.Ext(p), ".png") {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func main() {
	if strings.TrimSpace(wifStr) == "" {
		fmt.Println("请在源码顶部把 wifStr 设成你的 WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	address := addr.AddressString
	collectionID := collectionIDInit
	contractID := contractIDInit

	if doCollection {
		if filePath == "" {
			fmt.Println("doCollection 需要设置 filePath")
			os.Exit(1)
		}
		data := &contract.CollectionData{
			CollectionName: collectionName,
			Description:    "demo collection",
			Supply:         10,
			File:           encodeByBase64(filePath),
		}
		utxos, err := api.GetUTXOs(address, 0.2, network)
		mustExit(err, "GetUTXOs")
		raw, err := contract.CreateCollection(address, priv, data, utxos)
		mustExit(err, "CreateCollection")
		id, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast collection")
		fmt.Println("collection id:", id)
		collectionID = id
	}

	if doMint {
		if collectionID == "" {
			fmt.Println("缺少 collectionID")
			os.Exit(1)
		}
		fileContent := encodeByBase64(filePath)
		data := &contract.NFTData{
			NftName:     nftName,
			Symbol:      nftSymbol,
			Description: "first one",
			Attributes:  "{}",
			File:        fileContent,
		}
		var utxos []*bt.UTXO
		if data.File != "" {
			utxos, err = api.GetUTXOs(address, 0.2, network)
		} else {
			utxos, err = api.GetUTXOs(address, 0.001, network)
		}
		mustExit(err, "GetUTXOs")
		mintScript, err := contract.BuildMintScript(address)
		mustExit(err, "BuildMintScript")
		nftUTXO, err := api.FetchNFTUTXO(hex.EncodeToString(mintScript.Bytes()), collectionID, network)
		mustExit(err, "FetchNFTUTXO")
		raw, err := contract.CreateNFT(collectionID, address, priv, data, utxos, nftUTXO)
		mustExit(err, "CreateNFT")
		id, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast nft")
		fmt.Println("nft contract id:", id)
		contractID = id
	}

	if doBatch {
		if collectionID == "" {
			fmt.Println("缺少 collectionID")
			os.Exit(1)
		}
		const number = 10
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
		mustExit(err, "GetUTXOs")
		mintScript, err := contract.BuildMintScript(address)
		mustExit(err, "BuildMintScript")
		nftUTXOs, err := api.FetchNFTUTXOs(hex.EncodeToString(mintScript.Bytes()), collectionID, network)
		mustExit(err, "FetchNFTUTXOs")
		if len(nftUTXOs) > number {
			nftUTXOs = nftUTXOs[:number]
		}
		raws, err := contract.BatchCreateNFT(collectionID, address, priv, datas, utxos, nftUTXOs, network)
		mustExit(err, "BatchCreateNFT")
		_, err = api.BroadcastTXsRaw(raws, network)
		mustExit(err, "broadcast batch")
		fmt.Println("batch broadcast:", len(raws))
	}

	if doTransfer {
		if transferTo == "" || contractID == "" {
			fmt.Println("缺少 transferTo 或 contractID")
			os.Exit(1)
		}
		nft := contract.NewNFT(contractID)
		info, err := api.FetchNFTInfo(contractID, network)
		mustExit(err, "FetchNFTInfo")
		nft.Initialize(info)

		utxos, err := api.GetUTXOs(address, 0.01, network)
		mustExit(err, "GetUTXOs")
		code, err := contract.BuildCodeScript(info.CollectionID, uint32(info.CollectionIndex))
		mustExit(err, "BuildCodeScript")
		nftUTXO, err := api.FetchNFTUTXO(hex.EncodeToString(code.Bytes()), "", network)
		mustExit(err, "FetchNFTUTXO")
		preTx, err := api.FetchTXRaw(hex.EncodeToString(nftUTXO.TxID), network)
		mustExit(err, "FetchTXRaw preTx")

		var prePreTx *bt.Tx
		if info.NFTTransferTimeCount == 0 {
			prePreTx, err = api.FetchTXRaw(info.CollectionID, network)
		} else {
			prePreTx, err = api.FetchTXRaw(preTx.Inputs[0].PreviousTxIDStr(), network)
		}
		mustExit(err, "FetchTXRaw prePreTx")

		raw, err := nft.TransferNFT(address, transferTo, priv, utxos, preTx, prePreTx, false)
		mustExit(err, "TransferNFT")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast transfer")
		fmt.Println("transfer:", txid)
	}
}
