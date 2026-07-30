package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"time"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const musigSignerScript = "test/testnet-parity/js-musig-sign.js"

func runMuSigSigner(arguments ...string) ([]byte, error) {
	command := exec.Command("node", append([]string{musigSignerScript}, arguments...)...)
	command.Env = os.Environ()
	output, err := command.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("external MuSig signer: %s", exitError.Stderr)
		}
		return nil, fmt.Errorf("external MuSig signer: %w", err)
	}
	return output, nil
}

func loadAggregateAdminKey() ([]byte, error) {
	output, err := runMuSigSigner("aggregate")
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(string(output))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("external MuSig signer returned invalid aggregate key")
	}
	return key, nil
}

func signAdminPrepared(prepared *contract.AdminPrepared) ([][]byte, error) {
	arguments := []string{"sign"}
	for _, item := range prepared.Sighashes {
		arguments = append(arguments, hex.EncodeToString(item.Sighash))
	}
	output, err := runMuSigSigner(arguments...)
	if err != nil {
		return nil, err
	}
	var encoded []string
	if err := json.Unmarshal(output, &encoded); err != nil {
		return nil, fmt.Errorf("decode external MuSig signatures: %w", err)
	}
	signatures := make([][]byte, len(encoded))
	for i, item := range encoded {
		signatures[i], err = hex.DecodeString(item)
		if err != nil || len(signatures[i]) != 64 {
			return nil, fmt.Errorf("external MuSig signature %d is not 64 bytes", i)
		}
	}
	return signatures, nil
}

func finalizeAdminPrepared(prepared *contract.AdminPrepared) ([]string, error) {
	signatures, err := signAdminPrepared(prepared)
	if err != nil {
		return nil, err
	}
	return prepared.Finalize(signatures)
}

func stableCoinInput(
	tx *bt.Tx,
	codeOutput int,
) (*contractutil.FtUTXO, error) {
	return ftUTXOFromTX(tx, codeOutput)
}

func runStableCoinLifecycle(cfg config, decoded *wif.WIF, address string) error {
	aggregateKey, err := loadAggregateAdminKey()
	if err != nil {
		return err
	}
	funding, err := api.FetchUTXO(address, 0.02, cfg.Network)
	if err != nil {
		return err
	}
	fundingParent, err := api.FetchTXRaw(hex.EncodeToString(funding.TxID), cfg.Network)
	if err != nil {
		return err
	}
	stableCoin, err := contract.NewStableCoin(&contract.FtParams{
		Name:    "Go Stable Fee",
		Symbol:  "GSF",
		Amount:  1_000,
		Decimal: 2,
	})
	if err != nil {
		return err
	}
	create, err := stableCoin.PrepareCreateCoin(
		aggregateKey,
		decoded.PrivKey,
		address,
		funding,
		fundingParent,
		"go stablecoin signed-fee testnet validation",
	)
	if err != nil {
		return fmt.Errorf("StableCoin create prepare: %w", err)
	}
	createRaws, err := finalizeAdminPrepared(create)
	if err != nil {
		return fmt.Errorf("StableCoin create finalize: %w", err)
	}
	if len(createRaws) != 2 {
		return fmt.Errorf("StableCoin create returned %d transactions", len(createRaws))
	}
	coinNFTTX, _, err := broadcastOne("stablecoin-coin-nft-create", createRaws[0], cfg.Network)
	if err != nil {
		return err
	}
	mintTX, _, err := broadcastOne("stablecoin-mint", createRaws[1], cfg.Network)
	if err != nil {
		return err
	}
	fmt.Printf("stablecoin contract_id=%s coin_nft_id=%s\n",
		mintTX.TxID(), coinNFTTX.TxID())

	const mintedCodeOutput = 3
	mintedCoin, err := stableCoinInput(mintTX, mintedCodeOutput)
	if err != nil {
		return err
	}
	transferFee, err := changeUTXO(mintTX)
	if err != nil {
		return err
	}
	mintedPrePre, err := api.FetchFtPrePreTxData(mintTX, mintedCodeOutput, cfg.Network)
	if err != nil {
		return fmt.Errorf("StableCoin mint ancestry: %w", err)
	}
	transferRaw, err := stableCoin.Transfer(
		decoded.PrivKey,
		address,
		big.NewInt(1_000),
		[]*contractutil.FtUTXO{mintedCoin},
		transferFee,
		[]*bt.Tx{mintTX},
		[]string{mintedPrePre},
		0,
	)
	if err != nil {
		return fmt.Errorf("StableCoin owner transfer build: %w", err)
	}
	transferTX, _, err := broadcastOne("stablecoin-owner-transfer", transferRaw, cfg.Network)
	if err != nil {
		return err
	}

	coinToFreeze, err := stableCoinInput(transferTX, 0)
	if err != nil {
		return err
	}
	freezeFee, err := changeUTXO(transferTX)
	if err != nil {
		return err
	}
	freezePrePre, err := localPrePre(mintTX, mintedCodeOutput)
	if err != nil {
		return err
	}
	lockTime := uint32(time.Now().Unix() - 60)
	freeze, err := stableCoin.PrepareFreezeCoinUTXO(
		aggregateKey,
		decoded.PrivKey,
		lockTime,
		[]*contractutil.FtUTXO{coinToFreeze},
		freezeFee,
		[]*bt.Tx{transferTX},
		[]string{freezePrePre},
	)
	if err != nil {
		return fmt.Errorf("StableCoin freeze prepare: %w", err)
	}
	freezeRaws, err := finalizeAdminPrepared(freeze)
	if err != nil {
		return fmt.Errorf("StableCoin freeze finalize: %w", err)
	}
	freezeTX, _, err := broadcastOne("stablecoin-freeze", freezeRaws[0], cfg.Network)
	if err != nil {
		return err
	}

	frozenCoin, err := stableCoinInput(freezeTX, 0)
	if err != nil {
		return err
	}
	unfreezeFee, err := changeUTXO(freezeTX)
	if err != nil {
		return err
	}
	unfreezePrePre, err := localPrePre(transferTX, 0)
	if err != nil {
		return err
	}
	unfreeze, err := stableCoin.PrepareUnfreezeCoinUTXO(
		aggregateKey,
		decoded.PrivKey,
		[]*contractutil.FtUTXO{frozenCoin},
		unfreezeFee,
		[]*bt.Tx{freezeTX},
		[]string{unfreezePrePre},
	)
	if err != nil {
		return fmt.Errorf("StableCoin unfreeze prepare: %w", err)
	}
	unfreezeRaws, err := finalizeAdminPrepared(unfreeze)
	if err != nil {
		return fmt.Errorf("StableCoin unfreeze finalize: %w", err)
	}
	if _, _, err := broadcastOne("stablecoin-unfreeze", unfreezeRaws[0], cfg.Network); err != nil {
		return err
	}
	fmt.Println("StableCoin create, transfer, freeze, and unfreeze scenarios pass")
	return nil
}
