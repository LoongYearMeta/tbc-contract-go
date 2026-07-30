// Package contract — StableCoin port of tbc-contract/lib/contract/stableCoin.ts.
//
// stableCoin is an FT subclass with two execution paths:
//
//  1. Owner-signed FT moves (Transfer / BatchTransfer / MergeCoin) which
//     reuse FT.getFTunlock with isCoin=true. These produce raw txs ready to
//     broadcast.
//
//  2. Admin-MuSig2 paths (CreateCoin / MintCoin / FreezeCoinUTXO /
//     UnfreezeCoinUTXO) which return *AdminPrepared. The Go side cannot do
//     MuSig2 (tbc-lib-go has no Schnorr/BIP340), so the prepare* methods
//     pre-seed admin-input unlocks with 64-byte zero placeholders, freeze
//     the fee, and return the SHA256d sighashes for those inputs. The
//     caller runs the external MuSig ceremony, gets back 64-byte BIP340
//     sigs, and passes them to (*AdminPrepared).Finalize, which swaps the
//     placeholders for real sigs (same byte length → no fee shift) and
//     serializes the tx.
package contract

import (
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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

//go:embed asm/stablecoin_mint.asm
var stablecoinMintTemplateASM string

//go:embed asm/stablecoin_coinnft_code.asm
var stablecoinCoinNftCodeTemplateASM string

// stablecoinSequenceNoLockTime is the sequence number that allows nLockTime to
// take effect (0xfffffffe). Mirrors TS tx.setInputSequence(i, 4294967294).
const stablecoinSequenceNoLockTime uint32 = 0xfffffffe

// dummySchnorrSig64 is a fixed-length 64-byte all-zero placeholder used while
// pre-seeding admin unlock scripts. Its byte length matches a real BIP340
// signature so every size-dependent computation (fee estimate, hashOutputs)
// produces the same result before and after we swap in real signatures.
var dummySchnorrSig64 = make([]byte, 64)

// CoinNftData mirrors TS coinNftData. Carried as the JSON-encoded tape data
// chunk on the coin-NFT issuance certificate output.
type CoinNftData struct {
	NftName         string `json:"nftName"`
	NftSymbol       string `json:"nftSymbol"`
	Description     string `json:"description"`
	CoinDecimal     int    `json:"coinDecimal"`
	CoinTotalSupply string `json:"coinTotalSupply"`
}

// StableCoin mirrors TS class stableCoin extends FT.
type StableCoin struct {
	*FT
}

// NewStableCoin uses the same constructor surface as NewFT.
func NewStableCoin(txidOrParams interface{}) (*StableCoin, error) {
	f, err := NewFT(txidOrParams)
	if err != nil {
		return nil, err
	}
	return &StableCoin{FT: f}, nil
}

// AdminSighash describes one admin-signed input. Sighash is the 32-byte
// SHA256d digest expected by an external BIP340 / MuSig2 signer.
type AdminSighash struct {
	InputIndex uint32
	Sighash    []byte
}

// AdminPrepared bundles a prepared transaction whose admin inputs are seeded
// with 64-byte placeholders. The MuSig ceremony signs each Sighashes[i]
// externally; pass the resulting 64-byte BIP340 sigs to Finalize in matching
// order. Finalize returns one or more raw tx hex strings ready to broadcast.
type AdminPrepared struct {
	Tx        *bt.Tx
	Sighashes []AdminSighash
	finalize  func(sigs64 [][]byte) ([]string, error)
}

// Finalize swaps in the real Schnorr sigs and produces the broadcast raw(s).
func (a *AdminPrepared) Finalize(sigs64 [][]byte) ([]string, error) {
	return a.finalize(sigs64)
}

// =============================================================================
// Owner-signed FT-style paths
// =============================================================================

// Transfer transfers stableCoin to address_to, with optional bundled TBC.
// Mirrors TS stableCoin.transfer(privateKey_from, address_to, ft_amount,
// ftutxo_a, utxo, preTX, prepreTxData, tbc_amount?).
func (sc *StableCoin) Transfer(
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
	var lockTimeMax uint32
	for i, fu := range ftutxos {
		tapeAmountSet[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
		lt, err := GetLockTimeFromTape(preTXs[i].Outputs[fu.Vout+1].LockingScript)
		if err != nil {
			return "", fmt.Errorf("read locktime from input %d tape: %w", i, err)
		}
		if lt > lockTimeMax {
			lockTimeMax = lt
		}
	}
	if amount.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("insufficient balance, please add more FT UTXOs")
	}
	if sc.Decimal > 18 {
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

	// LockTime + sequence MUST be set BEFORE building unlock scripts: BIP143
	// preimage commits to both, so any later change invalidates every signed
	// input. Same rule as the rest of the repo (CLAUDE.md "Set tx.LockTime
	// and per-input SequenceNumber BEFORE calling ft.GetFTunlock /
	// signP2PKHAtIdx").
	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = stablecoinSequenceNoLockTime
	}
	tx.LockTime = lockTimeMax

	codeScript, err := BuildFTtransferCode(sc.CodeScript, addressTo)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript, err := BuildFTtransferTape(sc.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if tbcAmountSat > 0 {
		tx.To(addressTo, tbcAmountSat)
	}
	if amount.Cmp(tapeAmountSum) < 0 {
		changeCode, err := BuildFTtransferCode(sc.CodeScript, addressFrom)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
		changeTape, err := BuildFTtransferTape(sc.TapeScript, changeHex)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", fmt.Errorf("ChangeToAddress: %w", err)
	}

	if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		return scSignAllOwner(tx, privKey, ftutxos, preTXs, prepreTxDatas)
	}); err != nil {
		return "", fmt.Errorf("stablecoin transfer: finalize fee: %w", err)
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BatchTransfer batch-transfers stableCoin to multiple recipients (≤5/tx),
// chaining FT change between txs. Mirrors TS stableCoin.batchTransfer.
func (sc *StableCoin) BatchTransfer(
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

	totalBalance := new(big.Int)
	for _, fu := range ftutxos {
		totalBalance.Add(totalBalance, fu.FtBalance)
	}

	var batches [][]AddressAmount
	for i := 0; i < len(receivers); i += 5 {
		end := i + 5
		if end > len(receivers) {
			end = len(receivers)
		}
		batches = append(batches, receivers[i:end])
	}

	var txsraw []string
	currentPreTXs := preTXs
	currentPrepreTxDatas := prepreTxDatas
	currentFtutxos := ftutxos
	currentFeeUTXO := feeUTXO
	balance := new(big.Int).Set(totalBalance)
	var prevBatchSize int

	for b, batch := range batches {
		receiverAmounts := make([]*big.Int, len(batch))
		totalBatchAmount := new(big.Int)
		for i, r := range batch {
			if r.Amount == nil || r.Amount.Sign() < 0 {
				return nil, fmt.Errorf("invalid amount for address %s", r.Address)
			}
			receiverAmounts[i] = r.Amount
			totalBatchAmount.Add(totalBatchAmount, r.Amount)
		}

		tapeAmountSetIn := make([]*big.Int, 0)
		var lockTimeMax uint32

		ftChangeIndex := prevBatchSize * 2
		tbcChangeIndex := prevBatchSize*2 + 2

		if b == 0 {
			for i, fu := range currentFtutxos {
				tapeAmountSetIn = append(tapeAmountSetIn, new(big.Int).Set(fu.FtBalance))
				lt, err := GetLockTimeFromTape(currentPreTXs[i].Outputs[fu.Vout+1].LockingScript)
				if err != nil {
					return nil, fmt.Errorf("read locktime from input %d tape: %w", i, err)
				}
				if lt > lockTimeMax {
					lockTimeMax = lt
				}
			}
		} else {
			tapeAmountSetIn = append(tapeAmountSetIn, new(big.Int).Set(balance))
			lt, err := GetLockTimeFromTape(currentPreTXs[0].Outputs[ftChangeIndex+1].LockingScript)
			if err != nil {
				return nil, fmt.Errorf("read chained locktime: %w", err)
			}
			lockTimeMax = lt
		}

		tapeHexes, err := BuildMultiTapeAmounts(receiverAmounts, tapeAmountSetIn)
		if err != nil {
			return nil, err
		}

		tx := newFTTx()
		var ftutxosForTx []*util.FtUTXO
		var preTXsForTx []*bt.Tx
		var prepreForTx []string
		if b == 0 {
			if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(currentFtutxos)...); err != nil {
				return nil, err
			}
			if err := tx.FromUTXOs(currentFeeUTXO); err != nil {
				return nil, err
			}
			ftutxosForTx = currentFtutxos
			preTXsForTx = currentPreTXs
			prepreForTx = currentPrepreTxDatas
		} else {
			prevTx := currentPreTXs[0]
			if err := addInputFromPrevTxOutput(tx, prevTx, ftChangeIndex); err != nil {
				return nil, fmt.Errorf("add chained FT input vout %d: %w", ftChangeIndex, err)
			}
			if err := addInputFromPrevTxOutput(tx, prevTx, tbcChangeIndex); err != nil {
				return nil, fmt.Errorf("add chained fee input vout %d: %w", tbcChangeIndex, err)
			}
			// Synthesise a single FT input descriptor for the chained tx.
			prevTxIDBytes, err := hex.DecodeString(prevTx.TxID())
			if err != nil {
				return nil, fmt.Errorf("decode chained txid: %w", err)
			}
			ftutxosForTx = []*util.FtUTXO{{
				TxID:          prevTxIDBytes,
				Vout:          uint32(ftChangeIndex),
				LockingScript: prevTx.Outputs[ftChangeIndex].LockingScript,
				Satoshis:      prevTx.Outputs[ftChangeIndex].Satoshis,
				FtBalance:     new(big.Int).Set(balance),
			}}
			preTXsForTx = []*bt.Tx{prevTx}
			prepreForTx = currentPrepreTxDatas
		}

		nFt := len(ftutxosForTx)
		for i := 0; i < nFt; i++ {
			tx.Inputs[i].SequenceNumber = stablecoinSequenceNoLockTime
		}
		tx.LockTime = lockTimeMax

		for i, r := range batch {
			cs, err := BuildFTtransferCode(sc.CodeScript, r.Address)
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: cs, Satoshis: 500})
			ts, err := BuildFTtransferTape(sc.TapeScript, tapeHexes[i])
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: ts, Satoshis: 0})
		}
		if totalBatchAmount.Cmp(balance) < 0 {
			changeCode, err := BuildFTtransferCode(sc.CodeScript, addressFrom)
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
			changeTape, err := BuildFTtransferTape(sc.TapeScript, tapeHexes[len(batch)])
			if err != nil {
				return nil, err
			}
			tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
		}

		if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
			return nil, fmt.Errorf("batch ChangeToAddress: %w", err)
		}

		if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
			return scSignAllOwner(tx, privKey, ftutxosForTx, preTXsForTx, prepreForTx)
		}); err != nil {
			return nil, fmt.Errorf("stablecoin batch transfer: finalize fee: %w", err)
		}

		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))

		// Rebuild prepreTxData for the next chained batch (mirrors TS).
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

