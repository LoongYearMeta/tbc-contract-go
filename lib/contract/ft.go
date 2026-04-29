package contract

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

//go:embed ft_mint_template.asm
var ftMintTemplateASM string

const ftSatPerKB = 80

// FtParams holds parameters for creating a new FT.
type FtParams struct {
	Name    string
	Symbol  string
	Amount  int64
	Decimal int
}

// FtInfo holds FT metadata, used by Initialize.
type FtInfo struct {
	ContractTxid string
	Name         string
	Symbol       string
	Decimal      int
	TotalSupply  *big.Int
	CodeScript   string
	TapeScript   string
}

// FT represents a Fungible Token contract.
// Mirrors tbc-contract/lib/contract/ft.ts class FT.
type FT struct {
	Name         string
	Symbol       string
	Decimal      int
	TotalSupply  *big.Int
	CodeScript   string
	TapeScript   string
	ContractTxid string
}

// NewFT creates an FT instance from either a txid string or *FtParams.
// Mirrors TS constructor(txidOrParams).
func NewFT(txidOrParams interface{}) (*FT, error) {
	ft := &FT{}
	switch v := txidOrParams.(type) {
	case string:
		ft.ContractTxid = v
	case *FtParams:
		if v.Amount <= 0 {
			return nil, fmt.Errorf("amount must be a natural number")
		}
		if v.Decimal <= 0 || v.Decimal > 18 {
			return nil, fmt.Errorf("decimal must be a positive integer not exceeding 18")
		}
		maxAmount := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(18-v.Decimal)), nil)
		if big.NewInt(v.Amount).Cmp(maxAmount) > 0 {
			return nil, fmt.Errorf("when decimal is %d, the maximum amount cannot exceed %s", v.Decimal, maxAmount)
		}
		ft.Name = v.Name
		ft.Symbol = v.Symbol
		ft.Decimal = v.Decimal
		ft.TotalSupply = big.NewInt(v.Amount)
	default:
		return nil, fmt.Errorf("invalid constructor arguments")
	}
	return ft, nil
}

// Initialize populates the FT from FtInfo.
// Mirrors TS FT.initialize(ftInfo).
func (f *FT) Initialize(info *FtInfo) {
	f.Name = info.Name
	f.Symbol = info.Symbol
	f.Decimal = info.Decimal
	if info.TotalSupply != nil {
		f.TotalSupply = new(big.Int).Set(info.TotalSupply)
	}
	f.CodeScript = info.CodeScript
	f.TapeScript = info.TapeScript
	if info.ContractTxid != "" {
		f.ContractTxid = info.ContractTxid
	}
}

// MintFT mints new FT tokens and returns [txSourceRaw, txMintRaw].
// Mirrors TS FT.MintFT(privateKey_from, address_to, utxo).
func (f *FT) MintFT(privKey *bec.PrivateKey, addressTo string, utxo *bt.UTXO) ([]string, error) {
	totalSupply := new(big.Int).Mul(
		f.TotalSupply,
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(f.Decimal)), nil),
	)
	tapeAmount := writeTapeAmount(totalSupply)

	nameHex := hex.EncodeToString([]byte(f.Name))
	symbolHex := hex.EncodeToString([]byte(f.Symbol))
	decimalHex := fmt.Sprintf("%02x", f.Decimal)

	tapeASM := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s 4654617065", tapeAmount, decimalHex, nameHex, symbolHex)
	tapeScript, err := bscript.NewFromASM(tapeASM)
	if err != nil {
		return nil, fmt.Errorf("build tape ASM: %w", err)
	}
	tapeSize := len(tapeScript.Bytes())

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	pubKeyHash := addr.PublicKeyHash
	flagHex := hex.EncodeToString([]byte("for ft mint"))

	sourceOutputScript, err := bscript.NewFromASM(fmt.Sprintf(
		"OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s", pubKeyHash, flagHex))
	if err != nil {
		return nil, fmt.Errorf("build source output script: %w", err)
	}

	txSource := newFTTx()
	if err := txSource.FromUTXOs(utxo); err != nil {
		return nil, err
	}
	txSource.AddOutput(&bt.Output{LockingScript: sourceOutputScript, Satoshis: 9900})
	txSource.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	// Mirror TS: if txSize < 1000 → fee = 80 sat; else feePerKb=80
	estSource := txSource.JSEstimateSize()
	targetSourceFee := mintSourceTargetFeeSat(estSource)
	changeScript, err := bscript.NewP2PKHFromAddress(addr.AddressString)
	if err != nil {
		return nil, err
	}
	inSat := txSource.TotalInputSatoshis()
	outSat := txSource.TotalOutputSatoshis()
	initialChange := int64(inSat) - int64(outSat) - int64(targetSourceFee)
	if initialChange < 0 {
		initialChange = 0
	}
	txSource.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: uint64(initialChange)})

	ctx := context.Background()
	if err := txSource.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return nil, err
	}
	txSourceRaw := hex.EncodeToString(txSource.Bytes())

	sourceTxID := txSource.TxID()
	codeScript, err := getFTmintCode(sourceTxID, 0, addressTo, tapeSize)
	if err != nil {
		return nil, err
	}
	f.CodeScript = hex.EncodeToString(codeScript.Bytes())
	f.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	txMint := newFTTx()
	if err := txMint.From(sourceTxID, 0, sourceOutputScript.String(), 9900); err != nil {
		return nil, err
	}
	txMint.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	txMint.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})
	if err := txMint.ChangeToAddress(addr.AddressString, newFeeQuote80()); err != nil {
		// fallback: manual
		mintInSat := txMint.TotalInputSatoshis()
		mintOutSat := txMint.TotalOutputSatoshis()
		mintFee := mintSourceTargetFeeSat(txMint.JSEstimateSize())
		mintChange := int64(mintInSat) - int64(mintOutSat) - int64(mintFee)
		if mintChange < 0 {
			mintChange = 0
		}
		txMint.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: uint64(mintChange)})
	}

	if err := signP2PKHInput(txMint, privKey, 0); err != nil {
		return nil, err
	}

	f.ContractTxid = txMint.TxID()
	return []string{txSourceRaw, hex.EncodeToString(txMint.Bytes())}, nil
}

