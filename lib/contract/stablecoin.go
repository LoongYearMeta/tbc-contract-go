//go:build legacy_stablecoin
// +build legacy_stablecoin

// Package contract — stableCoin 扩展 FT（对齐 tbc-contract/lib/contract/stableCoin.ts）。
package contract

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/libsv/go-bk/bec"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

//go:embed stablecoin_mint_template.txt
var stablecoinMintTemplateASM string

// StableCoin 稳定币合约句柄（嵌入 *FT，复用 Mint/Transfer 等）。
type StableCoin struct {
	*FT
}

// CoinNftData 对应 TS coinNftData 接口
type CoinNftData struct {
	NftName          string `json:"nftName"`
	NftSymbol        string `json:"nftSymbol"`
	Description      string `json:"description"`
	CoinDecimal      int    `json:"coinDecimal"`
	CoinTotalSupply  string `json:"coinTotalSupply"`
}

// NewStableCoin 使用与 NewFT 相同参数形式：txid 字符串或 *FtParams。
func NewStableCoin(txidOrParams interface{}) (*StableCoin, error) {
	ft, err := NewFT(txidOrParams)
	if err != nil {
		return nil, err
	}
	return &StableCoin{FT: ft}, nil
}

// CreateCoin 对齐 TS stableCoin.createCoin。
// 创建 coin NFT 交易 + mint 交易，返回 [coinNftTXRaw, coinMintTXRaw]。
func (sc *StableCoin) CreateCoin(
	privKeyAdmin *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	utxoTX *bt.Tx,
	mintMessage string,
) ([]string, error) {
	pubKey := privKeyAdmin.PubKey()
	adminAddress, err := bscript.NewAddressFromPublicKey(pubKey, true)
	if err != nil {
		return nil, err
	}
	name := sc.Name
	symbol := sc.Symbol
	decimal := sc.Decimal
	totalSupply := ParseDecimalToBigInt(fmt.Sprintf("%d", sc.TotalSupply), decimal)

	// Build tape amount
	amountHex := bigIntToUint64LEHex(totalSupply)
	for i := 1; i < 6; i++ {
		amountHex += "0000000000000000"
	}

	nameHex := hex.EncodeToString([]byte(name))
	symbolHex := hex.EncodeToString([]byte(symbol))
	// 与 tbc-lib-js decodeASM+writePushData 不同：链上 SCRIPT_VERIFY_MINIMALDATA 拒绝 decimal 的 0x01 0x06 形式，须用 OP_1..OP_16（decimal 常见为 6 → OP_6）。
	decTok := stableCoinTapeDecimalASM(decimal)
	lockTimeHex := "00000000"

	tapeASM := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s %s 4654617065",
		amountHex, decTok, nameHex, symbolHex, lockTimeHex)
	tapeScript, err := bscript.NewFromASM(tapeASM)
	if err != nil {
		return nil, fmt.Errorf("build tape script: %w", err)
	}
	tapeSize := tapeScript.Len()

	// Build coin NFT
	data := &CoinNftData{
		NftName:         name + " NFT",
		NftSymbol:       symbol + " NFT",
		Description:     "The sole issuance certificate for the stablecoin, dynamically recording cumulative supply and issuance history. Non-transferable, real-time updated, ensuring full transparency and auditability.",
		CoinDecimal:     decimal,
		CoinTotalSupply: "0",
	}
	coinNftTX, err := BuildCoinNftTX(privKeyAdmin, utxo, data)
	if err != nil {
		return nil, fmt.Errorf("build coin nft tx: %w", err)
	}
	coinNftTXRaw := hex.EncodeToString(coinNftTX.Bytes())

	data.CoinTotalSupply = totalSupply.String()
	coinNftOutputs, err := BuildCoinNftOutput(
		coinNftTX.Outputs[0].LockingScript,
		coinNftTX.Outputs[1].LockingScript,
		GetCoinNftTapeScript(data),
	)
	if err != nil {
		return nil, fmt.Errorf("build coin nft output: %w", err)
	}

	// Build code script for mint
	originCodeHash := sha256Hex(coinNftTX.Outputs[0].LockingScript.Bytes())
	codeScript, err := GetCoinMintCode(adminAddress.AddressString, addressTo, originCodeHash, tapeSize)
	if err != nil {
		return nil, fmt.Errorf("build code script: %w", err)
	}
	sc.CodeScript = hex.EncodeToString(codeScript.Bytes())
	sc.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	// Build the mint transaction
	tx := newFTTx()
	if err := addInputFromPrevTxOutput(tx, coinNftTX, 0); err != nil {
		return nil, err
	}
	if err := addInputFromPrevTxOutput(tx, coinNftTX, 1); err != nil {
		return nil, err
	}
	if err := addInputFromPrevTxOutput(tx, coinNftTX, 3); err != nil {
		return nil, err
	}

	for _, out := range coinNftOutputs {
		tx.AddOutput(out)
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if mintMessage != "" {
		msgHex := hex.EncodeToString([]byte(mintMessage))
		msgASM := fmt.Sprintf("OP_FALSE OP_RETURN %s", msgHex)
		msgScript, _ := bscript.NewFromASM(msgASM)
		tx.AddOutput(&bt.Output{LockingScript: msgScript, Satoshis: 0})
	}

	// 与 stableCoin.ts createCoin：tx.feePerKb(80).change(admin) 后再 setInputScript；否则 getCurrentTxdata 不含找零，与链上合约 OP_SPLIT 不一致。
	fq := newFeeQuoteWithSatPerKB(nftFeeSatPerKBFromEnv())
	if err := tx.ChangeToAddress(adminAddress.AddressString, fq); err != nil {
		return nil, fmt.Errorf("mint ChangeToAddress: %w", err)
	}

	// Sign: input 0 = NFT unlock, input 1 = P2PKH sig, input 2 = P2PKH (change from coinNftTX)
	nftUnlocker := &nftIn0Unlocker{
		priv:    privKeyAdmin,
		preTx:   coinNftTX,
		prePre:  utxoTX,
	}
	tx.Inputs[0].UnlockingScript, err = nftUnlocker.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 0})
	if err != nil {
		return nil, fmt.Errorf("nft unlock input 0: %w", err)
	}

	holdUnlock := &p2pkhOrMintPrefixUnlocker{priv: privKeyAdmin}
	tx.Inputs[1].UnlockingScript, err = holdUnlock.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 1})
	if err != nil {
		return nil, fmt.Errorf("sign input 1 (coin NFT hold): %w", err)
	}

	sigP2PKH := &unlocker.Simple{PrivateKey: privKeyAdmin}
	tx.Inputs[2].UnlockingScript, err = sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 2})
	if err != nil {
		return nil, fmt.Errorf("sign input 2: %w", err)
	}

	coinMintRaw := hex.EncodeToString(tx.Bytes())
	sc.ContractTxid = tx.TxID()
	return []string{coinNftTXRaw, coinMintRaw}, nil
}

