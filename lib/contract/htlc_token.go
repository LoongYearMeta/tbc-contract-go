package contract

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
)

const (
	htlcTokenCodeDust     = uint64(500)
	htlcTokenContractDust = uint64(100)
	htlcTokenFeePerKB     = 80
	htlcTokenFTUnlockSize = 2100
)

func htlcTokenTargetFee(tx *bt.Tx, extraBytes int) int {
	size := tx.JSEstimateSize() + extraBytes
	fee := (size*htlcTokenFeePerKB + 999) / 1000
	if fee < htlcTokenFeePerKB {
		return htlcTokenFeePerKB
	}
	return fee
}

func htlcTokenClassify(script *bscript.Script) (util.FTScriptInfo, error) {
	if script == nil {
		return util.FTScriptInfo{}, fmt.Errorf("unsupported FT code: nil script")
	}
	switch len(script.Bytes()) {
	case util.FTV2CodeLength, util.LegacyCoinCodeLength, util.FTV4CodeLength:
	default:
		return util.FTScriptInfo{}, fmt.Errorf(
			"unsupported FT code length %d; expected %d, %d, %d",
			len(script.Bytes()), util.FTV2CodeLength, util.LegacyCoinCodeLength, util.FTV4CodeLength,
		)
	}
	info, err := util.ClassifyFTScript(script)
	if err != nil {
		return util.FTScriptInfo{}, fmt.Errorf("unsupported FT code: %w", err)
	}
	return info, nil
}

func htlcTokenTapeAt(tx *bt.Tx, codeVout int) (*bscript.Script, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil FT parent transaction")
	}
	tapeVout := codeVout + 1
	if codeVout < 0 || tapeVout >= len(tx.Outputs) {
		return nil, fmt.Errorf("FT tape output %d is out of range", tapeVout)
	}
	return tx.Outputs[tapeVout].LockingScript, nil
}

func htlcTokenBalance(tape *bscript.Script) (*big.Int, error) {
	if tape == nil {
		return nil, fmt.Errorf("nil FT tape")
	}
	amount, err := util.GetFtBalanceFromTape(hex.EncodeToString(tape.Bytes()))
	if err != nil {
		return nil, err
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("FT tape encodes zero balance")
	}
	return amount, nil
}

func htlcTokenAddChange(tx *bt.Tx, address string, extraBytes int) error {
	if err := tx.ChangeToAddress(address, newFeeQuote80()); err != nil {
		return err
	}
	return tx.AdjustImplicitFeeToTarget(htlcTokenTargetFee(tx, extraBytes))
}

