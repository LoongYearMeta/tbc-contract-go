// FT 测试程序 — 对应 docs/test-cases/ft.md。
//
// 改这一节即可：把 addressB / ftContractTxid 替换成你的值，
// 再把对应的 do* 开关改成 true。任何阶段失败立即退出。
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

// === 配置区（直接改这里） ===
const (
	network        = "testnet"
	addressB       = "143KgKGcse57nXBnXyJwtQrf2KP4KWto59"                               // Transfer / BatchTransfer 接收方
	ftContractTxid = "62ac8fb58fc18d7c0bbcc7e4fa11c704ef62faa17be078e3c530d5dec9cc231f" // Mint 之外阶段必填

	ftName    = "test0430"
	ftSymbol  = "test0430"
	ftAmount  = 100_000_000 // 精度 6，上限 1 万亿
	ftDecimal = 6

	doMint     = false
	doTransfer = false
	doBatch    = false
	doMerge    = true
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func requiredEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fmt.Fprintln(os.Stderr, "missing required environment variable:", name)
		os.Exit(2)
	}
	return value
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
	wifA := requiredEnv("TBC_TESTNET_WIF")
	dec, err := wif.DecodeWIF(wifA)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	addressA := addr.AddressString
	currentFTTxid := ftContractTxid

	if doMint {
		token, err := contract.NewFT(&contract.FtParams{
			Name:    ftName,
			Symbol:  ftSymbol,
			Amount:  ftAmount,
			Decimal: ftDecimal,
		})
		mustExit(err, "NewFT")
		utxo, err := api.FetchUTXO(addressA, 0.01, network)
		mustExit(err, "FetchUTXO")
		mintTX, err := token.MintFT(priv, addressA, utxo)
		mustExit(err, "MintFT")
		_, err = api.BroadcastTXRaw(mintTX[0], network)
		mustExit(err, "broadcast source")
		id, err := api.BroadcastTXRaw(mintTX[1], network)
		mustExit(err, "broadcast mint")
		fmt.Println("mint contract id:", id)
		currentFTTxid = id
	}

	loadToken := func() *contract.FT {
		if currentFTTxid == "" {
			fmt.Println("ftContractTxid 未设置")
			os.Exit(1)
		}
		token, err := contract.NewFT(currentFTTxid)
		mustExit(err, "NewFT")
		info, err := api.FetchFtInfo(token.ContractTxid, network)
		mustExit(err, "FetchFtInfo")
		token.Initialize(ftInfoFromAPI(token.ContractTxid, info))
		return token
	}

	if doTransfer {
		if addressB == "" {
			fmt.Println("addressB 未设置")
			os.Exit(1)
		}
		token := loadToken()
		utxo, err := api.FetchUTXO(addressA, 0.01, network)
		mustExit(err, "FetchUTXO")
		amount, err := util.ParseDecimalToBigInt("1000", token.Decimal)
		mustExit(err, "ParseDecimalToBigInt")

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addressA, ftCodeHex, network, amount)
		mustExit(err, "FetchFtUTXOs")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw preTX")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raw, err := token.Transfer(priv, addressB, amount, ftutxos, utxo, preTXs, prepreTxDatas, 0)
		mustExit(err, "Transfer")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast transfer")
		fmt.Println("transfer:", txid)
	}

	if doBatch {
		if addressB == "" {
			fmt.Println("addressB 未设置")
			os.Exit(1)
		}
		token := loadToken()
		mustBN := func(s string) *big.Int {
			n, err := util.ParseDecimalToBigInt(s, token.Decimal)
			mustExit(err, "ParseDecimalToBigInt "+s)
			return n
		}
		receivers := []contract.AddressAmount{
			{Address: addressA, Amount: mustBN("500")},
			{Address: addressB, Amount: mustBN("700")},
		}
		batchCount := (len(receivers) + 4) / 5
		utxo, err := api.FetchUTXO(addressA, 0.005*float64(batchCount), network)
		mustExit(err, "FetchUTXO")

		total := new(big.Int)
		for _, r := range receivers {
			total.Add(total, r.Amount)
		}
		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addressA, hex.EncodeToString(ftCode.Bytes()), network, total)
		mustExit(err, "FetchFtUTXOs")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raws, err := token.BatchTransfer(priv, receivers, ftutxos, utxo, preTXs, prepreTxDatas)
		mustExit(err, "BatchTransfer")
		if len(raws) == 0 {
			fmt.Println("BatchTransfer: nothing to broadcast")
			return
		}
		_, err = api.BroadcastTXsRaw(raws, network)
		mustExit(err, "broadcast batch")
		fmt.Println("batch transfer txs:", len(raws))
	}

	if doMerge {
		token := loadToken()
		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.FetchFtUTXOList(token.ContractTxid, addressA, hex.EncodeToString(ftCode.Bytes()), network)
		mustExit(err, "FetchFtUTXOList")
		utxo, err := api.FetchUTXO(addressA, 0.005*float64(len(ftutxos)), network)
		mustExit(err, "FetchUTXO")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raws, err := token.MergeFT(priv, ftutxos, utxo, preTXs, prepreTxDatas, nil)
		mustExit(err, "MergeFT")
		if len(raws) == 0 {
			fmt.Println("merge: already merged")
			return
		}
		_, err = api.BroadcastTXsRaw(raws, network)
		mustExit(err, "broadcast merge")
		fmt.Println("merge chained txs:", len(raws))
	}
}
