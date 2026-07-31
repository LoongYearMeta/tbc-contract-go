package main

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const (
	nftStageFundingMinimumSatoshis = uint64(300_000)
	nftTempFundingSatoshis         = uint64(200_000)
)

type nftLifecyclePlan struct {
	Transactions    []plannedTransaction
	Collection      *bt.Tx
	SingleMint      *bt.Tx
	BatchMintOne    *bt.Tx
	BatchMintTwo    *bt.Tx
	TempFunding     *bt.Tx
	Transfer        *bt.Tx
	TransferWithTBC *bt.Tx
	MainAddress     string
	TempAddress     string
	tempPrivateKey  *bec.PrivateKey
}

func validateNFTOutputs(tx *bt.Tx) error {
	if tx == nil || len(tx.Outputs) < 3 {
		return fmt.Errorf("NFT transaction requires Code/Hold/Tape outputs")
	}
	if tx.Outputs[0].Satoshis != 200 {
		return fmt.Errorf("NFT Code satoshis=%d want=200", tx.Outputs[0].Satoshis)
	}
	if tx.Outputs[1].Satoshis != 100 {
		return fmt.Errorf("NFT Hold satoshis=%d want=100", tx.Outputs[1].Satoshis)
	}
	if tx.Outputs[2].Satoshis != 0 || !tx.Outputs[2].LockingScript.IsSafeDataOut() {
		return fmt.Errorf("NFT Tape is not zero-satoshi safe data")
	}
	return nil
}

func validatePaymentOutput(tx *bt.Tx, address string, amount uint64) error {
	if tx == nil {
		return fmt.Errorf("nil payment transaction")
	}
	script, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return err
	}
	for _, output := range tx.Outputs {
		if output.Satoshis == amount && bytes.Equal(output.LockingScript.Bytes(), script.Bytes()) {
			return nil
		}
	}
	return fmt.Errorf("payment output address=%s satoshis=%d not found", address, amount)
}

