package validator

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	bt "github.com/LoongYearMeta/tbc-lib-go"
)

type TokenValidationStatus string

const (
	ValidationValid   TokenValidationStatus = "VALID"
	ValidationInvalid TokenValidationStatus = "INVALID"
	ValidationUnknown TokenValidationStatus = "UNKNOWN"
)

type TokenProtocolDescriptor struct {
	Family  string `json:"family"`
	Version uint8  `json:"version"`
}

type TokenValidationPolicy struct {
	Preset                   string `json:"preset,omitempty"`
	RequireExactTapeEnvelope *bool  `json:"requireExactTapeEnvelope,omitempty"`
}

type TokenValidationIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Stage    string `json:"stage"`
	Message  string `json:"message"`
	Vin      *int   `json:"vin,omitempty"`
	Vout     *int   `json:"vout,omitempty"`
	Slot     *int   `json:"slot,omitempty"`
	TxID     string `json:"txid,omitempty"`
	Identity string `json:"identity,omitempty"`
}

type TokenTransactionFetcher interface {
	FetchTokenTransaction(ctx context.Context, txid, network string) (*bt.Tx, error)
}

type TokenValidationOptions struct {
	Transaction *bt.Tx
	RawHex      string
	Network     string
	Policy      TokenValidationPolicy
	Fetcher     TokenTransactionFetcher
}

type TokenInputEvidence struct {
	Vin        int                      `json:"vin"`
	PrevTxID   string                   `json:"prevTxid"`
	PrevVout   uint32                   `json:"prevVout"`
	Kind       string                   `json:"kind"`
	Resolution string                   `json:"resolution"`
	SourceRole string                   `json:"sourceRole,omitempty"`
	ParentTxID string                   `json:"parentTxid,omitempty"`
	CodeVout   *int                     `json:"codeVout,omitempty"`
	TapeVout   *int                     `json:"tapeVout,omitempty"`
	Identity   string                   `json:"identity,omitempty"`
	Protocol   *TokenProtocolDescriptor `json:"protocol,omitempty"`
	Slots      []uint64                 `json:"slots,omitempty"`
	BalanceRaw *big.Int                 `json:"balanceRaw,omitempty"`
}

type TokenOutputGroup struct {
	LogicalIndex       int                      `json:"logicalIndex"`
	Kind               string                   `json:"kind"`
	FirstVout          int                      `json:"firstVout"`
	PhysicalVoutCount  int                      `json:"physicalVoutCount"`
	CodeVout           *int                     `json:"codeVout,omitempty"`
	TapeVout           *int                     `json:"tapeVout,omitempty"`
	Identity           string                   `json:"identity,omitempty"`
	Protocol           *TokenProtocolDescriptor `json:"protocol,omitempty"`
	RecognizedContract *TokenProtocolDescriptor `json:"recognizedContract,omitempty"`
	Slots              []uint64                 `json:"slots,omitempty"`
	BalanceRaw         *big.Int                 `json:"balanceRaw,omitempty"`
}

type TokenAssetEvidence struct {
	Identity     string                  `json:"identity"`
	Protocol     TokenProtocolDescriptor `json:"protocol"`
	InputVins    []int                   `json:"inputVins"`
	OutputGroups []int                   `json:"outputGroups"`
	InputRaw     *big.Int                `json:"inputRaw"`
	OutputRaw    *big.Int                `json:"outputRaw"`
	EnvelopeHash string                  `json:"envelopeHash,omitempty"`
}

type TokenAncestorEdge struct {
	CurrentVin       int    `json:"currentVin"`
	ParentTxID       string `json:"parentTxid"`
	ParentCodeVout   int    `json:"parentCodeVout"`
	ParentSlot       int    `json:"parentSlot"`
	ParentVin        int    `json:"parentVin"`
	ABIPrepreIndex   int    `json:"abiPrepreIndex"`
	AncestorTxID     string `json:"ancestorTxid"`
	AncestorVout     uint32 `json:"ancestorVout"`
	ParentIdentity   string `json:"parentIdentity"`
	AncestorIdentity string `json:"ancestorIdentity"`
	Resolution       string `json:"resolution"`
}

