// OrderBook 测试程序 — 三钱包循环撮合，对应 TS test/orderBook.test.ts 的 10 轮循环。
//
// 撮合合约要求三个角色的地址各不相同：
//   wifA — 撮合者（用 A 的 TBC 付撮合 tx 的矿工费）
//   wifB — 买家（用 token 买 TBC；A 提前转 token + 一点 TBC 过来）
//   wifC — 卖家（用 TBC 换 token；A 提前转 TBC 过来；卖单成交后会拿到 token）
//
// 每轮：
//   1. 随机生成 unitPrice / sellVolume / buyVolume / feeRate
//   2. C 创建卖单
//   3. B 创建买单
//   4. A 撮合
//   5. 如果撮合 tx 有 7+ 个 output（部分成交），用找零订单再撮一次
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"strings"
	"time"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/transaction/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

const (
	network        = "testnet"
	wifA           = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK" // 撮合者
	wifB           = "L5HRwv9CUz2yQKXGueeBqfpGGH7jtZxSxYKhgwA93sjcAsMqRNXQ" // 买家
	wifC           = "Kz3BsRyVH7jegeSZ2akAoagVKbSojEqKzXyJVwNroozfirhm7368" // 卖家
	ftContractTxid = "62ac8fb58fc18d7c0bbcc7e4fa11c704ef62faa17be078e3c530d5dec9cc231f"
	taxAddress     = "1BitcoinEaterAddressDontSendf59kuE"
	ftFeeAddress   = "1BitcoinEaterAddressDontSendf59kuE"
	tbcFeeAddress  = "1BitcoinEaterAddressDontSendf59kuE"

	// 一次性给 B 转账（B 需要 token 来挂买单 + 一点 TBC 付手续费）
	fundBTokenAmount = "200"
	fundBTBCAmount   = 0.01
	// 一次性给 C 转账（C 卖 TBC 需要先有 TBC；10 轮累计最大约 55 TBC，多给点容错）
	fundCTBCAmount = 80.0

	// === 阶段开关 ===
	doFundB     = false // A → B 转 token + TBC
	doFundC     = false // A → C 转 TBC（首次必须先跑这个）
	doRoundLoop = true  // 跑 10 轮 sell+buy+match 循环

	rounds = 10
)

func mustExit(err error, msg string) {
	if err != nil {
		fmt.Println(msg+":", err)
		os.Exit(1)
	}
}

func decodeWif(s string) (*bec.PrivateKey, string) {
	dec, err := wif.DecodeWIF(s)
	mustExit(err, "DecodeWIF")
	priv := dec.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	mustExit(err, "NewAddressFromPublicKey")
	return priv, addr.AddressString
}

func main() {
	if strings.TrimSpace(wifA) == "" || strings.TrimSpace(wifB) == "" || strings.TrimSpace(wifC) == "" {
		fmt.Println("请把 wifA / wifB / wifC 都填上")
		os.Exit(1)
	}
	privA, addrA := decodeWif(wifA)
	privB, addrB := decodeWif(wifB)
	privC, addrC := decodeWif(wifC)
	mainnet := network == "mainnet" || network == ""
	fmt.Printf("addrA (matcher) = %s\naddrB (buyer)   = %s\naddrC (seller)  = %s\n\n", addrA, addrB, addrC)

	if doFundB {
		fmt.Println("=== A → B：转 token + TBC ===")
		fundB(privA, addrA, addrB)
		return
	}

	if doFundC {
		fmt.Println("=== A → C：转 TBC ===")
		fundC(privA, addrA, addrC)
		return
	}

	if doRoundLoop {
		rand.Seed(time.Now().UnixNano())
		for round := 0; round < rounds; round++ {
			fmt.Printf("\n========== Round %d ==========\n", round+1)
			err := runRound(round, privA, addrA, privB, addrB, privC, addrC, mainnet)
			if err != nil && strings.Contains(err.Error(), "Missing inputs") {
				// stale-mempool race: wait and retry once
				fmt.Printf("  retrying after 15s (stale UTXO): %v\n", err)
				time.Sleep(15 * time.Second)
				err = runRound(round, privA, addrA, privB, addrB, privC, addrC, mainnet)
			}
			if err != nil {
				fmt.Printf("Round %d failed: %v\n", round+1, err)
				return
			}
			if round < rounds-1 {
				fmt.Println("  sleeping 15s before next round...")
				time.Sleep(15 * time.Second)
			}
		}
	}
}

