// MultiSig 测试程序 — 对应 docs/test-cases/multisig.md。2-of-3 演示。
package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

// === 配置区（直接改这里） ===
const (
	network = "testnet"
	wifA    = "" // 签名方 A WIF（必填）
	wifB    = "" // 签名方 B WIF（必填）
	wifC    = "" // 签名方 C WIF（必填）

	sigCount = 2
	pkCount  = 3

	amountSat = uint64(100_000) // 演示发送数量

	ftContractTxid = "" // FT 阶段必填

	doCreate  = false
	doSendTBC = false
	doSendFT  = false
	doFTOut   = false
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func loadKey(name, w string) (*bec.PrivateKey, string, string) {
	if strings.TrimSpace(w) == "" {
		fmt.Println("缺少", name)
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(w)
	mustExit(err, "DecodeWIF "+name)
	addr, err := bscript.NewAddressFromPublicKey(dec.PrivKey.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey "+name)
	pub := hex.EncodeToString(dec.PrivKey.PubKey().SerialiseCompressed())
	return dec.PrivKey, addr.AddressString, pub
}

func main() {
	privA, addrA, pubA := loadKey("wifA", wifA)
	privB, _, pubB := loadKey("wifB", wifB)
	privC, _, pubC := loadKey("wifC", wifC)

	pubKeys := []string{pubA, pubB, pubC}
	sort.Strings(pubKeys)

	multiSigAddr, err := contract.GetMultiSigAddress(pubKeys, sigCount, pkCount)
	mustExit(err, "GetMultiSigAddress")
	fmt.Println("multisig address:", multiSigAddr)

	if doCreate {
		creationSat := uint64(1_000_000)
		utxos, err := api.GetUTXOs(addrA, 1.001, network)
		mustExit(err, "GetUTXOs")
		raw, err := contract.CreateMultiSigWallet(addrA, pubKeys, sigCount, pkCount, creationSat, utxos, privA)
		mustExit(err, "CreateMultiSigWallet")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast create")
		fmt.Println("create wallet:", txid)
	}

	if doSendTBC {
		// P2PKH → multisig
		utxos, err := api.GetUTXOs(addrA, float64(amountSat)/1e6+0.001, network)
		mustExit(err, "GetUTXOs in")
		raw, err := contract.P2PKHToMultiSigSendTBC(addrA, multiSigAddr, amountSat, utxos, privA)
		mustExit(err, "P2PKHToMultiSigSendTBC")
		_, err = api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast in")

		// multisig → P2PKH
		scriptASM, err := contract.GetMultiSigLockScript(multiSigAddr)
		mustExit(err, "GetMultiSigLockScript")
		umtxos, err := api.GetUMTXOs(scriptASM, float64(amountSat)/1e6+0.001, network)
		mustExit(err, "GetUMTXOs")
		multiTx, err := contract.BuildMultiSigTransactionSendTBC(multiSigAddr, addrA, amountSat, umtxos)
		mustExit(err, "BuildMultiSigTransactionSendTBC")
		sigsA, err := contract.SignMultiSigTransactionSendTBC(multiSigAddr, multiTx, privA)
		mustExit(err, "SignMultiSigTransactionSendTBC A")
		sigsB, err := contract.SignMultiSigTransactionSendTBC(multiSigAddr, multiTx, privB)
		mustExit(err, "SignMultiSigTransactionSendTBC B")
		sigs := make([][]string, len(sigsA))
		for i := range sigsA {
			sigs[i] = []string{sigsA[i], sigsB[i]}
		}
		raw, err = contract.FinishMultiSigTransactionSendTBC(multiTx.TxRaw, sigs, pubKeys)
		mustExit(err, "FinishMultiSigTransactionSendTBC")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast out")
		fmt.Println("ms→p2pkh tbc:", txid)
	}

	loadFT := func() *contract.FT {
		if ftContractTxid == "" {
			fmt.Println("ftContractTxid 未设置")
			os.Exit(1)
		}
		token, err := contract.NewFT(ftContractTxid)
		mustExit(err, "NewFT")
		info, err := api.FetchFtInfo(ftContractTxid, network)
		mustExit(err, "FetchFtInfo")
		token.Initialize(&contract.FtInfo{
			ContractTxid: ftContractTxid,
			Name:         info.Name,
			Symbol:       info.Symbol,
			Decimal:      info.Decimal,
			TotalSupply:  info.TotalSupply,
			CodeScript:   info.CodeScript,
			TapeScript:   info.TapeScript,
		})
		return token
	}

	if doSendFT {
		token := loadFT()
		amount := big.NewInt(10000)
		utxo, err := api.FetchUTXO(addrA, 0.01, network)
		mustExit(err, "FetchUTXO")

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addrA)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addrA, hex.EncodeToString(ftCode.Bytes()), network, amount)
		mustExit(err, "FetchFtUTXOs")
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		raw, err := contract.P2PKHToMultiSigTransferFT(addrA, multiSigAddr, token, amount, utxo, ftutxos, preTXs, prepreTxDatas, privA, 0)
		mustExit(err, "P2PKHToMultiSigTransferFT")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast ft in")
		fmt.Println("p2pkh→ms ft:", txid)
	}

	if doFTOut {
		token := loadFT()
		scriptASM, err := contract.GetMultiSigLockScript(multiSigAddr)
		mustExit(err, "GetMultiSigLockScript")
		umtxo, err := api.FetchUMTXO(scriptASM, 0.01, network)
		mustExit(err, "FetchUMTXO")

		hashFrom, err := contract.GetCombineHash(multiSigAddr)
		mustExit(err, "GetCombineHash")
		amount := big.NewInt(600 * 1_000_000)
		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, hashFrom)
		mustExit(err, "BuildFTtransferCode")
		ftutxos, err := api.GetFtUTXOsMultiSig(token.ContractTxid, hashFrom, hex.EncodeToString(ftCode.Bytes()), network, amount)
		mustExit(err, "GetFtUTXOsMultiSig")
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			mustExit(err, "FetchTXRaw")
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			mustExit(err, "FetchFtPrePreTxData")
		}
		contractTX, err := api.FetchTXRaw(hex.EncodeToString(umtxo.TxID), network)
		mustExit(err, "FetchTXRaw umtxo")

		multiTx, err := contract.BuildMultiSigTransactionTransferFT(multiSigAddr, addrA, token, amount, umtxo, ftutxos, preTXs, prepreTxDatas, contractTX, privC)
		mustExit(err, "BuildMultiSigTransactionTransferFT")
		sigsA, err := contract.SignMultiSigTransactionTransferFT(multiSigAddr, multiTx, privA)
		mustExit(err, "SignMultiSigTransactionTransferFT A")
		sigsC, err := contract.SignMultiSigTransactionTransferFT(multiSigAddr, multiTx, privC)
		mustExit(err, "SignMultiSigTransactionTransferFT C")
		sigs := [][]string{{sigsA[0], sigsC[0]}}
		raw, err := contract.FinishMultiSigTransactionTransferFT(multiTx.TxRaw, sigs, pubKeys)
		mustExit(err, "FinishMultiSigTransactionTransferFT")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast ft out")
		fmt.Println("ms→p2pkh ft:", txid)
	}

	_ = privB // privB 仅在 SendTBC 共签时被使用，避免未引用提示
}
