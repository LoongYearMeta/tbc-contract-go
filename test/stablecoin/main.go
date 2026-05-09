// StableCoin 测试程序 — 对应 docs/test-cases/stablecoin.md。
//
// 仅覆盖 owner-signed 路径（Transfer / BatchTransfer / MergeCoin）。管理员
// 路径 (CreateCoin / MintCoin / FreezeCoinUTXO / UnfreezeCoinUTXO) 走
// AdminPrepared 两阶段：本仓 Go 侧只生成 sighash + 模板交易，BIP327 / MuSig2
// Schnorr 签名仪式必须在外部完成（通常用 tbc-lib-js 或自带 Schnorr 实现的
// 钱包），拿到 64 字节 sigs 后再 .Finalize(sigs) 得到 raw 广播。这里不演示。
//
// 改这一节即可：把 wifA / addressB / coinContractTxid 替换成你的值，
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
	network          = "testnet"
	wifA             = "" // 持币方 WIF（必填）
	addressB         = "" // Transfer / BatchTransfer 接收方
	coinContractTxid = "" // 已部署的 stableCoin 合约 mint txid（owner 路径必填）

	doInfo     = false // 拉一下 FetchCoinInfo + GetCoinBalance（只读）
	doTransfer = false
	doBatch    = false
	doMerge    = false
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func coinFTInfoFromAPI(txid string, info *api.CoinInfoResponse) *contract.FtInfo {
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
	if strings.TrimSpace(wifA) == "" {
		fmt.Println("请在源码顶部把 wifA 设成你的 WIF")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifA)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	addressA := addr.AddressString

	loadCoin := func() *contract.StableCoin {
		if strings.TrimSpace(coinContractTxid) == "" {
			fmt.Println("coinContractTxid 未设置")
			os.Exit(1)
		}
		sc, err := contract.NewStableCoin(coinContractTxid)
		mustExit(err, "NewStableCoin")
		info, err := api.FetchCoinInfo(coinContractTxid, network)
		mustExit(err, "FetchCoinInfo")
		sc.Initialize(coinFTInfoFromAPI(coinContractTxid, info))
		return sc
	}

	if doInfo {
		info, err := api.FetchCoinInfo(coinContractTxid, network)
		mustExit(err, "FetchCoinInfo")
		fmt.Printf("coin: %s (%s) decimal=%d supply=%s nft=%s\n",
			info.Name, info.Symbol, info.Decimal, info.TotalSupply.String(), info.NftTXID)
		bal, err := api.GetCoinBalance(coinContractTxid, addressA, network)
		mustExit(err, "GetCoinBalance")
		fmt.Println("balance:", bal.String())
	}

	if doTransfer {
		if strings.TrimSpace(addressB) == "" {
			fmt.Println("addressB 未设置")
			os.Exit(1)
		}
		sc := loadCoin()
		amount, err := util.ParseDecimalToBigInt("1", sc.Decimal)
		mustExit(err, "ParseDecimalToBigInt")

		ftCode, err := contract.BuildFTtransferCode(sc.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchCoinUTXOs(sc.ContractTxid, addressA, ftCodeHex, network, amount, 5)
		mustExit(err, "FetchCoinUTXOs")

		feeUTXO, err := api.FetchUTXO(addressA, 0.01, network)
		mustExit(err, "FetchUTXO")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw preTX")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raw, err := sc.Transfer(priv, addressB, amount, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
		mustExit(err, "Transfer")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast transfer")
		fmt.Println("transfer:", txid)
	}

	if doBatch {
		if strings.TrimSpace(addressB) == "" {
			fmt.Println("addressB 未设置")
			os.Exit(1)
		}
		sc := loadCoin()
		mustBN := func(s string) *big.Int {
			n, err := util.ParseDecimalToBigInt(s, sc.Decimal)
			mustExit(err, "ParseDecimalToBigInt "+s)
			return n
		}
		receivers := []contract.AddressAmount{
			{Address: addressA, Amount: mustBN("0.5")},
			{Address: addressB, Amount: mustBN("0.7")},
		}
		batchCount := (len(receivers) + 4) / 5
		feeUTXO, err := api.FetchUTXO(addressA, 0.01*float64(batchCount), network)
		mustExit(err, "FetchUTXO")

		total := new(big.Int)
		for _, r := range receivers {
			total.Add(total, r.Amount)
		}
		ftCode, err := contract.BuildFTtransferCode(sc.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.FetchCoinUTXOs(sc.ContractTxid, addressA, hex.EncodeToString(ftCode.Bytes()), network, total, 5)
		mustExit(err, "FetchCoinUTXOs")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raws, err := sc.BatchTransfer(priv, receivers, ftutxos, feeUTXO, preTXs, prepreTxDatas)
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
		sc := loadCoin()
		ftCode, err := contract.BuildFTtransferCode(sc.CodeScript, addressA)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.FetchCoinUTXOList(sc.ContractTxid, addressA, hex.EncodeToString(ftCode.Bytes()), network)
		mustExit(err, "FetchCoinUTXOList")
		feeUTXO, err := api.FetchUTXO(addressA, 0.01*float64(len(ftutxos)), network)
		mustExit(err, "FetchUTXO")

		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raws, err := sc.MergeCoin(priv, ftutxos, feeUTXO, preTXs, prepreTxDatas, nil)
		mustExit(err, "MergeCoin")
		if len(raws) == 0 {
			fmt.Println("merge: already merged")
			return
		}
		_, err = api.BroadcastTXsRaw(raws, network)
		mustExit(err, "broadcast merge")
		fmt.Println("merge chained txs:", len(raws))
	}
}