func buildNFTSupportFunding(
	privateKey *bec.PrivateKey,
	fromAddress string,
	toAddress string,
	funding *bt.UTXO,
) (string, error) {
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(funding); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(toAddress, nftTempFundingSatoshis); err != nil {
		return "", err
	}
	if err := tx.ChangeToAddress(fromAddress, harnessFeeQuote80()); err != nil {
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

func buildNFTLifecyclePlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
) (*nftLifecyclePlan, error) {
	collectionData := &contract.CollectionData{
		CollectionName: "GoFullMatrixNFT",
		Description:    "full Go SDK testnet matrix",
		Supply:         4,
		File:           "",
	}
	collectionRaw, err := contract.CreateCollection(
		address,
		privateKey,
		collectionData,
		[]*bt.UTXO{funding},
	)
	if err != nil {
		return nil, fmt.Errorf("NFT collection build: %w", err)
	}
	collection, err := parsePlannedTransaction("nft-collection-create", collectionRaw)
	if err != nil {
		return nil, err
	}

	singleData := contract.NFTData{
		NftName:     "Go Full Matrix #1",
		Symbol:      "GFMNFT",
		Description: "single mint and transfer lifecycle",
		Attributes:  `{"suite":"full-matrix","index":1}`,
		File:        "matrix://nft/1",
	}
	singleMintSlot, err := outputUTXO(collection, 1)
	if err != nil {
		return nil, err
	}
	singleFee, err := changeUTXO(collection)
	if err != nil {
		return nil, err
	}
	singleRaw, err := contract.CreateNFT(
		collection.TxID(),
		address,
		privateKey,
		&singleData,
		[]*bt.UTXO{singleFee},
		singleMintSlot,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT single mint build: %w", err)
	}
	singleMint, err := parsePlannedTransaction("nft-single-mint", singleRaw)
	if err != nil {
		return nil, err
	}

	batchSlotOne, err := outputUTXO(collection, 2)
	if err != nil {
		return nil, err
	}
	batchSlotTwo, err := outputUTXO(collection, 3)
	if err != nil {
		return nil, err
	}
	batchFee, err := changeUTXO(singleMint)
	if err != nil {
		return nil, err
	}
	batchRaws, err := contract.BatchCreateNFT(
		collection.TxID(),
		address,
		privateKey,
		[]contract.NFTData{
			{
				NftName: "Go Full Matrix #2", Symbol: "GFMNFT",
				Description: "batch mint one", Attributes: `{"index":2}`,
				File: "matrix://nft/2",
			},
			{
				NftName: "Go Full Matrix #3", Symbol: "GFMNFT",
				Description: "batch mint two", Attributes: `{"index":3}`,
				File: "matrix://nft/3",
			},
		},
		[]*bt.UTXO{batchFee},
		[]*bt.UTXO{batchSlotOne, batchSlotTwo},
		"testnet",
	)
	if err != nil {
		return nil, fmt.Errorf("NFT batch mint build: %w", err)
	}
	if len(batchRaws) != 2 {
		return nil, fmt.Errorf("NFT batch mint returned %d transactions, want=2", len(batchRaws))
	}
	batchMintOne, err := parsePlannedTransaction("nft-batch-mint-1", batchRaws[0])
	if err != nil {
		return nil, err
	}
	batchMintTwo, err := parsePlannedTransaction("nft-batch-mint-2", batchRaws[1])
	if err != nil {
		return nil, err
	}

	tempPrivateKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		return nil, err
	}
	tempAddressValue, err := bscript.NewAddressFromPublicKey(tempPrivateKey.PubKey(), false)
	if err != nil {
		return nil, err
	}
	tempAddress := tempAddressValue.AddressString
	tempFundingInput, err := changeUTXO(batchMintTwo)
	if err != nil {
		return nil, err
	}
	tempFundingRaw, err := buildNFTSupportFunding(
		privateKey, address, tempAddress, tempFundingInput,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT temporary fee funding build: %w", err)
	}
	tempFunding, err := parsePlannedTransaction("nft-temp-funding", tempFundingRaw)
	if err != nil {
		return nil, err
	}

	nft := contract.NewNFT(singleMint.TxID())
	nft.CollectionID = collection.TxID()
	nft.CollectionIndex = 1
	nft.CollectionName = collectionData.CollectionName
	nft.NftData = singleData
	transferFee, err := changeUTXO(tempFunding)
	if err != nil {
		return nil, err
	}
	transferRaw, err := nft.TransferNFT(
		address,
		tempAddress,
		privateKey,
		[]*bt.UTXO{transferFee},
		singleMint,
		collection,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT transfer build: %w", err)
	}
	transfer, err := parsePlannedTransaction("nft-transfer", transferRaw)
	if err != nil {
		return nil, err
	}

	tempFee, err := outputUTXO(tempFunding, 0)
	if err != nil {
		return nil, err
	}
	withTBCRaw, err := nft.TransferNFTWithTBC(
		tempAddress,
		address,
		address,
		tempPrivateKey,
		[]*bt.UTXO{tempFee},
		transfer,
		singleMint,
		10_000,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT transfer with TBC build: %w", err)
	}
	withTBC, err := parsePlannedTransaction("nft-transfer-with-tbc", withTBCRaw)
	if err != nil {
		return nil, err
	}

	return &nftLifecyclePlan{
		Transactions: []plannedTransaction{
			{Label: "nft-collection-create", Raw: collectionRaw},
			{Label: "nft-single-mint", Raw: singleRaw},
			{Label: "nft-batch-mint-1", Raw: batchRaws[0]},
			{Label: "nft-batch-mint-2", Raw: batchRaws[1]},
			{Label: "nft-temp-funding", Raw: tempFundingRaw},
			{Label: "nft-transfer", Raw: transferRaw},
			{Label: "nft-transfer-with-tbc", Raw: withTBCRaw},
		},
		Collection:      collection,
		SingleMint:      singleMint,
		BatchMintOne:    batchMintOne,
		BatchMintTwo:    batchMintTwo,
		TempFunding:     tempFunding,
		Transfer:        transfer,
		TransferWithTBC: withTBC,
		MainAddress:     address,
		TempAddress:     tempAddress,
		tempPrivateKey:  tempPrivateKey,
	}, nil
}

func validateNFTHold(tx *bt.Tx, address string) error {
	want, err := contract.BuildNFTHoldScript(address)
	if err != nil {
		return err
	}
	if tx == nil || len(tx.Outputs) < 2 {
		return fmt.Errorf("NFT Hold output is missing")
	}
	if !bytes.Equal(tx.Outputs[1].LockingScript.Bytes(), want.Bytes()) {
		return fmt.Errorf("NFT Hold owner mismatch")
	}
	return nil
}

