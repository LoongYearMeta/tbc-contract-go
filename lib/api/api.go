package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
)

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

var (
	ErrNoSufficientUTXO = errors.New("no single UTXO satisfies the requested minimum")
	ErrInvalidTBCAmount = errors.New("invalid TBC amount")
)

const (
	mainnetAPIURL = "https://api.turingbitchain.io/api/tbc/"
	testnetAPIURL = "https://api.tbcdev.org/api/tbc/"
)

// getBaseURL resolves network → base URL. Empty / "mainnet" → mainnet.
// "testnet" → testnet. Anything ending in "/" → custom base.
func getBaseURL(network string) string {
	switch network {
	case "testnet":
		return testnetAPIURL
	case "mainnet", "":
		return mainnetAPIURL
	default:
		if len(network) > 0 && network[len(network)-1] == '/' {
			return network
		}
		return network + "/"
	}
}

// isRetryableHTTPGetErr returns true for transient network errors that warrant a retry.
func isRetryableHTTPGetErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "server closed") ||
		strings.Contains(msg, "unexpected eof")
}

// httpGetResponseWithRetry retries GET on transient transport errors. Caller
// owns the returned *http.Response and MUST close its Body. POST broadcasts
// MUST NOT use this — only idempotent GETs are safe to retry.
func httpGetResponseWithRetry(url string) (*http.Response, error) {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(300*attempt) * time.Millisecond)
		}
		resp, err := defaultHTTPClient.Get(url)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableHTTPGetErr(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// httpGetWithRetry is the read-and-discard variant of httpGetResponseWithRetry:
// it returns the response body bytes and closes the response.
func httpGetWithRetry(url string) ([]byte, error) {
	resp, err := httpGetResponseWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// apiCodeError returns an error if the response body's top-level `code`
// field is set to a non-success value. The TBC indexer convention is
// `code: "200"` on success — any other non-empty value indicates a logical
// error (auth, rate limit, missing object, etc.) returned via HTTP 200.
//
// Returns nil when:
//   - The body is not a JSON object (e.g., array, scalar, malformed).
//   - The body has no `code` field at all (some endpoints don't include one).
//   - The `code` field is empty, "200", or "0".
//
// So this helper is safe to call after every json.Unmarshal: at worst it's
// a no-op for endpoints that don't surface a code field.
func apiCodeError(body []byte) error {
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	if env.Code == "" || env.Code == "200" || env.Code == "0" {
		return nil
	}
	msg := env.Message
	if msg == "" {
		msg = env.Error
	}
	if msg == "" {
		msg = "(no message)"
	}
	return fmt.Errorf("api: code=%s: %s", env.Code, msg)
}

// indexerP2PKHLookupAddress converts testnet addresses for the TBC indexer.
// The public testnet indexer (api.tbcdev.org) expects legacy mainnet-style
// addresses for P2PKH balance/UTXO lookups. Custom base URLs are not rewritten.
func indexerP2PKHLookupAddress(network, address string) string {
	if strings.HasPrefix(network, "http://") || strings.HasPrefix(network, "https://") {
		return address
	}
	if network != "testnet" {
		return address
	}
	a, err := bscript.NewAddressFromString(address)
	if err != nil {
		return address
	}
	pkh, err := hex.DecodeString(a.PublicKeyHash)
	if err != nil || len(pkh) != 20 {
		return address
	}
	main, err := bscript.NewAddressFromPublicKeyHash(pkh, true)
	if err != nil {
		return address
	}
	return main.AddressString
}

// === Response structs ===

type balanceResponse struct {
	Data struct {
		Balance uint64 `json:"balance"`
	} `json:"data"`
}

type utxoListItem struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	Value uint64 `json:"value"`
}

type utxoListResponse struct {
	Data struct {
		UTXOs []utxoListItem `json:"utxos"`
	} `json:"data"`
}

func selectUTXOAtLeast(utxos []utxoListItem, minimumSat uint64) (*utxoListItem, error) {
	for i := range utxos {
		if utxos[i].Value >= minimumSat {
			return &utxos[i], nil
		}
	}
	return nil, fmt.Errorf("%w: requested %d sat", ErrNoSufficientUTXO, minimumSat)
}

func tbcAmountToSatoshis(amountTBC float64) (uint64, error) {
	if math.IsNaN(amountTBC) || math.IsInf(amountTBC, 0) || amountTBC < 0 {
		return 0, ErrInvalidTBCAmount
	}

	scaled := amountTBC * 1_000_000
	if math.IsInf(scaled, 0) || scaled >= math.Ldexp(1, 64) {
		return 0, ErrInvalidTBCAmount
	}
	rounded := math.Round(scaled)
	if math.Abs(scaled-rounded) > 1e-6 {
		return 0, fmt.Errorf("%w: amount has more than six decimal places", ErrInvalidTBCAmount)
	}
	return uint64(rounded), nil
}

type broadcastResponse struct {
	Code string `json:"code"`
	Data struct {
		TxID    string `json:"txid"`
		Error   string `json:"error"`
		Success int    `json:"success"`
		Failed  int    `json:"failed"`
	} `json:"data"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// FlexStringOrNumber unmarshals a JSON value that may be either a string or a number.
type FlexStringOrNumber string

func (f *FlexStringOrNumber) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexStringOrNumber(s)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(b, &num); err == nil {
		*f = FlexStringOrNumber(num.String())
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexStringOrNumber(strconv.FormatFloat(v, 'f', -1, 64))
	return nil
}

// String returns the decoded scalar as text.
func (f FlexStringOrNumber) String() string { return string(f) }

// BlockHeaderInfo is the per-block-header shape returned by FetchBlockHeaders.
type BlockHeaderInfo struct {
	Hash              string             `json:"hash"`
	Confirmations     int                `json:"confirmations"`
	Height            int                `json:"height"`
	Version           int                `json:"version"`
	VersionHex        string             `json:"versionHex"`
	MerkleRoot        string             `json:"merkleroot"`
	Time              int64              `json:"time"`
	Nonce             uint32             `json:"nonce"`
	Bits              string             `json:"bits"`
	Difficulty        FlexStringOrNumber `json:"difficulty"`
	PreviousBlockHash string             `json:"previoushash"`
	NextBlockHash     string             `json:"nexthash"`
}

type blockHeadersResponse struct {
	Data []BlockHeaderInfo `json:"data"`
}

type lockTimeResponse struct {
	Data struct {
		LockTime uint32 `json:"locktime"`
	} `json:"data"`
}

// BroadcastTXsRequestItem is the per-tx element for BroadcastTXsRaw.
type BroadcastTXsRequestItem struct {
	TxRaw string `json:"txraw"`
}

var txidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

func normalizeTxID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isValidTxIDString(txid string) bool {
	txid = normalizeTxID(txid)
	if len(txid) != 64 {
		return false
	}
	_, err := hex.DecodeString(txid)
	return err == nil
}

func findTxIDInText(text string) (string, bool) {
	m := txidPattern.FindString(text)
	if m == "" {
		return "", false
	}
	return normalizeTxID(m), true
}

// isBroadcastAlreadyKnownErr returns true for "already known" error responses.
// Duplicate broadcasts should be treated as success.
func isBroadcastAlreadyKnownErr(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already-known") ||
		strings.Contains(m, "already known") ||
		strings.Contains(m, "txn-already") ||
		strings.Contains(m, "rejecting duplicated") ||
		strings.Contains(m, "tx-already-in-mempool")
}

// === Public API ===

// GetTBCBalance returns the TBC balance (in satoshis) for an address.
func GetTBCBalance(address, network string) (uint64, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sbalance/address/%s", baseURL, indexerP2PKHLookupAddress(network, address))

	body, err := httpGetWithRetry(url)
	if err != nil {
		return 0, fmt.Errorf("请求余额接口失败: %w", err)
	}

	var br balanceResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return 0, fmt.Errorf("解析余额响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return 0, err
	}
	return br.Data.Balance, nil
}

// FetchUTXOList returns all UTXOs for an address.
func FetchUTXOList(address, network string) ([]*bt.UTXO, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/address/%s", baseURL, indexerP2PKHLookupAddress(network, address))

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 UTXO 接口失败: %w", err)
	}

	var ur utxoListResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return nil, fmt.Errorf("解析 UTXO 响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(ur.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("该地址没有可用的 UTXO")
	}

	lockingScript, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return nil, fmt.Errorf("创建锁定脚本失败: %w", err)
	}

	result := make([]*bt.UTXO, 0, len(ur.Data.UTXOs))
	for i := range ur.Data.UTXOs {
		txidBytes, err := hex.DecodeString(ur.Data.UTXOs[i].TxID)
		if err != nil {
			return nil, fmt.Errorf("解码 txid 失败: %w", err)
		}
		result = append(result, &bt.UTXO{
			TxID:          txidBytes,
			Vout:          uint32(ur.Data.UTXOs[i].Index),
			Satoshis:      ur.Data.UTXOs[i].Value,
			LockingScript: lockingScript,
		})
	}
	return result, nil
}

func fetchUTXOInternalSat(address string, minimumSat uint64, network string) (*bt.UTXO, string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/address/%s", baseURL, indexerP2PKHLookupAddress(network, address))

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, "", fmt.Errorf("请求 UTXO 接口失败: %w", err)
	}

	var ur utxoListResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return nil, "", fmt.Errorf("解析 UTXO 响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, "", err
	}
	if len(ur.Data.UTXOs) == 0 {
		return nil, "", fmt.Errorf("该地址没有可用的 UTXO")
	}

	selected, err := selectUTXOAtLeast(ur.Data.UTXOs, minimumSat)
	if err != nil {
		return nil, "", err
	}

	apiTxid := strings.ToLower(strings.TrimSpace(selected.TxID))
	txidBytes, err := hex.DecodeString(selected.TxID)
	if err != nil {
		return nil, "", fmt.Errorf("解码 txid 失败: %w", err)
	}

	chainTx, err := FetchTXRaw(selected.TxID, network)
	if err != nil {
		return nil, "", fmt.Errorf("拉取 UTXO 父交易以校准 script/金额失败: %w", err)
	}
	if selected.Index < 0 || selected.Index >= len(chainTx.Outputs) {
		return nil, "", fmt.Errorf("UTXO vout %d 超出父交易输出数 %d", selected.Index, len(chainTx.Outputs))
	}
	out := chainTx.Outputs[selected.Index]
	if out.LockingScript == nil {
		return nil, "", fmt.Errorf("链上输出 %s:%d 无 locking script", selected.TxID, selected.Index)
	}
	if out.Satoshis < minimumSat {
		return nil, "", fmt.Errorf("%w: chain output %s:%d has %d sat, requested %d sat",
			ErrNoSufficientUTXO, selected.TxID, selected.Index, out.Satoshis, minimumSat)
	}

	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(selected.Index),
		Satoshis:      out.Satoshis,
		LockingScript: out.LockingScript,
	}, apiTxid, nil
}

// FetchUTXOSat returns the first UTXO whose chain value meets minimumSat.
func FetchUTXOSat(address string, minimumSat uint64, network string) (*bt.UTXO, error) {
	u, _, err := fetchUTXOInternalSat(address, minimumSat, network)
	return u, err
}

// FetchUTXO returns a single UTXO for an address meeting the minimum TBC amount.
func FetchUTXO(address string, amountTBC float64, network string) (*bt.UTXO, error) {
	minimumSat, err := tbcAmountToSatoshis(amountTBC)
	if err != nil {
		return nil, err
	}
	return FetchUTXOSat(address, minimumSat, network)
}

// FetchUTXOWithAPITxIDSat returns a sufficient UTXO plus its normalized API txid.
func FetchUTXOWithAPITxIDSat(address string, minimumSat uint64, network string) (*bt.UTXO, string, error) {
	return fetchUTXOInternalSat(address, minimumSat, network)
}

// FetchUTXOWithAPITxID returns a UTXO plus the raw txid string from the API.
func FetchUTXOWithAPITxID(address string, amountTBC float64, network string) (*bt.UTXO, string, error) {
	minimumSat, err := tbcAmountToSatoshis(amountTBC)
	if err != nil {
		return nil, "", err
	}
	return FetchUTXOWithAPITxIDSat(address, minimumSat, network)
}

// FetchUTXOs returns all UTXOs for an address (alias for FetchUTXOList with different error handling).
func FetchUTXOs(address, network string) ([]*bt.UTXO, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/address/%s", baseURL, indexerP2PKHLookupAddress(network, address))

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 UTXO 接口失败: %w", err)
	}

	var ur utxoListResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return nil, fmt.Errorf("解析 UTXO 响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(ur.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	lockingScript, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return nil, fmt.Errorf("创建锁定脚本失败: %w", err)
	}

	result := make([]*bt.UTXO, 0, len(ur.Data.UTXOs))
	for i := range ur.Data.UTXOs {
		txidBytes, err := hex.DecodeString(ur.Data.UTXOs[i].TxID)
		if err != nil {
			return nil, fmt.Errorf("解码 txid 失败: %w", err)
		}
		result = append(result, &bt.UTXO{
			TxID:          txidBytes,
			Vout:          uint32(ur.Data.UTXOs[i].Index),
			Satoshis:      ur.Data.UTXOs[i].Value,
			LockingScript: lockingScript,
		})
	}
	return result, nil
}

// GetUTXOsSat returns all UTXOs for an address when their checked total meets
// minimumSat.
func GetUTXOsSat(address string, minimumSat uint64, network string) ([]*bt.UTXO, error) {
	utxos, err := FetchUTXOs(address, network)
	if err != nil {
		return nil, err
	}
	var total uint64
	for _, u := range utxos {
		next, carry := bits.Add64(total, u.Satoshis, 0)
		if carry != 0 {
			return nil, bt.ErrAmountOverflow
		}
		total = next
	}
	if total < minimumSat {
		return nil, fmt.Errorf("Insufficient tbc balance")
	}
	return utxos, nil
}

// GetUTXOs returns all UTXOs for an address, returning an error if the total
// balance is less than the requested amount.
func GetUTXOs(address string, amountTBC float64, network string) ([]*bt.UTXO, error) {
	minimumSat, err := tbcAmountToSatoshis(amountTBC)
	if err != nil {
		return nil, err
	}
	return GetUTXOsSat(address, minimumSat, network)
}

// MergeUTXO merges all UTXOs for the address derived from privKey into a single UTXO.
func MergeUTXO(privKey *bec.PrivateKey, network string) (bool, error) {
	address, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return false, fmt.Errorf("derive address from private key: %w", err)
	}
	utxos, err := FetchUTXOs(address.AddressString, network)
	if err != nil {
		return false, err
	}
	if len(utxos) <= 1 {
		return false, nil
	}

	tx := bt.NewTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return false, fmt.Errorf("add inputs: %w", err)
	}
	if err := tx.ChangeToAddress(address.AddressString, bt.NewFeeQuote()); err != nil {
		return false, fmt.Errorf("change to address: %w", err)
	}
	getter := unlocker.Getter{PrivateKey: privKey}
	if err := tx.FillAllInputs(context.Background(), &getter); err != nil {
		return false, fmt.Errorf("sign: %w", err)
	}

	_, err = BroadcastTXRaw(tx.String(), network)
	if err != nil {
		return false, err
	}
	return true, nil
}

// FetchTXRaw fetches and parses a transaction by txid.
func FetchTXRaw(txid, network string) (*bt.Tx, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}

	var tr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TxRaw string `json:"txraw"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("解析 TXRaw 响应失败: %w", err)
	}
	if tr.Code != "" && tr.Code != "200" {
		msg := tr.Message
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("TXRaw 接口业务失败 code=%s: %s", tr.Code, msg)
	}
	raw := strings.TrimSpace(tr.Data.TxRaw)
	if raw == "" {
		return nil, fmt.Errorf("TXRaw 响应缺少 txraw (txid=%s)", normalizeTxID(txid))
	}
	return bt.NewTxFromString(raw)
}