// MergeCoin merges stableCoin UTXOs in batches of ≤5 until ≤1 remains, then
// recursively merges the resulting outputs. Mirrors TS stableCoin.mergeCoin.
func (sc *StableCoin) MergeCoin(
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	localTXs []*bt.Tx,
) ([]string, error) {
	if len(ftutxos) <= 1 {
		return nil, nil
	}
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
		var err error
		if iteration == 0 {
			tx, err = sc.mergeCoinSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, feeUTXO)
		} else {
			tx, err = sc.mergeCoinSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, nil)
		}
		if err != nil {
			return nil, err
		}
		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))

		idx := (iteration + 1) * maxBatch
		endIdx = idx + maxBatch
		if endIdx > len(ftutxos) {
			endIdx = len(ftutxos)
		}

		currentPreTXs = nil
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

	utxoTX := currentPreTXs[len(currentPreTXs)-1]
	nonEmpty := len(currentPreTXs) - 1
	newFeeUTXO, err := buildUTXOFromTx(utxoTX, 2)
	if err != nil {
		return nil, err
	}

	var newFtutxos []*util.FtUTXO
	if currentFtutxos != nil {
		newFtutxos = append(newFtutxos, currentFtutxos...)
	}
	newPreTXs := append([]*bt.Tx(nil), currentPreTXs[:nonEmpty]...)
	for _, rawHex := range txsraw {
		txBytes, _ := hex.DecodeString(rawHex)
		t, err := bt.NewTxFromBytes(txBytes)
		if err != nil {
			return nil, err
		}
		newPreTXs = append(newPreTXs, t)
		ftBal, err := util.GetFtBalanceFromTape(hex.EncodeToString(t.Outputs[1].LockingScript.Bytes()))
		if err != nil {
			return nil, err
		}
		mergedTxIDBytes, err := hex.DecodeString(t.TxID())
		if err != nil {
			return nil, fmt.Errorf("decode merged txid: %w", err)
		}
		newFtutxos = append(newFtutxos, &util.FtUTXO{
			TxID:          mergedTxIDBytes,
			Vout:          0,
			LockingScript: t.Outputs[0].LockingScript,
			Satoshis:      t.Outputs[0].Satoshis,
			FtBalance:     ftBal,
		})
	}

	prepreLookup := localTXs
	if len(prepreLookup) == 0 {
		prepreLookup = preTXsCopy
	}
	newPrepreTxDatas := make([]string, 0, nonEmpty+(len(newPreTXs)-nonEmpty))
	if nonEmpty > 0 && nonEmpty <= len(currentPrepreTxDatas) {
		newPrepreTxDatas = append(newPrepreTxDatas, currentPrepreTxDatas[:nonEmpty]...)
	}
	for i := nonEmpty; i < len(newPreTXs); i++ {
		ppd, err := util.BuildFtPrePreTxData(newPreTXs[i], 0, prepreLookup)
		if err != nil {
			return nil, fmt.Errorf("BuildFtPrePreTxData merge: %w", err)
		}
		newPrepreTxDatas = append(newPrepreTxDatas, ppd)
	}

	rec, err := sc.MergeCoin(privKey, newFtutxos, newFeeUTXO, newPreTXs, newPrepreTxDatas, newPreTXs)
	if err != nil {
		return nil, err
	}
	return append(txsraw, rec...), nil
}