// Transfer transfers FT tokens to addressTo.
// amount is the raw scaled token amount (use util.ParseDecimalToBigInt to convert human amounts).
// Mirrors TS FT.transfer(privateKey_from, address_to, ft_amount, ftutxo_a, utxo, preTX, prepreTxData, tbc_amount?).
func (f *FT) Transfer(
	privKey *bec.PrivateKey,
	addressTo string,
	amount *big.Int,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	tbcAmountSat uint64,
) (string, error) {
	if amount == nil || amount.Sign() < 0 {
		return "", fmt.Errorf("invalid amount input")
	}
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return "", err
	}
	addressFrom := addr.AddressString

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		tapeAmountSet[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
	}
	if amount.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("insufficient balance, please add more FT UTXOs")
	}
	if f.Decimal > 18 {
		return "", fmt.Errorf("decimal cannot exceed 18")
	}

	amountHex, changeHex := BuildTapeAmount(amount, tapeAmountSet)

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(feeUTXO); err != nil {
		return "", err
	}

	codeScript, err := BuildFTtransferCode(f.CodeScript, addressTo)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if tbcAmountSat > 0 {
		tx.To(addressTo, tbcAmountSat)
	}
	if amount.Cmp(tapeAmountSum) < 0 {
		changeCode, err := BuildFTtransferCode(f.CodeScript, addressFrom)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
		changeTape := BuildFTtransferTape(f.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", fmt.Errorf("ChangeToAddress: %w", err)
	}

	nFt := len(ftutxos)
	if err := ftSignFeeInputs(tx, privKey, nFt); err != nil {
		return "", err
	}
	ftUnlocks, err := f.buildFTUnlocks(privKey, tx, preTXs, prepreTxDatas, ftutxos)
	if err != nil {
		return "", err
	}
	if err := ftInsertUnlocks(tx, ftUnlocks, nFt); err != nil {
		return "", err
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// AddressAmount is a receiver address + raw token amount pair for BatchTransfer.
type AddressAmount struct {
	Address string
	Amount  *big.Int
}

// BatchTransfer batch-transfers FT to multiple recipients, up to 5 per chained tx.
// Mirrors TS FT.batchTransfer(privateKey_from, receivers, ftutxo, utxo, preTX, prepreTxData).
func (f *FT) BatchTransfer(
	privKey *bec.PrivateKey,
	receivers []AddressAmount,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) ([]string, error) {
	if len(receivers) == 0 {
		return nil, fmt.Errorf("no receivers specified")
	}

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	addressFrom := addr.AddressString

	var txsraw []string
	ftutxoBalance := new(big.Int)
	for _, fu := range ftutxos {
		ftutxoBalance.Add(ftutxoBalance, fu.FtBalance)
	}

	// Group into batches of 5 (mirrors TS batchTransfer)
	var batches [][]AddressAmount
	for i := 0; i < len(receivers); i += 5 {
		end := i + 5
		if end > len(receivers) {
			end = len(receivers)
		}
		batches = append(batches, receivers[i:end])
	}

	currentPreTXs := preTXs
	currentPrepreTxDatas := prepreTxDatas
	currentFtutxos := ftutxos
	currentFeeUTXO := feeUTXO
	balance := new(big.Int).Set(ftutxoBalance)
	var prevBatchSize int

	for b, batch := range batches {
		receiverAmounts := make([]*big.Int, len(batch))
		totalBatchAmount := new(big.Int)
		for i, r := range batch {
			if r.Amount == nil || r.Amount.Sign() <= 0 {
				return nil, fmt.Errorf("invalid amount for address %s", r.Address)
			}
			receiverAmounts[i] = r.Amount
			totalBatchAmount.Add(totalBatchAmount, r.Amount)
		}

		tapeAmountSetIn := make([]*big.Int, 0)
		if b == 0 {
			for _, fu := range currentFtutxos {
				tapeAmountSetIn = append(tapeAmountSetIn, new(big.Int).Set(fu.FtBalance))
			}
		} else {
			tapeAmountSetIn = append(tapeAmountSetIn, new(big.Int).Set(balance))
		}

		tapeHexes, err := buildMultiTapeAmountsRaw(receiverAmounts, tapeAmountSetIn)
		if err != nil {
			return nil, err
		}

		ftChangeIndex := prevBatchSize * 2
		tbcChangeIndex := prevBatchSize*2 + 2

		tx := newFTTx()
		var nFt int
		if b == 0 {
			if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(currentFtutxos)...); err != nil {
				return nil, err
			}
			if err := tx.FromUTXOs(currentFeeUTXO); err != nil {
				return nil, err
			}
			nFt = len(currentFtutxos)
		} else {
			prevTx := currentPreTXs[0]
			if err := addInputFromPrevTxOutput(tx, prevTx, ftChangeIndex); err != nil {
				return nil, fmt.Errorf("add FT input from prev tx vout %d: %w", ftChangeIndex, err)
			}
			if err := addInputFromPrevTxOutput(tx, prevTx, tbcChangeIndex); err != nil {
				return nil, fmt.Errorf("add fee input from prev tx vout %d: %w", tbcChangeIndex, err)
			}
			nFt = 1
		}

		for i, r := range batch {
			cs, err := BuildFTtransferCode(f.CodeScript, r.Address)
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: cs, Satoshis: 500})
			ts := BuildFTtransferTape(f.TapeScript, tapeHexes[i])
			tx.AddOutput(&bt.Output{LockingScript: ts, Satoshis: 0})
		}
		if totalBatchAmount.Cmp(balance) < 0 {
			changeCode, err := BuildFTtransferCode(f.CodeScript, addressFrom)
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
			changeTape := BuildFTtransferTape(f.TapeScript, tapeHexes[len(batch)])
			tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
		}

		if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
			return nil, fmt.Errorf("batch ChangeToAddress: %w", err)
		}

		if err := ftSignFeeInputs(tx, privKey, nFt); err != nil {
			return nil, err
		}
		var ftUnlocks []*bscript.Script
		if b == 0 {
			ftUnlocks, err = f.buildFTUnlocks(privKey, tx, currentPreTXs, currentPrepreTxDatas, currentFtutxos)
		} else {
			ftUnlocks, err = f.buildFTUnlocksFromPrevVouts(privKey, tx, currentPreTXs, currentPrepreTxDatas, []int{ftChangeIndex})
		}
		if err != nil {
			return nil, err
		}
		if err := ftInsertUnlocks(tx, ftUnlocks, nFt); err != nil {
			return nil, err
		}

		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))

		// Rebuild prepreTxData for next batch
		if b == 0 {
			var prepretxdata string
			for j := 0; j < len(currentPreTXs); j++ {
				d, err := util.GetPrePreTxdata(currentPreTXs[j], int(tx.Inputs[j].PreviousTxOutIndex))
				if err != nil {
					return nil, err
				}
				prepretxdata = d + prepretxdata
			}
			currentPrepreTxDatas = []string{"57" + prepretxdata}
		} else {
			d, err := util.GetPrePreTxdata(currentPreTXs[0], int(tx.Inputs[0].PreviousTxOutIndex))
			if err != nil {
				return nil, err
			}
			currentPrepreTxDatas = []string{"57" + d}
		}
		currentPreTXs = []*bt.Tx{tx}
		prevBatchSize = len(batch)
		balance.Sub(balance, totalBatchAmount)
	}
	return txsraw, nil
}

