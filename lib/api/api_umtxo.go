package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

// SimpleUTXO is a generic UTXO for custom-script (UM) lookups.
type SimpleUTXO struct {
	TxID     string
	Vout     uint32
	Script   string
	Satoshis uint64
}

// SimpleUTXOToBTUTXO converts a SimpleUTXO to a *bt.UTXO.
func SimpleUTXOToBTUTXO(u *SimpleUTXO) (*bt.UTXO, error) {
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

type utxoByScriptHashResponse struct {
	Data struct {
		UTXOs []utxoListItem `json:"utxos"`
	} `json:"data"`
}

// FetchUMTXO fetches a single UTXO by custom script ASM, meeting the minimum TBC amount.
func FetchUMTXO(scriptASM string, tbcAmount float64, network string) (*bt.UTXO, error) {
	minimumSat, err := tbcAmountToSatoshis(tbcAmount)
	if err != nil {
		return nil, err
	}
	return FetchUMTXOSat(scriptASM, minimumSat, network)
}

// FetchUMTXOSat fetches a single custom-script UTXO meeting minimumSat.
func FetchUMTXOSat(scriptASM string, minimumSat uint64, network string) (*bt.UTXO, error) {
	script, err := bscript.NewFromASM(scriptASM)
	if err != nil {
		return nil, err
	}
	multiScript := hex.EncodeToString(script.Bytes())
	hash, err := scriptHashFromHex(multiScript)
	if err != nil {
		return nil, err
	}

	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r utxoByScriptHashResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	selected, err := selectUTXOAtLeast(r.Data.UTXOs, minimumSat)
	if err != nil {
		var total uint64
		for i := range r.Data.UTXOs {
			next, carry := bits.Add64(total, r.Data.UTXOs[i].Value, 0)
			if carry != 0 {
				return nil, bt.ErrAmountOverflow
			}
			total = next
		}
		if total < minimumSat {
			return nil, fmt.Errorf("%w: total balance %d sat, requested %d sat",
				ErrNoSufficientUTXO, total, minimumSat)
		}
		return nil, fmt.Errorf("%w: total balance is sufficient but must be merged",
			ErrNoSufficientUTXO)
	}

	ls, err := bscript.NewFromHexString(multiScript)
	if err != nil {
		return nil, err
	}
	txidBytes, err := hex.DecodeString(selected.TxID)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(selected.Index),
		LockingScript: ls,
		Satoshis:      selected.Value,
	}, nil
}

// FetchUMTXOs fetches all UTXOs for a custom script ASM.
func FetchUMTXOs(scriptASM string, network string) ([]*bt.UTXO, error) {
	script, err := bscript.NewFromASM(scriptASM)
	if err != nil {
		return nil, err
	}
	multiScript := hex.EncodeToString(script.Bytes())
	hash, err := scriptHashFromHex(multiScript)
	if err != nil {
		return nil, err
	}

	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	body, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}

	var r utxoByScriptHashResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if err := apiCodeError(body); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	ls, err := bscript.NewFromHexString(multiScript)
	if err != nil {
		return nil, err
	}

	result := make([]*bt.UTXO, 0, len(r.Data.UTXOs))
	for i := range r.Data.UTXOs {
		u := &r.Data.UTXOs[i]
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

// GetUMTXOsSat returns all custom-script UTXOs when their checked total meets
// minimumSat.
func GetUMTXOsSat(scriptASM string, minimumSat uint64, network string) ([]*bt.UTXO, error) {
	utxos, err := FetchUMTXOs(scriptASM, network)
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

// GetUMTXOs returns all UTXOs for a custom script ASM, checking total balance.
func GetUMTXOs(scriptASM string, amountTBC float64, network string) ([]*bt.UTXO, error) {
	minimumSat, err := tbcAmountToSatoshis(amountTBC)
	if err != nil {
		return nil, err
	}
	return GetUMTXOsSat(scriptASM, minimumSat, network)
}
