package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

func harnessFeeQuote80() *bt.FeeQuote {
	quote := bt.NewFeeQuote()
	for _, feeType := range []bt.FeeType{bt.FeeTypeStandard, bt.FeeTypeData} {
		quote.AddQuote(feeType, &bt.Fee{
			FeeType: feeType,
			MiningFee: bt.FeeUnit{
				Satoshis: 80,
				Bytes:    1000,
			},
			RelayFee: bt.FeeUnit{
				Satoshis: 80,
				Bytes:    1000,
			},
		})
	}
	return quote
}

func outputUTXO(tx *bt.Tx, outputIndex int) (*bt.UTXO, error) {
	if tx == nil || outputIndex < 0 || outputIndex >= len(tx.Outputs) {
		return nil, fmt.Errorf("output %d is outside transaction", outputIndex)
	}
	txid, err := hex.DecodeString(tx.TxID())
	if err != nil {
		return nil, err
	}
	output := tx.Outputs[outputIndex]
	return &bt.UTXO{
		TxID:          txid,
		Vout:          uint32(outputIndex),
		LockingScript: output.LockingScript,
		Satoshis:      output.Satoshis,
	}, nil
}

func changeUTXO(tx *bt.Tx) (*bt.UTXO, error) {
	if tx == nil || len(tx.Outputs) == 0 {
		return nil, fmt.Errorf("transaction has no change output")
	}
	return outputUTXO(tx, len(tx.Outputs)-1)
}

func runPlainTransfer(
	cfg config,
	decoded *wif.WIF,
	address string,
	funding *bt.UTXO,
) (*bt.Tx, error) {
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(funding); err != nil {
		return nil, err
	}
	if err := tx.PayToAddress(address, 10_000); err != nil {
		return nil, err
	}
	if err := tx.ChangeToAddress(address, harnessFeeQuote80()); err != nil {
		return nil, err
	}
	if err := tx.FillAllInputs(
		context.Background(),
		&unlocker.Getter{PrivateKey: decoded.PrivKey},
	); err != nil {
		return nil, err
	}
	broadcast, _, err := broadcastOne("plain-p2pkh-self-transfer", tx.String(), cfg.Network)
	return broadcast, err
}

func runNFTLifecycle(
	cfg config,
	decoded *wif.WIF,
	address string,
	funding *bt.UTXO,
) (*bt.Tx, error) {
	collectionData := &contract.CollectionData{
		CollectionName: "GoFeeSafety",
		Description:    "testnet signed-fee validation",
		Supply:         1,
		File:           "",
	}
	collectionRaw, err := contract.CreateCollection(
		address, decoded.PrivKey, collectionData, []*bt.UTXO{funding},
	)
	if err != nil {
		return nil, fmt.Errorf("NFT collection build: %w", err)
	}
	collectionTX, _, err := broadcastOne("nft-collection-create", collectionRaw, cfg.Network)
	if err != nil {
		return nil, err
	}
	mintUTXO, err := outputUTXO(collectionTX, 1)
	if err != nil {
		return nil, err
	}
	mintFee, err := changeUTXO(collectionTX)
	if err != nil {
		return nil, err
	}

	nftData := &contract.NFTData{
		NftName:     "Go Fee Safety #1",
		Symbol:      "GFS",
		Description: "real testnet fee validation",
		Attributes:  `{"suite":"go-fee-safety"}`,
	}
	nftRaw, err := contract.CreateNFT(
		collectionTX.TxID(), address, decoded.PrivKey, nftData,
		[]*bt.UTXO{mintFee}, mintUTXO,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT mint build: %w", err)
	}
	nftTX, _, err := broadcastOne("nft-mint", nftRaw, cfg.Network)
	if err != nil {
		return nil, err
	}
	transferFee, err := changeUTXO(nftTX)
	if err != nil {
		return nil, err
	}

	nft := contract.NewNFT(nftTX.TxID())
	nft.Initialize(&contractutil.NFTInfo{
		CollectionID:         collectionTX.TxID(),
		CollectionIndex:      int(mintUTXO.Vout),
		CollectionName:       collectionData.CollectionName,
		NFTName:              nftData.NftName,
		NFTSymbol:            nftData.Symbol,
		NFTAttributes:        nftData.Attributes,
		NFTDescription:       nftData.Description,
		NFTTransferTimeCount: 0,
	})
	transferRaw, err := nft.TransferNFT(
		address, address, decoded.PrivKey, []*bt.UTXO{transferFee},
		nftTX, collectionTX, false,
	)
	if err != nil {
		return nil, fmt.Errorf("NFT transfer build: %w", err)
	}
	transferTX, _, err := broadcastOne("nft-self-transfer", transferRaw, cfg.Network)
	return transferTX, err
}