// MergeFT merges FT UTXOs recursively until one remains.
// Mirrors TS FT.mergeFT(privateKey_from, ftutxo, utxo, preTX, prepreTxData, localTX).
func (f *FT) MergeFT(
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	localTXs []*bt.Tx,
) ([]string, error) {
	preTXsCopy := make([]*bt.Tx, len(preTXs))
	copy(preTXsCopy, preTXs)

	const maxBatch = 5
	endIdx := maxBatch
	if endIdx > len(ftutxos) {
		endIdx = len(ftutxos)
	}
	currentFtutxos := ftutxos[:endIdx]
	currentPreTXs := preTXs[:endIdx]
	currentPrepreTxDatas := prepreTxDatas[:endIdx]

	var txsraw []string

	for iteration := 0; len(currentFtutxos) > 1; iteration++ {
		var tx *bt.Tx
		var mergeErr error
		if iteration == 0 {
			tx, mergeErr = f.mergeFTSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, feeUTXO)
		} else {
			tx, mergeErr = f.mergeFTSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, nil)
		}
		if mergeErr != nil {
			return nil, mergeErr
		}
		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))

		idx := (iteration + 1) * maxBatch
		endIdx = idx + maxBatch
		if endIdx > len(ftutxos) {
			endIdx = len(ftutxos)
		}

		currentPreTXs = make([]*bt.Tx, 0)
		if idx < len(preTXs) {
			end := endIdx
			if end > len(preTXs) {
				end = len(preTXs)
			}
			currentPreTXs = append(currentPreTXs, preTXs[idx:end]...)
		}
		currentPreTXs = append(currentPreTXs, tx)

		if idx < len(prepreTxDatas) {
			end := endIdx
			if end > len(prepreTxDatas) {
				end = len(prepreTxDatas)
			}
			currentPrepreTxDatas = prepreTxDatas[idx:end]
		} else {
			currentPrepreTxDatas = nil
		}

		if idx < len(ftutxos) {
			currentFtutxos = ftutxos[idx:endIdx]
		} else {
			currentFtutxos = nil
		}
	}

	if len(txsraw) <= 1 && len(currentFtutxos) < 1 {
		return txsraw, nil
	}

	// Recursive phase: rebuild from prior merge results
	utxoTX := currentPreTXs[len(currentPreTXs)-1]
	nonEmpty := len(currentPreTXs) - 1
	newFeeUTXO, err := buildUTXOFromTx(utxoTX, 2)
	if err != nil {
		return nil, err
	}

	newFtutxos := make([]*util.FtUTXO, 0)
	if currentFtutxos != nil {
		newFtutxos = append(newFtutxos, currentFtutxos...)
	}
	newPreTXs := append([]*bt.Tx(nil), currentPreTXs[:nonEmpty]...)

	for _, rawHex := range txsraw {
		txBytes, _ := hex.DecodeString(rawHex)
		tx, err := bt.NewTxFromBytes(txBytes)
		if err != nil {
			return nil, err
		}
		newPreTXs = append(newPreTXs, tx)
		ftBal, err := util.GetFtBalanceFromTape(hex.EncodeToString(tx.Outputs[1].LockingScript.Bytes()))
		if err != nil {
			return nil, err
		}
		txidBytes, _ := hex.DecodeString(tx.TxID())
		newFtutxos = append(newFtutxos, &util.FtUTXO{
			TxID:          txidBytes,
			Vout:          0,
			LockingScript: tx.Outputs[0].LockingScript,
			Satoshis:      tx.Outputs[0].Satoshis,
			FtBalance:     ftBal,
		})
	}

	if len(localTXs) == 0 {
		localTXs = preTXsCopy
	}

	newPrepreTxDatas := make([]string, 0)
	for i := nonEmpty; i < len(newPreTXs); i++ {
		ppd, err := util.BuildFtPrePreTxData(newPreTXs[i], 0, localTXs)
		if err != nil {
			return nil, fmt.Errorf("BuildFtPrePreTxData merge: %w", err)
		}
		newPrepreTxDatas = append(newPrepreTxDatas, ppd)
	}
	localTXs = newPreTXs

	recursiveResults, err := f.MergeFT(privKey, newFtutxos, newFeeUTXO, newPreTXs, newPrepreTxDatas, localTXs)
	if err != nil {
		return nil, err
	}
	return append(txsraw, recursiveResults...), nil
}