type TokenValidationSource struct {
	Network             string   `json:"network"`
	API                 string   `json:"api"`
	TrustModel          string   `json:"trustModel"`
	RootTrustModel      string   `json:"rootTrustModel"`
	QueriedTxIDs        []string `json:"queriedTxids"`
	ResolvedTxIDs       []string `json:"resolvedTxids"`
	RequiredSourceTxIDs []string `json:"requiredSourceTxids"`
}

type TokenValidationResult struct {
	Status                 TokenValidationStatus    `json:"status"`
	TxID                   string                   `json:"txid,omitempty"`
	Kind                   string                   `json:"kind"`
	Protocol               *TokenProtocolDescriptor `json:"protocol,omitempty"`
	Assurances             []string                 `json:"assurances"`
	Issues                 []TokenValidationIssue   `json:"issues"`
	Inputs                 []TokenInputEvidence     `json:"inputs"`
	OutputGroups           []TokenOutputGroup       `json:"outputGroups"`
	Assets                 []TokenAssetEvidence     `json:"assets"`
	Matrix                 [][]*big.Int             `json:"matrix"`
	AncestorEdges          []TokenAncestorEdge      `json:"ancestorEdges"`
	Source                 *TokenValidationSource   `json:"source,omitempty"`
	ResolvedTransactions   int                      `json:"resolvedTransactions"`
	ParentsChecked         int                      `json:"parentsChecked"`
	AncestorsChecked       int                      `json:"ancestorsChecked"`
	OriginalUTXOBoundaries int                      `json:"originalUTXOBoundaries"`
}

type TokenValidationError struct {
	Report *TokenValidationResult
}

func decimalStrings(values []uint64) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = new(big.Int).SetUint64(value).String()
	}
	return result
}

// MarshalJSON follows the JS validator's toJSON contract: every token amount
// is emitted as a base-10 string so values above JavaScript's safe-integer
// range cannot be rounded by JSON consumers.
func (result TokenValidationResult) MarshalJSON() ([]byte, error) {
	type plainResult TokenValidationResult
	raw, err := json.Marshal(plainResult(result))
	if err != nil {
		return nil, err
	}
	var document map[string]interface{}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	if inputs, ok := document["inputs"].([]interface{}); ok {
		for index, item := range inputs {
			object, _ := item.(map[string]interface{})
			if index < len(result.Inputs) && len(result.Inputs[index].Slots) > 0 {
				object["slots"] = decimalStrings(result.Inputs[index].Slots)
			}
			if index < len(result.Inputs) && result.Inputs[index].BalanceRaw != nil {
				object["balanceRaw"] = result.Inputs[index].BalanceRaw.String()
			}
		}
	}
	if groups, ok := document["outputGroups"].([]interface{}); ok {
		for index, item := range groups {
			object, _ := item.(map[string]interface{})
			if index < len(result.OutputGroups) && len(result.OutputGroups[index].Slots) > 0 {
				object["slots"] = decimalStrings(result.OutputGroups[index].Slots)
			}
			if index < len(result.OutputGroups) && result.OutputGroups[index].BalanceRaw != nil {
				object["balanceRaw"] = result.OutputGroups[index].BalanceRaw.String()
			}
		}
	}
	if assets, ok := document["assets"].([]interface{}); ok {
		for index, item := range assets {
			object, _ := item.(map[string]interface{})
			if index < len(result.Assets) && result.Assets[index].InputRaw != nil {
				object["inputRaw"] = result.Assets[index].InputRaw.String()
			}
			if index < len(result.Assets) && result.Assets[index].OutputRaw != nil {
				object["outputRaw"] = result.Assets[index].OutputRaw.String()
			}
		}
	}
	matrix := make([][]string, len(result.Matrix))
	for rowIndex, row := range result.Matrix {
		matrix[rowIndex] = make([]string, len(row))
		for columnIndex, value := range row {
			if value == nil {
				matrix[rowIndex][columnIndex] = "0"
			} else {
				matrix[rowIndex][columnIndex] = value.String()
			}
		}
	}
	document["matrix"] = matrix
	return json.Marshal(document)
}

func (err *TokenValidationError) Error() string {
	codes := ""
	for index, issue := range err.Report.Issues {
		if index > 0 {
			codes += ", "
		}
		codes += issue.Code
	}
	return fmt.Sprintf("token validation %s: %s", string(err.Report.Status), codes)
}
