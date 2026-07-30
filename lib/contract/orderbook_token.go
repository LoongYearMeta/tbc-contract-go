package contract

import (
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
)

const (
	tokenOrderPrefixLength = 1152
	tokenOrderDataLength   = 180
	tokenOrderLength       = tokenOrderPrefixLength + tokenOrderDataLength
	tokenOrderSizeHex      = "023405"
)

//go:embed asm/orderbook_token_sell.hex
var tokenSellOrderHexTemplate string

//go:embed asm/orderbook_token_buy.hex
var tokenBuyOrderHexTemplate string

// TokenOrderData is the decoded 180-byte data suffix shared by Token Order
// sell and buy contracts.
type TokenOrderData struct {
	HoldAddress    string
	SaleVolume     *big.Int
	FTAPartialHash string
	FTBPartialHash string
	FeeRate        *big.Int
	UnitPrice      *big.Int
	FTAID          string
	FTBID          string
}

func tokenOrderUint64(value *big.Int, field string) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("%s is required", field)
	}
	if value.Sign() < 0 || value.BitLen() > 64 {
		return nil, fmt.Errorf("%s must fit uint64", field)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, value.Uint64())
	return buf, nil
}

func tokenOrderHash(value, field string) ([]byte, error) {
	if !obSHA256HexPattern.MatchString(value) {
		return nil, fmt.Errorf("%s must be a 32-byte hex string", field)
	}
	return hex.DecodeString(value)
}

// BuildTokenOrderData mirrors buildTokenOrderData() in tbc-contract 1.6.5.
func (o *OrderBook) BuildTokenOrderData() (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(o.HoldAddress)
	if err != nil {
		return nil, fmt.Errorf("BuildTokenOrderData: hold address: %w", err)
	}
	holdHash, err := hex.DecodeString(addr.PublicKeyHash)
	if err != nil {
		return nil, fmt.Errorf("BuildTokenOrderData: hold address hash: %w", err)
	}
	saleVolume, err := tokenOrderUint64(o.TokenSaleVolume, "sale volume")
	if err != nil {
		return nil, err
	}
	feeRate, err := tokenOrderUint64(o.TokenFeeRate, "fee rate")
	if err != nil {
		return nil, err
	}
	unitPrice, err := tokenOrderUint64(o.TokenUnitPrice, "unit price")
	if err != nil {
		return nil, err
	}
	ftaPartial, err := tokenOrderHash(o.FtPartialHash, "FTA partial hash")
	if err != nil {
		return nil, err
	}
	ftbPartial, err := tokenOrderHash(o.FtBPartialHash, "FTB partial hash")
	if err != nil {
		return nil, err
	}
	ftaID, err := tokenOrderHash(o.FtID, "FTA ID")
	if err != nil {
		return nil, err
	}
	ftbID, err := tokenOrderHash(o.FtBID, "FTB ID")
	if err != nil {
		return nil, err
	}

	script := bscript.NewScript()
	for _, field := range [][]byte{
		holdHash,
		saleVolume,
		ftaPartial,
		ftbPartial,
		feeRate,
		unitPrice,
		ftaID,
		ftbID,
	} {
		if err := script.AppendPushData(field); err != nil {
			return nil, fmt.Errorf("BuildTokenOrderData: append field: %w", err)
		}
	}
	if script.Len() != tokenOrderDataLength {
		return nil, fmt.Errorf("BuildTokenOrderData: length %d, want %d", script.Len(), tokenOrderDataLength)
	}
	return script, nil
}

func (o *OrderBook) tokenOrderCode(template, taxAddress string) (*bscript.Script, error) {
	address, err := obAddrPKH(o.HoldAddress)
	if err != nil {
		return nil, fmt.Errorf("token order hold address: %w", err)
	}
	taxAddressHex, err := obAddrPKH(taxAddress)
	if err != nil {
		return nil, fmt.Errorf("token order tax address: %w", err)
	}
	codeHex := strings.NewReplacer(
		"${address}", address,
		"${taxAddressHex}", taxAddressHex,
		"${buyCodeSize}", tokenOrderSizeHex,
		"${sellCodeSize}", tokenOrderSizeHex,
	).Replace(strings.TrimSpace(template))
	code, err := bscript.NewFromHexString(codeHex)
	if err != nil {
		return nil, fmt.Errorf("token order code template: %w", err)
	}
	if code.Len() != tokenOrderPrefixLength {
		return nil, fmt.Errorf("token order prefix length %d, want %d", code.Len(), tokenOrderPrefixLength)
	}
	data, err := o.BuildTokenOrderData()
	if err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(code.Bytes(), data.Bytes()...)), nil
}