// mergeFTSingle merges up to 5 FT UTXOs into one. Mirrors TS FT._mergeFT.
func (f *FT) mergeFTSingle(
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	feeUTXO *bt.UTXO,
) (*bt.Tx, error) {
	if len(ftutxos) == 0 {
		return nil, fmt.Errorf("no FT UTXOs available for merge")
	}
	if len(ftutxos) == 1 {
		return nil, fmt.Errorf("single UTXO does not need merge")
	}

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	addressFrom := addr.AddressString

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		tapeAmountSet[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSet)
	if changeHex != strings.Repeat("0", 96) {
		return nil, fmt.Errorf("change amount is not zero during merge")
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return nil, err
	}
	if feeUTXO != nil {
		if err := tx.FromUTXOs(feeUTXO); err != nil {
			return nil, err
		}
	} else {
		if err := addInputFromPrevTxOutput(tx, preTXs[len(preTXs)-1], 2); err != nil {
			return nil, err
		}
	}

	codeScript, err := BuildFTtransferCode(f.CodeScript, addressFrom)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("merge ChangeToAddress: %w", err)
	}

	nFt := len(ftutxos)
	if nFt > 5 {
		nFt = 5
	}

	if err := ftSignFeeInputs(tx, privKey, nFt); err != nil {
		return nil, err
	}
	ftUnlocks, err := f.buildFTUnlocks(privKey, tx, preTXs, prepreTxDatas, ftutxos)
	if err != nil {
		return nil, err
	}
	if err := ftInsertUnlocks(tx, ftUnlocks, nFt); err != nil {
		return nil, err
	}
	return tx, nil
}

// GetFTunlock generates the FT unlock script for a given tx input.
// Exported for use by pool contracts.
// isCoin=true appends "00" between pubkey and pretxdata (stablecoin transfer).
func (f *FT) GetFTunlock(privKey *bec.PrivateKey, tx *bt.Tx, preTX *bt.Tx, prepreTxData string, inputIdx, preTxVout int, isCoin bool) (*bscript.Script, error) {
	return ftBuildUnlock(privKey, tx, preTX, prepreTxData, inputIdx, preTxVout, isCoin)
}

// GetFTunlockSwap generates the FT unlock script for a swap input (pool contracts).
// ftVersion: 1 or 2. isCoin: for stablecoin path.
func (f *FT) GetFTunlockSwap(privKey *bec.PrivateKey, currentTX *bt.Tx, preTX *bt.Tx, prepreTxData string, contractTX *bt.Tx, currentUnlockIndex, preTxVout, ftVersion int, isCoin bool) (*bscript.Script, error) {
	return ftBuildUnlockSwap(privKey, currentTX, preTX, prepreTxData, contractTX, currentUnlockIndex, preTxVout, ftVersion, isCoin)
}

// StaticGetFTunlock is the static variant of getFTunlock.
// sigs and pubKey are hex-encoded serialised bytes.
// Mirrors TS static FT.getFTunlock(sigs, pubKey, currentTX, preTX, prepreTxData, ...).
func StaticGetFTunlock(sigs, pubKey string, currentTX *bt.Tx, preTX *bt.Tx, prepreTxData string, currentUnlockIndex, preTxVout int, isCoin bool) (*bscript.Script, error) {
	pretxdata, err := util.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := util.GetCurrentTxdata(currentTX, currentUnlockIndex)
	if err != nil {
		return nil, err
	}
	sigBytes, err := hex.DecodeString(sigs)
	if err != nil {
		return nil, err
	}
	pubKeyBytes, err := hex.DecodeString(pubKey)
	if err != nil {
		return nil, err
	}
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKeyBytes), hex.EncodeToString(pubKeyBytes))
	coinFlag := ""
	if isCoin {
		coinFlag = "00"
	}
	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + coinFlag + pretxdata
	return bscript.NewFromHexString(unlockHex)
}

