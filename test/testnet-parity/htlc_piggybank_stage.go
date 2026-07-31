package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

type baseHTLCPlan struct {
	Transactions   []plannedTransaction
	Plain          *bt.Tx
	WithdrawDeploy *bt.Tx
	Withdraw       *bt.Tx
	RefundDeploy   *bt.Tx
	Refund         *bt.Tx
	Address        string
	Hashlock       string
	secret         string
	Timelock       uint32
}

type piggyBankPlan struct {
	Transactions []plannedTransaction
	Freeze       *bt.Tx
	Unfreeze     *bt.Tx
	Address      string
	LockTime     uint32
	Height       uint32
}

func buildPlainSelfTransfer(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
) (string, error) {
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(funding); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(address, 10_000); err != nil {
		return "", err
	}
	if err := tx.ChangeToAddress(address, harnessFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.FillAllInputs(
		context.Background(),
		&unlocker.Getter{PrivateKey: privateKey},
	); err != nil {
		return "", err
	}
	return tx.String(), nil
}

func buildBaseHTLCPlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
	height uint32,
) (*baseHTLCPlan, error) {
	plainRaw, err := buildPlainSelfTransfer(privateKey, address, funding)
	if err != nil {
		return nil, fmt.Errorf("plain P2PKH build: %w", err)
	}
	plain, err := parsePlannedTransaction("plain-p2pkh-self-transfer", plainRaw)
	if err != nil {
		return nil, err
	}
	deployFunding, err := changeUTXO(plain)
	if err != nil {
		return nil, err
	}
	secretBytes := make([]byte, 32)
	if _, err := cryptorand.Read(secretBytes); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(secretBytes)
	hashlock := hex.EncodeToString(crypto.Sha256(secretBytes))
	timelock := height
	if timelock > 0 {
		timelock--
	}
	withdrawDeployRaw, err := contract.DeployHTLCWithSign(
		address,
		address,
		hashlock,
		timelock,
		100_000,
		deployFunding,
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("base HTLC withdraw deploy build: %w", err)
	}
	withdrawDeploy, err := parsePlannedTransaction(
		"base-htlc-withdraw-deploy", withdrawDeployRaw,
	)
	if err != nil {
		return nil, err
	}
	withdrawContract, err := outputUTXO(withdrawDeploy, 0)
	if err != nil {
		return nil, err
	}
	withdrawRaw, err := contract.WithdrawWithSign(
		privateKey,
		address,
		withdrawContract,
		secret,
	)
	if err != nil {
		return nil, fmt.Errorf("base HTLC withdraw build: %w", err)
	}
	withdraw, err := parsePlannedTransaction("base-htlc-withdraw", withdrawRaw)
	if err != nil {
		return nil, err
	}

	refundFunding, err := changeUTXO(withdrawDeploy)
	if err != nil {
		return nil, err
	}
	refundDeployRaw, err := contract.DeployHTLCWithSign(
		address,
		address,
		hashlock,
		timelock,
		100_000,
		refundFunding,
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("base HTLC refund deploy build: %w", err)
	}
	refundDeploy, err := parsePlannedTransaction(
		"base-htlc-refund-deploy", refundDeployRaw,
	)
	if err != nil {
		return nil, err
	}
	refundContract, err := outputUTXO(refundDeploy, 0)
	if err != nil {
		return nil, err
	}
	refundRaw, err := contract.RefundWithSign(
		address,
		refundContract,
		privateKey,
		timelock,
	)
	if err != nil {
		return nil, fmt.Errorf("base HTLC refund build: %w", err)
	}
	refund, err := parsePlannedTransaction("base-htlc-refund", refundRaw)
	if err != nil {
		return nil, err
	}
	return &baseHTLCPlan{
		Transactions: []plannedTransaction{
			{Label: "plain-p2pkh-self-transfer", Raw: plainRaw},
			{Label: "base-htlc-withdraw-deploy", Raw: withdrawDeployRaw},
			{Label: "base-htlc-withdraw", Raw: withdrawRaw},
			{Label: "base-htlc-refund-deploy", Raw: refundDeployRaw},
			{Label: "base-htlc-refund", Raw: refundRaw},
		},
		Plain:          plain,
		WithdrawDeploy: withdrawDeploy,
		Withdraw:       withdraw,
		RefundDeploy:   refundDeploy,
		Refund:         refund,
		Address:        address,
		Hashlock:       hashlock,
		secret:         secret,
		Timelock:       timelock,
	}, nil
}

func validateHTLCDeploy(
	tx *bt.Tx,
	plan *baseHTLCPlan,
) error {
	if tx == nil || len(tx.Outputs) != 2 {
		return fmt.Errorf("base HTLC deploy output layout mismatch")
	}
	sender, err := bscript.NewAddressFromString(plan.Address)
	if err != nil {
		return err
	}
	want, err := contract.GetHTLCCode(
		sender.PublicKeyHash,
		sender.PublicKeyHash,
		plan.Hashlock,
		plan.Timelock,
	)
	if err != nil {
		return err
	}
	if tx.Outputs[0].Satoshis != 100_000 ||
		!bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), want.Bytes()) {
		return fmt.Errorf("base HTLC contract script/value mismatch")
	}
	return nil
}