// GetTokenSellOrderCode mirrors getTokenSellOrderCode() in JS 1.6.5.
func (o *OrderBook) GetTokenSellOrderCode(taxAddress string) (*bscript.Script, error) {
	return o.tokenOrderCode(tokenSellOrderHexTemplate, taxAddress)
}

// GetTokenBuyOrderCode mirrors getTokenBuyOrderCode() in JS 1.6.5.
func (o *OrderBook) GetTokenBuyOrderCode(taxAddress string) (*bscript.Script, error) {
	return o.tokenOrderCode(tokenBuyOrderHexTemplate, taxAddress)
}

// GetTokenOrderData decodes the eight pushed fields at the end of a Token
// Order script. It accepts either the complete 1332-byte contract or its
// standalone 180-byte data suffix.
func GetTokenOrderData(codeHex string, mainnet bool) (*TokenOrderData, error) {
	script, err := bscript.NewFromHexString(codeHex)
	if err != nil {
		return nil, fmt.Errorf("GetTokenOrderData: decode script: %w", err)
	}
	if script.Len() < tokenOrderDataLength {
		return nil, fmt.Errorf("GetTokenOrderData: script is shorter than %d bytes", tokenOrderDataLength)
	}
	chunks := script.Chunks()
	if len(chunks) < 8 {
		return nil, fmt.Errorf("GetTokenOrderData: expected at least 8 chunks")
	}
	fields := chunks[len(chunks)-8:]
	wantLengths := []int{20, 8, 32, 32, 8, 8, 32, 32}
	for i, want := range wantLengths {
		if len(fields[i].Buf) != want {
			return nil, fmt.Errorf("GetTokenOrderData: field %d length %d, want %d", i, len(fields[i].Buf), want)
		}
	}
	addr, err := bscript.NewAddressFromPublicKeyHash(fields[0].Buf, mainnet)
	if err != nil {
		return nil, fmt.Errorf("GetTokenOrderData: hold address: %w", err)
	}
	readAmount := func(field []byte) *big.Int {
		return new(big.Int).SetUint64(binary.LittleEndian.Uint64(field))
	}
	return &TokenOrderData{
		HoldAddress:    addr.AddressString,
		SaleVolume:     readAmount(fields[1].Buf),
		FTAPartialHash: hex.EncodeToString(fields[2].Buf),
		FTBPartialHash: hex.EncodeToString(fields[3].Buf),
		FeeRate:        readAmount(fields[4].Buf),
		UnitPrice:      readAmount(fields[5].Buf),
		FTAID:          hex.EncodeToString(fields[6].Buf),
		FTBID:          hex.EncodeToString(fields[7].Buf),
	}, nil
}

// UpdateTokenSaleVolume replaces only the uint64 sale-volume field and
// preserves the rest of the contract byte-for-byte.
func UpdateTokenSaleVolume(codeHex string, newVolume *big.Int) (string, error) {
	volume, err := tokenOrderUint64(newVolume, "sale volume")
	if err != nil {
		return "", err
	}
	if _, err := GetTokenOrderData(codeHex, true); err != nil {
		return "", err
	}
	code, err := hex.DecodeString(codeHex)
	if err != nil {
		return "", err
	}
	dataStart := len(code) - tokenOrderDataLength
	copy(code[dataStart+22:dataStart+30], volume)
	return hex.EncodeToString(code), nil
}

// GetTokenOrderUnlock mirrors the Token Order use of the common OrderBook
// current/pre-transaction serialization and selects match/cancel option 1.
func GetTokenOrderUnlock(currentTX, preTX *bt.Tx, preTxVout int) (string, error) {
	preTxData, err := util.GetPreTxdataOB(preTX, preTxVout, 1)
	if err != nil {
		return "", err
	}
	currentTxData, err := util.GetCurrentTxOutputsDataOBFixed(currentTX, 12)
	if err != nil {
		return "", err
	}
	return currentTxData + preTxData + "51", nil
}

