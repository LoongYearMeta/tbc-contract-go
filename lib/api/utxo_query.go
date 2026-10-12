package api

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
)

// UTXOQueryOptions selects all pages, one merge batch, or a raw-unit target.
type UTXOQueryOptions struct {
	Target                *big.Int
	Limit                 int
	SinglePage, AllowNull bool
}

func isUTXOURL(u string) bool {
	for _, p := range []string{"/utxo/", "/lputxo/", "/frozenUtxo/"} {
		if strings.Contains(u, p) {
			return true
		}
	}
	return false
}

// FetchUTXORecords retains JSON integer precision and deduplicates overlapping pages.
func FetchUTXORecords(path, network string, options UTXOQueryOptions) ([]json.RawMessage, error) {
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("invalid UTXO query path")
	}
	body, e := fetchUTXOEnvelope(getBaseURL(network)+path, options)
	if e != nil {
		return nil, e
	}
	var v struct {
		Data struct {
			UTXOs []json.RawMessage `json:"utxos"`
		} `json:"data"`
	}
	e = json.Unmarshal(body, &v)
	return v.Data.UTXOs, e
}
func httpGetWithRetry(u string) ([]byte, error) {
	if isUTXOURL(u) {
		return fetchUTXOEnvelope(u, UTXOQueryOptions{AllowNull: strings.Contains(u, "/lputxo/")})
	}
	return httpGetSingleWithRetry(u)
}
func fetchUTXOEnvelope(u string, options UTXOQueryOptions) ([]byte, error) {
	if options.Target != nil && options.Target.Sign() <= 0 || options.Limit < 0 || options.Limit > 1000 {
		return nil, fmt.Errorf("target must be positive and limit must be 1..1000")
	}
	limit := options.Limit
	if limit == 0 && options.Target == nil {
		limit = 1000
	}
	all := []json.RawMessage{}
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		parsed, e := url.Parse(u)
		if e != nil {
			return nil, e
		}
		q := parsed.Query()
		if limit != 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if options.Target != nil {
			q.Set("target", options.Target.String())
		}
		if cursor != "" {
			q.Set("after", cursor)
		}
		parsed.RawQuery = q.Encode()
		body, e := httpGetSingleWithRetry(parsed.String())
		if e != nil {
			return nil, e
		}
		if e = apiCodeError(body); e != nil {
			return nil, e
		}
		var envelope map[string]json.RawMessage
		if e = json.Unmarshal(body, &envelope); e != nil {
			return nil, e
		}
		if raw := envelope["error"]; len(raw) > 0 && string(raw) != "null" && string(raw) != "\"\"" {
			return nil, fmt.Errorf("index error: %s", raw)
		}
		var data map[string]json.RawMessage
		if e = json.Unmarshal(envelope["data"], &data); e != nil {
			return nil, e
		}
		raw, exists := data["utxos"]
		if !exists || string(raw) == "null" && !options.AllowNull {
			return nil, fmt.Errorf("missing UTXO array")
		}
		var records []json.RawMessage
		if e = json.Unmarshal(raw, &records); e != nil {
			return nil, e
		}
		page := map[string]bool{}
		for _, record := range records {
			var fields map[string]json.RawMessage
			if json.Unmarshal(record, &fields) == nil {
				var id string
				_ = json.Unmarshal(fields["txid"], &id)
				index := fields["index"]
				if index == nil {
					index = fields["vout"]
				}
				value := strings.Trim(string(index), "\"")
				digits := value != ""
				for _, c := range value {
					if c < '0' || c > '9' {
						digits = false
					}
				}
				if n, ok := new(big.Int).SetString(value, 10); id != "" && digits && ok {
					point := strings.ToLower(id) + ":" + n.String()
					if page[point] {
						return nil, fmt.Errorf("duplicate UTXO within a page")
					}
					page[point] = true
					if seen[point] {
						continue
					}
					seen[point] = true
				}
			}
			all = append(all, record)
		}
		if options.Target != nil || options.SinglePage {
			if options.SinglePage && limit > 0 && len(all) > limit {
				all = all[:limit]
			}
		} else if next, ok := data["next_cursor"]; ok {
			var value string
			if string(next) == "null" || json.Unmarshal(next, &value) != nil {
				return nil, fmt.Errorf("invalid next_cursor")
			}
			if value != "" {
				if cursors[value] {
					return nil, fmt.Errorf("repeated UTXO pagination cursor")
				}
				cursors[value] = true
				cursor = value
				continue
			}
		}
		data["utxos"], _ = json.Marshal(all)
		delete(data, "next_cursor")
		envelope["data"], _ = json.Marshal(data)
		return json.Marshal(envelope)
	}
}

func getUTXOListBody(u string, batchSize []int) ([]byte, error) {
	options := UTXOQueryOptions{AllowNull: strings.Contains(u, "/lputxo/")}
	if len(batchSize) > 1 || len(batchSize) == 1 && (batchSize[0] < 1 || batchSize[0] > 1000) {
		return nil, fmt.Errorf("batch size must be 1..1000")
	}
	if len(batchSize) == 1 {
		options.Limit = batchSize[0]
		options.SinglePage = true
	}
	return fetchUTXOEnvelope(u, options)
}

// IndexError preserves index selection failures for errors.As callers.
type IndexError struct {
	Code      string
	Status    int
	RequestID string
	Message   string
}

func (e *IndexError) Error() string {
	return fmt.Sprintf("index API HTTP %d, code %s: %s", e.Status, e.Code, e.Message)
}
func indexResponseError(body []byte, status int) error {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	raw := v["error"]
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "\"\"" {
		raw = v["code"]
	}
	if len(raw) == 0 {
		return nil
	}
	code := strings.Trim(string(raw), "\"")
	if status >= 200 && status < 300 && (code == "" || code == "0" || code == "200" || code == "success") {
		return nil
	}
	var message, id string
	_ = json.Unmarshal(v["message"], &message)
	_ = json.Unmarshal(v["request_id"], &id)
	return &IndexError{Code: code, Status: status, RequestID: id, Message: message}
}