// MintCoin 对齐 TS stableCoin.mintCoin，追加铸造稳定币。
func (sc *StableCoin) MintCoin(
	privKeyAdmin *bec.PrivateKey,
	addressTo string,
	mintAmount string,
	utxo *bt.UTXO,
	nftPreTX *bt.Tx,
	nftPrePreTX *bt.Tx,
	mintMessage string,
) (string, error) {
	pubKey := privKeyAdmin.PubKey()
	adminAddress, err := bscript.NewAddressFromPublicKey(pubKey, true)
	if err != nil {
		return "", err
	}
	decimal := sc.Decimal
	totalSupply := big.NewInt(sc.TotalSupply)
	newMintAmount := ParseDecimalToBigInt(mintAmount, decimal)
	newTotalSupply := new(big.Int).Add(totalSupply, newMintAmount)

	coinNftTX := nftPreTX

	amountHex := bigIntToUint64LEHex(newMintAmount)
	for i := 1; i < 6; i++ {
		amountHex += "0000000000000000"
	}

	nameHex := hex.EncodeToString([]byte(sc.Name))
	symbolHex := hex.EncodeToString([]byte(sc.Symbol))
	decTok := stableCoinTapeDecimalASM(decimal)
	lockTimeHex := "00000000"

	tapeASM := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s %s 4654617065",
		amountHex, decTok, nameHex, symbolHex, lockTimeHex)
	tapeScript, err := bscript.NewFromASM(tapeASM)
	if err != nil {
		return "", err
	}
	tapeSize := tapeScript.Len()

	updatedTape := UpdateCoinNftTapeScript(coinNftTX.Outputs[2].LockingScript, newTotalSupply.String())
	coinNftOutputs, err := BuildCoinNftOutput(
		coinNftTX.Outputs[0].LockingScript,
		coinNftTX.Outputs[1].LockingScript,
		updatedTape,
	)
	if err != nil {
		return "", err
	}

	originCodeHash := sha256Hex(coinNftTX.Outputs[0].LockingScript.Bytes())
	codeScript, err := GetCoinMintCode(adminAddress.AddressString, addressTo, originCodeHash, tapeSize)
	if err != nil {
		return "", err
	}
	sc.CodeScript = hex.EncodeToString(codeScript.Bytes())
	sc.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	tx := newFTTx()
	if err := addInputFromPrevTxOutput(tx, coinNftTX, 0); err != nil {
		return "", err
	}
	if err := addInputFromPrevTxOutput(tx, coinNftTX, 1); err != nil {
		return "", err
	}
	utxoTxIDHex := hex.EncodeToString(utxo.TxID)
	if err := tx.From(utxoTxIDHex, utxo.Vout, utxo.LockingScript.String(), utxo.Satoshis); err != nil {
		return "", err
	}

	for _, out := range coinNftOutputs {
		tx.AddOutput(out)
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if mintMessage != "" {
		msgHex := hex.EncodeToString([]byte(mintMessage))
		msgASM := fmt.Sprintf("OP_FALSE OP_RETURN %s", msgHex)
		msgScript, _ := bscript.NewFromASM(msgASM)
		tx.AddOutput(&bt.Output{LockingScript: msgScript, Satoshis: 0})
	}

	fq := newFeeQuoteWithSatPerKB(nftFeeSatPerKBFromEnv())
	if err := tx.ChangeToAddress(adminAddress.AddressString, fq); err != nil {
		return "", fmt.Errorf("mint ChangeToAddress: %w", err)
	}

	nftUnlocker := &nftIn0Unlocker{
		priv:    privKeyAdmin,
		preTx:   nftPreTX,
		prePre:  nftPrePreTX,
	}
	tx.Inputs[0].UnlockingScript, err = nftUnlocker.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 0})
	if err != nil {
		return "", fmt.Errorf("nft unlock input 0: %w", err)
	}

	holdUnlock := &p2pkhOrMintPrefixUnlocker{priv: privKeyAdmin}
	tx.Inputs[1].UnlockingScript, err = holdUnlock.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 1})
	if err != nil {
		return "", fmt.Errorf("sign input 1 (coin NFT hold): %w", err)
	}

	sigP2PKH := &unlocker.Simple{PrivateKey: privKeyAdmin}
	tx.Inputs[2].UnlockingScript, err = sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 2})
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// TransferCoin 对齐 TS stableCoin.transfer，转移稳定币（带 lockTime）。
func (sc *StableCoin) TransferCoin(
	privKey *bec.PrivateKey,
	addressTo string,
	ftAmount string,
	ftutxos []*bt.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxData []string,
	tbcAmount uint64,
) (string, error) {
	pubKey := privKey.PubKey()
	addr, err := bscript.NewAddressFromPublicKey(pubKey, true)
	if err != nil {
		return "", err
	}
	addressFrom := addr.AddressString
	decimal := sc.Decimal
	isCoin := 1

	amountBN := ParseDecimalToBigInt(ftAmount, decimal)
	if amountBN.Sign() < 0 {
		return "", fmt.Errorf("invalid amount input")
	}

	tapeAmountSet := make([]*big.Int, 0, len(ftutxos))
	tapeAmountSum := new(big.Int)
	lockTimeMax := uint32(0)

	for i, fu := range ftutxos {
		bal := new(big.Int)
		bal.SetString(fu.FtBalance, 10)
		tapeAmountSet = append(tapeAmountSet, bal)
		tapeAmountSum.Add(tapeAmountSum, bal)
		lt := GetLockTimeFromTape(preTXs[i].Outputs[fu.Vout+1].LockingScript)
		if lt > lockTimeMax {
			lockTimeMax = lt
		}
	}

	if amountBN.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("insufficient balance, please add more FT UTXOs")
	}
	if decimal > 18 {
		return "", fmt.Errorf("the maximum value for decimal cannot exceed 18")
	}
	if err := checkFTAmountHumanJSMax(decimal, strings.TrimSpace(ftAmount)); err != nil {
		return "", err
	}

	amountHex, changeHex := BuildTapeAmount(amountBN, tapeAmountSet)

	ftUTXOs, err := util.FtUTXOsToUTXOs(ftutxos)
	if err != nil {
		return "", fmt.Errorf("ft utxos: %w", err)
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(ftUTXOs...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(feeUTXO); err != nil {
		return "", err
	}

	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = 4294967294
	}
	tx.LockTime = lockTimeMax

	codeScript := BuildFTtransferCode(sc.CodeScript, addressTo)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})

	tapeScript := BuildFTtransferTape(sc.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if tbcAmount > 0 {
		tx.To(addressTo, tbcAmount)
	}

	if amountBN.Cmp(tapeAmountSum) < 0 {
		changeCode := BuildFTtransferCode(sc.CodeScript, addressFrom)
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
		changeTape := BuildFTtransferTape(sc.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	changeScript, err := bscript.NewP2PKHFromAddress(addressFrom)
	if err != nil {
		return "", fmt.Errorf("NewP2PKHFromAddress for change: %w", err)
	}
	inputTotal := tx.TotalInputSatoshis()
	outputTotal := tx.TotalOutputSatoshis()
	if inputTotal <= outputTotal {
		return "", fmt.Errorf("insufficient input satoshis: in=%d out=%d", inputTotal, outputTotal)
	}
	tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: inputTotal - outputTotal})

	satPerKB := feeSatPerKBFromEnv()
	nFt := len(ftutxos)
	ftUnlockLens := make([]int, nFt)
	for i := 0; i < nFt; i++ {
		us, err := sc.getFTunlockCoin(privKey, tx, preTXs[i], prepreTxData[i], i, int(ftutxos[i].Vout), isCoin)
		if err != nil {
			return "", fmt.Errorf("probe FT unlock length input %d: %w", i, err)
		}
		ftUnlockLens[i] = us.Len()
	}
	if err := adjustFTTransferChangeFee(tx, satPerKB, nFt, ftUnlockLens); err != nil {
		return "", fmt.Errorf("adjust transfer fee: %w", err)
	}

	ctx := context.Background()
	for ii := nFt; ii < len(tx.Inputs); ii++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(ii),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", fmt.Errorf("sign fee input %d: %w", ii, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(ii), us); err != nil {
			return "", err
		}
	}
	for i := range ftutxos {
		us, err := sc.getFTunlockCoin(privKey, tx, preTXs[i], prepreTxData[i], i, int(ftutxos[i].Vout), isCoin)
		if err != nil {
			return "", fmt.Errorf("ft unlock input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// FreezeCoinUTXO 对齐 TS stableCoin.freezeCoinUTXO，管理员冻结 coin UTXO。
func (sc *StableCoin) FreezeCoinUTXO(
	privKeyAdmin *bec.PrivateKey,
	lockTime uint32,
	ftutxos []*bt.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxData []string,
) (string, error) {
	if len(ftutxos) == 0 {
		return "", fmt.Errorf("no FT UTXO available")
	}

	address := GetAddressFromCode(ftutxos[0].Script)
	isCoin := 1
	tapeAmountSet := make([]*big.Int, 0, len(ftutxos))
	tapeAmountSum := new(big.Int)
	lockTimeMax := uint32(0)

	for i, fu := range ftutxos {
		bal := new(big.Int)
		bal.SetString(fu.FtBalance, 10)
		tapeAmountSet = append(tapeAmountSet, bal)
		tapeAmountSum.Add(tapeAmountSum, bal)
		lt := GetLockTimeFromTape(preTXs[i].Outputs[fu.Vout+1].LockingScript)
		if lt > lockTimeMax {
			lockTimeMax = lt
		}
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSet)
	if changeHex != strings.Repeat("0", 96) {
		return "", fmt.Errorf("change amount is not zero")
	}

	tx := newFTTx()
	for _, fu := range ftutxos {
		if err := tx.From(fu.TxID, fu.Vout, fu.Script, fu.Satoshis); err != nil {
			return "", err
		}
	}
	if err := tx.From(hex.EncodeToString(feeUTXO.TxID), feeUTXO.Vout, feeUTXO.LockingScript.String(), feeUTXO.Satoshis); err != nil {
		return "", err
	}

	codeScript := BuildFTtransferCode(sc.CodeScript, address)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})

	tapeScript := BuildFTtransferTape(sc.TapeScript, amountHex)
	tapeScript = SetLockTimeInTape(tapeScript, lockTime)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = 4294967294
		unlock, err := sc.getFTunlockCoin(privKeyAdmin, tx, preTXs[i], prepreTxData[i], i, int(ftutxos[i].Vout), isCoin)
		if err != nil {
			return "", fmt.Errorf("ft unlock input %d: %w", i, err)
		}
		tx.Inputs[i].UnlockingScript = unlock
	}

	sigP2PKH := &unlocker.Simple{PrivateKey: privKeyAdmin}
	feeIdx := len(ftutxos)
	feeUnlock, feeErr := sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: uint32(feeIdx)})
	if feeErr != nil {
		return "", feeErr
	}
	tx.Inputs[feeIdx].UnlockingScript = feeUnlock

	tx.LockTime = lockTimeMax
	return hex.EncodeToString(tx.Bytes()), nil
}