// mergeCoinSingle merges up to 5 coin UTXOs into one. Mirrors TS _mergeCoin.
func (sc *StableCoin) mergeCoinSingle(
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
	feeUTXO *bt.UTXO,
) (*bt.Tx, error) {
	if len(ftutxos) == 0 {
		return nil, fmt.Errorf("no FT UTXO available")
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
	var lockTimeMax uint32
	for i, fu := range ftutxos {
		tapeAmountSet[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
		lt, err := GetLockTimeFromTape(preTXs[i].Outputs[fu.Vout+1].LockingScript)
		if err != nil {
			return nil, fmt.Errorf("read locktime from input %d tape: %w", i, err)
		}
		if lt > lockTimeMax {
			lockTimeMax = lt
		}
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
	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = stablecoinSequenceNoLockTime
	}
	tx.LockTime = lockTimeMax

	codeScript, err := BuildFTtransferCode(sc.CodeScript, addressFrom)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript, err := BuildFTtransferTape(sc.TapeScript, amountHex)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("merge ChangeToAddress: %w", err)
	}

	if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		return scSignAllOwner(tx, privKey, ftutxos, preTXs, prepreTxDatas)
	}); err != nil {
		return nil, fmt.Errorf("stablecoin merge: finalize fee: %w", err)
	}
	return tx, nil
}

// =============================================================================
// Admin-MuSig prepare/finalize paths
// =============================================================================

