// HTLC 测试程序 — 对应 docs/test-cases/htlc.md。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
	network    = "testnet"
	wifSender  = "" // 发送方 WIF（必填）
	wifRecv    = "" // 接收方 WIF（必填）
	amountTBC  = 0.001
	timelock   = uint32(1_774_427_165) // 退款解锁时间（unix 秒）
	secretInit = ""                    // 32 字节 hex；为空时 doDeploy 会随机生成
	htlcTXID   = ""                    // 已部署的合约 txid（Withdraw / Refund 必填）

	doDeploy   = false
	doWithdraw = false
	doRefund   = false
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func loadKey(name, w string) (*bec.PrivateKey, string) {
	if strings.TrimSpace(w) == "" {
		fmt.Println("缺少", name)
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(w)
	mustExit(err, "DecodeWIF "+name)
	addr, err := bscript.NewAddressFromPublicKey(dec.PrivKey.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey "+name)
	return dec.PrivKey, addr.AddressString
}

func loadHTLCUTXO(htlcTxid string) *bt.UTXO {
	htlcTX, err := api.FetchTXRaw(htlcTxid, network)
	mustExit(err, "FetchTXRaw htlcTX")
	txidBytes, err := hex.DecodeString(htlcTxid)
	mustExit(err, "decode htlcTxid")
	const outIdx = 0
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          outIdx,
		LockingScript: htlcTX.Outputs[outIdx].LockingScript,
		Satoshis:      htlcTX.Outputs[outIdx].Satoshis,
	}
}

func main() {
	privSender, addrSender := loadKey("wifSender", wifSender)
	privReceiver, addrReceiver := loadKey("wifRecv", wifRecv)

	amountSat := uint64(amountTBC * 1e6)
	const feeTBC = 0.001

	var secret, hashlock string
	if strings.TrimSpace(secretInit) != "" {
		secret = secretInit
		raw, err := hex.DecodeString(secret)
		mustExit(err, "decode secret")
		sum := sha256.Sum256(raw)
		hashlock = hex.EncodeToString(sum[:])
	} else if doDeploy {
		buf := make([]byte, 32)
		_, err := rand.Read(buf)
		mustExit(err, "rand.Read")
		secret = hex.EncodeToString(buf)
		sum := sha256.Sum256(buf)
		hashlock = hex.EncodeToString(sum[:])
		fmt.Println("generated secret:", secret)
		fmt.Println("hashlock:", hashlock)
	}

	currentHTLCTxid := htlcTXID

	if doDeploy {
		utxo, err := api.FetchUTXO(addrSender, amountTBC+feeTBC, network)
		mustExit(err, "FetchUTXO")
		raw, err := contract.DeployHTLCWithSign(addrSender, addrReceiver, hashlock, timelock, amountSat, utxo, privSender)
		mustExit(err, "DeployHTLCWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast deploy")
		fmt.Println("deploy:", txid)
		currentHTLCTxid = txid
	}

	if doWithdraw {
		if currentHTLCTxid == "" {
			fmt.Println("缺少 htlcTXID")
			os.Exit(1)
		}
		if secret == "" {
			fmt.Println("缺少 secretInit")
			os.Exit(1)
		}
		htlcUTXO := loadHTLCUTXO(currentHTLCTxid)
		raw, err := contract.WithdrawWithSign(privReceiver, addrReceiver, htlcUTXO, secret)
		mustExit(err, "WithdrawWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast withdraw")
		fmt.Println("withdraw:", txid)
	}

	if doRefund {
		if currentHTLCTxid == "" {
			fmt.Println("缺少 htlcTXID")
			os.Exit(1)
		}
		htlcUTXO := loadHTLCUTXO(currentHTLCTxid)
		raw, err := contract.RefundWithSign(addrSender, htlcUTXO, privSender, timelock)
		mustExit(err, "RefundWithSign")
		txid, err := api.BroadcastTXRaw(raw, network)
		mustExit(err, "broadcast refund")
		fmt.Println("refund:", txid)
	}
}