// DeployHTLCToken builds an unsigned transaction that atomically locks an FT
// amount to an HTLC script. Inputs are [FT..., TBC fee].
func DeployHTLCToken(
	sender, receiver, hashlock string,
	timelock uint32,
	amount *big.Int,
	ftUTXOs []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prePreTxData []string,
) (string, error) {
	if _, err := htlcAddressToPKH(sender); err != nil {
		return "", fmt.Errorf("DeployHTLCToken: invalid sender: %w", err)
	}
	if _, err := htlcAddressToPKH(receiver); err != nil {
		return "", fmt.Errorf("DeployHTLCToken: invalid receiver: %w", err)
	}
	if !util.IsValidSHA256Hash(hashlock) {
		return "", fmt.Errorf("DeployHTLCToken: invalid hashlock")
	}
	if len(ftUTXOs) == 0 {
		return "", fmt.Errorf("DeployHTLCToken: ftUTXOs must be non-empty")
	}
	if len(ftUTXOs) > 5 {
		return "", fmt.Errorf("DeployHTLCToken: ftUTXOs length must be <= 5")
	}
	if len(preTXs) != len(ftUTXOs) || len(prePreTxData) != len(ftUTXOs) {
		return "", fmt.Errorf("DeployHTLCToken: ancestry length must match ftUTXOs length")
	}
	if amount == nil || amount.Sign() <= 0 {
		return "", fmt.Errorf("DeployHTLCToken: amount must be positive")
	}
	if feeUTXO == nil {
		return "", fmt.Errorf("DeployHTLCToken: fee UTXO is required")
	}

	senderPKH, _ := htlcAddressToPKH(sender)
	receiverPKH, _ := htlcAddressToPKH(receiver)
	htlcScript, err := GetHTLCCode(senderPKH, receiverPKH, hashlock, timelock)
	if err != nil {
		return "", err
	}
	htlcHash := hex.EncodeToString(crypto.Hash160(crypto.Sha256(htlcScript.Bytes())))

	firstInfo, err := htlcTokenClassify(ftUTXOs[0].LockingScript)
	if err != nil {
		return "", fmt.Errorf("DeployHTLCToken: %w", err)
	}
	tapeAmounts := make([]*big.Int, len(ftUTXOs))
	total := new(big.Int)
	var lockTimeMax uint32
	for i, ftUTXO := range ftUTXOs {
		if ftUTXO == nil || ftUTXO.FtBalance == nil || ftUTXO.FtBalance.Sign() < 0 {
			return "", fmt.Errorf("DeployHTLCToken: invalid FT UTXO %d", i)
		}
		info, err := htlcTokenClassify(ftUTXO.LockingScript)
		if err != nil {
			return "", fmt.Errorf("DeployHTLCToken: FT input %d: %w", i, err)
		}
		if info != firstInfo {
			return "", fmt.Errorf("DeployHTLCToken: FT input %d type differs from input 0", i)
		}
		tapeAmounts[i] = new(big.Int).Set(ftUTXO.FtBalance)
		total.Add(total, ftUTXO.FtBalance)
		if firstInfo.IsCoin {
			tape, err := htlcTokenTapeAt(preTXs[i], int(ftUTXO.Vout))
			if err != nil {
				return "", fmt.Errorf("DeployHTLCToken: FT input %d: %w", i, err)
			}
			lockTime, err := GetLockTimeFromTape(tape)
			if err != nil {
				return "", fmt.Errorf("DeployHTLCToken: FT input %d locktime: %w", i, err)
			}
			if lockTime > lockTimeMax {
				lockTimeMax = lockTime
			}
		}
	}
	if amount.Cmp(total) > 0 {
		return "", fmt.Errorf("DeployHTLCToken: insufficient FT balance")
	}

	firstTape, err := htlcTokenTapeAt(preTXs[0], int(ftUTXOs[0].Vout))
	if err != nil {
		return "", fmt.Errorf("DeployHTLCToken: %w", err)
	}
	amountHex, changeHex := BuildTapeAmount(amount, tapeAmounts)
	codeHex := hex.EncodeToString(ftUTXOs[0].LockingScript.Bytes())
	tapeHex := hex.EncodeToString(firstTape.Bytes())

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftUTXOs)...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(feeUTXO); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: htlcScript, Satoshis: htlcTokenContractDust})
	lockedCode, err := BuildFTtransferCode(codeHex, htlcHash)
	if err != nil {
		return "", err
	}
	lockedTape, err := BuildFTtransferTape(tapeHex, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockedCode, Satoshis: htlcTokenCodeDust})
	tx.AddOutput(&bt.Output{LockingScript: lockedTape, Satoshis: 0})
	if amount.Cmp(total) < 0 {
		changeCode, err := BuildFTtransferCode(codeHex, sender)
		if err != nil {
			return "", err
		}
		changeTape, err := BuildFTtransferTape(tapeHex, changeHex)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: htlcTokenCodeDust})
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}
	if firstInfo.IsCoin {
		for i := range ftUTXOs {
			tx.Inputs[i].SequenceNumber = 0xfffffffe
		}
		tx.LockTime = lockTimeMax
	}
	if err := htlcTokenAddChange(
		tx,
		sender,
		len(ftUTXOs)*htlcTokenFTUnlockSize,
	); err != nil {
		return "", fmt.Errorf("DeployHTLCToken: change/fee: %w", err)
	}
	return tx.String(), nil
}