// PrepareCreateCoin issues a brand-new stableCoin: builds the coin-NFT-creation
// tx (ECDSA-signed by feePrivateKey upfront) and the mint tx, whose inputs 0/1
// (coin-NFT code + hold) require external Schnorr MuSig admin signatures.
//
// Returns *AdminPrepared whose Sighashes covers inputs 0 and 1 of the mint tx.
// Finalize(sigs) returns [coinNftRaw, mintRaw].
//
// Mirrors TS stableCoin.createCoin.
func (sc *StableCoin) PrepareCreateCoin(
	aggPubkey32 []byte,
	feePrivateKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	utxoTX *bt.Tx,
	mintMessage string,
) (*AdminPrepared, error) {
	if len(aggPubkey32) != 32 {
		return nil, fmt.Errorf("aggPubkey32 must be 32 bytes (x-only)")
	}
	adminPubHash := hex.EncodeToString(crypto.Hash160(aggPubkey32))

	totalSupply := new(big.Int).Mul(
		sc.TotalSupply,
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(sc.Decimal)), nil),
	)

	tapeScript, err := buildStableCoinTapeScript(sc.Name, sc.Symbol, sc.Decimal, totalSupply, 0)
	if err != nil {
		return nil, err
	}
	tapeSize := len(tapeScript.Bytes())

	data := &CoinNftData{
		NftName:         sc.Name + " NFT",
		NftSymbol:       sc.Symbol + " NFT",
		Description:     "The sole issuance certificate for the stablecoin, dynamically recording cumulative supply and issuance history. Non-transferable, real-time updated, ensuring full transparency and auditability.",
		CoinDecimal:     sc.Decimal,
		CoinTotalSupply: "0",
	}
	coinNftTX, err := BuildCoinNftTX(feePrivateKey, adminPubHash, utxo, data)
	if err != nil {
		return nil, fmt.Errorf("build coin NFT tx: %w", err)
	}
	coinNftRaw := hex.EncodeToString(coinNftTX.Bytes())

	data.CoinTotalSupply = totalSupply.String()
	updatedTape, err := GetCoinNftTapeScript(data)
	if err != nil {
		return nil, err
	}
	coinNftOutputs := BuildCoinNftOutput(
		coinNftTX.Outputs[0].LockingScript,
		coinNftTX.Outputs[1].LockingScript,
		updatedTape,
	)

	originCodeHash := hex.EncodeToString(crypto.Sha256(coinNftTX.Outputs[0].LockingScript.Bytes()))
	codeScript, err := GetCoinMintCode(adminPubHash, addressTo, originCodeHash, tapeSize)
	if err != nil {
		return nil, err
	}
	sc.CodeScript = hex.EncodeToString(codeScript.Bytes())
	sc.TapeScript = hex.EncodeToString(tapeScript.Bytes())

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
		msg, err := buildOpReturnMessage(mintMessage)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: msg, Satoshis: 0})
	}

	feeAddr, err := bscript.NewAddressFromPublicKey(feePrivateKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	if err := tx.ChangeToAddress(feeAddr.AddressString, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("mint ChangeToAddress: %w", err)
	}

	// Pre-seed admin inputs with dummy 64-byte zero sigs and freeze fee via
	// two-pass adjust. Input 0 is the coin-NFT-code unlock (Schnorr sig +
	// xonly-pub + currTxData + prepre + pre); input 1 is the coin-NFT-hold
	// P2PKH-on-Schnorr unlock (Schnorr sig + xonly-pub).
	rebuildIn0 := func(sig64 []byte) (*bscript.Script, error) {
		return BuildCoinNftUnlockScriptSchnorr(sig64, aggPubkey32, tx, coinNftTX, utxoTX, 0)
	}
	rebuildIn1 := func(sig64 []byte) (*bscript.Script, error) {
		return buildSchnorrP2PKHLikeUnlock(sig64, aggPubkey32)
	}
	if err := scPreseedAndFreezeFee(tx, feePrivateKey, []scAdminBuilder{
		{InputIndex: 0, Build: rebuildIn0},
		{InputIndex: 1, Build: rebuildIn1},
	}, []int{2}); err != nil {
		return nil, err
	}

	sighashes, err := scComputeSighashes(tx, []uint32{0, 1})
	if err != nil {
		return nil, err
	}

	finalize := func(sigs64 [][]byte) ([]string, error) {
		if len(sigs64) != 2 {
			return nil, fmt.Errorf("createCoin.Finalize: expected 2 Schnorr sigs, got %d", len(sigs64))
		}
		us0, err := rebuildIn0(sigs64[0])
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(0, us0); err != nil {
			return nil, err
		}
		us1, err := rebuildIn1(sigs64[1])
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(1, us1); err != nil {
			return nil, err
		}
		// Fee input was signed by preseed; re-sign now in case the unlock
		// length is RFC-6979-stable (it is — same key, same digest).
		if err := signP2PKHAtIdx(tx, feePrivateKey, 2); err != nil {
			return nil, err
		}
		sc.ContractTxid = tx.TxID()
		return []string{coinNftRaw, hex.EncodeToString(tx.Bytes())}, nil
	}

	return &AdminPrepared{Tx: tx, Sighashes: sighashes, finalize: finalize}, nil
}

// PrepareMintCoin mints additional supply on an existing stableCoin. Inputs:
// 0 = coin-NFT-code (admin MuSig), 1 = coin-NFT-hold (admin MuSig), 2 = fee
// utxo (ECDSA). Mirrors TS stableCoin.mintCoin.
func (sc *StableCoin) PrepareMintCoin(
	aggPubkey32 []byte,
	feePrivateKey *bec.PrivateKey,
	addressTo string,
	mintAmount *big.Int,
	utxo *bt.UTXO,
	nftPreTX *bt.Tx,
	nftPrePreTX *bt.Tx,
	mintMessage string,
) (*AdminPrepared, error) {
	if len(aggPubkey32) != 32 {
		return nil, fmt.Errorf("aggPubkey32 must be 32 bytes (x-only)")
	}
	if mintAmount == nil || mintAmount.Sign() <= 0 {
		return nil, fmt.Errorf("mintAmount must be positive")
	}
	adminPubHash := hex.EncodeToString(crypto.Hash160(aggPubkey32))

	newTotalSupply := new(big.Int).Add(sc.TotalSupply, mintAmount)
	tapeScript, err := buildStableCoinTapeScript(sc.Name, sc.Symbol, sc.Decimal, mintAmount, 0)
	if err != nil {
		return nil, err
	}
	tapeSize := len(tapeScript.Bytes())

	updatedTape, err := UpdateCoinNftTapeScript(nftPreTX.Outputs[2].LockingScript, newTotalSupply.String())
	if err != nil {
		return nil, err
	}
	coinNftOutputs := BuildCoinNftOutput(
		nftPreTX.Outputs[0].LockingScript,
		nftPreTX.Outputs[1].LockingScript,
		updatedTape,
	)

	originCodeHash := hex.EncodeToString(crypto.Sha256(nftPreTX.Outputs[0].LockingScript.Bytes()))
	codeScript, err := GetCoinMintCode(adminPubHash, addressTo, originCodeHash, tapeSize)
	if err != nil {
		return nil, err
	}
	sc.CodeScript = hex.EncodeToString(codeScript.Bytes())
	sc.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	tx := newFTTx()
	if err := addInputFromPrevTxOutput(tx, nftPreTX, 0); err != nil {
		return nil, err
	}
	if err := addInputFromPrevTxOutput(tx, nftPreTX, 1); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return nil, err
	}
	for _, out := range coinNftOutputs {
		tx.AddOutput(out)
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})
	if mintMessage != "" {
		msg, err := buildOpReturnMessage(mintMessage)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: msg, Satoshis: 0})
	}

	feeAddr, err := bscript.NewAddressFromPublicKey(feePrivateKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	if err := tx.ChangeToAddress(feeAddr.AddressString, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("mint ChangeToAddress: %w", err)
	}

	rebuildIn0 := func(sig64 []byte) (*bscript.Script, error) {
		return BuildCoinNftUnlockScriptSchnorr(sig64, aggPubkey32, tx, nftPreTX, nftPrePreTX, 0)
	}
	rebuildIn1 := func(sig64 []byte) (*bscript.Script, error) {
		return buildSchnorrP2PKHLikeUnlock(sig64, aggPubkey32)
	}
	if err := scPreseedAndFreezeFee(tx, feePrivateKey, []scAdminBuilder{
		{InputIndex: 0, Build: rebuildIn0},
		{InputIndex: 1, Build: rebuildIn1},
	}, []int{2}); err != nil {
		return nil, err
	}

	sighashes, err := scComputeSighashes(tx, []uint32{0, 1})
	if err != nil {
		return nil, err
	}

	finalize := func(sigs64 [][]byte) ([]string, error) {
		if len(sigs64) != 2 {
			return nil, fmt.Errorf("mintCoin.Finalize: expected 2 Schnorr sigs, got %d", len(sigs64))
		}
		us0, err := rebuildIn0(sigs64[0])
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(0, us0); err != nil {
			return nil, err
		}
		us1, err := rebuildIn1(sigs64[1])
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(1, us1); err != nil {
			return nil, err
		}
		if err := signP2PKHAtIdx(tx, feePrivateKey, 2); err != nil {
			return nil, err
		}
		return []string{hex.EncodeToString(tx.Bytes())}, nil
	}

	return &AdminPrepared{Tx: tx, Sighashes: sighashes, finalize: finalize}, nil
}

