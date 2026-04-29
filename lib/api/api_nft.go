package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

// NFTUTXO is the NFT UTXO shape (script kept as hex string for consumers that need it).
type NFTUTXO struct {
	TxID     string
	Vout     uint32
	Script   string
	Satoshis uint64
}

// NFTUTXOToBTUTXO converts an NFTUTXO to a *bt.UTXO.
func NFTUTXOToBTUTXO(u *NFTUTXO) (*bt.UTXO, error) {
	txid, err := hex.DecodeString(u.TxID)
	if err != nil {
		return nil, err
	}
	ls, err := bscript.NewFromHexString(u.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txid,
		Vout:          u.Vout,
		LockingScript: ls,
		Satoshis:      u.Satoshis,
	}, nil
}

type nftUtxoRaw struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	Value uint64 `json:"value"`
}

type nftUtxoListResponse struct {
	Data struct {
		UTXOs []nftUtxoRaw `json:"utxos"`
	} `json:"data"`
}

type nftInfoResponse struct {
	Data struct {
		CollectionID     string `json:"collection_id"`
		CollectionIndex  int    `json:"collection_index"`
		CollectionName   string `json:"collection_name"`
		NftName          string `json:"nft_name"`
		NftSymbol        string `json:"nft_symbol"`
		NftAttributes    string `json:"nft_attributes"`
		NftDescription   string `json:"nft_description"`
		NftTransferCount int    `json:"nft_transfer_count"`
		NftIcon          string `json:"nft_icon"`
	} `json:"data"`
}

type nftListItem struct {
	NftHolder     string `json:"nft_holder"`
	NftContractID string `json:"nft_contract_id"`
}

type nftListResponse struct {
	Data struct {
		NftList []nftListItem `json:"nft_list"`
	} `json:"data"`
}

// scriptHashFromHex computes the TBC indexer's scriptpubkeyhash (reversed SHA256).
func scriptHashFromHex(scriptHex string) (string, error) {
	b, err := hex.DecodeString(scriptHex)
	if err != nil {
		return "", err
	}
	h := crypto.Sha256(b)
	return hex.EncodeToString(bt.ReverseBytes(h)), nil
}

// FetchNFTUTXO fetches a single NFT UTXO by script and optional txHash filter.
// Mirrors TS fetchNFTTXO.
func FetchNFTUTXO(script, txHash, network string) (*bt.UTXO, error) {
	hash, err := scriptHashFromHex(script)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	var url string
	if strings.TrimSpace(txHash) != "" {
		url = fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)
	} else {
		url = fmt.Sprintf("%snft/utxo/scriptpubkeyhash/%s", baseURL, hash)
	}

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r nftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("No matching UTXO found.")
	}

	var chosen nftUtxoRaw
	if txHash != "" {
		want := strings.TrimSpace(txHash)
		var filtered []nftUtxoRaw
		for _, u := range r.Data.UTXOs {
			if strings.EqualFold(strings.TrimSpace(u.TxID), want) {
				filtered = append(filtered, u)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("No matching UTXO found.")
		}
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Index < filtered[j].Index
		})
		chosen = filtered[0]
	} else {
		chosen = r.Data.UTXOs[0]
	}

	txidBytes, err := hex.DecodeString(chosen.TxID)
	if err != nil {
		return nil, err
	}
	ls, err := bscript.NewFromHexString(script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(chosen.Index),
		LockingScript: ls,
		Satoshis:      chosen.Value,
	}, nil
}

// FetchNFTUTXOs fetches all NFT UTXOs matching script and txHash.
func FetchNFTUTXOs(script, txHash, network string) ([]*bt.UTXO, error) {
	hash, err := scriptHashFromHex(script)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r nftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}

	want := strings.TrimSpace(txHash)
	var filtered []nftUtxoRaw
	for _, u := range r.Data.UTXOs {
		if strings.EqualFold(strings.TrimSpace(u.TxID), want) {
			filtered = append(filtered, u)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("The collection supply has been exhausted.")
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Index < filtered[j].Index
	})

	ls, err := bscript.NewFromHexString(script)
	if err != nil {
		return nil, err
	}

	result := make([]*bt.UTXO, 0, len(filtered))
	for _, u := range filtered {
		txidBytes, err := hex.DecodeString(u.TxID)
		if err != nil {
			return nil, err
		}
		result = append(result, &bt.UTXO{
			TxID:          txidBytes,
			Vout:          uint32(u.Index),
			LockingScript: ls,
			Satoshis:      u.Value,
		})
	}
	return result, nil
}

// FetchNFTInfo returns NFT metadata for a contract ID.
// Returns *util.NFTInfo (defined in util to avoid api↔contract cycle).
func FetchNFTInfo(contractID, network string) (*util.NFTInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%snft/nftinfo/nftid/%s", baseURL, contractID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r nftInfoResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	return &util.NFTInfo{
		CollectionID:         r.Data.CollectionID,
		CollectionIndex:      r.Data.CollectionIndex,
		CollectionName:       r.Data.CollectionName,
		NFTName:              r.Data.NftName,
		NFTSymbol:            r.Data.NftSymbol,
		NFTAttributes:        r.Data.NftAttributes,
		NFTDescription:       r.Data.NftDescription,
		NFTTransferTimeCount: r.Data.NftTransferCount,
		NFTIcon:              r.Data.NftIcon,
	}, nil
}

// FetchNFTs returns NFT brief info for a collection, filtered by owner address.
// Returns []*util.NFTBrief (defined in util to avoid api↔contract cycle).
func FetchNFTs(collectionID, address string, start, end int, network string) ([]*util.NFTBrief, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%snft/nftbycollection/collectionid/%s/start/%d/end/%d", baseURL, collectionID, start, end)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r nftListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	var result []*util.NFTBrief
	for _, n := range r.Data.NftList {
		if address == "" || n.NftHolder == address {
			result = append(result, &util.NFTBrief{
				NFTHolder:     n.NftHolder,
				NFTContractID: n.NftContractID,
			})
		}
	}
	return result, nil
}
