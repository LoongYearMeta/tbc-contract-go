package api

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// FrozenUTXO is a piggybank / frozen TBC UTXO.
type FrozenUTXO struct {
	TxID     string
	Vout     uint32
	Script   string
	Satoshis uint64
}

type frozenBalanceResponse struct {
	Data struct {
		Balance uint64 `json:"balance"`
	} `json:"data"`
}

type frozenUtxoRaw struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	Value uint64 `json:"value"`
}

type frozenUtxoListResponse struct {
	Data struct {
		UTXOs []frozenUtxoRaw `json:"utxos"`
	} `json:"data"`
}

// FetchFrozenTBCBalance returns the frozen TBC balance for an address.
func FetchFrozenTBCBalance(address, network string) (uint64, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sfrozenBalance/address/%s/piggyBank", baseURL, address)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return 0, err
	}

	var r frozenBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, err
	}
	return r.Data.Balance, nil
}

// fetchTBCLockTimeFromScript parses the lock time from a piggybank script hex.
func fetchTBCLockTimeFromScript(scriptHex string) (uint32, error) {
	if len(scriptHex) != 106*2 {
		return 0, fmt.Errorf("Invalid Piggy Bank script")
	}
	b, err := hex.DecodeString(scriptHex)
	if err != nil {
		return 0, err
	}
	script := bscript.NewFromBytes(b)
	chunks := script.DecodeChunks()
	if len(chunks) < 8 {
		return 0, fmt.Errorf("Invalid Piggy Bank script")
	}
	idx := len(chunks) - 8
	if idx < 0 {
		return 0, fmt.Errorf("Invalid Piggy Bank script")
	}
	chunk := chunks[idx]
	if len(chunk.Buf) < 4 {
		return 0, fmt.Errorf("Invalid Piggy Bank script")
	}
	return binary.LittleEndian.Uint32(chunk.Buf[:4]), nil
}

// FetchFrozenUTXOList returns all frozen piggybank UTXOs for an address.
func FetchFrozenUTXOList(addr, network string) ([]*FrozenUTXO, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sfrozenUtxo/address/%s/piggyBank", baseURL, addr)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r frozenUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}

	list := make([]*FrozenUTXO, 0, len(r.Data.UTXOs))
	for _, u := range r.Data.UTXOs {
		list = append(list, &FrozenUTXO{
			TxID:     u.TxID,
			Vout:     uint32(u.Index),
			Script:   "",
			Satoshis: u.Value,
		})
	}

	const batchSize = 5
	for i := 0; i < len(list); i += batchSize {
		end := i + batchSize
		if end > len(list) {
			end = len(list)
		}
		for j := i; j < end; j++ {
			tx, err := FetchTXRaw(list[j].TxID, network)
			if err != nil {
				return nil, err
			}
			vout := int(list[j].Vout)
			if vout >= len(tx.Outputs) {
				return nil, fmt.Errorf("output index out of range")
			}
			list[j].Script = hex.EncodeToString(tx.Outputs[vout].LockingScript.Bytes())
		}
	}
	return list, nil
}

// FetchUnfrozenUTXOList returns frozen UTXOs whose lockTime <= current block height.
func FetchUnfrozenUTXOList(addr, network string) ([]*FrozenUTXO, error) {
	list, err := FetchFrozenUTXOList(addr, network)
	if err != nil {
		return nil, err
	}
	headers, err := FetchBlockHeaders(0, 1, network)
	if err != nil || len(headers) == 0 {
		return nil, fmt.Errorf("Failed to fetch block headers")
	}
	currentBlock := headers[0].Height

	var unfrozen []*FrozenUTXO
	for _, u := range list {
		lockTime, err := fetchTBCLockTimeFromScript(u.Script)
		if err != nil {
			continue
		}
		if int64(lockTime) <= int64(currentBlock) {
			unfrozen = append(unfrozen, u)
		}
	}
	if len(unfrozen) == 0 {
		return nil, fmt.Errorf("No unfrozen UTXO available")
	}
	return unfrozen, nil
}

// FrozenToBTUTXO converts a FrozenUTXO to a *bt.UTXO.
func FrozenToBTUTXO(f *FrozenUTXO) (*bt.UTXO, error) {
	txid, err := hex.DecodeString(f.TxID)
	if err != nil {
		return nil, err
	}
	script, err := bscript.NewFromHexString(f.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txid,
		Vout:          f.Vout,
		LockingScript: script,
		Satoshis:      f.Satoshis,
	}, nil
}
