package main

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const ftV3PartialOffset = 1856

type plannedTransaction struct {
	Label string
	Raw   string
}

type ftLifecyclePlan struct {
	Token        *contract.FT
	Transactions []plannedTransaction
	Source       *bt.Tx
	Mint         *bt.Tx
	Transfer     *bt.Tx
	Additional   *bt.Tx
	Batch        *bt.Tx
	Merge        *bt.Tx
}

type tokenHTLCPlan struct {
	Transactions   []plannedTransaction
	Hashlock       string
	secret         string
	WithdrawDeploy *bt.Tx
	Withdraw       *bt.Tx
	RefundDeploy   *bt.Tx
	Refund         *bt.Tx
}

type plannedTransactionAcceptor func(
	item plannedTransaction,
	validate func(*bt.Tx) error,
) (*bt.Tx, error)

func validateFTV3Outputs(tx *bt.Tx, codeVout int) (*big.Int, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil FT transaction")
	}
	tapeVout := codeVout + 1
	if codeVout < 0 || tapeVout >= len(tx.Outputs) {
		return nil, fmt.Errorf("FT Code/Tape outputs %d/%d are out of range", codeVout, tapeVout)
	}
	codeOutput := tx.Outputs[codeVout]
	tapeOutput := tx.Outputs[tapeVout]
	if codeOutput.Satoshis != 500 {
		return nil, fmt.Errorf("FT code output satoshis=%d want=500", codeOutput.Satoshis)
	}
	if tapeOutput.Satoshis != 0 || !tapeOutput.LockingScript.IsSafeDataOut() {
		return nil, fmt.Errorf("FT Tape output is not zero-satoshi safe data")
	}
	codeBytes := codeOutput.LockingScript.Bytes()
	if len(codeBytes) != contractutil.FTV4CodeLength {
		return nil, fmt.Errorf("FT code length=%d want=%d", len(codeBytes), contractutil.FTV4CodeLength)
	}
	info, err := contractutil.ClassifyFTScript(codeOutput.LockingScript)
	if err != nil {
		return nil, err
	}
	if info.Version != contractutil.FTVersion4 || info.IsCoin {
		return nil, fmt.Errorf("FT code classified version=%d coin=%t, want ordinary v4", info.Version, info.IsCoin)
	}
	partialHash, err := contract.ComputeFtPartialHash(codeOutput.LockingScript.ToHex(), false)
	if err != nil {
		return nil, err
	}
	wantPartialHash := partialsha256.CalculatePartialHash(codeBytes[:contractutil.FTV4PartialOffset])
	if partialHash != wantPartialHash {
		return nil, fmt.Errorf("FT v4 partial hash mismatch")
	}
	balance, err := contractutil.GetFtBalanceFromTape(tapeOutput.LockingScript.ToHex())
	if err != nil {
		return nil, fmt.Errorf("decode FT Tape balance: %w", err)
	}
	if balance.Sign() < 0 {
		return nil, fmt.Errorf("negative FT Tape balance")
	}
	return balance, nil
}

func parsePlannedTransaction(label, raw string) (*bt.Tx, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", label, err)
	}
	return tx, nil
}

func temporaryTestnetAddress() (string, error) {
	privateKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		return "", err
	}
	address, err := bscript.NewAddressFromPublicKey(privateKey.PubKey(), false)
	if err != nil {
		return "", err
	}
	return address.AddressString, nil
}

func buildFTLifecyclePlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
) (*ftLifecyclePlan, error) {
	token, err := contract.NewFT(&contract.FtParams{
		Name: "GoFullMatrix", Symbol: "GFM", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		return nil, err
	}
	mintRaws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		return nil, fmt.Errorf("FT mint build: %w", err)
	}
	if len(mintRaws) != 2 {
		return nil, fmt.Errorf("FT mint returned %d transactions, want 2", len(mintRaws))
	}
	source, err := parsePlannedTransaction("ft-source", mintRaws[0])
	if err != nil {
		return nil, err
	}
	mint, err := parsePlannedTransaction("ft-mint", mintRaws[1])
	if err != nil {
		return nil, err
	}
	mintedBalance, err := validateFTV3Outputs(mint, 0)
	if err != nil {
		return nil, err
	}
	if mintedBalance.Cmp(big.NewInt(100_000_000)) != 0 {
		return nil, fmt.Errorf("FT minted balance=%s want=100000000", mintedBalance)
	}

	mintedFT, err := ftUTXOFromTX(mint, 0)
	if err != nil {
		return nil, err
	}
	transferFee, err := changeUTXO(mint)
	if err != nil {
		return nil, err
	}
	mintPrePre, err := localPrePre(source, 0)
	if err != nil {
		return nil, err
	}
	transferRaw, err := token.Transfer(
		privateKey,
		address,
		big.NewInt(20_000_000),
		[]*contractutil.FtUTXO{mintedFT},
		transferFee,
		[]*bt.Tx{mint},
		[]string{mintPrePre},
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("FT transfer build: %w", err)
	}
	transfer, err := parsePlannedTransaction("ft-transfer", transferRaw)
	if err != nil {
		return nil, err
	}
	if _, err := validateFTV3Outputs(transfer, 0); err != nil {
		return nil, err
	}
	if _, err := validateFTV3Outputs(transfer, 2); err != nil {
		return nil, err
	}

	additionalFT, err := ftUTXOFromTX(transfer, 0)
	if err != nil {
		return nil, err
	}
	additionalFee, err := changeUTXO(transfer)
	if err != nil {
		return nil, err
	}
	transferPrePre, err := localPrePre(mint, 0)
	if err != nil {
		return nil, err
	}
	additionalRaw, err := token.TransferWithAdditionalInfo(
		privateKey,
		address,
		big.NewInt(10_000_000),
		[]*contractutil.FtUTXO{additionalFT},
		additionalFee,
		[]*bt.Tx{transfer},
		[]string{transferPrePre},
		[]byte("go-js-1.6.5-full-matrix"),
	)
	if err != nil {
		return nil, fmt.Errorf("FT additional-info transfer build: %w", err)
	}
	additional, err := parsePlannedTransaction("ft-transfer-additional-info", additionalRaw)
	if err != nil {
		return nil, err
	}
	if _, err := validateFTV3Outputs(additional, 0); err != nil {
		return nil, err
	}
	if _, err := validateFTV3Outputs(additional, 2); err != nil {
		return nil, err
	}

	batchFT, err := ftUTXOFromTX(transfer, 2)
	if err != nil {
		return nil, err
	}
	batchFee, err := changeUTXO(additional)
	if err != nil {
		return nil, err
	}
	recipientA, err := temporaryTestnetAddress()
	if err != nil {
		return nil, err
	}
	recipientB, err := temporaryTestnetAddress()
	if err != nil {
		return nil, err
	}
	batchRaws, err := token.BatchTransfer(
		privateKey,
		[]contract.AddressAmount{
			{Address: recipientA, Amount: big.NewInt(5_000_000)},
			{Address: recipientB, Amount: big.NewInt(7_000_000)},
		},
		[]*contractutil.FtUTXO{batchFT},
		batchFee,
		[]*bt.Tx{transfer},
		[]string{transferPrePre},
	)
	if err != nil {
		return nil, fmt.Errorf("FT batch transfer build: %w", err)
	}
	if len(batchRaws) != 1 {
		return nil, fmt.Errorf("FT batch returned %d transactions, want 1", len(batchRaws))
	}
	batch, err := parsePlannedTransaction("ft-batch-transfer", batchRaws[0])
	if err != nil {
		return nil, err
	}
	for _, codeVout := range []int{0, 2, 4} {
		if _, err := validateFTV3Outputs(batch, codeVout); err != nil {
			return nil, err
		}
	}

	mergeA, err := ftUTXOFromTX(additional, 0)
	if err != nil {
		return nil, err
	}
	mergeB, err := ftUTXOFromTX(additional, 2)
	if err != nil {
		return nil, err
	}
	mergeC, err := ftUTXOFromTX(batch, 4)
	if err != nil {
		return nil, err
	}
	mergeFee, err := changeUTXO(batch)
	if err != nil {
		return nil, err
	}
	additionalPrePre, err := localPrePre(transfer, 0)
	if err != nil {
		return nil, err
	}
	batchPrePre, err := localPrePre(transfer, 2)
	if err != nil {
		return nil, err
	}
	mergeRaws, err := token.MergeFT(
		privateKey,
		[]*contractutil.FtUTXO{mergeA, mergeB, mergeC},
		mergeFee,
		[]*bt.Tx{additional, additional, batch},
		[]string{additionalPrePre, additionalPrePre, batchPrePre},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("FT merge build: %w", err)
	}
	if len(mergeRaws) != 1 {
		return nil, fmt.Errorf("FT merge returned %d transactions, want 1", len(mergeRaws))
	}
	merge, err := parsePlannedTransaction("ft-merge", mergeRaws[0])
	if err != nil {
		return nil, err
	}
	if _, err := validateFTV3Outputs(merge, 0); err != nil {
		return nil, err
	}

	return &ftLifecyclePlan{
		Token: token,
		Transactions: []plannedTransaction{
			{Label: "ft-source", Raw: mintRaws[0]},
			{Label: "ft-mint", Raw: mintRaws[1]},
			{Label: "ft-transfer", Raw: transferRaw},
			{Label: "ft-transfer-additional-info", Raw: additionalRaw},
			{Label: "ft-batch-transfer", Raw: batchRaws[0]},
			{Label: "ft-merge", Raw: mergeRaws[0]},
		},
		Source:     source,
		Mint:       mint,
		Transfer:   transfer,
		Additional: additional,
		Batch:      batch,
		Merge:      merge,
	}, nil
}