// PrepareFreezeCoinUTXO freezes a set of coin UTXOs under lockTime. Each FT
// input is admin-MuSig-signed; the trailing fee input is ECDSA. Mirrors TS
// stableCoin.freezeCoinUTXO.
func (sc *StableCoin) PrepareFreezeCoinUTXO(
	aggPubkey32 []byte,
	feePrivateKey *bec.PrivateKey,
	lockTime uint32,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) (*AdminPrepared, error) {
	return sc.prepareFreezeUnfreeze(aggPubkey32, feePrivateKey, lockTime, true, ftutxos, feeUTXO, preTXs, prepreTxDatas)
}

// PrepareUnfreezeCoinUTXO releases coin UTXOs from a lockTime. Mirrors TS
// stableCoin.unfreezeCoinUTXO.
func (sc *StableCoin) PrepareUnfreezeCoinUTXO(
	aggPubkey32 []byte,
	feePrivateKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) (*AdminPrepared, error) {
	return sc.prepareFreezeUnfreeze(aggPubkey32, feePrivateKey, 0, false, ftutxos, feeUTXO, preTXs, prepreTxDatas)
}

func (sc *StableCoin) prepareFreezeUnfreeze(
	aggPubkey32 []byte,
	feePrivateKey *bec.PrivateKey,
	newLockTime uint32,
	isFreeze bool,
	ftutxos []*util.FtUTXO,
	feeUTXO *bt.UTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) (*AdminPrepared, error) {
	if len(aggPubkey32) != 32 {
		return nil, fmt.Errorf("aggPubkey32 must be 32 bytes (x-only)")
	}
	if len(ftutxos) == 0 {
		return nil, fmt.Errorf("no FT UTXO available")
	}
	if len(ftutxos) > 5 {
		return nil, fmt.Errorf("too many FT UTXOs (max 5)")
	}
	address, _, err := GetAddressFromCode(hex.EncodeToString(ftutxos[0].LockingScript.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("read holder from coin code: %w", err)
	}

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	var lockTimeMax uint32
	for i, fu := range ftutxos {
		tapeAmountSet[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
		if isFreeze {
			lt, err := GetLockTimeFromTape(preTXs[i].Outputs[fu.Vout+1].LockingScript)
			if err != nil {
				return nil, fmt.Errorf("read locktime from input %d tape: %w", i, err)
			}
			if lt > lockTimeMax {
				lockTimeMax = lt
			}
		}
	}
	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSet)
	if changeHex != strings.Repeat("0", 96) {
		return nil, fmt.Errorf("change amount is not zero")
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(feeUTXO); err != nil {
		return nil, err
	}
	codeScript, err := BuildFTtransferCode(sc.CodeScript, address)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScriptBase, err := BuildFTtransferTape(sc.TapeScript, amountHex)
	if err != nil {
		return nil, err
	}
	tapeScript, err := SetLockTimeInTape(tapeScriptBase, newLockTime)
	if err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	for i := range ftutxos {
		tx.Inputs[i].SequenceNumber = stablecoinSequenceNoLockTime
	}
	tx.LockTime = lockTimeMax

	feeAddr, err := bscript.NewAddressFromPublicKey(feePrivateKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	if err := tx.ChangeToAddress(feeAddr.AddressString, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("freeze ChangeToAddress: %w", err)
	}

	xOnlyHex := hex.EncodeToString(aggPubkey32)
	const isCoinFlag = true
	rebuilders := make([]scAdminBuilder, len(ftutxos))
	for i := range ftutxos {
		idx := i
		rebuilders[i] = scAdminBuilder{
			InputIndex: uint32(idx),
			Build: func(sig64 []byte) (*bscript.Script, error) {
				sigHex := encodeSchnorrSig65Hex(sig64)
				return StaticGetFTunlock(
					sigHex, xOnlyHex, tx, preTXs[idx], prepreTxDatas[idx],
					idx, int(ftutxos[idx].Vout), isCoinFlag,
				)
			},
		}
	}
	feeIdx := len(ftutxos)
	if err := scPreseedAndFreezeFee(tx, feePrivateKey, rebuilders, []int{feeIdx}); err != nil {
		return nil, err
	}

	adminIdx := make([]uint32, len(ftutxos))
	for i := range ftutxos {
		adminIdx[i] = uint32(i)
	}
	sighashes, err := scComputeSighashes(tx, adminIdx)
	if err != nil {
		return nil, err
	}

	finalize := func(sigs64 [][]byte) ([]string, error) {
		if len(sigs64) != len(ftutxos) {
			return nil, fmt.Errorf("freeze/unfreeze.Finalize: expected %d Schnorr sigs, got %d", len(ftutxos), len(sigs64))
		}
		for i := range ftutxos {
			us, err := rebuilders[i].Build(sigs64[i])
			if err != nil {
				return nil, err
			}
			if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
				return nil, err
			}
		}
		if err := signP2PKHAtIdx(tx, feePrivateKey, uint32(feeIdx)); err != nil {
			return nil, err
		}
		return []string{hex.EncodeToString(tx.Bytes())}, nil
	}

	return &AdminPrepared{Tx: tx, Sighashes: sighashes, finalize: finalize}, nil
}

// =============================================================================
// Static helpers — coin NFT / mint code
// =============================================================================

// BuildCoinNftOutput builds the 3 outputs of a coin-NFT issuance certificate.
// Mirrors TS stableCoin.buildCoinNftOutput.
func BuildCoinNftOutput(nftCode, nftHold, nftTape *bscript.Script) []*bt.Output {
	return []*bt.Output{
		{LockingScript: nftCode, Satoshis: 200},
		{LockingScript: nftHold, Satoshis: 100},
		{LockingScript: nftTape, Satoshis: 0},
	}
}

// BuildCoinNftTX constructs the coin-NFT-creation tx. Funded and ECDSA-signed
// by feePrivateKey; the hold-script output is sent to HASH160(adminPubHash20)
// so the later mint tx is unlocked by the Schnorr MuSig aggregate key.
//
// Mirrors TS stableCoin.buildCoinNftTX.
func BuildCoinNftTX(feePrivateKey *bec.PrivateKey, adminPubHash20Hex string, utxo *bt.UTXO, data *CoinNftData) (*bt.Tx, error) {
	feeAddr, err := bscript.NewAddressFromPublicKey(feePrivateKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	nftCode, err := GetCoinNftCode(hex.EncodeToString(utxo.TxID), utxo.Vout)
	if err != nil {
		return nil, err
	}
	nftHold, err := GetCoinNftHoldScriptFromHash(adminPubHash20Hex, data.NftName)
	if err != nil {
		return nil, err
	}
	nftTape, err := GetCoinNftTapeScript(data)
	if err != nil {
		return nil, err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return nil, err
	}
	for _, out := range BuildCoinNftOutput(nftCode, nftHold, nftTape) {
		tx.AddOutput(out)
	}
	if err := tx.ChangeToAddress(feeAddr.AddressString, newFeeQuote80()); err != nil {
		return nil, fmt.Errorf("coin NFT ChangeToAddress: %w", err)
	}
	if err := signP2PKHAtIdx(tx, feePrivateKey, 0); err != nil {
		return nil, err
	}
	return tx, nil
}

// GetCoinMintCode builds the FT mint code script for stableCoin. adminPubHash20Hex
// is HASH160(xOnly aggregate pubkey). receiveAddress is the initial mint
// recipient. codeHash is sha256 of the coin-NFT code script. tapeSize is the
// tape script byte length. Mirrors TS stableCoin.getCoinMintCode.
func GetCoinMintCode(adminPubHash20Hex, receiveAddress, codeHash string, tapeSize int) (*bscript.Script, error) {
	if tapeSize <= 0 {
		return nil, fmt.Errorf("invalid tapeSize")
	}
	recv, err := bscript.NewAddressFromString(receiveAddress)
	if err != nil {
		return nil, err
	}
	hash := recv.PublicKeyHash + "00"
	tapeSizeHex := hex.EncodeToString(util.GetSize(tapeSize))

	asm := stablecoinMintTemplateASM
	asm = strings.ReplaceAll(asm, "${adminPubHash}", adminPubHash20Hex)
	asm = strings.ReplaceAll(asm, "${codeHash}", codeHash)
	asm = strings.ReplaceAll(asm, "${tapeSizeHex}", tapeSizeHex)
	asm = strings.ReplaceAll(asm, "${hash}", hash)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// SetLockTimeInTape mutates the lockTime chunk in-place (chunks[len-2].buf,
// 4 bytes LE). lockTime=0 means unlock; otherwise must be ≥ 500_000_000
// (Unix epoch). Mirrors TS stableCoin.setLockTimeInTape.
func SetLockTimeInTape(tapeScript *bscript.Script, lockTime uint32) (*bscript.Script, error) {
	if lockTime != 0 && lockTime < 500000000 {
		return nil, fmt.Errorf("lockTime must be a Unix timestamp (>= 500000000)")
	}
	chunks := tapeScript.Chunks()
	if len(chunks) < 2 {
		return nil, fmt.Errorf("tape script too short for setLockTimeInTape")
	}
	idx := len(chunks) - 2
	c := &chunks[idx]
	if len(c.Buf) < 4 {
		return nil, fmt.Errorf("tape lockTime chunk has no usable buffer")
	}
	binary.LittleEndian.PutUint32(c.Buf[:4], lockTime)
	out, err := bscript.FromChunks(chunks)
	if err != nil {
		return nil, fmt.Errorf("re-serialise tape chunks: %w", err)
	}
	return out, nil
}

// GetLockTimeFromTape reads the 4-byte LE lockTime field from the tape.
// Mirrors TS stableCoin.getLockTimeFromTape.
func GetLockTimeFromTape(tapeScript *bscript.Script) (uint32, error) {
	chunks := tapeScript.Chunks()
	if len(chunks) < 2 {
		return 0, fmt.Errorf("tape script too short")
	}
	c := chunks[len(chunks)-2]
	if len(c.Buf) < 4 {
		return 0, fmt.Errorf("tape lockTime chunk has no usable buffer")
	}
	return binary.LittleEndian.Uint32(c.Buf[:4]), nil
}

// GetAddressFromCode parses the 21-byte holder hash chunk from a coin code
// script: 20-byte pkh + 1-byte type tag (0x00 = address, else contract).
// Returns the address (or hex contract hash) and an isContract flag.
//
// Mirrors TS stableCoin.getAddressFromCode.
func GetAddressFromCode(codeScriptHex string) (string, bool, error) {
	raw, err := hex.DecodeString(codeScriptHex)
	if err != nil {
		return "", false, fmt.Errorf("invalid code hex: %w", err)
	}
	s := bscript.NewFromBytes(raw)
	chunks := s.Chunks()
	if len(chunks) < 2 {
		return "", false, fmt.Errorf("code script has < 2 chunks")
	}
	c := chunks[len(chunks)-2]
	if len(c.Buf) < 21 {
		return "", false, fmt.Errorf("holder chunk shorter than 21 bytes")
	}
	pkh := c.Buf[:20]
	typeTag := c.Buf[20]
	if typeTag == 0x00 {
		addr, err := bscript.NewAddressFromPublicKeyHash(pkh, true)
		if err != nil {
			return "", false, err
		}
		return addr.AddressString, false, nil
	}
	return hex.EncodeToString(pkh), true, nil
}

// =============================================================================
// Coin-NFT helpers (TS coinNft.*)
// =============================================================================

// GetCoinNftCode mirrors TS coinNft.getCoinNftCode.
func GetCoinNftCode(txHash string, outputIndex uint32) (*bscript.Script, error) {
	rev, err := hex.DecodeString(txHash)
	if err != nil || len(rev) != 32 {
		return nil, fmt.Errorf("invalid txHash for GetCoinNftCode")
	}
	internal := make([]byte, 32)
	for i := 0; i < 32; i++ {
		internal[i] = rev[31-i]
	}
	voutBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(voutBuf, outputIndex)
	txIDVout := hex.EncodeToString(internal) + hex.EncodeToString(voutBuf)

	asm := stablecoinCoinNftCodeTemplateASM
	asm = strings.ReplaceAll(asm, "${txIDVout}", txIDVout)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// GetCoinNftHoldScript returns the hold script bound to a conventional
// address. Mirrors TS coinNft.getHoldScript. Used by callers managing the
// coin NFT with a regular ECDSA key (legacy path).
func GetCoinNftHoldScript(address, flag string) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	flagHex := hex.EncodeToString([]byte("For Coin " + flag + " NHold"))
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s",
		addr.PublicKeyHash, flagHex)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// GetCoinNftHoldScriptFromHash is the Schnorr MuSig variant: the holder is
// HASH160(xOnly aggregate pubkey 32B). Mirrors TS coinNft.getHoldScriptFromHash.
func GetCoinNftHoldScriptFromHash(pubKeyHash20Hex, flag string) (*bscript.Script, error) {
	if len(pubKeyHash20Hex) != 40 {
		return nil, fmt.Errorf("pubKeyHash20Hex must be 40 hex chars")
	}
	if _, err := hex.DecodeString(pubKeyHash20Hex); err != nil {
		return nil, fmt.Errorf("invalid pubKeyHash20Hex: %w", err)
	}
	flagHex := hex.EncodeToString([]byte("For Coin " + flag + " NHold"))
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s",
		pubKeyHash20Hex, flagHex)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// GetCoinNftTapeScript builds the JSON-tape script for a coin-NFT issuance
// certificate. Mirrors TS coinNft.getTapeScript.
func GetCoinNftTapeScript(data *CoinNftData) (*bscript.Script, error) {
	j, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", hex.EncodeToString(j))
	return bscript.NewFromASM(asm)
}

// UpdateCoinNftTapeScript rewrites the coinTotalSupply field in the existing
// tape script. Mirrors TS coinNft.updateTapeScript.
func UpdateCoinNftTapeScript(tape *bscript.Script, newTotalSupply string) (*bscript.Script, error) {
	chunks := tape.Chunks()
	if len(chunks) < 2 {
		return nil, fmt.Errorf("tape script too short")
	}
	dataChunk := chunks[len(chunks)-2].Buf
	if dataChunk == nil {
		return nil, fmt.Errorf("tape data chunk empty")
	}
	var data map[string]interface{}
	if err := json.Unmarshal(dataChunk, &data); err != nil {
		return nil, fmt.Errorf("decode tape JSON: %w", err)
	}
	data["coinTotalSupply"] = newTotalSupply
	newJSON, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", hex.EncodeToString(newJSON))
	return bscript.NewFromASM(asm)
}

// DecodeCoinNftTapeScript reads the JSON-tape data chunk back to a generic
// map. Mirrors TS coinNft.decodeTapeScript.
func DecodeCoinNftTapeScript(tape *bscript.Script) (map[string]interface{}, error) {
	chunks := tape.Chunks()
	if len(chunks) < 2 {
		return nil, fmt.Errorf("tape script too short")
	}
	c := chunks[len(chunks)-2].Buf
	if c == nil {
		return nil, fmt.Errorf("tape data chunk empty")
	}
	var data map[string]interface{}
	if err := json.Unmarshal(c, &data); err != nil {
		return nil, fmt.Errorf("decode tape JSON: %w", err)
	}
	return data, nil
}

// BuildCoinNftUnlockScriptSchnorr produces the unlock script for an input
// spending a coin-NFT code output, where the authorising signature is a
// 64-byte BIP340 Schnorr signature over the SIGHASH_ALL_FORKID digest. The
// on-chain OP_CHECKSIG dispatches on sig length (64 → Schnorr) and pubkey
// length (32 → x-only), so HASH160(xOnlyPubkey32) must match the embedded
// admin pubkey hash.
//
// Mirrors TS coinNft.buildUnlockScriptSchnorr.
func BuildCoinNftUnlockScriptSchnorr(
	schnorrSig64 []byte,
	xOnlyPubkey32 []byte,
	currentTX *bt.Tx,
	preTX *bt.Tx,
	prepreTX *bt.Tx,
	currentUnlockIndex uint32,
) (*bscript.Script, error) {
	if len(xOnlyPubkey32) != 32 {
		return nil, fmt.Errorf("xOnlyPubkey32 must be 32 bytes")
	}
	if len(schnorrSig64) != 64 {
		return nil, fmt.Errorf("schnorrSig64 must be 64 bytes")
	}
	cur, err := util.GetNFTCurrentTxdata(currentTX)
	if err != nil {
		return nil, err
	}
	prepre, err := util.GetNFTPrePreTxdata(prepreTX)
	if err != nil {
		return nil, err
	}
	pre, err := util.GetNFTPreTxdata(preTX)
	if err != nil {
		return nil, err
	}
	// Push the 65-byte sig (sig64 || sighash flag) followed by the 32-byte
	// x-only pubkey. Both lengths are < 76 so the push opcode IS the length.
	sigHex := encodeSchnorrSig65PushHex(schnorrSig64)
	pubHex := "20" + hex.EncodeToString(xOnlyPubkey32)
	return bscript.NewFromHexString(sigHex + pubHex + cur + prepre + pre)
}

// =============================================================================
// internal helpers
// =============================================================================

// scAdminBuilder describes one admin-signed input: where to install the unlock
// script (InputIndex) and how to build it from a 64-byte Schnorr sig.
type scAdminBuilder struct {
	InputIndex uint32
	Build      func(sig64 []byte) (*bscript.Script, error)
}

// scPreseedAndFreezeFee installs dummy-sig admin unlock scripts so the byte
// layout is final, then runs a two-pass ECDSA fee adjust on the listed fee
// inputs. The second pass converges because admin-input length is fixed
// (64-byte placeholder) and ECDSA RFC-6979 makes fee-input sigs deterministic.
//
// After this returns the tx is fee-stable: subsequent changes to outputs must
// be avoided, otherwise admin sighashes would shift.
func scPreseedAndFreezeFee(
	tx *bt.Tx,
	feePrivKey *bec.PrivateKey,
	admins []scAdminBuilder,
	feeInputIdxs []int,
) error {
	return finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		for _, b := range admins {
			us, err := b.Build(dummySchnorrSig64)
			if err != nil {
				return fmt.Errorf("preseed admin input %d: %w", b.InputIndex, err)
			}
			if err := tx.InsertInputUnlockingScript(b.InputIndex, us); err != nil {
				return err
			}
		}
		for _, fi := range feeInputIdxs {
			if err := signP2PKHAtIdx(tx, feePrivKey, uint32(fi)); err != nil {
				return err
			}
		}
		return nil
	})
}

// scComputeSighashes returns the 32-byte SHA256d sighashes for the given
// input indices using SIGHASH_ALL_FORKID. The values can be handed to an
// external BIP340 / MuSig2 signer.
func scComputeSighashes(tx *bt.Tx, inputs []uint32) ([]AdminSighash, error) {
	out := make([]AdminSighash, len(inputs))
	for i, idx := range inputs {
		sh, err := tx.CalcInputSignatureHash(idx, sighash.AllForkID)
		if err != nil {
			return nil, fmt.Errorf("compute sighash for input %d: %w", idx, err)
		}
		out[i] = AdminSighash{InputIndex: idx, Sighash: sh}
	}
	return out, nil
}

// scSignAllOwner signs every FT input (isCoin=true) and every fee input on a
// stableCoin owner-signed tx.
func scSignAllOwner(
	tx *bt.Tx,
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) error {
	if err := ftSignFeeInputs(tx, privKey, len(ftutxos)); err != nil {
		return err
	}
	for i, fu := range ftutxos {
		us, err := ftBuildUnlock(privKey, tx, preTXs[i], prepreTxDatas[i], i, int(fu.Vout), true /* isCoin */)
		if err != nil {
			return fmt.Errorf("build coin unlock input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return fmt.Errorf("insert coin unlock %d: %w", i, err)
		}
	}
	return nil
}

// scAdjustFeeAndResign finalizes the actual signed-tx fee and re-signs every
// input until the serialized size and change amount converge.
func scAdjustFeeAndResign(
	tx *bt.Tx,
	privKey *bec.PrivateKey,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
	prepreTxDatas []string,
) error {
	return finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		return scSignAllOwner(tx, privKey, ftutxos, preTXs, prepreTxDatas)
	})
}

// encodeSchnorrSig65Hex returns the raw 65-byte signature data: the 64-byte
// BIP340 signature followed by SIGHASH_ALL_FORKID.
func encodeSchnorrSig65Hex(sig64 []byte) string {
	return fmt.Sprintf("%s%02x", hex.EncodeToString(sig64), byte(sighash.AllForkID))
}

// encodeSchnorrSig65PushHex adds the direct 65-byte push opcode used when
// constructing a complete unlocking script by concatenating raw script hex.
func encodeSchnorrSig65PushHex(sig64 []byte) string {
	return "41" + encodeSchnorrSig65Hex(sig64)
}

// buildSchnorrP2PKHLikeUnlock builds the unlock for a coin-NFT-hold input,
// which is a P2PKH-style script over an x-only Schnorr pubkey:
//
//	<65-byte sig+flag> <32-byte xonly pubkey>
func buildSchnorrP2PKHLikeUnlock(sig64 []byte, xOnlyPubkey32 []byte) (*bscript.Script, error) {
	if len(sig64) != 64 {
		return nil, fmt.Errorf("sig64 must be 64 bytes")
	}
	if len(xOnlyPubkey32) != 32 {
		return nil, fmt.Errorf("xOnlyPubkey32 must be 32 bytes")
	}
	hexStr := encodeSchnorrSig65PushHex(sig64) + "20" + hex.EncodeToString(xOnlyPubkey32)
	return bscript.NewFromHexString(hexStr)
}

// buildOpReturnMessage assembles `OP_FALSE OP_RETURN <msgHex>` for a tx
// memo output.
func buildOpReturnMessage(msg string) (*bscript.Script, error) {
	return bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s", hex.EncodeToString([]byte(msg))))
}

// buildStableCoinTapeScript constructs the stableCoin tape script. The amount
// is the head field; lockTime is the per-tape lockTime (0 = unlocked).
//
// Layout: OP_FALSE OP_RETURN <48B amount tape> <decimal hex> <name> <symbol>
//
//	<4B lockTime LE> 4654617065  // "FTape"
func buildStableCoinTapeScript(name, symbol string, decimal int, headAmount *big.Int, lockTime uint32) (*bscript.Script, error) {
	tape := writeTapeAmount(headAmount)
	nameHex := hex.EncodeToString([]byte(name))
	symbolHex := hex.EncodeToString([]byte(symbol))
	decimalHex := fmt.Sprintf("%02x", decimal)
	ltBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(ltBuf, lockTime)
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s %s 4654617065",
		tape, decimalHex, nameHex, symbolHex, hex.EncodeToString(ltBuf))
	return bscript.NewFromASM(asm)
}
