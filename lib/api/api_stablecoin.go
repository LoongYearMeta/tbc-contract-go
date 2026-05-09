package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// CoinInfoResponse mirrors the `coinInfo` half of TS API.fetchCoinInfo's
// return shape — same fields as FT info plus the parent NFT txid.
type CoinInfoResponse struct {
	NftTXID     string
	Name        string
	Symbol      string
	Decimal     int
	TotalSupply *big.Int
	CodeScript  string
	TapeScript  string
}

type stableCoinInfoRaw struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data    struct {
		CodeScript string          `json:"code_script"`
		TapeScript string          `json:"tape_script"`
		Supply     json.RawMessage `json:"supply"`
		Decimal    int             `json:"decimal"`
		Name       string          `json:"name"`
		Symbol     string          `json:"symbol"`
		Utxo       struct {
			Txid string `json:"txid"`
		} `json:"utxo"`
	} `json:"data"`
}

// FetchCoinInfo fetches stableCoin metadata + the on-chain coin-NFT txid via
// `stablecoin/info/stablecoinid/{contractTxid}`. Mirrors TS API.fetchCoinInfo.
func FetchCoinInfo(contractTxID, network string) (*CoinInfoResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/info/stablecoinid/%s", baseURL, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r stableCoinInfoRaw
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 stablecoin info 响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	totalSupply, err := parseBigIntOrUint64(r.Data.Supply)
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
	return &CoinInfoResponse{
		NftTXID:     txid,
		CodeScript:  r.Data.CodeScript,
		TapeScript:  r.Data.TapeScript,
		TotalSupply: totalSupply,
		Decimal:     r.Data.Decimal,
		Name:        r.Data.Name,
		Symbol:      r.Data.Symbol,
	}, nil
}

func getCoinBalanceByHash(contractTxID, hash, network string) (*big.Int, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/tokenbalance/combinescript/%s/stablecoinid/%s", baseURL, hash, contractTxID)
	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 stablecoin 余额响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

// GetCoinBalance queries stableCoin balance for an address or combinescript hash.
// Mirrors TS API.getCoinbalance. Returns the raw on-chain integer (apply
// decimal scaling on the caller side).
func GetCoinBalance(contractTxID, addressOrHash, network string) (*big.Int, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return nil, err
	}
	bal, err := getCoinBalanceByHash(contractTxID, hashJS, network)
	if err != nil {
		return nil, err
	}
	if ftBalancePositive(bal) {
		return bal, nil
	}
	hashAlt, err2 := buildAddressOrHash(addressOrHash)
	if err2 != nil || hashAlt == hashJS {
		return bal, nil
	}
	bal2, err3 := getCoinBalanceByHash(contractTxID, hashAlt, network)
	if err3 != nil {
		return bal, nil
	}
	return bal2, nil
}

func fetchCoinUTXOListResponse(contractTxID, hash, network string) (ftUtxoListResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sstablecoin/utxo/combinescript/%s/stablecoinid/%s", baseURL, hash, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return ftUtxoListResponse{}, err
	}
	var r ftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ftUtxoListResponse{}, fmt.Errorf("解析 stablecoin UTXO 响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return ftUtxoListResponse{}, err
	}
	return r, nil
}

// FetchCoinUTXOList returns all stableCoin UTXOs for an address/hash.
// Mirrors TS API.fetchCoinUTXOList. The locking scripts on the returned UTXOs
// are enriched from chain (the indexer response itself doesn't carry them).
func FetchCoinUTXOList(contractTxID, addressOrHash, codeScript, network string) ([]*util.FtUTXO, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return nil, err
	}
	r, err := fetchCoinUTXOListResponse(contractTxID, hashJS, network)
	if err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		hashAlt, err2 := buildAddressOrHash(addressOrHash)
		if err2 == nil && hashAlt != hashJS {
			r, err = fetchCoinUTXOListResponse(contractTxID, hashAlt, network)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The ft balance in the account is zero.")
	}

	var initScript *bscript.Script
	if codeScript != "" {
		initScript, err = bscript.NewFromHexString(codeScript)
		if err != nil {
			initScript = nil
		}
	}

	result := make([]*util.FtUTXO, 0, len(r.Data.UTXOs))
	for i := range r.Data.UTXOs {
		fv, err := parseBigIntOrUint64(r.Data.UTXOs[i].FTValue)
		if err != nil {
			return nil, err
		}
		txidBytes, err := hex.DecodeString(r.Data.UTXOs[i].TxID)
		if err != nil {
			return nil, fmt.Errorf("decode txid: %w", err)
		}
		result = append(result, &util.FtUTXO{
			TxID:          txidBytes,
			Vout:          uint32(r.Data.UTXOs[i].Index),
			LockingScript: initScript,
			Satoshis:      r.Data.UTXOs[i].TBCValue,
			FtBalance:     fv,
		})
	}
	if err := enrichFtUtxoScriptsFromChain(result, network); err != nil {
		return nil, err
	}
	return result, nil
}

// FetchCoinUTXOs returns up to `number` (or all if 0) stableCoin UTXOs sorted
// by descending balance, picking the smallest prefix whose sum >= amount.
// Mirrors TS API.fetchCoinUTXOs.
func FetchCoinUTXOs(contractTxID, addressOrHash, codeScript, network string, amount *big.Int, number int) ([]*util.FtUTXO, error) {
	if number < 0 {
		return nil, fmt.Errorf("Number must be a positive integer greater than 0")
	}
	list, err := FetchCoinUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		a := list[i].FtBalance
		b := list[j].FtBalance
		if a == nil {
			return true
		}
		if b == nil {
			return false
		}
		return a.Cmp(b) > 0
	})

	maxCount := number
	if maxCount == 0 || maxCount > len(list) {
		maxCount = len(list)
	}

	sum := new(big.Int)
	var result []*util.FtUTXO
	for i := 0; i < maxCount; i++ {
		if list[i].FtBalance != nil {
			sum.Add(sum, list[i].FtBalance)
		}
		result = append(result, list[i])
		if sum.Cmp(amount) >= 0 {
			return result, nil
		}
	}

	totalBalance := new(big.Int)
	for _, u := range list {
		if u.FtBalance != nil {
			totalBalance.Add(totalBalance, u.FtBalance)
		}
	}
	if totalBalance.Cmp(amount) >= 0 {
		if number > 0 {
			return nil, fmt.Errorf("Insufficient Coinbalance within number limit, please merge Coin UTXOs or increase number")
		}
		return nil, fmt.Errorf("Insufficient Coinbalance, please merge Coin UTXOs")
	}
	return nil, fmt.Errorf("Coinbalance not enough!")
}