func validateBaseHTLCTransaction(
	label string,
	tx *bt.Tx,
	plan *baseHTLCPlan,
) error {
	switch label {
	case "plain-p2pkh-self-transfer":
		return validatePaymentOutput(tx, plan.Address, 10_000)
	case "base-htlc-withdraw-deploy", "base-htlc-refund-deploy":
		return validateHTLCDeploy(tx, plan)
	case "base-htlc-withdraw":
		return validatePaymentOutput(tx, plan.Address, 99_920)
	case "base-htlc-refund":
		if tx == nil || len(tx.Inputs) != 1 {
			return fmt.Errorf("base HTLC refund input layout mismatch")
		}
		if tx.LockTime != plan.Timelock || tx.Inputs[0].SequenceNumber != 0xfffffffe {
			return fmt.Errorf("base HTLC refund locktime/sequence mismatch")
		}
		return validatePaymentOutput(tx, plan.Address, 99_920)
	default:
		return fmt.Errorf("unknown base HTLC label %q", label)
	}
}

func buildPiggyBankPlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
	height uint32,
) (*piggyBankPlan, error) {
	lockTime := height
	if lockTime > 0 {
		lockTime--
	}
	freezeRaw, err := contract.FreezeTBCWithSign(
		privateKey,
		100_000,
		lockTime,
		[]*bt.UTXO{funding},
	)
	if err != nil {
		return nil, fmt.Errorf("PiggyBank freeze build: %w", err)
	}
	freeze, err := parsePlannedTransaction("piggybank-freeze", freezeRaw)
	if err != nil {
		return nil, err
	}
	frozen, err := outputUTXO(freeze, 0)
	if err != nil {
		return nil, err
	}
	unfreezeRaw, err := contract.UnfreezeTBCWithSign(
		privateKey,
		[]*bt.UTXO{frozen},
		height,
	)
	if err != nil {
		return nil, fmt.Errorf("PiggyBank unfreeze build: %w", err)
	}
	unfreeze, err := parsePlannedTransaction("piggybank-unfreeze", unfreezeRaw)
	if err != nil {
		return nil, err
	}
	return &piggyBankPlan{
		Transactions: []plannedTransaction{
			{Label: "piggybank-freeze", Raw: freezeRaw},
			{Label: "piggybank-unfreeze", Raw: unfreezeRaw},
		},
		Freeze:   freeze,
		Unfreeze: unfreeze,
		Address:  address,
		LockTime: lockTime,
		Height:   height,
	}, nil
}

func validatePiggyBankTransaction(
	label string,
	tx *bt.Tx,
	plan *piggyBankPlan,
) error {
	switch label {
	case "piggybank-freeze":
		if tx == nil || len(tx.Outputs) != 2 {
			return fmt.Errorf("PiggyBank freeze output layout mismatch")
		}
		if tx.Outputs[0].Satoshis != 100_000 {
			return fmt.Errorf("PiggyBank frozen satoshis=%d want=100000", tx.Outputs[0].Satoshis)
		}
		lockTime, err := contract.FetchTBCLockTimeFromScript(tx.Outputs[0].LockingScript)
		if err != nil {
			return err
		}
		if lockTime != plan.LockTime {
			return fmt.Errorf("PiggyBank script locktime=%d want=%d", lockTime, plan.LockTime)
		}
		return nil
	case "piggybank-unfreeze":
		if tx == nil || len(tx.Inputs) != 1 || len(tx.Outputs) != 1 {
			return fmt.Errorf("PiggyBank unfreeze layout mismatch")
		}
		if tx.LockTime != plan.Height || tx.Inputs[0].SequenceNumber != 0xfffffffe {
			return fmt.Errorf("PiggyBank unfreeze locktime/sequence mismatch")
		}
		p2pkh, err := bscript.NewP2PKHFromAddress(plan.Address)
		if err != nil {
			return err
		}
		if !bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), p2pkh.Bytes()) {
			return fmt.Errorf("PiggyBank unfreeze recipient mismatch")
		}
		return nil
	default:
		return fmt.Errorf("unknown PiggyBank label %q", label)
	}
}

func runBaseHTLCStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(address, 0.01, cfg.Network)
	if err != nil {
		return fmt.Errorf("base HTLC funding: %w", err)
	}
	height, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return err
	}
	plan, err := buildBaseHTLCPlan(decoded.PrivKey, address, funding, height)
	if err != nil {
		return err
	}
	state := publicState{}
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"p2pkh-base-htlc-lock-branches",
			func(tx *bt.Tx) error {
				return validateBaseHTLCTransaction(label, tx, plan)
			},
		)
		if err != nil {
			return err
		}
		state.LastTxID = accepted.TxID()
	}
	return writePublicState(os.Stdout, state)
}

func runPiggyBankStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(address, 0.01, cfg.Network)
	if err != nil {
		return fmt.Errorf("PiggyBank funding: %w", err)
	}
	height, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return err
	}
	plan, err := buildPiggyBankPlan(decoded.PrivKey, address, funding, height)
	if err != nil {
		return err
	}
	state := publicState{}
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"piggybank-locktime-sequence",
			func(tx *bt.Tx) error {
				return validatePiggyBankTransaction(label, tx, plan)
			},
		)
		if err != nil {
			return err
		}
		state.LastTxID = accepted.TxID()
	}
	return writePublicState(os.Stdout, state)
}