// UnfreezeCoinUTXO 对齐 TS stableCoin.unfreezeCoinUTXO，管理员解冻 coin UTXO。
func (sc *StableCoin) UnfreezeCoinUTXO(
	privKeyAdmin *bec.PrivateKey,
	ftutxos []*bt.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxData []string,
) (string, error) {
	if len(ftutxos) == 0 {
		return "", fmt.Errorf("no FT UTXO available")
	}

	address := GetAddressFromCode(ftutxos[0].Script)
	isCoin := 1
	tapeAmountSet := make([]*big.Int, 0, len(ftutxos))
	tapeAmountSum := new(big.Int)

	for _, fu := range ftutxos {
		bal := new(big.Int)
		bal.SetString(fu.FtBalance, 10)
		tapeAmountSet = append(tapeAmountSet, bal)
		tapeAmountSum.Add(tapeAmountSum, bal)
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSet)
	if changeHex != strings.Repeat("0", 96) {
		return "", fmt.Errorf("change amount is not zero")
	}

	tx := newFTTx()
	for _, fu := range ftutxos {
		if err := tx.From(fu.TxID, fu.Vout, fu.Script, fu.Satoshis); err != nil {
			return "", err
		}
	}
	if err := tx.From(hex.EncodeToString(feeUTXO.TxID), feeUTXO.Vout, feeUTXO.LockingScript.String(), feeUTXO.Satoshis); err != nil {
		return "", err
	}

	codeScript := BuildFTtransferCode(sc.CodeScript, address)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})

	tapeScript := BuildFTtransferTape(sc.TapeScript, amountHex)
	tapeScript = SetLockTimeInTape(tapeScript, 0)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = 4294967294
		unlock, err := sc.getFTunlockCoin(privKeyAdmin, tx, preTXs[i], prepreTxData[i], i, int(ftutxos[i].Vout), isCoin)
		if err != nil {
			return "", fmt.Errorf("ft unlock input %d: %w", i, err)
		}
		tx.Inputs[i].UnlockingScript = unlock
	}

	sigP2PKH := &unlocker.Simple{PrivateKey: privKeyAdmin}
	feeIdx := len(ftutxos)
	feeUnlock, feeErr := sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: uint32(feeIdx)})
	if feeErr != nil {
		return "", feeErr
	}
	tx.Inputs[feeIdx].UnlockingScript = feeUnlock

	tx.LockTime = 0
	return hex.EncodeToString(tx.Bytes()), nil
}