// FillSigDeployHTLCToken fills the FT and final P2PKH fee inputs.
func FillSigDeployHTLCToken(
	raw string,
	sigs []string,
	publicKey string,
	preTXs []*bt.Tx,
	prePreTxData []string,
) (string, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return "", fmt.Errorf("FillSigDeployHTLCToken: invalid raw transaction: %w", err)
	}
	if len(preTXs) == 0 || len(preTXs) != len(prePreTxData) {
		return "", fmt.Errorf("FillSigDeployHTLCToken: preTX/prePreTxData length mismatch")
	}
	if len(sigs) != len(preTXs)+1 {
		return "", fmt.Errorf("FillSigDeployHTLCToken: sigs length must be %d", len(preTXs)+1)
	}
	if len(tx.Inputs) != len(sigs) {
		return "", fmt.Errorf("FillSigDeployHTLCToken: transaction input count mismatch")
	}
	firstVout := int(tx.Inputs[0].PreviousTxOutIndex)
	if firstVout < 0 || firstVout >= len(preTXs[0].Outputs) {
		return "", fmt.Errorf("FillSigDeployHTLCToken: FT input vout out of range")
	}
	info, err := htlcTokenClassify(preTXs[0].Outputs[firstVout].LockingScript)
	if err != nil {
		return "", err
	}
	for i := range preTXs {
		vout := int(tx.Inputs[i].PreviousTxOutIndex)
		unlock, err := StaticGetFTunlock(
			sigs[i], publicKey, tx, preTXs[i], prePreTxData[i],
			i, vout, info.IsCoin,
		)
		if err != nil {
			return "", fmt.Errorf("FillSigDeployHTLCToken: FT input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), unlock); err != nil {
			return "", err
		}
	}
	feeUnlock, err := bscript.NewFromASM(sigs[len(preTXs)] + " " + publicKey)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(uint32(len(preTXs)), feeUnlock); err != nil {
		return "", err
	}
	return tx.String(), nil
}

func buildHTLCTokenRedemption(
	address string,
	htlcUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	deployTX *bt.Tx,
	feeUTXO *bt.UTXO,
	refundLockTime *uint32,
) (string, error) {
	if _, err := htlcAddressToPKH(address); err != nil {
		return "", fmt.Errorf("invalid redemption address: %w", err)
	}
	if htlcUTXO == nil || ftUTXO == nil || feeUTXO == nil || deployTX == nil {
		return "", fmt.Errorf("HTLC, FT, deploy, and fee inputs are required")
	}
	info, err := htlcTokenClassify(ftUTXO.LockingScript)
	if err != nil {
		return "", err
	}
	tape, err := htlcTokenTapeAt(deployTX, int(ftUTXO.Vout))
	if err != nil {
		return "", err
	}
	total, err := htlcTokenBalance(tape)
	if err != nil {
		return "", err
	}
	amountHex, _ := BuildTapeAmountWithFtInputIndex(total, []*big.Int{total}, 1)
	codeOut, err := BuildFTtransferCode(hex.EncodeToString(ftUTXO.LockingScript.Bytes()), address)
	if err != nil {
		return "", err
	}
	tapeOut, err := BuildFTtransferTape(hex.EncodeToString(tape.Bytes()), amountHex)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUTXO, util.FtUTXOToUTXO(ftUTXO), feeUTXO); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeOut, Satoshis: htlcTokenCodeDust})
	tx.AddOutput(&bt.Output{LockingScript: tapeOut, Satoshis: 0})

	var coinLockTime uint32
	if info.IsCoin {
		tx.Inputs[1].SequenceNumber = 0xfffffffe
		coinLockTime, err = GetLockTimeFromTape(tape)
		if err != nil {
			return "", err
		}
	}
	if refundLockTime != nil {
		tx.Inputs[0].SequenceNumber = 0xfffffffe
		tx.LockTime = *refundLockTime
		if coinLockTime > tx.LockTime {
			tx.LockTime = coinLockTime
		}
	} else if info.IsCoin {
		tx.LockTime = coinLockTime
	}
	if err := htlcTokenAddChange(tx, address, 3000); err != nil {
		return "", fmt.Errorf("redemption change/fee: %w", err)
	}
	return tx.String(), nil
}

