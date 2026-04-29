package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// PoolNFTInfo holds the indexer response for a pool NFT.
type PoolNFTInfo struct {
	FtLpAmount             string `json:"ft_lp_amount"`
	FtAAmount              string `json:"ft_a_amount"`
	TBCAmount              string `json:"tbc_amount"`
	FtLpPartialHash        string `json:"ft_lp_partialhash"`
	FtAPartialHash         string `json:"ft_a_partialhash"`
	FtAContractTxID        string `json:"ft_a_contractTxid"`
	ServiceFeeRate         string `json:"service_fee_rate"`
	ServiceProvider        string `json:"service_provider"`
	PoolNftCode            string `json:"poolnft_code"`
	PoolVersion            int    `json:"pool_version"`
	CurrentContractTxID    string `json:"currentContractTxid"`
	CurrentContractVout    int    `json:"currentContractVout"`
	CurrentContractSatoshi uint64 `json:"currentContractSatoshi"`
}

// LpUTXO holds an LP token UTXO.
type LpUTXO struct {
	TxID      string
	Vout      uint32
	Script    string
	Satoshis  uint64
	FtBalance *big.Int
}

type poolNftInfoRaw struct {
	Data struct {
		LpBalance       json.RawMessage `json:"lp_balance"`
		TokenBalance    json.RawMessage `json:"token_balance"`
		TBCBalance      json.RawMessage `json:"tbc_balance"`
		FtLpPartialHash string          `json:"ft_lp_partial_hash"`
		FtAPartialHash  string          `json:"ft_a_partial_hash"`
		FtContractID    string          `json:"ft_contract_id"`
		ServiceFeeRate  string          `json:"service_fee_rate"`
		ServiceProvider string          `json:"service_provider"`
		PoolCodeScript  string          `json:"pool_code_script"`
		Version         int             `json:"version"`
		TxID            string          `json:"txid"`
		Vout            int             `json:"vout"`
		Value           uint64          `json:"value"`
	} `json:"data"`
}

type lpUtxoRaw struct {
	TxID       string          `json:"txid"`
	Index      int             `json:"index"`
	TBCBalance uint64          `json:"tbc_balance"`
	LpBalance  json.RawMessage `json:"lp_balance"`
}

type lpUtxoListResponse struct {
	Data struct {
		UTXOs []lpUtxoRaw `json:"utxos"`
	} `json:"data"`
}

// FetchPoolNFTInfo fetches pool NFT info by contract txid.
func FetchPoolNFTInfo(contractTxID, network string) (*PoolNFTInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%spool/poolinfo/poolid/%s", baseURL, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r poolNftInfoRaw
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 Pool NFT Info 失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}

	lpBal, _ := parseBigIntOrUint64(r.Data.LpBalance)
	tokenBal, _ := parseBigIntOrUint64(r.Data.TokenBalance)
	tbcBal, _ := parseBigIntOrUint64(r.Data.TBCBalance)

	lpStr := "0"
	if lpBal != nil {
		lpStr = lpBal.String()
	}
	tokenStr := "0"
	if tokenBal != nil {
		tokenStr = tokenBal.String()
	}
	tbcStr := "0"
	if tbcBal != nil {
		tbcStr = tbcBal.String()
	}

	return &PoolNFTInfo{
		FtLpAmount:             lpStr,
		FtAAmount:              tokenStr,
		TBCAmount:              tbcStr,
		FtLpPartialHash:        r.Data.FtLpPartialHash,
		FtAPartialHash:         r.Data.FtAPartialHash,
		FtAContractTxID:        r.Data.FtContractID,
		ServiceFeeRate:         r.Data.ServiceFeeRate,
		ServiceProvider:        r.Data.ServiceProvider,
		PoolNftCode:            r.Data.PoolCodeScript,
		PoolVersion:            r.Data.Version,
		CurrentContractTxID:    r.Data.TxID,
		CurrentContractVout:    r.Data.Vout,
		CurrentContractSatoshi: r.Data.Value,
	}, nil
}

// FetchPoolNFTUTXO fetches the pool NFT UTXO.
func FetchPoolNFTUTXO(contractTxID, network string) (*bt.UTXO, error) {
	info, err := FetchPoolNFTInfo(contractTxID, network)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch PoolNFT UTXO.: %w", err)
	}
	script, err := bscript.NewFromHexString(info.PoolNftCode)
	if err != nil {
		return nil, err
	}
	txidBytes, err := hex.DecodeString(info.CurrentContractTxID)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(info.CurrentContractVout),
		Satoshis:      info.CurrentContractSatoshi,
		LockingScript: script,
	}, nil
}

// FetchFtLpBalance returns the total LP token balance for a given LP code script.
func FetchFtLpBalance(ftlpCode, network string) (*big.Int, error) {
	hash, err := scriptHashFromHex(ftlpCode)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%spool/lputxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r lpUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}

	sum := new(big.Int)
	for _, u := range r.Data.UTXOs {
		lb, _ := parseBigIntOrUint64(u.LpBalance)
		if lb != nil {
			sum.Add(sum, lb)
		}
	}
	return sum, nil
}

// FetchFtLpUTXOList returns all LP UTXOs for the given ftlpCode script.
func FetchFtLpUTXOList(ftlpCode, network string) ([]*LpUTXO, error) {
	hash, err := scriptHashFromHex(ftlpCode)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%spool/lputxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r lpUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}

	result := make([]*LpUTXO, 0, len(r.Data.UTXOs))
	for i := range r.Data.UTXOs {
		lb, _ := parseBigIntOrUint64(r.Data.UTXOs[i].LpBalance)
		if lb == nil {
			lb = new(big.Int)
		}
		result = append(result, &LpUTXO{
			TxID:      r.Data.UTXOs[i].TxID,
			Vout:      uint32(r.Data.UTXOs[i].Index),
			Script:    ftlpCode,
			Satoshis:  r.Data.UTXOs[i].TBCBalance,
			FtBalance: lb,
		})
	}
	return result, nil
}

// FetchFtLpUTXO returns a single LP UTXO meeting the requested amount.
func FetchFtLpUTXO(ftlpCode string, amount *big.Int, network string) (*LpUTXO, error) {
	hash, err := scriptHashFromHex(ftlpCode)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%spool/lputxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r lpUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("Insufficient FT-LP amount")
	}

	var selected lpUtxoRaw
	for i := range r.Data.UTXOs {
		lb, _ := parseBigIntOrUint64(r.Data.UTXOs[i].LpBalance)
		if lb != nil && lb.Cmp(amount) >= 0 {
			selected = r.Data.UTXOs[i]
			break
		}
		selected = r.Data.UTXOs[i]
	}

	selectedLp, _ := parseBigIntOrUint64(selected.LpBalance)
	if selectedLp == nil || selectedLp.Cmp(amount) < 0 {
		sum := new(big.Int)
		for i := range r.Data.UTXOs {
			lb, _ := parseBigIntOrUint64(r.Data.UTXOs[i].LpBalance)
			if lb != nil {
				sum.Add(sum, lb)
			}
		}
		if sum.Cmp(amount) < 0 {
			return nil, fmt.Errorf("Insufficient FT-LP amount")
		}
		return nil, fmt.Errorf("Please merge FT-LP UTXOs")
	}

	return &LpUTXO{
		TxID:      selected.TxID,
		Vout:      uint32(selected.Index),
		Script:    ftlpCode,
		Satoshis:  selected.TBCBalance,
		FtBalance: selectedLp,
	}, nil
}