// MergeCoin 对齐 TS stableCoin.mergeCoin，合并多个 coin UTXO。
func (sc *StableCoin) MergeCoin(
	privKey *bec.PrivateKey,
	ftutxos []*bt.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxData []string,
) ([]string, error) {
	if len(ftutxos) <= 1 {
		return nil, nil
	}

	pubKey := privKey.PubKey()
	addr, err := bscript.NewAddressFromPublicKey(pubKey, true)
	if err != nil {
		return nil, err
	}
	addressFrom := addr.AddressString
	isCoin := 1
	var txRaws []string

	for len(ftutxos) > 1 {
		batchSize := len(ftutxos)
		if batchSize > 5 {
			batchSize = 5
		}
		batch := ftutxos[:batchSize]
		batchPreTXs := preTXs[:batchSize]
		batchPrePreData := prepreTxData[:batchSize]

		tapeAmountSet := make([]*big.Int, 0, batchSize)
		tapeAmountSum := new(big.Int)
		lockTimeMax := uint32(0)

		for i, fu := range batch {
			bal := new(big.Int)
			bal.SetString(fu.FtBalance, 10)
			tapeAmountSet = append(tapeAmountSet, bal)
			tapeAmountSum.Add(tapeAmountSum, bal)
			lt := GetLockTimeFromTape(batchPreTXs[i].Outputs[fu.Vout+1].LockingScript)
			if lt > lockTimeMax {
				lockTimeMax = lt
			}
		}

		amtHex, _ := BuildTapeAmount(tapeAmountSum, tapeAmountSet)

		tx := newFTTx()
		for _, fu := range batch {
			if err := tx.From(fu.TxID, fu.Vout, fu.Script, fu.Satoshis); err != nil {
				return nil, err
			}
		}
		if err := tx.From(hex.EncodeToString(feeUTXO.TxID), feeUTXO.Vout, feeUTXO.LockingScript.String(), feeUTXO.Satoshis); err != nil {
			return nil, err
		}

		codeScript := BuildFTtransferCode(sc.CodeScript, addressFrom)
		tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
		tapeScript := BuildFTtransferTape(sc.TapeScript, amtHex)
		tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

		for i := range batch {
			tx.Inputs[i].SequenceNumber = 4294967294
			unlock, err := sc.getFTunlockCoin(privKey, tx, batchPreTXs[i], batchPrePreData[i], i, int(batch[i].Vout), isCoin)
			if err != nil {
				return nil, fmt.Errorf("ft unlock input %d: %w", i, err)
			}
			tx.Inputs[i].UnlockingScript = unlock
		}

		sigP2PKH := &unlocker.Simple{PrivateKey: privKey}
		feeIdx := batchSize
		tx.Inputs[feeIdx].UnlockingScript, err = sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: uint32(feeIdx)})
		if err != nil {
			return nil, err
		}

		tx.LockTime = lockTimeMax
		txRaw := hex.EncodeToString(tx.Bytes())
		txRaws = append(txRaws, txRaw)

		// Use output of this tx for the next round
		mergedUTXO := &bt.FtUTXO{
			TxID:      tx.TxID(),
			Vout:      0,
			Satoshis:  500,
			Script:    codeScript.String(),
			FtBalance: tapeAmountSum.String(),
		}
		ftutxos = append([]*bt.FtUTXO{mergedUTXO}, ftutxos[batchSize:]...)

		// Use output 2 as new fee UTXO from the merge tx
		if len(tx.Outputs) > 2 {
			txIDBytes, _ := hex.DecodeString(tx.TxID())
			feeUTXO = &bt.UTXO{
				TxID:          txIDBytes,
				Vout:          2,
				Satoshis:      tx.Outputs[2].Satoshis,
				LockingScript: tx.Outputs[2].LockingScript,
			}
		}

		preTXs = append([]*bt.Tx{tx}, preTXs[batchSize:]...)
		if len(prepreTxData) > batchSize {
			prepreTxData = prepreTxData[batchSize:]
		} else {
			prepreTxData = nil
		}
	}

	return txRaws, nil
}

