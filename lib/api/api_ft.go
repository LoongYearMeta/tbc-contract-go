package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

// FtInfoResponse mirrors TS FT info API response.
type FtInfoResponse struct {
	Name        string
	Symbol      string
	Decimal     int
	TotalSupply *big.Int
	CodeScript  string
	TapeScript  string
}

type ftBalanceResponse struct {
	Data struct {
		Balance json.RawMessage `json:"balance"`
	} `json:"data"`
}

type ftUtxoRaw struct {
	TxID     string          `json:"txid"`
	Index    int             `json:"index"`
	TBCValue uint64          `json:"tbc_value"`
	FTValue  json.RawMessage `json:"ft_value"`
}

type ftUtxoListResponse struct {
	Data struct {
		UTXOs []ftUtxoRaw `json:"utxos"`
	} `json:"data"`
}

type ftInfoResponseRaw struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data    struct {
		CodeScript string          `json:"code_script"`
		TapeScript string          `json:"tape_script"`
		Amount     json.RawMessage `json:"amount"`
		Decimal    int             `json:"decimal"`
		Name       string          `json:"name"`
		Symbol     string          `json:"symbol"`
	} `json:"data"`
}

// buildAddressOrHash converts an address/hash to a combinescript path segment
// (00||pkh for address, 01||hash for raw hash).
func buildAddressOrHash(addressOrHash string) (string, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, err := bscript.NewAddressFromString(addressOrHash)
		if err != nil {
			return "", err
		}
		return "00" + addr.PublicKeyHash, nil
	}
	if len(addressOrHash) == 40 && isHex(addressOrHash) {
		return "01" + addressOrHash, nil
	}
	return "", fmt.Errorf("Invalid address or hash")
}

// buildAddressOrHashLegacy is the main path used by FT queries, matching JS:
// address → pkh||"00", raw hash → hash||"01".
func buildAddressOrHashLegacy(addressOrHash string) (string, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, err := bscript.NewAddressFromString(addressOrHash)
		if err != nil {
			return "", err
		}
		return addr.PublicKeyHash + "00", nil
	}
	if len(addressOrHash) == 40 && isHex(addressOrHash) {
		return addressOrHash + "01", nil
	}
	return "", fmt.Errorf("Invalid address or hash")
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// enrichFtUtxoScriptsFromChain fetches parent txs and fills LockingScript/Satoshis
// in each FtUTXO. The indexer response doesn't include scripts; the parent tx
// must be fetched to get the correct locking script for signing.
func enrichFtUtxoScriptsFromChain(list []*util.FtUTXO, network string) error {
	txCache := make(map[string]*bt.Tx)
	for _, u := range list {
		if u == nil {
			continue
		}
		txidHex := hex.EncodeToString(u.TxID)
		tx, ok := txCache[txidHex]
		if !ok {
			var err error
			tx, err = FetchTXRaw(txidHex, network)
			if err != nil {
				return fmt.Errorf("enrich FtUTXO script: fetch tx %s: %w", txidHex, err)
			}
			txCache[txidHex] = tx
		}
		if int(u.Vout) >= len(tx.Outputs) {
			return fmt.Errorf("enrich FtUTXO script: vout %d out of range for tx %s (outputs=%d)", u.Vout, txidHex, len(tx.Outputs))
		}
		out := tx.Outputs[u.Vout]
		ls := out.LockingScript
		if ls == nil {
			return fmt.Errorf("enrich FtUTXO script: tx %s vout %d has nil locking script", txidHex, u.Vout)
		}
		u.LockingScript = ls
		u.Satoshis = out.Satoshis
	}
	return nil
}

func parseBigIntOrUint64(raw json.RawMessage) (*big.Int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return big.NewInt(0), nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return big.NewInt(0), nil
		}
		n := new(big.Int)
		if _, ok := n.SetString(s, 10); !ok {
			return nil, fmt.Errorf("invalid decimal integer string: %q", s)
		}
		return n, nil
	}
	// Use json.Number to avoid float64 precision loss for large FT values.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	num, ok := v.(json.Number)
	if !ok {
		return nil, fmt.Errorf("expected JSON number, got %T", v)
	}
	s := strings.TrimSpace(num.String())
	if s == "" {
		return big.NewInt(0), nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(s, 10); !ok {
		return nil, fmt.Errorf("invalid JSON integer: %q", s)
	}
	return n, nil
}