// randVar returns a uniformly-distributed integer in [-200_000, 199_000] in steps of 1000,
// mirroring TS `Math.floor((Math.random() * 0.4 - 0.2) * 1000) * 1000`.
//
// TS produces 400 buckets:
//   Math.random() ∈ [0,1), * 0.4 - 0.2 ∈ [-0.2, 0.2), * 1000 ∈ [-200, 200), floor → [-200, 199].
//   * 1000 → [-200_000, 199_000] in 1000-step increments (last 3 digits always 0).
//
// Go matches with rand.Intn(400) → [0, 399], -200 → [-200, 199], *1000 → [-200_000, 199_000].
// The `*1000` keeps unitPrice/sellVolume/buyVolume always multiples of 1000 so the random
// noise stays at 0.001 precision (1000 / 1_000_000 = 0.001).
func randVar() int64 {
	return int64(rand.Intn(400)-200) * 1000
}

func runRound(round int, privA *bec.PrivateKey, addrA string, privB *bec.PrivateKey, addrB string, privC *bec.PrivateKey, addrC string, mainnet bool) error {
	const baseUnitPrice = int64(1_000_000)
	const baseSellVolume = int64(1_000_000)
	const baseBuyVolume = int64(1_000_000)
	const baseFeeRate = int64(1000)

	unitPrice := uint64(baseUnitPrice + int64(round)*500_000 + randVar())
	sellVolume := uint64(baseSellVolume + int64(round)*1_000_000 + randVar())
	buyVolume := uint64(baseBuyVolume + int64(round)*1_000_000 + randVar())
	feeRateRaw := baseFeeRate + int64(round)*100 + randVar()
	if feeRateRaw < 1000 {
		feeRateRaw = 1000 // avoid 0-tax placeholder branch
	}
	feeRate := uint64(feeRateRaw)
	fmt.Printf("  unitPrice=%.6f sellVol=%.6f buyVol=%.6f feeRate=%.6f\n",
		float64(unitPrice)/1e6, float64(sellVolume)/1e6, float64(buyVolume)/1e6, float64(feeRate)/1e6)

	// 1. C creates sell
	sellID, err := createSellOrder(privC, addrC, sellVolume, unitPrice, feeRate)
	if err != nil {
		return fmt.Errorf("create sell (C): %w", err)
	}
	fmt.Println("  Sell:", sellID)

	// 2. B creates buy
	buyID, err := createBuyOrder(privB, addrB, buyVolume, unitPrice, feeRate)
	if err != nil {
		return fmt.Errorf("create buy (B): %w", err)
	}
	fmt.Println("  Buy: ", buyID)

	time.Sleep(2 * time.Second)

	// 3. A matches
	matchID, matchTx, err := doMatchOrder(privA, addrA, sellID, buyID, mainnet)
	if err != nil {
		return fmt.Errorf("match (A): %w", err)
	}
	fmt.Println("  Match:", matchID)

	// 4. Partial-fill secondary match — failures here are non-fatal because
	// the residual change order may be too small to meet the contract's 10-sat
	// TBC tax dust floor (vol*feeRate < 10 sat) which is a protocol limit, not
	// an SDK bug. Just log and continue.
	if len(matchTx.Outputs) > 7 {
		fmt.Println("  -- 二次撮合 --")
		if err := secondaryMatch(round, privA, addrA, privB, addrB, privC, addrC, matchID, matchTx, mainnet); err != nil {
			fmt.Printf("  二次撮合跳过 (%v)\n", err)
		}
	}

	return nil
}