// FetchTXRawHex returns the raw tx hex string without parsing.
func FetchTXRawHex(txid, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return "", fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}

	var tr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TxRaw string `json:"txraw"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("解析 TXRaw 响应失败: %w", err)
	}
	if tr.Code != "" && tr.Code != "200" {
		msg := tr.Message
		if msg == "" {
			msg = "unknown error"
		}
		return "", fmt.Errorf("TXRaw 接口业务失败 code=%s: %s", tr.Code, msg)
	}
	raw := strings.TrimSpace(tr.Data.TxRaw)
	if raw == "" {
		return "", fmt.Errorf("TXRaw 响应缺少 txraw (txid=%s)", normalizeTxID(txid))
	}
	return raw, nil
}

// BroadcastTXRaw broadcasts a raw transaction and returns the txid.
func BroadcastTXRaw(txraw, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := baseURL + "broadcasttx"

	payload := map[string]string{"txraw": txraw}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化请求体失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var br broadcastResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return "", fmt.Errorf("解析广播响应失败: %w, 内容: %s", err, string(body))
	}

	if br.Code == "200" {
		txid := normalizeTxID(br.Data.TxID)
		if isValidTxIDString(txid) {
			return txid, nil
		}
		if txidFromMsg, ok := findTxIDInText(br.Message); ok {
			return txidFromMsg, nil
		}
		if txidFromBody, ok := findTxIDInText(string(body)); ok {
			return txidFromBody, nil
		}
		return "", fmt.Errorf("广播成功但返回了无效 txid: %q", br.Data.TxID)
	}

	errMsg := br.Message
	if br.Data.Error != "" {
		errMsg = br.Data.Error
	}
	if br.Error != "" {
		errMsg = br.Error
	}
	if errMsg == "" {
		errMsg = fmt.Sprintf("广播失败，code=%s", br.Code)
	}

	combinedErr := errMsg + " " + string(body)
	if isBroadcastAlreadyKnownErr(combinedErr) {
		tx, parseErr := bt.NewTxFromString(txraw)
		if parseErr == nil {
			return tx.TxID(), nil
		}
	}

	return "", fmt.Errorf("%s", errMsg)
}

// BroadcastTXsRaw broadcasts multiple raw transactions.
func BroadcastTXsRaw(rawList []string, network string) (string, error) {
	type item struct {
		TxRaw string `json:"txraw"`
	}
	items := make([]item, len(rawList))
	for i, r := range rawList {
		items[i] = item{TxRaw: r}
	}
	baseURL := getBaseURL(network)
	url := baseURL + "broadcasttxs"

	jsonData, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("序列化请求体失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var br broadcastResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return "", fmt.Errorf("解析广播响应失败: %w, 内容: %s", err, string(body))
	}

	if br.Code == "200" {
		return fmt.Sprintf("success=%d failed=%d", br.Data.Success, br.Data.Failed), nil
	}
	if br.Code == "400" && (bytes.Contains(body, []byte("partial failure")) || br.Data.Success > 0) {
		return fmt.Sprintf("success=%d failed=%d", br.Data.Success, br.Data.Failed), nil
	}
	errMsg := br.Message
	if br.Error != "" {
		errMsg = br.Error
	}
	if errMsg == "" {
		errMsg = "Broadcast failed"
	}
	return "", fmt.Errorf("%s", errMsg)
}

// IsTxOnChain returns true if the transaction is on chain.
func IsTxOnChain(txid, network string) (bool, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	resp, err := httpGetResponseWithRetry(url)
	if err != nil {
		return false, fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("TXRaw 接口返回状态码 %d: %s", resp.StatusCode, string(body))
}

// FetchBlockHeaders returns block headers for the given start/end range.
func FetchBlockHeaders(start, end int, network string) ([]BlockHeaderInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%srecentblocks/start/%d/end/%d", baseURL, start, end)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求区块头接口失败: %w", err)
	}

	var r blockHeadersResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析区块头响应失败: %w", err)
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// FetchTBCLockTime fetches the current TBC lock time from the network.
// Moved here from util per spec §3.3 (util should not import net/http).
func FetchTBCLockTime(network string) (uint32, error) {
	headers, err := FetchBlockHeaders(0, 1, network)
	if err != nil || len(headers) == 0 {
		return 0, fmt.Errorf("failed to fetch block headers: %w", err)
	}
	return uint32(headers[0].Height), nil
}
