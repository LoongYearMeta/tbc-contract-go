//go:build legacy_stablecoin
// +build legacy_stablecoin

package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StableCoinInfoResult 对齐 tbc-contract API.fetchCoinInfo（stablecoin/info/stablecoinid）。
type StableCoinInfoResult struct {
	NftTXID     string
	CodeScript  string
	TapeScript  string
	TotalSupply string
	Decimal     uint
	Name        string
	Symbol      string
}

type stableCoinInfoResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data    struct {
		CodeScript string `json:"code_script"`
		TapeScript string `json:"tape_script"`
		Supply     json.RawMessage `json:"supply"`
		Decimal    uint   `json:"decimal"`
		Name       string `json:"name"`
		Symbol     string `json:"symbol"`
		Utxo       struct {
			Txid string `json:"txid"`
		} `json:"utxo"`
	} `json:"data"`
}

// FetchStableCoinInfo 请求 stablecoin/info/stablecoinid/{contractTxid}，返回 FT 元数据与当前 coin NFT 所在交易 id（用于 MintCoin 的 nftPreTX）。
func FetchStableCoinInfo(contractTxID, network string) (*StableCoinInfoResult, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/info/stablecoinid/%s", baseURL, contractTxID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var r stableCoinInfoResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 stablecoin info 响应失败: %w", err)
	}
	if r.Code != "" && r.Code != "200" {
		msg := r.Message
		if r.Error != "" {
			msg = r.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%s", r.Code)
		}
		return nil, fmt.Errorf("stablecoin info 接口返回失败: %s", msg)
	}
	supply, err := parseBigIntOrUint64(r.Data.Supply)
	if err != nil {
		return nil, fmt.Errorf("解析 supply 失败: %w", err)
	}
	if strings.TrimSpace(r.Data.CodeScript) == "" || strings.TrimSpace(r.Data.TapeScript) == "" {
		return nil, fmt.Errorf("stablecoin info 响应缺少 code/tape 脚本")
	}
	txid := strings.TrimSpace(r.Data.Utxo.Txid)
	if txid == "" {
		return nil, fmt.Errorf("stablecoin info 响应缺少 utxo.txid")
	}
	return &StableCoinInfoResult{
		NftTXID:     txid,
		CodeScript:  r.Data.CodeScript,
		TapeScript:  r.Data.TapeScript,
		TotalSupply: supply,
		Decimal:     r.Data.Decimal,
		Name:        r.Data.Name,
		Symbol:      r.Data.Symbol,
	}, nil
}

// StableCoinIndexerIDFromMintContractTx 返回索引器 stablecoin/.../stablecoinid/{id} 所用的 id：
// 对稳定币首铸 mint 交易，与 tbc-contract fetchCoinUTXOList 一致，该 id 为 **coin NFT 交易**（mint 的 input0 所花费的上游 txid）。
// 若 contractTxID 不是 mint 或无法取父交易，返回错误。
func StableCoinIndexerIDFromMintContractTx(contractTxID, network string) (string, error) {
	tx, err := FetchTXRaw(contractTxID, network)
	if err != nil {
		return "", err
	}
	if len(tx.Inputs) == 0 {
		return "", fmt.Errorf("tx %s has no inputs", contractTxID)
	}
	prev := strings.TrimSpace(strings.ToLower(hex.EncodeToString(tx.Inputs[0].PreviousTxID())))
	if prev == "" {
		return "", fmt.Errorf("tx %s input0 has empty prev txid", contractTxID)
	}
	return prev, nil
}

func fetchStableCoinUtxoListResponse(stablecoinIndexerID, hash, network string) (ftUtxoListResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/utxo/combinescript/%s/stablecoinid/%s", baseURL, hash, stablecoinIndexerID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return ftUtxoListResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ftUtxoListResponse{}, err
	}

	var r ftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ftUtxoListResponse{}, fmt.Errorf("解析 stablecoin UTXO 响应失败: %w", err)
	}
	return r, nil
}

func getStableCoinTokenBalanceByAddress(stablecoinIndexerID, address, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/tokenbalance/address/%s/stablecoinid/%s", baseURL, address, stablecoinIndexerID)
	resp, err := httpGetWithRetry(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("解析 stablecoin 按地址余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

func getStableCoinTokenBalanceByCombinescript(stablecoinIndexerID, hash, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/tokenbalance/combinescript/%s/stablecoinid/%s", baseURL, hash, stablecoinIndexerID)
	resp, err := httpGetWithRetry(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("解析 stablecoin combinescript 余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

// getStableCoinTokenBalanceCombinescriptDual 与 getFTBalanceCombinescriptDual 相同的两段 combinescript 键，路径为 stablecoin/tokenbalance/.../stablecoinid。
func getStableCoinTokenBalanceCombinescriptDual(stablecoinIndexerID, addressOrHash, network string) (string, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return "", err
	}
	bal, err := getStableCoinTokenBalanceByCombinescript(stablecoinIndexerID, hashJS, network)
	if err != nil {
		return "", err
	}
	if ftBalanceStringPositive(bal) {
		return bal, nil
	}
	hashAlt, err2 := buildAddressOrHash(addressOrHash)
	if err2 != nil || hashAlt == hashJS {
		return bal, nil
	}
	bal2, err3 := getStableCoinTokenBalanceByCombinescript(stablecoinIndexerID, hashAlt, network)
	if err3 != nil {
		return bal, nil
	}
	return bal2, nil
}