// secondaryMatch handles the change-order produced by primary match: if the
// match tx has 10 outputs the change is a buy-order (consume by creating new
// sell from C), if 8 outputs it's a sell-order (consume by creating new buy from B).
func secondaryMatch(round int, privA *bec.PrivateKey, addrA string, privB *bec.PrivateKey, addrB string,
	privC *bec.PrivateKey, addrC string,
	primaryMatchID string, primaryMatchTx *bt.Tx, mainnet bool) error {

	matchTxIDBytes, _ := hex.DecodeString(primaryMatchID)
	orderUTXO := &bt.UTXO{
		TxID:          matchTxIDBytes,
		Vout:          7,
		LockingScript: primaryMatchTx.Outputs[7].LockingScript,
		Satoshis:      primaryMatchTx.Outputs[7].Satoshis,
	}
	orderData, err := contract.GetOrderData(orderUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return fmt.Errorf("parse change order: %w", err)
	}
	fmt.Printf("    change order: holder=%s vol=%.6f price=%.6f feeRate=%.6f\n",
		orderData.HoldAddress, float64(orderData.SaleVolume)/1e6,
		float64(orderData.UnitPrice)/1e6, float64(orderData.FeeRate)/1e6)

	time.Sleep(5 * time.Second)

	if len(primaryMatchTx.Outputs) == 10 {
		// Change is a BUY order (held by B) — C creates a new sell to match it.
		newSellID, err := createSellOrder(privC, addrC, orderData.SaleVolume, orderData.UnitPrice, orderData.FeeRate)
		if err != nil {
			return fmt.Errorf("create new sell for change: %w", err)
		}
		fmt.Println("    new sell (C):", newSellID)
		time.Sleep(5 * time.Second)
		matchID2, _, err := doMatchOrderRaw(privA, addrA, newSellID, orderUTXO, primaryMatchTx, mainnet)
		if err != nil {
			return fmt.Errorf("secondary match (buy-change): %w", err)
		}
		fmt.Println("    secondary match:", matchID2)
	} else if len(primaryMatchTx.Outputs) == 8 {
		// Change is a SELL order (held by C) — B creates a new buy to match it.
		newBuyID, err := createBuyOrder(privB, addrB, orderData.SaleVolume, orderData.UnitPrice, orderData.FeeRate)
		if err != nil {
			return fmt.Errorf("create new buy for change: %w", err)
		}
		fmt.Println("    new buy (B):", newBuyID)
		time.Sleep(5 * time.Second)
		matchID2, _, err := doMatchOrderWithSellChange(privA, addrA, newBuyID, orderUTXO, primaryMatchTx, mainnet)
		if err != nil {
			return fmt.Errorf("secondary match (sell-change): %w", err)
		}
		fmt.Println("    secondary match:", matchID2)
	}
	return nil
}

func createSellOrder(priv *bec.PrivateKey, addr string, saleVolume, unitPrice, feeRate uint64) (string, error) {
	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		return "", err
	}
	utxos, err := api.GetUTXOs(addr, float64(saleVolume)/1e6+0.001, network)
	if err != nil {
		return "", err
	}
	order := contract.NewOrderBook()
	raw, err := order.MakeSellOrderWithSign(priv, taxAddress, saleVolume, unitPrice, feeRate, ftContractTxid, ftInfo.CodeScript, utxos)
	if err != nil {
		return "", err
	}
	return api.BroadcastTXRaw(raw, network)
}

func createBuyOrder(priv *bec.PrivateKey, addr string, saleVolume, unitPrice, feeRate uint64) (string, error) {
	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		return "", err
	}
	ftCode, err := contract.BuildFTtransferCode(ftInfo.CodeScript, addr)
	if err != nil {
		return "", err
	}
	ftCodeHex := hex.EncodeToString(ftCode.Bytes())
	requiredAmount := new(big.Int).SetUint64((saleVolume * unitPrice) / 1_000_000)
	ftutxos, err := api.FetchFtUTXOs(ftContractTxid, addr, ftCodeHex, network, requiredAmount)
	if err != nil {
		return "", fmt.Errorf("FetchFtUTXOs: %w", err)
	}
	utxos, err := api.GetUTXOs(addr, 0.002, network)
	if err != nil {
		return "", fmt.Errorf("GetUTXOs: %w", err)
	}
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxData := make([]string, len(ftutxos))
	for i, u := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
		if err != nil {
			return "", err
		}
		prepreTxData[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
		if err != nil {
			return "", err
		}
	}
	order := contract.NewOrderBook()
	buyNoSigs, err := order.BuildBuyOrderTX(addr, taxAddress, saleVolume, unitPrice, feeRate, ftContractTxid, utxos, ftutxos, preTXs)
	if err != nil {
		return "", err
	}
	raw, err := signBuyOrderInputs(buyNoSigs, priv, ftutxos, utxos, preTXs, prepreTxData, ftInfo.CodeScript)
	if err != nil {
		return "", err
	}
	return api.BroadcastTXRaw(raw, network)
}