// StaticGetFTunlockSwap is the static variant for swap inputs.
// Mirrors TS static FT.getFTunlockSwap(sigs, pubKey, ...).
func StaticGetFTunlockSwap(sigs, pubKey string, currentTX *bt.Tx, preTX *bt.Tx, prepreTxData string, contractTX *bt.Tx, currentUnlockIndex, preTxVout, ftVersion int, isCoin bool) (*bscript.Script, error) {
	pretxdata, err := util.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	var contracttxdata string
	if ftVersion == 2 {
		contracttxdata, err = util.GetContractTxdata(contractTX, -1)
	} else {
		if len(currentTX.Inputs) == 0 {
			return nil, fmt.Errorf("no inputs in current tx")
		}
		contracttxdata, err = util.GetContractTxdata(contractTX, int(currentTX.Inputs[0].PreviousTxOutIndex))
	}
	if err != nil {
		return nil, err
	}
	currentinputsdata, err := util.GetCurrentInputsdata(currentTX)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := util.GetCurrentTxdata(currentTX, currentUnlockIndex)
	if err != nil {
		return nil, err
	}
	sigBytes, err := hex.DecodeString(sigs)
	if err != nil {
		return nil, err
	}
	pubKeyBytes, err := hex.DecodeString(pubKey)
	if err != nil {
		return nil, err
	}
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKeyBytes), hex.EncodeToString(pubKeyBytes))
	coinFlag := ""
	if isCoin {
		coinFlag = "51"
	}
	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + currentinputsdata + contracttxdata + coinFlag + pretxdata
	return bscript.NewFromHexString(unlockHex)
}

// P2PKHOutputTBC describes a single TBC P2PKH output for multi-send.
type P2PKHOutputTBC struct {
	Address string
	TBC     float64
}

// P2PKHToP2PKHSendTBC sends TBC from one P2PKH address to another.
// Mirrors TS P2PKHToP2PKHSendTBC (defined in ft.ts).
func P2PKHToP2PKHSendTBC(addressFrom, addressTo string, tbcAmount float64, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	addrTo, err := bscript.NewAddressFromString(addressTo)
	if err != nil {
		return "", fmt.Errorf("invalid addressTo: %w", err)
	}
	ls, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addrTo.PublicKeyHash))
	if err != nil {
		return "", err
	}
	amtSat := uint64(math.Round(tbcAmount * 1e6))
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amtSat})
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", fmt.Errorf("ChangeToAddress: %w", err)
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// P2PKHToManyP2PKHSendTBC sends TBC from one address to multiple recipients in a single tx.
// Mirrors TS P2PKHToManyP2PKHSendTBC (defined in ft.ts).
func P2PKHToManyP2PKHSendTBC(addressFrom string, outputs []P2PKHOutputTBC, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	if len(outputs) == 0 {
		return "", fmt.Errorf("no outputs specified")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	for _, o := range outputs {
		addrTo, err := bscript.NewAddressFromString(o.Address)
		if err != nil {
			return "", fmt.Errorf("invalid address %s: %w", o.Address, err)
		}
		ls, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addrTo.PublicKeyHash))
		if err != nil {
			return "", err
		}
		amtSat := uint64(math.Round(o.TBC * 1e6))
		tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amtSat})
	}
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", fmt.Errorf("ChangeToAddress: %w", err)
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BuildTapeAmount mirrors TS FT.buildTapeAmount(amountBN, tapeAmountSet).
func BuildTapeAmount(amountBN *big.Int, tapeAmountSet []*big.Int) (amountHex, changeHex string) {
	return BuildTapeAmountWithFtInputIndex(amountBN, tapeAmountSet, 0)
}

// BuildTapeAmountWithFtInputIndex mirrors TS FT.buildTapeAmount(amountBN, tapeAmountSet, ftInputIndex).
func BuildTapeAmountWithFtInputIndex(amountBN *big.Int, tapeAmountSet []*big.Int, ftInputIndex int) (amountHex, changeHex string) {
	aw := &bytes.Buffer{}
	cw := &bytes.Buffer{}
	writeU64 := func(w *bytes.Buffer, v *big.Int) {
		u := uint64(0)
		if v != nil && v.Sign() > 0 {
			u = v.Uint64()
		}
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, u)
		w.Write(b)
	}
	j := 0
	for j < ftInputIndex {
		writeU64(aw, big.NewInt(0))
		writeU64(cw, big.NewInt(0))
		j++
	}
	remain := new(big.Int).Set(amountBN)
	i := 0
	for i = 0; i < 6; i++ {
		if remain.Sign() <= 0 {
			break
		}
		var slot *big.Int
		if i < len(tapeAmountSet) {
			slot = tapeAmountSet[i]
		}
		if slot == nil {
			slot = big.NewInt(0)
		}
		if slot.Cmp(remain) < 0 {
			writeU64(aw, slot)
			writeU64(cw, big.NewInt(0))
			remain.Sub(remain, slot)
		} else {
			writeU64(aw, remain)
			writeU64(cw, new(big.Int).Sub(slot, remain))
			remain = big.NewInt(0)
		}
	}
	for j += i; i < 6 && j < 6; i, j = i+1, j+1 {
		var slot *big.Int
		if i < len(tapeAmountSet) {
			slot = tapeAmountSet[i]
		}
		if slot != nil && slot.Sign() != 0 {
			writeU64(aw, big.NewInt(0))
			writeU64(cw, slot)
		} else {
			writeU64(aw, big.NewInt(0))
			writeU64(cw, big.NewInt(0))
		}
	}
	return hex.EncodeToString(aw.Bytes()), hex.EncodeToString(cw.Bytes())
}