// getFTunlockCoin 对齐 JS FT.getFTunlock(..., isCoin)：isCoin 非 0 时在解锁脚本中追加 0x00。
func (sc *StableCoin) getFTunlockCoin(privKey *bec.PrivateKey, tx *bt.Tx, preTX *bt.Tx, prepreTxData string, inputIdx, preTxVout, isCoin int) (*bscript.Script, error) {
	return sc.FT.getFTunlock(privKey, tx, preTX, prepreTxData, inputIdx, preTxVout, isCoin != 0)
}

// --- Static helper functions ---

// GetLockTimeFromTape 从 tape script 中读取 lockTime（对齐 TS stableCoin.getLockTimeFromTape）。
func GetLockTimeFromTape(tapeScript *bscript.Script) uint32 {
	chunks, err := bscript.DecodeParts(tapeScript.Bytes())
	if err != nil || len(chunks) < 2 {
		return 0
	}
	lockTimeChunk := chunks[len(chunks)-2]
	if len(lockTimeChunk) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(lockTimeChunk[:4])
}

// SetLockTimeInTape 设置 tape script 中的 lockTime（对齐 TS stableCoin.setLockTimeInTape）。
func SetLockTimeInTape(tapeScript *bscript.Script, lockTime uint32) *bscript.Script {
	if lockTime != 0 && lockTime < 500000000 {
		return tapeScript
	}
	chunks, err := bscript.DecodeParts(tapeScript.Bytes())
	if err != nil || len(chunks) < 2 {
		return tapeScript
	}
	ltBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(ltBuf, lockTime)
	chunks[len(chunks)-2] = ltBuf
	newBytes, err := bscript.EncodeParts(chunks)
	if err != nil {
		return tapeScript
	}
	result := bscript.NewFromBytes(newBytes)
	return result
}