// WithdrawHTLCToken builds an unsigned secret-branch redemption.
func WithdrawHTLCToken(
	receiver string,
	htlcUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	deployTX *bt.Tx,
	feeUTXO *bt.UTXO,
) (string, error) {
	return buildHTLCTokenRedemption(receiver, htlcUTXO, ftUTXO, deployTX, feeUTXO, nil)
}

// RefundHTLCToken builds an unsigned timelock-branch redemption.
func RefundHTLCToken(
	sender string,
	htlcUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	deployTX *bt.Tx,
	feeUTXO *bt.UTXO,
	timelock uint32,
) (string, error) {
	return buildHTLCTokenRedemption(sender, htlcUTXO, ftUTXO, deployTX, feeUTXO, &timelock)
}

func fillSigHTLCTokenRedemption(
	raw string,
	sigs []string,
	publicKey string,
	secret *string,
	deployTX *bt.Tx,
	prePreTxData string,
) (string, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return "", fmt.Errorf("invalid redemption raw transaction: %w", err)
	}
	if len(sigs) != 3 || len(tx.Inputs) != 3 {
		return "", fmt.Errorf("sigs must contain [HTLC, FTCode, tbcFee]")
	}
	if deployTX == nil || len(deployTX.Outputs) < 3 {
		return "", fmt.Errorf("deploy transaction is missing FT outputs")
	}
	info, err := htlcTokenClassify(deployTX.Outputs[1].LockingScript)
	if err != nil {
		return "", err
	}

	branch := "OP_FALSE"
	if secret != nil {
		if strings.TrimSpace(*secret) == "" {
			return "", fmt.Errorf("secret is required")
		}
		branch = *secret + " OP_TRUE"
	}
	htlcUnlock, err := bscript.NewFromASM(sigs[0] + " " + publicKey + " " + branch)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, htlcUnlock); err != nil {
		return "", err
	}

	ftVout := int(tx.Inputs[1].PreviousTxOutIndex)
	ftUnlock, err := StaticGetFTUnlockSwap(
		sigs[1], publicKey, tx, deployTX, prePreTxData, deployTX,
		1, ftVout, info.Version, info.IsCoin, false,
	)
	if err != nil {
		return "", fmt.Errorf("fill FT unlock: %w", err)
	}
	if err := tx.InsertInputUnlockingScript(1, ftUnlock); err != nil {
		return "", err
	}
	feeUnlock, err := bscript.NewFromASM(sigs[2] + " " + publicKey)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(2, feeUnlock); err != nil {
		return "", err
	}
	return tx.String(), nil
}

// FillSigWithdrawHTLCToken fills [HTLC, FTCode, fee] signatures.
func FillSigWithdrawHTLCToken(
	raw string,
	sigs []string,
	publicKey, secret string,
	deployTX *bt.Tx,
	prePreTxData string,
) (string, error) {
	return fillSigHTLCTokenRedemption(raw, sigs, publicKey, &secret, deployTX, prePreTxData)
}

// FillSigRefundHTLCToken fills [HTLC, FTCode, fee] signatures.
func FillSigRefundHTLCToken(
	raw string,
	sigs []string,
	publicKey string,
	deployTX *bt.Tx,
	prePreTxData string,
) (string, error) {
	return fillSigHTLCTokenRedemption(raw, sigs, publicKey, nil, deployTX, prePreTxData)
}