func tokenOrderPositiveUint64(value *big.Int, field string) error {
	if _, err := tokenOrderUint64(value, field); err != nil {
		return err
	}
	if value.Sign() <= 0 {
		return fmt.Errorf("%s must be positive", field)
	}
	return nil
}

func (o *OrderBook) prepareTokenOrder(
	orderType, holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID, ftaCode, ftbCode string,
) error {
	if !obIsValidAddress(holdAddress) || !obIsValidAddress(taxAddress) {
		return fmt.Errorf("invalid hold address or tax address")
	}
	if err := tokenOrderPositiveUint64(saleVolume, "sale volume"); err != nil {
		return err
	}
	if err := tokenOrderPositiveUint64(unitPrice, "unit price"); err != nil {
		return err
	}
	if _, err := tokenOrderUint64(feeRate, "fee rate"); err != nil {
		return err
	}
	if !obIsValidSHA256Hex(ftaID) || !obIsValidSHA256Hex(ftbID) {
		return fmt.Errorf("FTA ID and FTB ID must be 32-byte hex strings")
	}
	ftaInfo, err := classifyOrderBookFTCode(ftaCode)
	if err != nil {
		return fmt.Errorf("classify FTA code: %w", err)
	}
	ftbInfo, err := classifyOrderBookFTCode(ftbCode)
	if err != nil {
		return fmt.Errorf("classify FTB code: %w", err)
	}
	ftaPartial, err := ComputeFtPartialHash(ftaCode, ftaInfo.IsCoin)
	if err != nil {
		return fmt.Errorf("FTA partial hash: %w", err)
	}
	ftbPartial, err := ComputeFtPartialHash(ftbCode, ftbInfo.IsCoin)
	if err != nil {
		return fmt.Errorf("FTB partial hash: %w", err)
	}

	o.Type = orderType
	o.HoldAddress = holdAddress
	o.TokenSaleVolume = new(big.Int).Set(saleVolume)
	o.TokenUnitPrice = new(big.Int).Set(unitPrice)
	o.TokenFeeRate = new(big.Int).Set(feeRate)
	o.FtID = strings.ToLower(ftaID)
	o.FtBID = strings.ToLower(ftbID)
	o.FtPartialHash = ftaPartial
	o.FtBPartialHash = ftbPartial
	return nil
}

func tokenOrderTotalBalance(ftUTXOs []*util.FtUTXO) ([]*big.Int, *big.Int, error) {
	if len(ftUTXOs) == 0 {
		return nil, nil, fmt.Errorf("FT UTXOs must be non-empty")
	}
	if len(ftUTXOs) > 6 {
		return nil, nil, fmt.Errorf("FT UTXOs length must be <= 6")
	}
	amounts := make([]*big.Int, len(ftUTXOs))
	total := new(big.Int)
	for i, ftUTXO := range ftUTXOs {
		if ftUTXO == nil || ftUTXO.LockingScript == nil || ftUTXO.FtBalance == nil ||
			ftUTXO.FtBalance.Sign() < 0 || ftUTXO.FtBalance.BitLen() > 64 {
			return nil, nil, fmt.Errorf("invalid FT UTXO %d", i)
		}
		amounts[i] = new(big.Int).Set(ftUTXO.FtBalance)
		total.Add(total, ftUTXO.FtBalance)
	}
	return amounts, total, nil
}

func tokenOrderTargetFee(tx *bt.Tx, extraBytes int) int {
	return obTargetFee(tx.JSEstimateSize() + extraBytes)
}