func requireFTBalance(tx *bt.Tx, codeVout int, want int64) error {
	balance, err := validateFTV3Outputs(tx, codeVout)
	if err != nil {
		return err
	}
	if balance.Cmp(big.NewInt(want)) != 0 {
		return fmt.Errorf("FT output %d balance=%s want=%d", codeVout, balance, want)
	}
	return nil
}

func validateFTLifecycleTransaction(label string, tx *bt.Tx) error {
	switch label {
	case "ft-source":
		if tx == nil || len(tx.Outputs) != 3 {
			return fmt.Errorf("FT source outputs=%d want=3", len(tx.Outputs))
		}
		if tx.Version != 10 {
			return fmt.Errorf("FT source version=%d want=10", tx.Version)
		}
		if tx.Outputs[0].Satoshis < 10_000 {
			return fmt.Errorf("FT source contract dust=%d want>=10000", tx.Outputs[0].Satoshis)
		}
		if tx.Outputs[1].Satoshis != 0 || !tx.Outputs[1].LockingScript.IsSafeDataOut() {
			return fmt.Errorf("FT source Tape is not zero-satoshi safe data")
		}
		return nil
	case "ft-mint":
		return requireFTBalance(tx, 0, 100_000_000)
	case "ft-transfer":
		if err := requireFTBalance(tx, 0, 20_000_000); err != nil {
			return err
		}
		return requireFTBalance(tx, 2, 80_000_000)
	case "ft-transfer-additional-info":
		if err := requireFTBalance(tx, 0, 10_000_000); err != nil {
			return err
		}
		if err := requireFTBalance(tx, 2, 10_000_000); err != nil {
			return err
		}
		if len(tx.Outputs) < 2 {
			return fmt.Errorf("additional-info transaction has too few outputs")
		}
		info := tx.Outputs[len(tx.Outputs)-2]
		if info.Satoshis != 0 || !info.LockingScript.IsSafeDataOut() {
			return fmt.Errorf("additional-info output is not immediately before TBC change")
		}
		return nil
	case "ft-batch-transfer":
		for codeVout, want := range map[int]int64{
			0: 5_000_000,
			2: 7_000_000,
			4: 68_000_000,
		} {
			if err := requireFTBalance(tx, codeVout, want); err != nil {
				return err
			}
		}
		return nil
	case "ft-merge":
		return requireFTBalance(tx, 0, 88_000_000)
	default:
		return fmt.Errorf("unknown FT lifecycle label %q", label)
	}
}

func executeFTLifecyclePlan(
	plan *ftLifecyclePlan,
	accept plannedTransactionAcceptor,
) (publicState, error) {
	if plan == nil || plan.Token == nil {
		return publicState{}, fmt.Errorf("nil FT lifecycle plan")
	}
	if accept == nil {
		return publicState{}, fmt.Errorf("nil FT transaction acceptor")
	}
	state := publicState{
		TokenID: plan.Token.ContractTxid,
	}
	codeHash, err := contractutil.ScriptHash(plan.Token.CodeScript)
	if err != nil {
		return publicState{}, fmt.Errorf("FT code hash: %w", err)
	}
	state.TokenCode = codeHash
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, err := accept(item, func(tx *bt.Tx) error {
			return validateFTLifecycleTransaction(label, tx)
		})
		if err != nil {
			return publicState{}, fmt.Errorf("%s: %w", label, err)
		}
		if accepted == nil {
			return publicState{}, fmt.Errorf("%s: acceptor returned nil transaction", label)
		}
		state.LastTxID = accepted.TxID()
		state.LastVout = 0
	}
	return state, nil
}

func runFTStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(address, 0.02, cfg.Network)
	if err != nil {
		return fmt.Errorf("FT funding: %w", err)
	}
	plan, err := buildFTLifecyclePlan(decoded.PrivKey, address, funding)
	if err != nil {
		return err
	}
	state, err := executeFTLifecyclePlan(
		plan,
		func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
			accepted, _, err := broadcastAndVerify(
				item.Label,
				item.Raw,
				cfg.Network,
				"ft-v3-layout-and-amounts",
				validate,
			)
			return accepted, err
		},
	)
	if err != nil {
		return err
	}
	timelock, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return fmt.Errorf("Token HTLC lock height: %w", err)
	}
	if timelock > 0 {
		timelock--
	}
	htlcPlan, err := buildTokenHTLCPlan(plan, decoded.PrivKey, address, timelock)
	if err != nil {
		return err
	}
	for _, item := range htlcPlan.Transactions {
		label := item.Label
		accepted, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"token-htlc-layout-amounts-locktime",
			func(tx *bt.Tx) error {
				return validateTokenHTLCTransaction(label, tx, timelock)
			},
		)
		if err != nil {
			return err
		}
		state.LastTxID = accepted.TxID()
		state.LastVout = 0
	}
	return writePublicState(os.Stdout, state)
}

func buildTokenHTLCPlan(
	ftPlan *ftLifecyclePlan,
	privateKey *bec.PrivateKey,
	address string,
	timelock uint32,
) (*tokenHTLCPlan, error) {
	if ftPlan == nil || ftPlan.Merge == nil {
		return nil, fmt.Errorf("FT lifecycle merge transaction is required")
	}
	secretBytes := make([]byte, 32)
	if _, err := cryptorand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generate Token HTLC secret: %w", err)
	}
	secret := hex.EncodeToString(secretBytes)
	hashlock := hex.EncodeToString(crypto.Sha256(secretBytes))

	mergedFT, err := ftUTXOFromTX(ftPlan.Merge, 0)
	if err != nil {
		return nil, err
	}
	deployFee, err := changeUTXO(ftPlan.Merge)
	if err != nil {
		return nil, err
	}
	mergePrePre, err := contractutil.BuildFtPrePreTxData(
		ftPlan.Merge,
		0,
		[]*bt.Tx{ftPlan.Additional, ftPlan.Batch},
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC merge ancestry: %w", err)
	}
	withdrawDeployRaw, err := contract.DeployHTLCTokenWithSign(
		address,
		address,
		hashlock,
		timelock,
		big.NewInt(4_000_000),
		[]*contractutil.FtUTXO{mergedFT},
		deployFee,
		[]*bt.Tx{ftPlan.Merge},
		[]string{mergePrePre},
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC withdraw deploy build: %w", err)
	}
	withdrawDeploy, err := parsePlannedTransaction(
		"token-htlc-withdraw-deploy", withdrawDeployRaw,
	)
	if err != nil {
		return nil, err
	}

	withdrawContract, err := outputUTXO(withdrawDeploy, 0)
	if err != nil {
		return nil, err
	}
	withdrawFT, err := ftUTXOFromTX(withdrawDeploy, 1)
	if err != nil {
		return nil, err
	}
	withdrawFee, err := changeUTXO(withdrawDeploy)
	if err != nil {
		return nil, err
	}
	withdrawPrePre, err := contractutil.BuildFtPrePreTxData(
		withdrawDeploy,
		1,
		[]*bt.Tx{ftPlan.Merge},
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC withdraw ancestry: %w", err)
	}
	withdrawRaw, err := contract.WithdrawHTLCTokenWithSign(
		privateKey,
		address,
		withdrawContract,
		withdrawFT,
		withdrawDeploy,
		withdrawPrePre,
		withdrawFee,
		secret,
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC withdraw build: %w", err)
	}
	withdraw, err := parsePlannedTransaction("token-htlc-withdraw", withdrawRaw)
	if err != nil {
		return nil, err
	}

	refundInput, err := ftUTXOFromTX(withdrawDeploy, 3)
	if err != nil {
		return nil, err
	}
	refundDeployFee, err := changeUTXO(withdraw)
	if err != nil {
		return nil, err
	}
	refundDeployPrePre, err := contractutil.BuildFtPrePreTxData(
		withdrawDeploy,
		3,
		[]*bt.Tx{ftPlan.Merge},
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC refund deploy ancestry: %w", err)
	}
	refundDeployRaw, err := contract.DeployHTLCTokenWithSign(
		address,
		address,
		hashlock,
		timelock,
		big.NewInt(2_000_000),
		[]*contractutil.FtUTXO{refundInput},
		refundDeployFee,
		[]*bt.Tx{withdrawDeploy},
		[]string{refundDeployPrePre},
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC refund deploy build: %w", err)
	}
	refundDeploy, err := parsePlannedTransaction(
		"token-htlc-refund-deploy", refundDeployRaw,
	)
	if err != nil {
		return nil, err
	}

	refundContract, err := outputUTXO(refundDeploy, 0)
	if err != nil {
		return nil, err
	}
	refundFT, err := ftUTXOFromTX(refundDeploy, 1)
	if err != nil {
		return nil, err
	}
	refundFee, err := changeUTXO(refundDeploy)
	if err != nil {
		return nil, err
	}
	refundPrePre, err := contractutil.BuildFtPrePreTxData(
		refundDeploy,
		1,
		[]*bt.Tx{withdrawDeploy},
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC refund ancestry: %w", err)
	}
	refundRaw, err := contract.RefundHTLCTokenWithSign(
		privateKey,
		address,
		refundContract,
		refundFT,
		refundDeploy,
		refundPrePre,
		refundFee,
		timelock,
	)
	if err != nil {
		return nil, fmt.Errorf("Token HTLC refund build: %w", err)
	}
	refund, err := parsePlannedTransaction("token-htlc-refund", refundRaw)
	if err != nil {
		return nil, err
	}

	return &tokenHTLCPlan{
		Transactions: []plannedTransaction{
			{Label: "token-htlc-withdraw-deploy", Raw: withdrawDeployRaw},
			{Label: "token-htlc-withdraw", Raw: withdrawRaw},
			{Label: "token-htlc-refund-deploy", Raw: refundDeployRaw},
			{Label: "token-htlc-refund", Raw: refundRaw},
		},
		Hashlock:       hashlock,
		secret:         secret,
		WithdrawDeploy: withdrawDeploy,
		Withdraw:       withdraw,
		RefundDeploy:   refundDeploy,
		Refund:         refund,
	}, nil
}