func validateNFTCode(tx *bt.Tx, collectionID string, collectionIndex uint32) error {
	want, err := contract.BuildCodeScript(collectionID, collectionIndex)
	if err != nil {
		return err
	}
	if tx == nil || len(tx.Outputs) < 1 {
		return fmt.Errorf("NFT Code output is missing")
	}
	if !bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), want.Bytes()) {
		return fmt.Errorf(
			"NFT Code collection/index mismatch for %s/%d",
			collectionID,
			collectionIndex,
		)
	}
	return nil
}

func validateNFTLifecycleTransaction(
	label string,
	tx *bt.Tx,
	plan *nftLifecyclePlan,
) error {
	switch label {
	case "nft-collection-create":
		if tx == nil || len(tx.Outputs) != 6 {
			return fmt.Errorf("NFT collection outputs incomplete")
		}
		if tx.Outputs[0].Satoshis != 0 || !tx.Outputs[0].LockingScript.IsSafeDataOut() {
			return fmt.Errorf("NFT collection Tape is invalid")
		}
		for i := 1; i <= 4; i++ {
			if tx.Outputs[i].Satoshis != 100 {
				return fmt.Errorf("NFT mint slot %d satoshis=%d want=100", i, tx.Outputs[i].Satoshis)
			}
		}
		return nil
	case "nft-single-mint":
		if err := validateNFTOutputs(tx); err != nil {
			return err
		}
		if err := validateNFTCode(tx, plan.Collection.TxID(), 1); err != nil {
			return err
		}
		return validateNFTHold(tx, plan.MainAddress)
	case "nft-batch-mint-1":
		if err := validateNFTOutputs(tx); err != nil {
			return err
		}
		if err := validateNFTCode(tx, plan.Collection.TxID(), 2); err != nil {
			return err
		}
		return validateNFTHold(tx, plan.MainAddress)
	case "nft-batch-mint-2":
		if err := validateNFTOutputs(tx); err != nil {
			return err
		}
		if err := validateNFTCode(tx, plan.Collection.TxID(), 3); err != nil {
			return err
		}
		return validateNFTHold(tx, plan.MainAddress)
	case "nft-temp-funding":
		return validatePaymentOutput(tx, plan.TempAddress, nftTempFundingSatoshis)
	case "nft-transfer":
		if err := validateNFTOutputs(tx); err != nil {
			return err
		}
		if err := validateNFTCode(tx, plan.Collection.TxID(), 1); err != nil {
			return err
		}
		if err := validateNFTHold(tx, plan.TempAddress); err != nil {
			return err
		}
		if !bytes.Equal(tx.Outputs[2].LockingScript.Bytes(), plan.SingleMint.Outputs[2].LockingScript.Bytes()) {
			return fmt.Errorf("NFT metadata changed during transfer")
		}
		return nil
	case "nft-transfer-with-tbc":
		if err := validateNFTOutputs(tx); err != nil {
			return err
		}
		if err := validateNFTCode(tx, plan.Collection.TxID(), 1); err != nil {
			return err
		}
		mainAddress, err := bscript.NewAddressFromPublicKey(
			plan.tempPrivateKey.PubKey(),
			false,
		)
		if err != nil {
			return err
		}
		if mainAddress.AddressString != plan.TempAddress {
			return fmt.Errorf("temporary signer/address mismatch")
		}
		if err := validateNFTHold(tx, plan.MainAddress); err != nil {
			return err
		}
		if !bytes.Equal(tx.Outputs[2].LockingScript.Bytes(), plan.SingleMint.Outputs[2].LockingScript.Bytes()) {
			return fmt.Errorf("NFT metadata changed during transfer with TBC")
		}
		return validatePaymentOutput(tx, plan.MainAddress, 10_000)
	default:
		return fmt.Errorf("unknown NFT lifecycle label %q", label)
	}
}

func runNFTStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(
		address,
		float64(nftStageFundingMinimumSatoshis)/1_000_000,
		cfg.Network,
	)
	if err != nil {
		return fmt.Errorf("NFT funding: %w", err)
	}
	plan, err := buildNFTLifecyclePlan(decoded.PrivKey, address, funding)
	if err != nil {
		return err
	}
	state := publicState{CollectionID: plan.Collection.TxID()}
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"nft-code-hold-tape-owner-payment",
			func(tx *bt.Tx) error {
				return validateNFTLifecycleTransaction(label, tx, plan)
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