func getFTBalanceByHash(contractTxID, hash, network string) (*big.Int, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/tokenbalance/combinescript/%s/contract/%s", baseURL, hash, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 FT 余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

func getFTBalanceByAddress(contractTxID, address, network string) (*big.Int, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/tokenbalance/address/%s/contract/%s", baseURL, address, contractTxID)
	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 FT 按地址余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

func ftBalancePositive(b *big.Int) bool {
	return b != nil && b.Sign() > 0
}

func getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network string) (*big.Int, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return nil, err
	}
	bal, err := getFTBalanceByHash(contractTxID, hashJS, network)
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
	bal2, err3 := getFTBalanceByHash(contractTxID, hashAlt, network)
	if err3 != nil {
		return bal, nil
	}
	return bal2, nil
}

// GetFTBalance queries the FT balance for an address or combinescript hash.
func GetFTBalance(contractTxID, addressOrHash, network string) (*big.Int, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		balAddr, err := getFTBalanceByAddress(contractTxID, addressOrHash, network)
		if err == nil && ftBalancePositive(balAddr) {
			return balAddr, nil
		}
		return getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network)
	}
	return getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network)
}

func fetchFtUTXOListResponse(contractTxID, hash, network string) (ftUtxoListResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/utxo/combinescript/%s/contract/%s", baseURL, hash, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return ftUtxoListResponse{}, err
	}

	var r ftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ftUtxoListResponse{}, fmt.Errorf("解析 FT UTXO 响应失败: %w", err)
	}
	return r, nil
}

// FetchFtUTXOList returns all FT UTXOs for an address/hash.
func FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network string) ([]*util.FtUTXO, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return nil, err
	}
	r, err := fetchFtUTXOListResponse(contractTxID, hashJS, network)
	if err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		hashAlt, err2 := buildAddressOrHash(addressOrHash)
		if err2 == nil && hashAlt != hashJS {
			r, err = fetchFtUTXOListResponse(contractTxID, hashAlt, network)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The ft balance in the account is zero.")
	}

	// Parse codeScript as initial locking script (will be enriched from chain below).
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

// FetchFtUTXO returns a single FT UTXO with at least the requested amount.
func FetchFtUTXO(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) (*util.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	var selected *util.FtUTXO
	for _, u := range list {
		if u.FtBalance != nil && u.FtBalance.Cmp(amount) >= 0 {
			selected = u
			break
		}
	}
	if selected == nil {
		selected = list[0]
	}
	if selected.FtBalance != nil && selected.FtBalance.Cmp(amount) < 0 {
		total, err := GetFTBalance(contractTxID, addressOrHash, network)
		if err != nil {
			return nil, err
		}
		if total != nil && total.Cmp(amount) >= 0 {
			return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
		}
		return nil, fmt.Errorf("FTbalance not enough!")
	}
	return selected, nil
}

// FetchFtUTXOs returns up to 5 FT UTXOs whose combined balance meets the target.
func FetchFtUTXOs(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) ([]*util.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
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

	if amount == nil || amount.Sign() == 0 {
		max := 5
		if len(list) < max {
			max = len(list)
		}
		return list[:max], nil
	}

	sum := new(big.Int)
	var result []*util.FtUTXO
	for i := 0; i < len(list) && i < 5; i++ {
		if list[i].FtBalance != nil {
			sum.Add(sum, list[i].FtBalance)
		}
		result = append(result, list[i])
		if sum.Cmp(amount) >= 0 {
			return result, nil
		}
	}
	total, err := GetFTBalance(contractTxID, addressOrHash, network)
	if err != nil {
		return nil, err
	}
	if total != nil && total.Cmp(amount) >= 0 {
		return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
	}
	return nil, fmt.Errorf("FTbalance not enough!")
}

// FetchFtUTXOsForPool returns up to n FT UTXOs for pool operations.
func FetchFtUTXOsForPool(contractTxID, addressOrHash, codeScript, network string, amount *big.Int, n int) ([]*util.FtUTXO, error) {
	if n <= 0 {
		return nil, fmt.Errorf("Number must be a positive integer greater than 0")
	}
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
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

	sum := new(big.Int)
	var result []*util.FtUTXO
	for i := 0; i < len(list) && i < n; i++ {
		if list[i].FtBalance != nil {
			sum.Add(sum, list[i].FtBalance)
		}
		result = append(result, list[i])
		if i >= 1 && sum.Cmp(amount) >= 0 {
			break
		}
	}
	if sum.Cmp(amount) < 0 {
		total, err := GetFTBalance(contractTxID, addressOrHash, network)
		if err != nil {
			return nil, err
		}
		if total != nil && total.Cmp(amount) >= 0 {
			return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
		}
		return nil, fmt.Errorf("FTbalance not enough!")
	}
	return result, nil
}