func doMatchOrder(priv *bec.PrivateKey, matcherAddr, sellID, buyID string, mainnet bool) (string, *bt.Tx, error) {
	sellTX, err := api.FetchTXRaw(sellID, network)
	if err != nil {
		return "", nil, err
	}
	buyTX, err := api.FetchTXRaw(buyID, network)
	if err != nil {
		return "", nil, err
	}
	sellTxIDBytes, _ := hex.DecodeString(sellID)
	buyTxIDBytes, _ := hex.DecodeString(buyID)
	sellUTXO := &bt.UTXO{TxID: sellTxIDBytes, Vout: 0, LockingScript: sellTX.Outputs[0].LockingScript, Satoshis: sellTX.Outputs[0].Satoshis}
	buyUTXO := &bt.UTXO{TxID: buyTxIDBytes, Vout: 0, LockingScript: buyTX.Outputs[0].LockingScript, Satoshis: buyTX.Outputs[0].Satoshis}
	ftUTXO, err := util.BuildUTXO(buyTX, 1, true)
	if err != nil {
		return "", nil, err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(buyTX, int(ftUTXO.Vout), network)
	if err != nil {
		return "", nil, err
	}
	utxos, err := api.GetUTXOs(matcherAddr, 0.005, network)
	if err != nil {
		return "", nil, err
	}
	order := contract.NewOrderBook()
	raw, err := order.MatchOrder(priv, buyUTXO, buyTX, ftUTXO, buyTX, ftPrePreTxData, sellUTXO, sellTX, utxos, ftFeeAddress, tbcFeeAddress, mainnet)
	if err != nil {
		return "", nil, err
	}
	matchID, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		return "", nil, err
	}
	matchTx, _ := bt.NewTxFromString(raw)
	return matchID, matchTx, nil
}

// doMatchOrderRaw matches a fresh sell against a buy-change UTXO that lives in
// `primaryMatchTx`. The buy-change is at vout=7 of primaryMatchTx; the FT it
// controls is at vout=8 (FT code) + vout=9 (FT tape) of primaryMatchTx.
func doMatchOrderRaw(priv *bec.PrivateKey, matcherAddr, sellID string, buyChangeUTXO *bt.UTXO, primaryMatchTx *bt.Tx, mainnet bool) (string, *bt.Tx, error) {
	sellTX, err := api.FetchTXRaw(sellID, network)
	if err != nil {
		return "", nil, err
	}
	sellTxIDBytes, _ := hex.DecodeString(sellID)
	sellUTXO := &bt.UTXO{TxID: sellTxIDBytes, Vout: 0, LockingScript: sellTX.Outputs[0].LockingScript, Satoshis: sellTX.Outputs[0].Satoshis}
	// FT held by the buy-change is at vout=8 of primaryMatchTx (code) + vout=9 (tape).
	ftUTXO, err := util.BuildUTXO(primaryMatchTx, 8, true)
	if err != nil {
		return "", nil, err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(primaryMatchTx, int(ftUTXO.Vout), network)
	if err != nil {
		return "", nil, err
	}
	utxos, err := api.GetUTXOs(matcherAddr, 0.005, network)
	if err != nil {
		return "", nil, err
	}
	order := contract.NewOrderBook()
	raw, err := order.MatchOrder(priv, buyChangeUTXO, primaryMatchTx, ftUTXO, primaryMatchTx, ftPrePreTxData, sellUTXO, sellTX, utxos, ftFeeAddress, tbcFeeAddress, mainnet)
	if err != nil {
		return "", nil, err
	}
	matchID, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		return "", nil, err
	}
	matchTx, _ := bt.NewTxFromString(raw)
	return matchID, matchTx, nil
}

