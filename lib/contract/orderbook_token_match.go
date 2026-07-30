package contract

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
)

type tokenOrderMatchBuild struct {
	tx       *bt.Tx
	buyData  *TokenOrderData
	sellData *TokenOrderData
	buyInfo  util.FTScriptInfo
	sellInfo util.FTScriptInfo
}

func tokenOrderMin(left, right *big.Int) *big.Int {
	if left.Cmp(right) < 0 {
		return new(big.Int).Set(left)
	}
	return new(big.Int).Set(right)
}

func tokenOrderMulDiv(left, right, divisor *big.Int) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(left, right), divisor)
}

func tokenOrderAddFTPair(
	tx *bt.Tx,
	codeHex, tapeHex, amountHex, addressOrHash string,
	satoshis uint64,
) error {
	code, err := BuildFTtransferCode(codeHex, addressOrHash)
	if err != nil {
		return err
	}
	tape, err := BuildFTtransferTape(tapeHex, amountHex)
	if err != nil {
		return err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: satoshis})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	return nil
}

func tokenOrderCheckedSatoshis(utxos ...*bt.UTXO) (*big.Int, error) {
	total := new(big.Int)
	for i, utxo := range utxos {
		if utxo == nil {
			return nil, fmt.Errorf("nil UTXO %d", i)
		}
		total.Add(total, new(big.Int).SetUint64(utxo.Satoshis))
	}
	return total, nil
}