// GetAddressFromCode 从 code script hex 中解析持有者地址（对齐 TS stableCoin.getAddressFromCode）。
func GetAddressFromCode(codeScriptHex string) string {
	raw, err := hex.DecodeString(codeScriptHex)
	if err != nil || len(raw) < 23 {
		return ""
	}
	chunks, err := bscript.DecodeParts(raw)
	if err != nil || len(chunks) < 2 {
		return ""
	}
	addrChunk := chunks[len(chunks)-2]
	if len(addrChunk) < 21 {
		return ""
	}
	pkhBytes := addrChunk[:20]
	mode := addrChunk[20]
	if mode == 0x00 {
		addr, err := bscript.NewAddressFromPublicKeyHash(pkhBytes, true)
		if err != nil {
			return hex.EncodeToString(pkhBytes)
		}
		return addr.AddressString
	}
	return hex.EncodeToString(pkhBytes)
}

// BuildCoinNftTX 构建 coin NFT 交易（对齐 TS stableCoin.buildCoinNftTX）。
func BuildCoinNftTX(privKey *bec.PrivateKey, utxo *bt.UTXO, data *CoinNftData) (*bt.Tx, error) {
	pubKey := privKey.PubKey()
	addr, err := bscript.NewAddressFromPublicKey(pubKey, true)
	if err != nil {
		return nil, err
	}
	address := addr.AddressString

	utxoTxIDStr := hex.EncodeToString(utxo.TxID)
	nftCodeScript, err := GetCoinNftCode(utxoTxIDStr, utxo.Vout)
	if err != nil {
		return nil, err
	}
	nftHoldScript := GetCoinNftHoldScript(address, data.NftName)
	nftTapeScript := GetCoinNftTapeScript(data)

	outputs, err := BuildCoinNftOutput(nftCodeScript, nftHoldScript, nftTapeScript)
	if err != nil {
		return nil, err
	}

	tx := newFTTx()
	if err := tx.From(utxoTxIDStr, utxo.Vout, utxo.LockingScript.String(), utxo.Satoshis); err != nil {
		return nil, err
	}
	for _, out := range outputs {
		tx.AddOutput(out)
	}

	changeScript, _ := bscript.NewP2PKHFromAddress(address)
	tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: 0})

	// Estimate fee and set change（与 CreateCoin 首铸一致：用 NFT/FT 环境费率，避免 testnet 上硬编码 80 过低）
	satPerKB := nftFeeSatPerKBFromEnv()
	if satPerKB < 1 {
		satPerKB = 80
	}
	estimatedSize := tx.Size() + 107
	fee := uint64(estimatedSize*satPerKB) / 1000
	minFee := uint64(satPerKB)
	if fee < minFee {
		fee = minFee
	}
	totalOut := uint64(0)
	for _, o := range tx.Outputs {
		totalOut += o.Satoshis
	}
	if utxo.Satoshis > totalOut+fee {
		tx.Outputs[len(tx.Outputs)-1].Satoshis = utxo.Satoshis - totalOut - fee + tx.Outputs[len(tx.Outputs)-1].Satoshis
	}

	sigP2PKH := &unlocker.Simple{PrivateKey: privKey}
	tx.Inputs[0].UnlockingScript, err = sigP2PKH.UnlockingScript(context.Background(), tx, bt.UnlockerParams{InputIdx: 0})
	if err != nil {
		return nil, err
	}

	return tx, nil
}