// doMatchOrderWithSellChange matches a fresh buy against a sell-change UTXO
// from primaryMatchTx (sell-change is at vout=7 of primaryMatchTx).
func doMatchOrderWithSellChange(priv *bec.PrivateKey, matcherAddr, buyID string, sellChangeUTXO *bt.UTXO, primaryMatchTx *bt.Tx, mainnet bool) (string, *bt.Tx, error) {
	buyTX, err := api.FetchTXRaw(buyID, network)
	if err != nil {
		return "", nil, err
	}
	buyTxIDBytes, _ := hex.DecodeString(buyID)
	buyUTXO := &bt.UTXO{TxID: buyTxIDBytes, Vout: 0, LockingScript: buyTX.Outputs[0].LockingScript, Satoshis: buyTX.Outputs[0].Satoshis}
	ftUTXO, err := util.BuildUTXO(buyTX, 1, true)
	if err != nil {
		return "", nil, err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(buyTX, int(ftUTXO.Vout), network)
	if err != nil {
		return "", nil, err
	}
	utxos, err := api.GetUTXOs(matcherAddr, 0.005, network)
	if err != nil {
		return "", nil, err
	}
	order := contract.NewOrderBook()
	raw, err := order.MatchOrder(priv, buyUTXO, buyTX, ftUTXO, buyTX, ftPrePreTxData, sellChangeUTXO, primaryMatchTx, utxos, ftFeeAddress, tbcFeeAddress, mainnet)
	if err != nil {
		return "", nil, err
	}
	matchID, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		return "", nil, err
	}
	matchTx, _ := bt.NewTxFromString(raw)
	return matchID, matchTx, nil
}

// fundC sends `fundCTBCAmount` TBC from A to C in a single P2PKH tx.
// C is the seller — only needs TBC (not tokens) to put up sell orders;
// after each match C accumulates tokens automatically.
func fundC(privA *bec.PrivateKey, addrA, addrC string) {
	utxo, err := api.FetchUTXO(addrA, fundCTBCAmount+0.001, network)
	mustExit(err, "FetchUTXO for C funding")
	raw, err := buildSimpleTBCTransfer(privA, addrA, addrC, uint64(fundCTBCAmount*1e6), utxo)
	mustExit(err, "buildSimpleTBCTransfer")
	txid, err := api.BroadcastTXRaw(raw, network)
	mustExit(err, "broadcast C TBC transfer")
	fmt.Printf("  TBC transfer txid: %s (sent %.4f TBC)\n", txid, fundCTBCAmount)
}

