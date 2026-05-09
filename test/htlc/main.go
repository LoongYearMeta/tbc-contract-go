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
//
//   wifSender = A 锁币（用 A 的 TBC 锁进 HTLC）
//   wifRecv   = B 取币（用 secret 解锁拿走）
const (
	network    = "testnet"
	wifSender  = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK" // 发送方 = orderbook 的 A
	wifRecv    = "L5HRwv9CUz2yQKXGueeBqfpGGH7jtZxSxYKhgwA93sjcAsMqRNXQ" // 接收方 = orderbook 的 B
	amountTBC  = 0.001
	timelock   = uint32(1_774_427_165) // 退款解锁时间（unix 秒）
	secretInit = "2033b33005b1072dc9af3f3a8d83f2551b95c8b8487951746a69cd01789e7924"
	htlcTXID   = "616b62642e180532e82a39f388fc04c8fe83972b11edef578722db844c4f7b17"

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
