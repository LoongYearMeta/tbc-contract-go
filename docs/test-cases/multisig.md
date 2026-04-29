# MultiSig 测试场景（Go）

参照 TS：[`../tbc-contract/docs/multiSIg.md`](../../tbc-contract/docs/multiSIg.md)。覆盖创建多签钱包、P2PKH→多签 TBC、多签→普通 TBC、P2PKH→多签 FT、多签→普通/多签 FT。下例用 2-of-3 演示。

> 多签输入要求：构建多签交易时，多签输出在第一个输入对应的 UTXO 上，且 vout=0；如果不满足，先用 `BuildMultiSigTransactionSendTBC` 合并 / 调整位置。
> Go 端 TBC 数量参数是 `uint64` satoshis（不是 TBC float64），与 FT 的 `*big.Int` 相同口径。

## 参数定义

| 名称 | 必填 | 说明 |
|------|------|------|
| `TBC_WIF_A` / `TBC_WIF_B` / `TBC_WIF_C` | 是 | 三位签名方 WIF |
| `TBC_NETWORK` | 否 | 默认 `"testnet"` |
| `MULTISIG_AMOUNT_SAT` | 否 | 演示发送数量（satoshi），默认 `100000`（0.1 TBC） |
| `FT_CONTRACT_TXID` | 跑 FT 部分必填 | FT 合约 txid |

## 最小可执行脚本