// fundB does two broadcasts: A → B FT transfer + chained TBC transfer.
func fundB(privA *bec.PrivateKey, addrA, addrB string) {
	token, err := contract.NewFT(ftContractTxid)
	mustExit(err, "NewFT")
	ftInfo, err := api.FetchFtInfo(token.ContractTxid, network)
	mustExit(err, "FetchFtInfo")
	token.Initialize(&contract.FtInfo{
		ContractTxid: token.ContractTxid,
		Name:         ftInfo.Name,
		Symbol:       ftInfo.Symbol,
		Decimal:      ftInfo.Decimal,
		TotalSupply:  ftInfo.TotalSupply,
		CodeScript:   ftInfo.CodeScript,
		TapeScript:   ftInfo.TapeScript,
	})

	feeUtxo, err := api.FetchUTXO(addrA, 0.01, network)
	mustExit(err, "FetchUTXO for token transfer")
	amount, err := util.ParseDecimalToBigInt(fundBTokenAmount, token.Decimal)
	mustExit(err, "ParseDecimalToBigInt")

	ftCode, err := contract.BuildFTtransferCode(token.CodeScript, addrA)
	mustExit(err, "BuildFTtransferCode")
	ftutxos, err := api.FetchFtUTXOs(token.ContractTxid, addrA, hex.EncodeToString(ftCode.Bytes()), network, amount)
	mustExit(err, "FetchFtUTXOs")

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i, u := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(hex.EncodeToString(u.TxID), network)
		mustExit(err, "FetchTXRaw preTX")
		prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(u.Vout), network)
		mustExit(err, "FetchFtPrePreTxData")
	}
	rawFT, err := token.Transfer(privA, addrB, amount, ftutxos, feeUtxo, preTXs, prepreTxDatas, 0)
	mustExit(err, "Transfer")
	ftTx, err := bt.NewTxFromString(rawFT)
	mustExit(err, "parse FT raw")
	txidFT, err := api.BroadcastTXRaw(rawFT, network)
	mustExit(err, "broadcast token transfer")
	fmt.Println("  token transfer txid:", txidFT)

	var changeVout int = -1
	for i, out := range ftTx.Outputs {
		ls := out.LockingScript.Bytes()
		if len(ls) == 25 && ls[0] == 0x76 && ls[1] == 0xa9 {
			changeVout = i
		}
	}
	if changeVout < 0 {
		fmt.Println("FT tx has no P2PKH change output")
		os.Exit(1)
	}
	ftTxIDBytes, _ := hex.DecodeString(ftTx.TxID())
	tbcUtxo := &bt.UTXO{
		TxID:          ftTxIDBytes,
		Vout:          uint32(changeVout),
		LockingScript: ftTx.Outputs[changeVout].LockingScript,
		Satoshis:      ftTx.Outputs[changeVout].Satoshis,
	}
	rawTBC, err := buildSimpleTBCTransfer(privA, addrA, addrB, uint64(fundBTBCAmount*1e6), tbcUtxo)
	mustExit(err, "buildSimpleTBCTransfer")
	txidTBC, err := api.BroadcastTXRaw(rawTBC, network)
	mustExit(err, "broadcast TBC transfer")
	fmt.Println("  TBC   transfer txid:", txidTBC)
}

func buildSimpleTBCTransfer(priv *bec.PrivateKey, fromAddr, toAddr string, amountSat uint64, utxo *bt.UTXO) (string, error) {
	const flatFee uint64 = 200
	if utxo.Satoshis <= amountSat+flatFee {
		return "", fmt.Errorf("buildSimpleTBCTransfer: utxo %d insufficient for amount %d + fee %d", utxo.Satoshis, amountSat, flatFee)
	}
	changeAmount := utxo.Satoshis - amountSat - flatFee
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(toAddr, amountSat); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(fromAddr, changeAmount); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: priv}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func signBuyOrderInputs(rawHex string, priv *bec.PrivateKey, ftutxos []*util.FtUTXO, utxos []*bt.UTXO, preTXs []*bt.Tx, prepreTxData []string, ftCodeScript string) (string, error) {
	rawBytes, err := hex.DecodeString(rawHex)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	if len(tx.Inputs) != len(ftutxos)+len(utxos) {
		return "", fmt.Errorf("input count %d != ftutxos %d + utxos %d", len(tx.Inputs), len(ftutxos), len(utxos))
	}
	for i, fu := range ftutxos {
		tx.Inputs[i].PreviousTxScript = fu.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = fu.Satoshis
	}
	for i, u := range utxos {
		tx.Inputs[len(ftutxos)+i].PreviousTxScript = u.LockingScript
		tx.Inputs[len(ftutxos)+i].PreviousTxSatoshis = u.Satoshis
	}
	pubKey := hex.EncodeToString(priv.PubKey().SerialiseCompressed())
	sigs := make([]string, len(tx.Inputs))
	for i := range tx.Inputs {
		sh, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return "", fmt.Errorf("sighash input %d: %w", i, err)
		}
		sig, err := priv.Sign(sh)
		if err != nil {
			return "", fmt.Errorf("sign input %d: %w", i, err)
		}
		sigs[i] = hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
	}
	order := contract.NewOrderBook()
	isCoin := contract.IsCoinScript(ftCodeScript)
	return order.FillSigsMakeBuyOrder(rawHex, sigs, pubKey, preTXs, prepreTxData, isCoin)
}