type multiSigSigner struct {
	privateKey *bec.PrivateKey
	publicKey  string
}

func runMultiSigLifecycle(
	cfg config,
	decoded *wif.WIF,
	address string,
	funding *bt.UTXO,
) (*bt.Tx, error) {
	signers := make([]multiSigSigner, 0, 3)
	signers = append(signers, multiSigSigner{
		privateKey: decoded.PrivKey,
		publicKey:  hex.EncodeToString(decoded.PrivKey.PubKey().SerialiseCompressed()),
	})
	for len(signers) < 3 {
		privateKey, err := bec.NewPrivateKey(bec.S256())
		if err != nil {
			return nil, err
		}
		signers = append(signers, multiSigSigner{
			privateKey: privateKey,
			publicKey:  hex.EncodeToString(privateKey.PubKey().SerialiseCompressed()),
		})
	}
	sort.Slice(signers, func(i, j int) bool {
		return signers[i].publicKey < signers[j].publicKey
	})
	publicKeys := make([]string, len(signers))
	for i := range signers {
		publicKeys[i] = signers[i].publicKey
	}
	multiSigAddress, err := contract.GetMultiSigAddress(publicKeys, 2, 3)
	if err != nil {
		return nil, err
	}
	createRaw, err := contract.CreateMultiSigWallet(
		address, publicKeys, 2, 3, 100_000,
		[]*bt.UTXO{funding}, decoded.PrivKey,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig wallet build: %w", err)
	}
	createTX, _, err := broadcastOne("multisig-wallet-create", createRaw, cfg.Network)
	if err != nil {
		return nil, err
	}
	multiSigUTXO, err := outputUTXO(createTX, 0)
	if err != nil {
		return nil, err
	}
	unsigned, err := contract.BuildMultiSigTransactionSendTBC(
		multiSigAddress, address, 50_000, []*bt.UTXO{multiSigUTXO},
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig spend build: %w", err)
	}
	signatures := make([][]string, 1)
	for signerIndex := 0; signerIndex < 2; signerIndex++ {
		signed, err := contract.SignMultiSigTransactionSendTBC(
			multiSigAddress, unsigned, signers[signerIndex].privateKey,
		)
		if err != nil {
			return nil, fmt.Errorf("MultiSig signer %d: %w", signerIndex, err)
		}
		signatures[0] = append(signatures[0], signed[0])
	}
	finished, err := contract.FinishMultiSigTransactionSendTBC(
		unsigned.TxRaw, signatures, publicKeys,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig finalize: %w", err)
	}
	if _, _, err := broadcastOne("multisig-spend", finished, cfg.Network); err != nil {
		return nil, err
	}
	return createTX, nil
}

func runPiggyBankLifecycle(
	cfg config,
	decoded *wif.WIF,
	funding *bt.UTXO,
	height uint32,
) (*bt.Tx, error) {
	lockTime := height
	if lockTime > 0 {
		lockTime--
	}
	freezeRaw, err := contract.FreezeTBCWithSign(
		decoded.PrivKey, 100_000, lockTime, []*bt.UTXO{funding},
	)
	if err != nil {
		return nil, fmt.Errorf("PiggyBank freeze build: %w", err)
	}
	freezeTX, _, err := broadcastOne("piggybank-freeze", freezeRaw, cfg.Network)
	if err != nil {
		return nil, err
	}
	frozen, err := outputUTXO(freezeTX, 0)
	if err != nil {
		return nil, err
	}
	unfreezeRaw, err := contract.UnfreezeTBCWithSign(
		decoded.PrivKey, []*bt.UTXO{frozen}, height,
	)
	if err != nil {
		return nil, fmt.Errorf("PiggyBank unfreeze build: %w", err)
	}
	if _, _, err := broadcastOne("piggybank-unfreeze", unfreezeRaw, cfg.Network); err != nil {
		return nil, err
	}
	return freezeTX, nil
}

