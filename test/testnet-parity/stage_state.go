package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type stageName string

const (
	stageFoundation stageName = "foundation"
	stageFT         stageName = "ft"
	stageNFT        stageName = "nft"
	stageMultiSig   stageName = "multisig"
	stageBaseHTLC   stageName = "base-htlc"
	stagePiggyBank  stageName = "piggybank"
	stageStableCoin stageName = "stablecoin"
	stagePoolCreate stageName = "pool-create"
	stagePoolTrade  stageName = "pool-trade"
	stagePoolLock   stageName = "pool-lock"
	stageOrderBook  stageName = "orderbook"
	stageTBC20      stageName = "tbc20"

	stageLegacyHTLC           stageName = "htlc"
	stageLegacyCoreContracts  stageName = "core-contracts"
	stageLegacyPoolFoundation stageName = "pool-foundation"
	stageLegacyPoolInit       stageName = "pool-init"
	stageLegacyPoolConsume    stageName = "pool-consume"
)

type publicState struct {
	TokenID      string `json:"token_id,omitempty"`
	TokenCode    string `json:"token_code_hash,omitempty"`
	MultiSig     string `json:"multisig_address,omitempty"`
	PoolID       string `json:"pool_id,omitempty"`
	CollectionID string `json:"collection_id,omitempty"`
	CoinID       string `json:"coin_id,omitempty"`
	LastTxID     string `json:"last_txid,omitempty"`
	LastVout     uint32 `json:"last_vout,omitempty"`
}

func parseStage(value string) (stageName, error) {
	if value == "" {
		return stageFoundation, nil
	}
	stage := stageName(value)
	switch stage {
	case stageFoundation,
		stageFT,
		stageNFT,
		stageMultiSig,
		stageBaseHTLC,
		stagePiggyBank,
		stageStableCoin,
		stagePoolCreate,
		stagePoolTrade,
		stagePoolLock,
		stageOrderBook,
		stageTBC20,
		stageLegacyHTLC,
		stageLegacyCoreContracts,
		stageLegacyPoolFoundation,
		stageLegacyPoolInit,
		stageLegacyPoolConsume:
		return stage, nil
	default:
		return "", fmt.Errorf("unknown testnet stage %q", value)
	}
}

func writePublicState(output io.Writer, state publicState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "STATE %s\n", encoded)
	return err
}