// BuildMultiTapeAmounts builds tape hex strings for multiple recipients plus a change tape.
// Returns len(outputAmounts)+1 strings: one per recipient followed by the change hex.
// Mirrors TS FT.buildMultiTapeAmounts(outputAmounts, tapeAmountSetIn).
func BuildMultiTapeAmounts(outputAmounts []*big.Int, tapeAmountSetIn []*big.Int) ([]string, error) {
	return buildMultiTapeAmountsRaw(outputAmounts, tapeAmountSetIn)
}

// buildMultiTapeAmountsRaw is the internal implementation of BuildMultiTapeAmounts.
func buildMultiTapeAmountsRaw(outputAmounts []*big.Int, tapeAmountSetIn []*big.Int) ([]string, error) {
	remaining := make([]*big.Int, 6)
	for i := 0; i < 6; i++ {
		if i < len(tapeAmountSetIn) && tapeAmountSetIn[i] != nil {
			remaining[i] = new(big.Int).Set(tapeAmountSetIn[i])
		} else {
			remaining[i] = big.NewInt(0)
		}
	}
	writeU64 := func(buf *bytes.Buffer, v *big.Int) {
		u := uint64(0)
		if v != nil && v.Sign() > 0 {
			u = v.Uint64()
		}
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, u)
		buf.Write(b)
	}
	tapeHexes := make([]string, 0, len(outputAmounts)+1)
	for _, needed := range outputAmounts {
		w := &bytes.Buffer{}
		n := new(big.Int).Set(needed)
		for i := 0; i < 6; i++ {
			if n.Sign() <= 0 {
				writeU64(w, big.NewInt(0))
			} else if remaining[i].Cmp(n) <= 0 {
				writeU64(w, remaining[i])
				n.Sub(n, remaining[i])
				remaining[i] = big.NewInt(0)
			} else {
				writeU64(w, n)
				remaining[i].Sub(remaining[i], n)
				n = big.NewInt(0)
			}
		}
		tapeHexes = append(tapeHexes, hex.EncodeToString(w.Bytes()))
	}
	// Change tape
	cw := &bytes.Buffer{}
	for i := 0; i < 6; i++ {
		writeU64(cw, remaining[i])
	}
	tapeHexes = append(tapeHexes, hex.EncodeToString(cw.Bytes()))
	return tapeHexes, nil
}

// BuildFTtransferCode builds a transfer code script for the recipient.
// Mirrors TS FT.buildFTtransferCode(code, addressOrHash).
func BuildFTtransferCode(codeHex, addressOrHash string) (*bscript.Script, error) {
	codeHex = strings.TrimSpace(codeHex)
	codeBuf, err := hex.DecodeString(codeHex)
	if err != nil || len(codeBuf) == 0 {
		return nil, fmt.Errorf("BuildFTtransferCode: invalid code hex")
	}
	var hashBuf []byte
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, _ := bscript.NewAddressFromString(addressOrHash)
		pkhBytes := hexDecode(addr.PublicKeyHash)
		hashBuf = make([]byte, 21)
		copy(hashBuf[:20], pkhBytes)
		hashBuf[20] = 0x00
	} else {
		if len(addressOrHash) != 40 {
			return nil, fmt.Errorf("BuildFTtransferCode: invalid address or hash length (expected 40 hex chars)")
		}
		hashBuf = hexDecode(addressOrHash + "01")
	}
	// Mirrors TS: replace chunks[len-2].buf with the new hashBuffer, then
	// re-serialise via the script chunks. TS does Script.fromASM(toASM()) to
	// re-normalize push opcodes; FromChunks does the equivalent in Go.
	//
	// The function is also called on FT-LP / coin-LP code (poolNFT2 paths),
	// not just FT v1/v2 code. The chunks layout always has the destination
	// 21-byte push as the second-to-last chunk (hash, then "Code" tag), so
	// the chunk walker is correct for every code variant. There is no
	// byte-offset fallback — bytes-blind patching at fixed [1537:1558] would
	// silently corrupt LP scripts whose padding pushes the destination chunk
	// elsewhere.
	s := bscript.NewFromBytes(codeBuf)
	chunks := s.Chunks()
	if len(chunks) < 2 {
		return nil, fmt.Errorf("BuildFTtransferCode: code script has fewer than 2 chunks")
	}
	idx := len(chunks) - 2
	c := &chunks[idx]
	if c.Buf == nil {
		return nil, fmt.Errorf("BuildFTtransferCode: chunks[%d] has no push data", idx)
	}
	if len(c.Buf) == len(hashBuf) {
		copy(c.Buf, hashBuf)
	} else {
		c.Buf = append([]byte(nil), hashBuf...)
		c.Len = len(c.Buf)
	}
	out, err := bscript.FromChunks(chunks)
	if err != nil || out == nil {
		return nil, fmt.Errorf("BuildFTtransferCode: re-serialise chunks: %w", err)
	}
	return out, nil
}

// BuildFTtransferTape builds a transfer tape script with the specified amount.
// Mirrors TS FT.buildFTtransferTape(tape, amountHex).
func BuildFTtransferTape(tapeHex, amountHex string) *bscript.Script {
	amountBuf, _ := hex.DecodeString(amountHex)
	tapeBuf, _ := hex.DecodeString(tapeHex)
	copy(tapeBuf[3:51], amountBuf[:48])
	return bscript.NewFromBytes(tapeBuf)
}

// GetBalanceFromTape extracts the FT balance from a tape hex string.
func GetBalanceFromTape(tapeHex string) *big.Int {
	b, err := util.GetFtBalanceFromTape(tapeHex)
	if err != nil || b == nil {
		return big.NewInt(0)
	}
	return b
}

// ===== internal helpers =====