func validateTokenHTLCTransaction(label string, tx *bt.Tx, timelock uint32) error {
	switch label {
	case "token-htlc-withdraw-deploy":
		if tx == nil || len(tx.Outputs) < 6 {
			return fmt.Errorf("Token HTLC withdraw deploy has incomplete outputs")
		}
		if tx.Outputs[0].Satoshis != 100 {
			return fmt.Errorf("Token HTLC contract dust=%d want=100", tx.Outputs[0].Satoshis)
		}
		if err := requireFTBalance(tx, 1, 4_000_000); err != nil {
			return err
		}
		return requireFTBalance(tx, 3, 84_000_000)
	case "token-htlc-withdraw":
		return requireFTBalance(tx, 0, 4_000_000)
	case "token-htlc-refund-deploy":
		if tx == nil || len(tx.Outputs) < 6 {
			return fmt.Errorf("Token HTLC refund deploy has incomplete outputs")
		}
		if err := requireFTBalance(tx, 1, 2_000_000); err != nil {
			return err
		}
		return requireFTBalance(tx, 3, 82_000_000)
	case "token-htlc-refund":
		if tx == nil || len(tx.Inputs) < 1 {
			return fmt.Errorf("Token HTLC refund has no inputs")
		}
		if tx.LockTime != timelock || tx.Inputs[0].SequenceNumber != 0xfffffffe {
			return fmt.Errorf(
				"Token HTLC refund locktime=%d sequence=%d want=%d/4294967294",
				tx.LockTime, tx.Inputs[0].SequenceNumber, timelock,
			)
		}
		return requireFTBalance(tx, 0, 2_000_000)
	default:
		return fmt.Errorf("unknown Token HTLC label %q", label)
	}
}