func (o *OrderBook) buildTokenOrderTX(
	orderType, holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID, ftaCode, ftbCode string,
	feeUTXOs []*bt.UTXO,
	ftUTXOs []*util.FtUTXO,
	preTXs []*bt.Tx,
) (string, error) {
	if len(preTXs) != len(ftUTXOs) || len(preTXs) == 0 {
		return "", fmt.Errorf("FT UTXOs and preTXs length mismatch")
	}
	if err := o.prepareTokenOrder(
		orderType, holdAddress, taxAddress, saleVolume, unitPrice, feeRate,
		ftaID, ftbID, ftaCode, ftbCode,
	); err != nil {
		return "", fmt.Errorf("BuildToken%sOrderTX: %w", strings.Title(orderType), err)
	}

	var orderCode *bscript.Script
	var err error
	if orderType == "sell" {
		orderCode, err = o.GetTokenSellOrderCode(taxAddress)
	} else {
		orderCode, err = o.GetTokenBuyOrderCode(taxAddress)
	}
	if err != nil {
		return "", err
	}

	lockAmount := new(big.Int).Set(saleVolume)
	if orderType == "buy" {
		lockAmount.Mul(lockAmount, unitPrice)
		lockAmount.Div(lockAmount, big.NewInt(1_000_000))
		if lockAmount.Sign() <= 0 {
			return "", fmt.Errorf("buy order FT amount is too small")
		}
	}
	tapeAmounts, tapeTotal, err := tokenOrderTotalBalance(ftUTXOs)
	if err != nil {
		return "", err
	}
	if lockAmount.Cmp(tapeTotal) > 0 {
		return "", fmt.Errorf("token order FT balance is insufficient")
	}
	amountHex, changeHex := BuildTapeAmount(lockAmount, tapeAmounts)
	firstTape, err := htlcTokenTapeAt(preTXs[0], int(ftUTXOs[0].Vout))
	if err != nil {
		return "", err
	}
	ftCodeHex := ftUTXOs[0].LockingScript.ToHex()
	ftTapeHex := firstTape.ToHex()
	orderHash160 := hex.EncodeToString(crypto.Hash160(crypto.Sha256(orderCode.Bytes())))
	lockedCode, err := BuildFTtransferCode(ftCodeHex, orderHash160)
	if err != nil {
		return "", err
	}
	lockedTape, err := BuildFTtransferTape(ftTapeHex, amountHex)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftUTXOs)...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(feeUTXOs...); err != nil {
		return "", err
	}
	orderDust := o.BuyCodeDust
	if orderDust == 0 {
		orderDust = 300
	}
	tx.AddOutput(&bt.Output{LockingScript: orderCode, Satoshis: orderDust})
	tx.AddOutput(&bt.Output{LockingScript: lockedCode, Satoshis: ftUTXOs[0].Satoshis})
	tx.AddOutput(&bt.Output{LockingScript: lockedTape, Satoshis: 0})
	if lockAmount.Cmp(tapeTotal) < 0 {
		changeCode, err := BuildFTtransferCode(ftCodeHex, holdAddress)
		if err != nil {
			return "", err
		}
		changeTape, err := BuildFTtransferTape(ftTapeHex, changeHex)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: ftUTXOs[0].Satoshis})
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}
	if err := tx.ChangeToAddress(holdAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.AdjustImplicitFeeToTarget(tokenOrderTargetFee(tx, len(ftUTXOs)*2000)); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// BuildTokenSellOrderTX locks Token A into a new Token-for-Token sell order.
func (o *OrderBook) BuildTokenSellOrderTX(
	holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID, ftaCode, ftbCode string,
	feeUTXOs []*bt.UTXO,
	ftUTXOs []*util.FtUTXO,
	preTXs []*bt.Tx,
) (string, error) {
	return o.buildTokenOrderTX(
		"sell", holdAddress, taxAddress, saleVolume, unitPrice, feeRate,
		ftaID, ftbID, ftaCode, ftbCode, feeUTXOs, ftUTXOs, preTXs,
	)
}

// BuildTokenBuyOrderTX locks Token B into a new Token-for-Token buy order.
func (o *OrderBook) BuildTokenBuyOrderTX(
	holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID, ftaCode, ftbCode string,
	feeUTXOs []*bt.UTXO,
	ftUTXOs []*util.FtUTXO,
	preTXs []*bt.Tx,
) (string, error) {
	return o.buildTokenOrderTX(
		"buy", holdAddress, taxAddress, saleVolume, unitPrice, feeRate,
		ftaID, ftbID, ftaCode, ftbCode, feeUTXOs, ftUTXOs, preTXs,
	)
}

func (o *OrderBook) buildCancelTokenOrderTX(
	orderUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	ftPreTX *bt.Tx,
	feeUTXOs []*bt.UTXO,
	mainnet ...bool,
) (string, error) {
	if orderUTXO == nil || ftUTXO == nil || ftPreTX == nil {
		return "", fmt.Errorf("order UTXO, FT UTXO, and FT preTX are required")
	}
	useMainnet := true
	if len(mainnet) > 0 {
		useMainnet = mainnet[0]
	}
	data, err := GetTokenOrderData(orderUTXO.LockingScript.ToHex(), useMainnet)
	if err != nil {
		return "", err
	}
	tapeAmounts, total, err := tokenOrderTotalBalance([]*util.FtUTXO{ftUTXO})
	if err != nil {
		return "", err
	}
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(total, tapeAmounts, 1)
	if changeHex != strings.Repeat("00", 48) {
		return "", fmt.Errorf("change amount is not zero")
	}
	tape, err := htlcTokenTapeAt(ftPreTX, int(ftUTXO.Vout))
	if err != nil {
		return "", err
	}
	returnCode, err := BuildFTtransferCode(ftUTXO.LockingScript.ToHex(), data.HoldAddress)
	if err != nil {
		return "", err
	}
	returnTape, err := BuildFTtransferTape(tape.ToHex(), amountHex)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(orderUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(util.FtUTXOToUTXO(ftUTXO)); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(feeUTXOs...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: returnCode, Satoshis: ftUTXO.Satoshis})
	tx.AddOutput(&bt.Output{LockingScript: returnTape, Satoshis: 0})
	if err := tx.ChangeToAddress(data.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.AdjustImplicitFeeToTarget(tokenOrderTargetFee(tx, 2000)); err != nil {
		return "", err
	}
	return tx.String(), nil
}

func (o *OrderBook) BuildCancelTokenSellOrderTX(
	orderUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	ftPreTX *bt.Tx,
	feeUTXOs []*bt.UTXO,
	mainnet ...bool,
) (string, error) {
	return o.buildCancelTokenOrderTX(orderUTXO, ftUTXO, ftPreTX, feeUTXOs, mainnet...)
}

func (o *OrderBook) BuildCancelTokenBuyOrderTX(
	orderUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	ftPreTX *bt.Tx,
	feeUTXOs []*bt.UTXO,
	mainnet ...bool,
) (string, error) {
	return o.buildCancelTokenOrderTX(orderUTXO, ftUTXO, ftPreTX, feeUTXOs, mainnet...)
}

func tokenOrderValidateFillArgs(raw string, sigs []string, publicKey string) (*bt.Tx, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid token order transaction: %w", err)
	}
	pubKeyBytes, err := hex.DecodeString(publicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex")
	}
	if _, err := bec.ParsePubKey(pubKeyBytes, bec.S256()); err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	if len(sigs) < len(tx.Inputs) {
		return nil, fmt.Errorf("signatures length is less than inputs length")
	}
	for i, sig := range sigs {
		if _, err := hex.DecodeString(sig); err != nil || sig == "" {
			return nil, fmt.Errorf("invalid signature %d", i)
		}
	}
	return tx, nil
}

func tokenOrderInsertP2PKHUnlock(tx *bt.Tx, index int, sig, publicKey string) error {
	script, err := bscript.NewFromASM(sig + " " + publicKey)
	if err != nil {
		return err
	}
	return tx.InsertInputUnlockingScript(uint32(index), script)
}

func (o *OrderBook) fillSigsMakeTokenOrder(
	raw string,
	sigs []string,
	publicKey string,
	preTXs []*bt.Tx,
	prePreTxData []string,
) (string, error) {
	tx, err := tokenOrderValidateFillArgs(raw, sigs, publicKey)
	if err != nil {
		return "", err
	}
	if len(preTXs) == 0 || len(preTXs) != len(prePreTxData) {
		return "", fmt.Errorf("preTXs and prePreTxData length mismatch")
	}
	if len(preTXs) > len(tx.Inputs) || len(tx.Outputs) < 2 {
		return "", fmt.Errorf("token order transaction structure mismatch")
	}
	info, err := util.ClassifyFTScript(tx.Outputs[1].LockingScript)
	if err != nil {
		return "", fmt.Errorf("classify locked FT output: %w", err)
	}
	for i := range preTXs {
		if preTXs[i] == nil {
			return "", fmt.Errorf("nil preTX %d", i)
		}
		if info.IsCoin {
			tx.Inputs[i].SequenceNumber = 0xfffffffe
		}
		vout := int(tx.Inputs[i].PreviousTxOutIndex)
		unlock, err := StaticGetFTunlock(
			sigs[i], publicKey, tx, preTXs[i], prePreTxData[i],
			i, vout, info.IsCoin,
		)
		if err != nil {
			return "", fmt.Errorf("FT input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), unlock); err != nil {
			return "", err
		}
	}
	for i := len(preTXs); i < len(tx.Inputs); i++ {
		if err := tokenOrderInsertP2PKHUnlock(tx, i, sigs[i], publicKey); err != nil {
			return "", err
		}
	}
	return tx.String(), nil
}

func (o *OrderBook) FillSigsMakeTokenSellOrder(
	raw string,
	sigs []string,
	publicKey string,
	preTXs []*bt.Tx,
	prePreTxData []string,
) (string, error) {
	return o.fillSigsMakeTokenOrder(raw, sigs, publicKey, preTXs, prePreTxData)
}

func (o *OrderBook) FillSigsMakeTokenBuyOrder(
	raw string,
	sigs []string,
	publicKey string,
	preTXs []*bt.Tx,
	prePreTxData []string,
) (string, error) {
	return o.fillSigsMakeTokenOrder(raw, sigs, publicKey, preTXs, prePreTxData)
}

func (o *OrderBook) fillSigsCancelTokenOrder(
	raw string,
	sigs []string,
	publicKey string,
	orderPreTX, ftPreTX *bt.Tx,
	ftPrePreTxData string,
) (string, error) {
	tx, err := tokenOrderValidateFillArgs(raw, sigs, publicKey)
	if err != nil {
		return "", err
	}
	if orderPreTX == nil || ftPreTX == nil || len(tx.Inputs) < 2 || len(tx.Outputs) == 0 {
		return "", fmt.Errorf("cancel token order transaction structure mismatch")
	}
	cancelUnlock, err := bscript.NewFromASM(sigs[0] + " " + publicKey + " OP_2")
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, cancelUnlock); err != nil {
		return "", err
	}
	info, err := util.ClassifyFTScript(tx.Outputs[0].LockingScript)
	if err != nil {
		return "", fmt.Errorf("classify returned FT output: %w", err)
	}
	if info.IsCoin {
		tx.Inputs[1].SequenceNumber = 0xfffffffe
	}
	vout := int(tx.Inputs[1].PreviousTxOutIndex)
	ftUnlock, err := StaticGetFTUnlockSwap(
		sigs[1], publicKey, tx, ftPreTX, ftPrePreTxData, orderPreTX,
		1, vout, info.Version, info.IsCoin, false,
	)
	if err != nil {
		return "", fmt.Errorf("FT swap input: %w", err)
	}
	if err := tx.InsertInputUnlockingScript(1, ftUnlock); err != nil {
		return "", err
	}
	for i := 2; i < len(tx.Inputs); i++ {
		if err := tokenOrderInsertP2PKHUnlock(tx, i, sigs[i], publicKey); err != nil {
			return "", err
		}
	}
	return tx.String(), nil
}

func (o *OrderBook) FillSigsCancelTokenSellOrder(
	raw string,
	sigs []string,
	publicKey string,
	orderPreTX, ftPreTX *bt.Tx,
	ftPrePreTxData string,
) (string, error) {
	return o.fillSigsCancelTokenOrder(
		raw, sigs, publicKey, orderPreTX, ftPreTX, ftPrePreTxData,
	)
}

func (o *OrderBook) FillSigsCancelTokenBuyOrder(
	raw string,
	sigs []string,
	publicKey string,
	orderPreTX, ftPreTX *bt.Tx,
	ftPrePreTxData string,
) (string, error) {
	return o.fillSigsCancelTokenOrder(
		raw, sigs, publicKey, orderPreTX, ftPreTX, ftPrePreTxData,
	)
}