```go
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
	privA, addrA, pubA := loadKey("TBC_WIF_A")
	privB, _, pubB := loadKey("TBC_WIF_B")
	privC, _, pubC := loadKey("TBC_WIF_C")

	// 公钥按字母序排序（多签地址生成约束）。
	pubKeys := []string{pubA, pubB, pubC}
	sort.Strings(pubKeys)

	const sigCount = 2
	const pkCount = 3
	multiSigAddr, err := contract.GetMultiSigAddress(pubKeys, sigCount, pkCount)
	must(err)
	fmt.Println("multisig address:", multiSigAddr)

	// 演示发送数量（satoshi）。
	amountSat := uint64(100000)
	if v := strings.TrimSpace(os.Getenv("MULTISIG_AMOUNT_SAT")); v != "" {
		var n uint64
		fmt.Sscanf(v, "%d", &n)
		amountSat = n
	}

	// ============ 创建多签钱包 =============
	{
		creationSat := uint64(1_000_000) // 1 TBC，演示用
		utxos, err := api.GetUTXOs(addrA, 1.001, network)
		must(err)
		raw, err := contract.CreateMultiSigWallet(addrA, pubKeys, sigCount, pkCount, creationSat, utxos, privA)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("Create multisig wallet:", txid)
	}

	// ============ 普通地址向多签地址转 TBC =============
	{
		utxos, err := api.GetUTXOs(addrA, float64(amountSat)/1e6+0.001, network)
		must(err)
		raw, err := contract.P2PKHToMultiSigSendTBC(addrA, multiSigAddr, amountSat, utxos, privA)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("P2PKH → multisig TBC:", txid)
	}

	// ============ 多签 → 普通地址 TBC =============
	{
		scriptASM, err := contract.GetMultiSigLockScript(multiSigAddr)
		must(err)
		umtxos, err := api.GetUMTXOs(scriptASM, float64(amountSat)/1e6+0.001, network)
		must(err)

		multiTx, err := contract.BuildMultiSigTransactionSendTBC(multiSigAddr, addrA, amountSat, umtxos)
		must(err)

		sigsA, err := contract.SignMultiSigTransactionSendTBC(multiSigAddr, multiTx, privA)
		must(err)
		sigsB, err := contract.SignMultiSigTransactionSendTBC(multiSigAddr, multiTx, privB)
		must(err)

		// 合成 sigs[i] = [sigA[i], sigB[i]]（也可以选 A+C 或 B+C）
		sigs := make([][]string, len(sigsA))
		for i := range sigsA {
			sigs[i] = []string{sigsA[i], sigsB[i]}
		}
		raw, err := contract.FinishMultiSigTransactionSendTBC(multiTx.TxRaw, sigs, pubKeys)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("multisig → P2PKH TBC:", txid)
	}

	// ============ 普通地址向多签地址转 FT =============
	ftContractTxid := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID"))
	if ftContractTxid == "" {
		fmt.Println("FT_CONTRACT_TXID 未设置，跳过 FT 部分")
		_ = privC
		return
	}
	{
		token, err := contract.NewFT(ftContractTxid)
		must(err)
		info, err := api.FetchFtInfo(ftContractTxid, network)
		must(err)
		token.Initialize(&contract.FtInfo{
			ContractTxid: ftContractTxid,
			Name:         info.Name,
			Symbol:       info.Symbol,
			Decimal:      info.Decimal,
			TotalSupply:  info.TotalSupply,
			CodeScript:   info.CodeScript,
			TapeScript:   info.TapeScript,
		})

		amount := big.NewInt(10000) // 单位是最小整数
		utxo, err := api.FetchUTXO(addrA, 0.01, network)
		must(err)

		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addrA)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addrA, ftCodeHex, network, amount)
		must(err)
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}
		// 同时转 TBC 时把最后一个参数（uint64 satoshi）调成正数即可
		raw, err := contract.P2PKHToMultiSigTransferFT(addrA, multiSigAddr, token, amount, utxo, ftutxos, preTXs, prepreTxDatas, privA, 0)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("P2PKH → multisig FT:", txid)
	}

	// ============ 多签 → 普通/多签 FT =============
	{
		token, err := contract.NewFT(ftContractTxid)
		must(err)
		info, err := api.FetchFtInfo(ftContractTxid, network)
		must(err)
		token.Initialize(&contract.FtInfo{
			ContractTxid: ftContractTxid,
			Name:         info.Name,
			Symbol:       info.Symbol,
			Decimal:      info.Decimal,
			TotalSupply:  info.TotalSupply,
			CodeScript:   info.CodeScript,
			TapeScript:   info.TapeScript,
		})

		scriptASM, err := contract.GetMultiSigLockScript(multiSigAddr)
		must(err)
		// 注意：用于 FT 输入的多签 utxo 的 outputIndex 必须为 0；不是的话先用 BuildMultiSigTransactionSendTBC 合并调整
		umtxo, err := api.FetchUMTXO(scriptASM, 0.01, network)
		must(err)

		hashFrom, err := contract.GetCombineHash(multiSigAddr)
		must(err)
		amount := big.NewInt(600 * 1_000_000) // 假设 decimal=6，转 600
		ftCode, err := contract.BuildFTtransferCode(token.CodeScript, hashFrom)
		must(err)
		ftCodeHex := hex.EncodeToString(ftCode.Bytes())
		ftutxos, err := api.GetFtUTXOsMultiSig(token.ContractTxid, hashFrom, ftCodeHex, network, amount)
		must(err)
		preTXs := make([]*bt.Tx, len(ftutxos))
		prepreTxDatas := make([]string, len(ftutxos))
		for i, u := range ftutxos {
			preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
			must(err)
			prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
			must(err)
		}
		contractTX, err := api.FetchTXRaw(hex.EncodeToString(umtxo.TxID), network)
		must(err)

		multiTx, err := contract.BuildMultiSigTransactionTransferFT(multiSigAddr, addrA, token, amount, umtxo, ftutxos, preTXs, prepreTxDatas, contractTX, privC)
		must(err)

		sigsA, err := contract.SignMultiSigTransactionTransferFT(multiSigAddr, multiTx, privA)
		must(err)
		sigsC, err := contract.SignMultiSigTransactionTransferFT(multiSigAddr, multiTx, privC)
		must(err)

		// 多签输入只占输入 0；故 sigs 只有一行
		sigs := [][]string{{sigsA[0], sigsC[0]}}
		raw, err := contract.FinishMultiSigTransactionTransferFT(multiTx.TxRaw, sigs, pubKeys)
		must(err)
		txid, err := api.BroadcastTXRaw(raw, network)
		must(err)
		fmt.Println("multisig → P2PKH FT:", txid)
	}
}
```

> 备注：`SignMultiSigTransactionTransferFT` 只对多签输入（input 0）签名，所以返回长度 1 的切片；`FinishMultiSigTransactionTransferFT` 的 `sigs[i][j]` 取自第 i 个多签输入第 j 个签名方。