func signHTLCTokenInputs(
	raw string,
	inputs []*bt.UTXO,
	privateKey *bec.PrivateKey,
) ([]string, string, error) {
	if privateKey == nil {
		return nil, "", fmt.Errorf("private key is required")
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, "", err
	}
	if len(tx.Inputs) != len(inputs) {
		return nil, "", fmt.Errorf("input metadata count mismatch")
	}
	sigs := make([]string, len(inputs))
	for i, input := range inputs {
		if input == nil || input.LockingScript == nil {
			return nil, "", fmt.Errorf("input metadata %d is missing", i)
		}
		tx.Inputs[i].PreviousTxScript = input.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = input.Satoshis
		hash, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return nil, "", fmt.Errorf("sign input %d: %w", i, err)
		}
		sig, err := privateKey.Sign(hash)
		if err != nil {
			return nil, "", fmt.Errorf("sign input %d: %w", i, err)
		}
		sigBytes := append(sig.Serialise(), byte(sighash.AllForkID))
		sigs[i] = hex.EncodeToString(sigBytes)
	}
	publicKey := hex.EncodeToString(privateKey.PubKey().SerialiseCompressed())
	return sigs, publicKey, nil
}

// DeployHTLCTokenWithSign builds and signs a Token HTLC deployment.
func DeployHTLCTokenWithSign(
	sender, receiver, hashlock string,
	timelock uint32,
	amount *big.Int,
	ftUTXOs []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prePreTxData []string,
	privateKey *bec.PrivateKey,
) (string, error) {
	raw, err := DeployHTLCToken(
		sender, receiver, hashlock, timelock, amount,
		ftUTXOs, feeUTXO, preTXs, prePreTxData,
	)
	if err != nil {
		return "", err
	}
	inputs := append(util.FtUTXOsToUTXOs(ftUTXOs), feeUTXO)
	sigs, publicKey, err := signHTLCTokenInputs(raw, inputs, privateKey)
	if err != nil {
		return "", err
	}
	return FillSigDeployHTLCToken(raw, sigs, publicKey, preTXs, prePreTxData)
}

// WithdrawHTLCTokenWithSign builds and signs a Token HTLC secret redemption.
func WithdrawHTLCTokenWithSign(
	privateKey *bec.PrivateKey,
	receiver string,
	htlcUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	deployTX *bt.Tx,
	prePreTxData string,
	feeUTXO *bt.UTXO,
	secret string,
) (string, error) {
	raw, err := WithdrawHTLCToken(receiver, htlcUTXO, ftUTXO, deployTX, feeUTXO)
	if err != nil {
		return "", err
	}
	inputs := []*bt.UTXO{htlcUTXO, util.FtUTXOToUTXO(ftUTXO), feeUTXO}
	sigs, publicKey, err := signHTLCTokenInputs(raw, inputs, privateKey)
	if err != nil {
		return "", err
	}
	return FillSigWithdrawHTLCToken(raw, sigs, publicKey, secret, deployTX, prePreTxData)
}

// RefundHTLCTokenWithSign builds and signs a Token HTLC timelock redemption.
func RefundHTLCTokenWithSign(
	privateKey *bec.PrivateKey,
	sender string,
	htlcUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	deployTX *bt.Tx,
	prePreTxData string,
	feeUTXO *bt.UTXO,
	timelock uint32,
) (string, error) {
	raw, err := RefundHTLCToken(sender, htlcUTXO, ftUTXO, deployTX, feeUTXO, timelock)
	if err != nil {
		return "", err
	}
	inputs := []*bt.UTXO{htlcUTXO, util.FtUTXOToUTXO(ftUTXO), feeUTXO}
	sigs, publicKey, err := signHTLCTokenInputs(raw, inputs, privateKey)
	if err != nil {
		return "", err
	}
	return FillSigRefundHTLCToken(raw, sigs, publicKey, deployTX, prePreTxData)
}