// BuildCoinNftOutput 构建 coin NFT 的三个输出（对齐 TS stableCoin.buildCoinNftOutput）。
func BuildCoinNftOutput(
	nftCodeScript, nftHoldScript, nftTapeScript *bscript.Script,
) ([]*bt.Output, error) {
	return []*bt.Output{
		{LockingScript: nftCodeScript, Satoshis: 200},
		{LockingScript: nftHoldScript, Satoshis: 100},
		{LockingScript: nftTapeScript, Satoshis: 0},
	}, nil
}

// GetCoinNftCode 构建 coin NFT 的 code script（对齐 TS coinNft.getCoinNftCode）。
func GetCoinNftCode(txHash string, outputIndex uint32) (*bscript.Script, error) {
	// Reverse txid to internal byte order
	txIDBytes, err := hex.DecodeString(txHash)
	if err != nil {
		return nil, err
	}
	reversed := make([]byte, 32)
	for i := 0; i < 32; i++ {
		reversed[i] = txIDBytes[31-i]
	}
	voutBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(voutBuf, outputIndex)
	txIDVout := hex.EncodeToString(reversed) + hex.EncodeToString(voutBuf)

	asmStr := "OP_1 OP_PICK OP_3 OP_SPLIT 0x01 0x14 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_OVER OP_TOALTSTACK OP_TOALTSTACK OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_OVER 0x01 0x24 OP_SPLIT OP_DROP OP_TOALTSTACK OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_HASH256 OP_6 OP_PUSH_META 0x01 0x20 OP_SPLIT OP_DROP OP_EQUALVERIFY OP_OVER OP_TOALTSTACK OP_TOALTSTACK OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP 0x01 0x20 OP_SPLIT OP_DROP OP_3 OP_ROLL OP_EQUALVERIFY OP_SWAP OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUAL OP_IF OP_DROP OP_ELSE 0x24 0x" + txIDVout + " OP_EQUALVERIFY OP_ENDIF OP_OVER OP_FROMALTSTACK OP_EQUALVERIFY OP_CAT OP_CAT OP_SHA256 OP_7 OP_PUSH_META OP_EQUALVERIFY OP_DUP OP_HASH160 OP_FROMALTSTACK OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x05 0x33436f6465"
	// 与 NFT.parseNFTCodeASM / getFTmintCode 一致：先 collapse 0xNN 0x<data> 再 strip，否则 0x01 0x14 会变成独立 token「01」「14」，
	// NewFromASM 按 hex 推成非最小 push，链上 mint 输入 0 会报 Data push larger than necessary。
	asmStr = collapseTbcMintASM(asmStr)
	asmStr = strip0xHexPushesInASM(asmStr)
	return bscript.NewFromASM(asmStr)
}