func runBaseHTLCLifecycle(
	cfg config,
	decoded *wif.WIF,
	address string,
	funding *bt.UTXO,
	height uint32,
) error {
	secretBytes := make([]byte, 32)
	if _, err := cryptorand.Read(secretBytes); err != nil {
		return err
	}
	secret := hex.EncodeToString(secretBytes)
	hashlock := hex.EncodeToString(crypto.Sha256(secretBytes))
	timelock := height
	if timelock > 0 {
		timelock--
	}

	withdrawDeployRaw, err := contract.DeployHTLCWithSign(
		address, address, hashlock, timelock, 100_000, funding, decoded.PrivKey,
	)
	if err != nil {
		return fmt.Errorf("base HTLC withdraw deploy build: %w", err)
	}
	withdrawDeployTX, _, err := broadcastOne(
		"base-htlc-withdraw-deploy", withdrawDeployRaw, cfg.Network,
	)
	if err != nil {
		return err
	}
	withdrawContract, err := outputUTXO(withdrawDeployTX, 0)
	if err != nil {
		return err
	}
	withdrawRaw, err := contract.WithdrawWithSign(
		decoded.PrivKey, address, withdrawContract, secret,
	)
	if err != nil {
		return fmt.Errorf("base HTLC withdraw build: %w", err)
	}
	if _, _, err := broadcastOne("base-htlc-withdraw", withdrawRaw, cfg.Network); err != nil {
		return err
	}

	refundFunding, err := changeUTXO(withdrawDeployTX)
	if err != nil {
		return err
	}
	refundDeployRaw, err := contract.DeployHTLCWithSign(
		address, address, hashlock, timelock, 100_000, refundFunding, decoded.PrivKey,
	)
	if err != nil {
		return fmt.Errorf("base HTLC refund deploy build: %w", err)
	}
	refundDeployTX, _, err := broadcastOne(
		"base-htlc-refund-deploy", refundDeployRaw, cfg.Network,
	)
	if err != nil {
		return err
	}
	refundContract, err := outputUTXO(refundDeployTX, 0)
	if err != nil {
		return err
	}
	refundRaw, err := contract.RefundWithSign(
		address, refundContract, decoded.PrivKey, timelock,
	)
	if err != nil {
		return fmt.Errorf("base HTLC refund build: %w", err)
	}
	if _, _, err := broadcastOne("base-htlc-refund", refundRaw, cfg.Network); err != nil {
		return err
	}
	return nil
}

func runCoreContracts(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(address, 0.02, cfg.Network)
	if err != nil {
		return err
	}
	plainTX, err := runPlainTransfer(cfg, decoded, address, funding)
	if err != nil {
		return err
	}
	funding, err = changeUTXO(plainTX)
	if err != nil {
		return err
	}
	nftTX, err := runNFTLifecycle(cfg, decoded, address, funding)
	if err != nil {
		return err
	}
	funding, err = changeUTXO(nftTX)
	if err != nil {
		return err
	}
	multiSigTX, err := runMultiSigLifecycle(cfg, decoded, address, funding)
	if err != nil {
		return err
	}
	funding, err = changeUTXO(multiSigTX)
	if err != nil {
		return err
	}
	height, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return err
	}
	piggyBankTX, err := runPiggyBankLifecycle(cfg, decoded, funding, height)
	if err != nil {
		return err
	}
	funding, err = changeUTXO(piggyBankTX)
	if err != nil {
		return err
	}
	if err := runBaseHTLCLifecycle(cfg, decoded, address, funding, height); err != nil {
		return err
	}
	fmt.Println("plain, NFT, MultiSig, PiggyBank, and base-HTLC scenarios pass")
	return nil
}