func (o *OrderBook) buildMatchTokenOrder(
	buyUTXO *bt.UTXO,
	buyPreTX *bt.Tx,
	buyFT *util.FtUTXO,
	buyFTPreTX *bt.Tx,
	sellUTXO *bt.UTXO,
	sellPreTX *bt.Tx,
	sellFT *util.FtUTXO,
	sellFTPreTX *bt.Tx,
	feeUTXOs []*bt.UTXO,
	ftaFeeAddress, ftbFeeAddress string,
	mainnet bool,
) (*tokenOrderMatchBuild, error) {
	if buyUTXO == nil || buyPreTX == nil || buyFT == nil || buyFTPreTX == nil ||
		sellUTXO == nil || sellPreTX == nil || sellFT == nil || sellFTPreTX == nil {
		return nil, fmt.Errorf("token order and FT UTXOs with parent transactions are required")
	}
	if !obIsValidAddress(ftaFeeAddress) || !obIsValidAddress(ftbFeeAddress) {
		return nil, fmt.Errorf("invalid fee address")
	}
	if len(feeUTXOs) == 0 {
		return nil, fmt.Errorf("TBC fee UTXOs required")
	}

	buyData, err := GetTokenOrderData(buyUTXO.LockingScript.ToHex(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("parse buy order: %w", err)
	}
	sellData, err := GetTokenOrderData(sellUTXO.LockingScript.ToHex(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("parse sell order: %w", err)
	}
	if buyData.FTAID != sellData.FTAID || buyData.FTBID != sellData.FTBID {
		return nil, fmt.Errorf("token order pair mismatch")
	}
	if buyData.FTAPartialHash != sellData.FTAPartialHash ||
		buyData.FTBPartialHash != sellData.FTBPartialHash {
		return nil, fmt.Errorf("token order code hash mismatch")
	}
	if buyData.UnitPrice.Cmp(sellData.UnitPrice) != 0 {
		return nil, fmt.Errorf("token order unit price mismatch")
	}

	precision := big.NewInt(1_000_000)
	matchedA := tokenOrderMin(buyData.SaleVolume, sellData.SaleVolume)
	tokenATax := tokenOrderMulDiv(matchedA, buyData.FeeRate, precision)
	tokenABuyer := new(big.Int).Sub(new(big.Int).Set(matchedA), tokenATax)
	newSellAmount := new(big.Int).Sub(new(big.Int).Set(sellData.SaleVolume), matchedA)
	tokenBPay := tokenOrderMulDiv(matchedA, sellData.UnitPrice, precision)
	tokenBTax := tokenOrderMulDiv(tokenBPay, sellData.FeeRate, precision)
	tokenBSeller := new(big.Int).Sub(new(big.Int).Set(tokenBPay), tokenBTax)
	newBuyAmount := new(big.Int).Sub(new(big.Int).Set(buyData.SaleVolume), matchedA)

	if tokenABuyer.Sign() <= 0 || tokenBSeller.Sign() <= 0 {
		return nil, fmt.Errorf("matched amount is too small after fee")
	}
	if sellFT.FtBalance == nil || sellFT.FtBalance.Cmp(matchedA) < 0 {
		return nil, fmt.Errorf("sell order FT balance is insufficient")
	}
	if buyFT.FtBalance == nil || buyFT.FtBalance.Cmp(tokenBPay) < 0 {
		return nil, fmt.Errorf("buy order FT balance is insufficient")
	}
	buyInfo, err := util.ClassifyFTScript(buyFT.LockingScript)
	if err != nil {
		return nil, fmt.Errorf("classify buy FT: %w", err)
	}
	sellInfo, err := util.ClassifyFTScript(sellFT.LockingScript)
	if err != nil {
		return nil, fmt.Errorf("classify sell FT: %w", err)
	}
	buyTape, err := htlcTokenTapeAt(buyFTPreTX, int(buyFT.Vout))
	if err != nil {
		return nil, fmt.Errorf("buy FT tape: %w", err)
	}
	sellTape, err := htlcTokenTapeAt(sellFTPreTX, int(sellFT.Vout))
	if err != nil {
		return nil, fmt.Errorf("sell FT tape: %w", err)
	}

	tokenABuyerHex, _ := BuildTapeAmountWithFtInputIndex(
		tokenABuyer, []*big.Int{new(big.Int).Set(sellFT.FtBalance)}, 3,
	)
	tokenARemainingAfterBuyer := new(big.Int).Sub(new(big.Int).Set(sellFT.FtBalance), tokenABuyer)
	tokenATaxHex, tokenAChangeHex := BuildTapeAmountWithFtInputIndex(
		tokenATax, []*big.Int{tokenARemainingAfterBuyer}, 3,
	)
	tokenBSellerHex, _ := BuildTapeAmountWithFtInputIndex(
		tokenBSeller, []*big.Int{new(big.Int).Set(buyFT.FtBalance)}, 1,
	)
	tokenBRemainingAfterSeller := new(big.Int).Sub(new(big.Int).Set(buyFT.FtBalance), tokenBSeller)
	tokenBTaxHex, tokenBChangeHex := BuildTapeAmountWithFtInputIndex(
		tokenBTax, []*big.Int{tokenBRemainingAfterSeller}, 1,
	)

	tx := newFTTx()
	if err := tx.FromUTXOs(buyUTXO); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(util.FtUTXOToUTXO(buyFT)); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(util.FtUTXOToUTXO(sellFT)); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(feeUTXOs...); err != nil {
		return nil, err
	}

	sellCodeHex := sellFT.LockingScript.ToHex()
	buyCodeHex := buyFT.LockingScript.ToHex()
	if err := tokenOrderAddFTPair(tx, sellCodeHex, sellTape.ToHex(), tokenABuyerHex, buyData.HoldAddress, sellFT.Satoshis); err != nil {
		return nil, err
	}
	if err := tokenOrderAddFTPair(tx, sellCodeHex, sellTape.ToHex(), tokenATaxHex, ftaFeeAddress, sellFT.Satoshis); err != nil {
		return nil, err
	}
	if err := tokenOrderAddFTPair(tx, buyCodeHex, buyTape.ToHex(), tokenBSellerHex, sellData.HoldAddress, buyFT.Satoshis); err != nil {
		return nil, err
	}
	if err := tokenOrderAddFTPair(tx, buyCodeHex, buyTape.ToHex(), tokenBTaxHex, ftbFeeAddress, buyFT.Satoshis); err != nil {
		return nil, err
	}

	feeChangeAddress, err := feeUTXOs[0].LockingScript.ToAddress(mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive fee change address: %w", err)
	}
	changeOutputIndex := len(tx.Outputs)
	if err := tx.PayToAddress(feeChangeAddress, 1); err != nil {
		return nil, err
	}

	orderDust := o.BuyCodeDust
	if orderDust == 0 {
		orderDust = 300
	}
	if newSellAmount.Sign() > 0 {
		if tokenAChangeHex == zeroFTTapeAmountHex {
			return nil, fmt.Errorf("sell order remains but Token A change is zero")
		}
		newOrderHex, err := UpdateTokenSaleVolume(sellUTXO.LockingScript.ToHex(), newSellAmount)
		if err != nil {
			return nil, err
		}
		newOrder, _ := bscript.NewFromHexString(newOrderHex)
		tx.AddOutput(&bt.Output{LockingScript: newOrder, Satoshis: orderDust})
		orderHash := hex.EncodeToString(crypto.Hash160(crypto.Sha256(newOrder.Bytes())))
		if err := tokenOrderAddFTPair(tx, sellCodeHex, sellTape.ToHex(), tokenAChangeHex, orderHash, sellFT.Satoshis); err != nil {
			return nil, err
		}
	} else if newBuyAmount.Sign() > 0 {
		if tokenBChangeHex == zeroFTTapeAmountHex {
			return nil, fmt.Errorf("buy order remains but Token B change is zero")
		}
		newOrderHex, err := UpdateTokenSaleVolume(buyUTXO.LockingScript.ToHex(), newBuyAmount)
		if err != nil {
			return nil, err
		}
		newOrder, _ := bscript.NewFromHexString(newOrderHex)
		tx.AddOutput(&bt.Output{LockingScript: newOrder, Satoshis: orderDust})
		orderHash := hex.EncodeToString(crypto.Hash160(crypto.Sha256(newOrder.Bytes())))
		if err := tokenOrderAddFTPair(tx, buyCodeHex, buyTape.ToHex(), tokenBChangeHex, orderHash, buyFT.Satoshis); err != nil {
			return nil, err
		}
	}

	allInputs := []*bt.UTXO{buyUTXO, util.FtUTXOToUTXO(buyFT), sellUTXO, util.FtUTXOToUTXO(sellFT)}
	allInputs = append(allInputs, feeUTXOs...)
	inputTotal, err := tokenOrderCheckedSatoshis(allInputs...)
	if err != nil {
		return nil, err
	}
	outputTotal := new(big.Int)
	for i, output := range tx.Outputs {
		if i != changeOutputIndex {
			outputTotal.Add(outputTotal, new(big.Int).SetUint64(output.Satoshis))
		}
	}
	fee := big.NewInt(int64(obTargetFee(tx.JSEstimateSize() + 2*1000 + 2*2000)))
	change := new(big.Int).Sub(inputTotal, outputTotal)
	change.Sub(change, fee)
	if change.Cmp(big.NewInt(24)) < 0 || change.BitLen() > 64 {
		return nil, fmt.Errorf("insufficient TBC fee UTXO for token match order")
	}
	tx.Outputs[changeOutputIndex].Satoshis = change.Uint64()

	return &tokenOrderMatchBuild{
		tx: tx, buyData: buyData, sellData: sellData,
		buyInfo: buyInfo, sellInfo: sellInfo,
	}, nil
}

const zeroFTTapeAmountHex = "000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// MatchTokenOrder atomically exchanges Token A and Token B, carries forward
// the unmatched order when needed, and signs all five input categories.
func (o *OrderBook) MatchTokenOrder(
	privateKey *bec.PrivateKey,
	buyUTXO *bt.UTXO,
	buyPreTX *bt.Tx,
	buyFT *util.FtUTXO,
	buyFTPreTX *bt.Tx,
	buyFTPrePre string,
	sellUTXO *bt.UTXO,
	sellPreTX *bt.Tx,
	sellFT *util.FtUTXO,
	sellFTPreTX *bt.Tx,
	sellFTPrePre string,
	feeUTXOs []*bt.UTXO,
	ftaFeeAddress, ftbFeeAddress string,
	mainnet ...bool,
) (string, error) {
	if privateKey == nil {
		return "", fmt.Errorf("private key is required")
	}
	useMainnet := true
	if len(mainnet) > 0 {
		useMainnet = mainnet[0]
	}
	built, err := o.buildMatchTokenOrder(
		buyUTXO, buyPreTX, buyFT, buyFTPreTX,
		sellUTXO, sellPreTX, sellFT, sellFTPreTX,
		feeUTXOs, ftaFeeAddress, ftbFeeAddress, useMainnet,
	)
	if err != nil {
		return "", err
	}
	tx := built.tx
	if built.buyInfo.IsCoin {
		tx.Inputs[1].SequenceNumber = 0xfffffffe
	}
	if built.sellInfo.IsCoin {
		tx.Inputs[3].SequenceNumber = 0xfffffffe
	}

	buyOrderUnlockHex, err := GetTokenOrderUnlock(tx, buyPreTX, int(buyUTXO.Vout))
	if err != nil {
		return "", err
	}
	buyOrderUnlock, _ := bscript.NewFromHexString(buyOrderUnlockHex)
	if err := tx.InsertInputUnlockingScript(0, buyOrderUnlock); err != nil {
		return "", err
	}
	buyFTUnlock, err := (&FT{ContractTxid: built.buyData.FTBID}).GetFTUnlockSwap(
		privateKey, tx, buyFTPreTX, buyFTPrePre, buyPreTX,
		1, int(buyFT.Vout), built.buyInfo.Version, built.buyInfo.IsCoin, true,
	)
	if err != nil {
		return "", fmt.Errorf("buy FT unlock: %w", err)
	}
	if err := tx.InsertInputUnlockingScript(1, buyFTUnlock); err != nil {
		return "", err
	}

	sellOrderUnlockHex, err := GetTokenOrderUnlock(tx, sellPreTX, int(sellUTXO.Vout))
	if err != nil {
		return "", err
	}
	sellOrderUnlock, _ := bscript.NewFromHexString(sellOrderUnlockHex)
	if err := tx.InsertInputUnlockingScript(2, sellOrderUnlock); err != nil {
		return "", err
	}
	sellFTUnlock, err := (&FT{ContractTxid: built.sellData.FTAID}).GetFTUnlockSwap(
		privateKey, tx, sellFTPreTX, sellFTPrePre, sellPreTX,
		3, int(sellFT.Vout), built.sellInfo.Version, built.sellInfo.IsCoin, true,
	)
	if err != nil {
		return "", fmt.Errorf("sell FT unlock: %w", err)
	}
	if err := tx.InsertInputUnlockingScript(3, sellFTUnlock); err != nil {
		return "", err
	}

	ctx := context.Background()
	for i := 4; i < len(tx.Inputs); i++ {
		simple := &unlocker.Simple{PrivateKey: privateKey}
		unlock, err := simple.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx: uint32(i), SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), unlock); err != nil {
			return "", err
		}
	}
	return tx.String(), nil
}