// GetCoinNftHoldScript 构建 coin NFT 的 hold script（对齐 TS coinNft.getHoldScript）。
func GetCoinNftHoldScript(address, flag string) *bscript.Script {
	p2pkh, _ := bscript.NewP2PKHFromAddress(address)
	flagHex := hex.EncodeToString([]byte(fmt.Sprintf("For Coin %s NHold", flag)))
	p2pkhASM, _ := p2pkh.ToASM()
	asmStr := fmt.Sprintf("%s OP_RETURN %s", p2pkhASM, flagHex)
	script, _ := bscript.NewFromASM(asmStr)
	return script
}

// GetCoinNftTapeScript 构建 coin NFT 的 tape script（对齐 TS coinNft.getTapeScript）。
func GetCoinNftTapeScript(data *CoinNftData) *bscript.Script {
	dataBytes, _ := json.Marshal(data)
	dataHex := hex.EncodeToString(dataBytes)
	asmStr := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", dataHex)
	script, _ := bscript.NewFromASM(asmStr)
	return script
}

// UpdateCoinNftTapeScript 更新 coin NFT tape 中的 totalSupply（对齐 TS coinNft.updateTapeScript）。
func UpdateCoinNftTapeScript(tapeScript *bscript.Script, newTotalSupply string) *bscript.Script {
	chunks, err := bscript.DecodeParts(tapeScript.Bytes())
	if err != nil || len(chunks) < 2 {
		return tapeScript
	}
	dataChunk := chunks[len(chunks)-2]
	var jsonData map[string]interface{}
	if err := json.Unmarshal(dataChunk, &jsonData); err != nil {
		return tapeScript
	}
	jsonData["coinTotalSupply"] = newTotalSupply
	newData, _ := json.Marshal(jsonData)
	dataHex := hex.EncodeToString(newData)
	asmStr := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", dataHex)
	script, _ := bscript.NewFromASM(asmStr)
	return script
}

// GetCoinMintCode 对齐 stableCoin.getCoinMintCode（admin / 接收方 / 原 code sha256 / tape 字节长）。
func GetCoinMintCode(adminAddress, receiveAddress, codeHash string, tapeSize int) (*bscript.Script, error) {
	if tapeSize <= 0 {
		return nil, fmt.Errorf("invalid tapeSize")
	}
	admin, err := bscript.NewAddressFromString(adminAddress)
	if err != nil {
		return nil, err
	}
	recv, err := bscript.NewAddressFromString(receiveAddress)
	if err != nil {
		return nil, err
	}
	hash := recv.PublicKeyHash + "00"
	tapeSizeHex := bt.GetSizeHex(tapeSize)
	asm := stablecoinMintTemplateASM
	asm = strings.ReplaceAll(asm, "${adminPubHash}", admin.PublicKeyHash)
	asm = strings.ReplaceAll(asm, "${codeHash}", codeHash)
	asm = strings.ReplaceAll(asm, "${tapeSizeHex}", tapeSizeHex)
	asm = strings.ReplaceAll(asm, "${hash}", hash)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// --- Utility helpers ---

// stableCoinTapeDecimalASM 与 tbc-contract stableCoin.js 一致：
// const decimalHex = decimal.toString(16).padStart(2, "0");
// 即 ASM 中为两位十六进制字面量（如 06），而非 OP_6，避免与 JS 构建的 tape 字节长/tapeSize 不一致导致 mint 合约 OP_SPLIT 等失败。
func stableCoinTapeDecimalASM(decimal int) string {
	if decimal < 0 {
		return "00"
	}
	return fmt.Sprintf("%02x", decimal)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func bigIntToUint64LEHex(n *big.Int) string {
	buf := make([]byte, 8)
	val := n.Uint64()
	binary.LittleEndian.PutUint64(buf, val)
	return hex.EncodeToString(buf)
}

// ParseDecimalToBigInt converts a decimal string like "1.5" with given decimal places into a big.Int.
func ParseDecimalToBigInt(amount string, decimal int) *big.Int {
	parts := strings.SplitN(amount, ".", 2)
	intPart := parts[0]
	fracPart := ""
	if len(parts) > 1 {
		fracPart = parts[1]
	}
	if len(fracPart) > decimal {
		fracPart = fracPart[:decimal]
	}
	for len(fracPart) < decimal {
		fracPart += "0"
	}
	combined := intPart + fracPart
	result := new(big.Int)
	result.SetString(combined, 10)
	return result
}