func ftBuildUnlock(privKey *bec.PrivateKey, tx *bt.Tx, preTX *bt.Tx, prepreTxData string, inputIdx, preTxVout int, isCoin bool) (*bscript.Script, error) {
	pretxdata, err := util.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := util.GetCurrentTxdata(tx, inputIdx)
	if err != nil {
		return nil, err
	}
	sh, err := tx.CalcInputSignatureHash(uint32(inputIdx), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKey := privKey.PubKey().SerialiseCompressed()
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKey), hex.EncodeToString(pubKey))
	coinFlag := ""
	if isCoin {
		coinFlag = "00"
	}
	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + coinFlag + pretxdata
	return bscript.NewFromHexString(unlockHex)
}

func ftBuildUnlockSwap(privKey *bec.PrivateKey, currentTX *bt.Tx, preTX *bt.Tx, prepreTxData string, contractTX *bt.Tx, currentUnlockIndex, preTxVout, ftVersion int, isCoin bool) (*bscript.Script, error) {
	pretxdata, err := util.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	var contracttxdata string
	if ftVersion == 2 {
		contracttxdata, err = util.GetContractTxdata(contractTX, -1)
	} else {
		if len(currentTX.Inputs) == 0 {
			return nil, fmt.Errorf("no inputs in current tx")
		}
		contracttxdata, err = util.GetContractTxdata(contractTX, int(currentTX.Inputs[0].PreviousTxOutIndex))
	}
	if err != nil {
		return nil, err
	}
	currentinputsdata, err := util.GetCurrentInputsdata(currentTX)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := util.GetCurrentTxdata(currentTX, currentUnlockIndex)
	if err != nil {
		return nil, err
	}
	sh, err := currentTX.CalcInputSignatureHash(uint32(currentUnlockIndex), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKey := privKey.PubKey().SerialiseCompressed()
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKey), hex.EncodeToString(pubKey))
	coinFlag := ""
	if isCoin {
		coinFlag = "51"
	}
	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + currentinputsdata + contracttxdata + coinFlag + pretxdata
	return bscript.NewFromHexString(unlockHex)
}

func (f *FT) buildFTUnlocks(privKey *bec.PrivateKey, tx *bt.Tx, preTXs []*bt.Tx, prepreTxDatas []string, ftutxos []*util.FtUTXO) ([]*bscript.Script, error) {
	unlocks := make([]*bscript.Script, len(ftutxos))
	for i, fu := range ftutxos {
		us, err := ftBuildUnlock(privKey, tx, preTXs[i], prepreTxDatas[i], i, int(fu.Vout), false)
		if err != nil {
			return nil, fmt.Errorf("build FT unlock input %d: %w", i, err)
		}
		unlocks[i] = us
	}
	return unlocks, nil
}

func (f *FT) buildFTUnlocksFromPrevVouts(privKey *bec.PrivateKey, tx *bt.Tx, preTXs []*bt.Tx, prepreTxDatas []string, vouts []int) ([]*bscript.Script, error) {
	unlocks := make([]*bscript.Script, len(vouts))
	for i, vout := range vouts {
		us, err := ftBuildUnlock(privKey, tx, preTXs[i], prepreTxDatas[i], i, vout, false)
		if err != nil {
			return nil, fmt.Errorf("build FT unlock input %d vout %d: %w", i, vout, err)
		}
		unlocks[i] = us
	}
	return unlocks, nil
}

func ftInsertUnlocks(tx *bt.Tx, unlocks []*bscript.Script, nFt int) error {
	for i, us := range unlocks {
		if i >= nFt {
			break
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return fmt.Errorf("insert FT unlock %d: %w", i, err)
		}
	}
	return nil
}

func ftSignFeeInputs(tx *bt.Tx, privKey *bec.PrivateKey, nFt int) error {
	ctx := context.Background()
	for i := nFt; i < len(tx.Inputs); i++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(i),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return fmt.Errorf("sign fee input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return fmt.Errorf("insert fee unlock %d: %w", i, err)
		}
	}
	return nil
}

func addInputFromPrevTxOutput(tx *bt.Tx, prevTx *bt.Tx, vout int) error {
	if vout >= len(prevTx.Outputs) {
		return fmt.Errorf("vout %d out of range for tx with %d outputs", vout, len(prevTx.Outputs))
	}
	out := prevTx.Outputs[vout]
	return tx.From(prevTx.TxID(), uint32(vout), out.LockingScript.String(), out.Satoshis)
}

func buildUTXOFromTx(tx *bt.Tx, vout int) (*bt.UTXO, error) {
	if vout >= len(tx.Outputs) {
		return nil, fmt.Errorf("output index %d out of range", vout)
	}
	txidBytes, _ := hex.DecodeString(tx.TxID())
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(vout),
		LockingScript: tx.Outputs[vout].LockingScript,
		Satoshis:      tx.Outputs[vout].Satoshis,
	}, nil
}

func newFTTx() *bt.Tx {
	tx := bt.NewTx()
	tx.Version = 10
	return tx
}

func newFeeQuote80() *bt.FeeQuote {
	fq := bt.NewFeeQuote()
	fq.AddQuote(bt.FeeTypeStandard, &bt.Fee{
		FeeType:   bt.FeeTypeStandard,
		MiningFee: bt.FeeUnit{Satoshis: ftSatPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: ftSatPerKB, Bytes: 1000},
	})
	fq.AddQuote(bt.FeeTypeData, &bt.Fee{
		FeeType:   bt.FeeTypeData,
		MiningFee: bt.FeeUnit{Satoshis: ftSatPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: ftSatPerKB, Bytes: 1000},
	})
	return fq
}