// FetchFtUTXOsMultiSig returns all FT UTXOs sorted ascending by balance (for multisig merge).
func FetchFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network string) ([]*util.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
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
		return a.Cmp(b) < 0
	})
	return list, nil
}

// GetFtUTXOsMultiSig returns exactly 5 FT UTXOs whose combined balance meets the target.
func GetFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) ([]*util.FtUTXO, error) {
	list, err := FetchFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	balances := make([]*big.Int, len(list))
	total := new(big.Int)
	for i := range list {
		b := list[i].FtBalance
		if b == nil {
			b = big.NewInt(0)
		}
		balances[i] = b
		total.Add(total, b)
	}
	if total.Cmp(amount) < 0 {
		return nil, fmt.Errorf("Insufficient FT balance")
	}
	if len(list) <= 5 {
		return list, nil
	}
	indices := util.FindMinFiveSum(balances, amount)
	if indices == nil {
		return nil, fmt.Errorf("Please merge MultiSig UTXO")
	}
	return []*util.FtUTXO{
		list[indices[0]],
		list[indices[1]],
		list[indices[2]],
		list[indices[3]],
		list[indices[4]],
	}, nil
}

// FetchFtInfo returns FT token metadata.
func FetchFtInfo(contractTxID, network string) (*FtInfoResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/info/contract/%s", baseURL, contractTxID)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r ftInfoResponseRaw
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 FT Info 响应失败: %w", err)
	}
	if r.Code != "" && r.Code != "200" {
		msg := r.Message
		if r.Error != "" {
			msg = r.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%s", r.Code)
		}
		return nil, fmt.Errorf("FT Info 接口返回失败: %s", msg)
	}
	totalSupply, err := parseBigIntOrUint64(r.Data.Amount)
	if err != nil {
		return nil, fmt.Errorf("解析 FT Info amount 失败: %w", err)
	}
	if strings.TrimSpace(r.Data.CodeScript) == "" || strings.TrimSpace(r.Data.TapeScript) == "" {
		return nil, fmt.Errorf("FT Info 响应缺少 code/tape 脚本")
	}
	return &FtInfoResponse{
		CodeScript:  r.Data.CodeScript,
		TapeScript:  r.Data.TapeScript,
		TotalSupply: totalSupply,
		Decimal:     r.Data.Decimal,
		Name:        r.Data.Name,
		Symbol:      r.Data.Symbol,
	}, nil
}

// FetchFtPrePreTxData fetches and assembles the pre-pre tx data for FT unlock.
func FetchFtPrePreTxData(preTX *bt.Tx, preTxVout int, network string) (string, error) {
	if preTxVout+1 >= len(preTX.Outputs) {
		return "", fmt.Errorf("preTxVout+1 out of range")
	}
	tapeScript := preTX.Outputs[preTxVout+1].LockingScript.Bytes()
	if len(tapeScript) < 51 {
		return "", fmt.Errorf("tape script too short")
	}
	tapeSlice := tapeScript[3:51]
	tapeHex := hex.EncodeToString(tapeSlice)

	var prepretxdata string
	for i := len(tapeHex) - 16; i >= 0; i -= 16 {
		chunk := tapeHex[i : i+16]
		if chunk != "0000000000000000" {
			inputIndex := i / 16
			if inputIndex >= len(preTX.Inputs) {
				return "", fmt.Errorf("input index out of range")
			}
			prevTxID := hex.EncodeToString(preTX.Inputs[inputIndex].PreviousTxID())
			prepreTX, err := FetchTXRaw(prevTxID, network)
			if err != nil {
				return "", err
			}
			data, err := util.GetPrePreTxdata(prepreTX, int(preTX.Inputs[inputIndex].PreviousTxOutIndex))
			if err != nil {
				return "", err
			}
			prepretxdata += data
		}
	}
	return "57" + prepretxdata, nil
}