// newFeeQuoteNFT returns a fee quote for NFT operations (same rate as FT).
func newFeeQuoteNFT() *bt.FeeQuote {
	return newFeeQuote80()
}

func mintSourceTargetFeeSat(estBytes int) int {
	if estBytes < 1000 {
		return ftSatPerKB
	}
	return int(math.Ceil(float64(estBytes) * float64(ftSatPerKB) / 1000.0))
}

func getFTmintCode(txid string, vout int, address string, tapeSize int) (*bscript.Script, error) {
	txidBytes, err := hex.DecodeString(txid)
	if err != nil || len(txidBytes) != 32 {
		return nil, fmt.Errorf("invalid txid for getFTmintCode")
	}
	utxoBuf := make([]byte, 36)
	for i := 0; i < 32; i++ {
		utxoBuf[i] = txidBytes[31-i]
	}
	binary.LittleEndian.PutUint32(utxoBuf[32:], uint32(vout))
	utxoHex := hex.EncodeToString(utxoBuf)

	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	hash := addr.PublicKeyHash + "00"
	tapeSizeHex := hex.EncodeToString(util.GetSize(tapeSize))

	asm := ftMintTemplateASM
	asm = strings.ReplaceAll(asm, "${utxoHex}", utxoHex)
	asm = strings.ReplaceAll(asm, "${tapeSizeHex}", tapeSizeHex)
	asm = strings.ReplaceAll(asm, "${hash}", hash)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

func writeTapeAmount(totalSupply *big.Int) string {
	buf := make([]byte, 48)
	for i := 0; i < 6; i++ {
		word := new(big.Int).Rsh(totalSupply, uint(i*64))
		word = word.And(word, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1)))
		binary.LittleEndian.PutUint64(buf[i*8:], word.Uint64())
	}
	return hex.EncodeToString(buf)
}

func collapseTbcMintASM(asm string) string {
	re01 := regexp.MustCompile(`0x01\s+0x([0-9a-fA-F]+)`)
	asm = re01.ReplaceAllStringFunc(asm, func(m string) string {
		sub := re01.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		h := sub[1]
		if len(h)%2 != 0 || len(h) < 2 {
			return m
		}
		return h
	})
	rePush := regexp.MustCompile(`0x([0-9a-fA-F]{2})\s+0x([0-9a-fA-F]+)`)
	asm = rePush.ReplaceAllStringFunc(asm, func(m string) string {
		sub := rePush.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		b, err := hex.DecodeString(sub[1])
		if err != nil || len(b) != 1 {
			return m
		}
		n := int(b[0])
		if n < 1 || n > 75 {
			return m
		}
		data := sub[2]
		if len(data) != 2*n {
			return m
		}
		return data
	})
	return asm
}

func strip0xHexPushesInASM(asm string) string {
	parts := strings.Fields(asm)
	for i, p := range parts {
		if !strings.HasPrefix(p, "0x") || len(p) <= 2 {
			continue
		}
		rest := p[2:]
		if len(rest) < 2 || len(rest)%2 != 0 {
			continue
		}
		if _, err := hex.DecodeString(rest); err == nil {
			parts[i] = rest
		}
	}
	return strings.Join(parts, " ")
}

func signP2PKHInput(tx *bt.Tx, privKey *bec.PrivateKey, inputIdx uint32) error {
	in := tx.Inputs[inputIdx]
	if in.PreviousTxScript != nil && in.PreviousTxScript.IsP2PKH() {
		scriptPKH, err := in.PreviousTxScript.PublicKeyHash()
		if err == nil {
			keyPKH := crypto.Hash160(privKey.PubKey().SerialiseCompressed())
			if !bytes.Equal(scriptPKH, keyPKH) {
				return fmt.Errorf("signP2PKHInput: input %d is P2PKH for a different pubkey hash than the signing key", inputIdx)
			}
		}
	}
	sh, err := tx.CalcInputSignatureHash(inputIdx, sighash.AllForkID)
	if err != nil {
		return err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return err
	}
	us, err := bscript.NewP2PKHUnlockingScript(
		privKey.PubKey().SerialiseCompressed(),
		sig.Serialise(),
		sighash.AllForkID,
	)
	if err != nil {
		return err
	}
	return tx.InsertInputUnlockingScript(inputIdx, us)
}

func hexDecode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// ftTransferUnlockerGetter satisfies bt.UnlockerGetter for FT transfers.
// Returns either an FT custom unlocker or a P2PKH unlocker depending on input index.
type ftTransferUnlockerGetter struct {
	ftScripts []*bscript.Script
	privKey   *bec.PrivateKey
	callIdx   int
}

func newFTTransferUnlockerGetter(ftScripts []*bscript.Script, privKey *bec.PrivateKey) *ftTransferUnlockerGetter {
	return &ftTransferUnlockerGetter{ftScripts: ftScripts, privKey: privKey}
}

func (g *ftTransferUnlockerGetter) Unlocker(_ context.Context, _ *bscript.Script) (bt.Unlocker, error) {
	idx := g.callIdx
	g.callIdx++
	if idx < len(g.ftScripts) {
		return &fixedScriptUnlocker{script: g.ftScripts[idx]}, nil
	}
	return &unlocker.Simple{PrivateKey: g.privKey}, nil
}

type fixedScriptUnlocker struct {
	script *bscript.Script
}

func (u *fixedScriptUnlocker) UnlockingScript(_ context.Context, _ *bt.Tx, _ bt.UnlockerParams) (*bscript.Script, error) {
	return u.script, nil
}
