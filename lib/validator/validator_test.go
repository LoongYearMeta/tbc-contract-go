package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type fixtureFetcher map[string]*bt.Tx

func (fetcher fixtureFetcher) FetchTokenTransaction(_ context.Context, txid, _ string) (*bt.Tx, error) {
	return fetcher[txid], nil
}

func readValidatorTransactions(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile("../contract/testdata/js-1.6.6/tbc20-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Transaction map[string]interface{} `json:"transactionFixture"`
	}
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string)
	for key, value := range fixture.Transaction {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func mustValidatorTx(t *testing.T, raw string) *bt.Tx {
	t.Helper()
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestInvalidReportsMatchJS166(t *testing.T) {
	report, err := ValidateOnChainTransaction(context.Background(), TokenValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationInvalid || report.Kind != "UNDETERMINED" || len(report.Issues) != 1 || report.Issues[0].Code != "INVALID_POLICY" || report.Issues[0].Message != "transaction must be a Transaction, Buffer, or hex string" {
		t.Fatalf("invalid policy report = %+v", report)
	}
	assertReportJSONMatchesFixture(t, report, "invalidPolicy")
	report, err = ValidateOnChainTransaction(context.Background(), TokenValidationOptions{RawHex: "zz", Network: "testnet"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationUnknown || len(report.Issues) != 1 || report.Issues[0].Code != "ROOT_RAW_INVALID" || report.Source == nil {
		t.Fatalf("invalid root report = %+v", report)
	}
	assertReportJSONMatchesFixture(t, report, "invalidRoot")
}

func TestTBC20TransferValidationMatchesJS166Graph(t *testing.T) {
	fixture := readValidatorTransactions(t)
	source := mustValidatorTx(t, fixture["sourceRaw"])
	mint := mustValidatorTx(t, fixture["mintRaw"])
	root := mustValidatorTx(t, fixture["transferRaw"])
	fetcher := fixtureFetcher{source.TxID(): source, mint.TxID(): mint}
	report, err := ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: root, Network: "testnet", Fetcher: fetcher})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationValid || report.Kind != "TRANSITION" || report.Protocol == nil || report.Protocol.Family != "TBC20" || report.Protocol.Version != 1 {
		t.Fatalf("report status = %+v issues=%+v", report, report.Issues)
	}
	wantAssurances := []string{"STRUCTURE", "TRANSITION", "OUTPUT_SOURCE_LINEAGE", "OUTPUT_SOURCE_GRAPH_RESOLVED"}
	if !reflect.DeepEqual(report.Assurances, wantAssurances) {
		t.Fatalf("assurances = %v", report.Assurances)
	}
	if len(report.Matrix) != 2 || report.Matrix[0][0].String() != "12345600000" || report.Matrix[1][0].String() != "87654400000" {
		t.Fatalf("matrix = %+v", report.Matrix)
	}
	if report.ResolvedTransactions != 2 || report.ParentsChecked != 1 || report.AncestorsChecked != 1 || report.OriginalUTXOBoundaries != 1 {
		t.Fatalf("graph counters = %d/%d/%d/%d", report.ResolvedTransactions, report.ParentsChecked, report.AncestorsChecked, report.OriginalUTXOBoundaries)
	}
	if len(report.AncestorEdges) != 1 || report.AncestorEdges[0].Resolution != "ORIGINAL_UTXO" || report.AncestorEdges[0].ABIPrepreIndex != 5 {
		t.Fatalf("ancestor edges = %+v", report.AncestorEdges)
	}
	assertReportJSONMatchesFixture(t, report, "validTransfer")
}

func assertReportJSONMatchesFixture(t *testing.T, report *TokenValidationResult, name string) {
	t.Helper()
	body, err := os.ReadFile("testdata/js-1.6.6/reports.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	actualRaw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var actual, expected interface{}
	if err := json.Unmarshal(actualRaw, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixtures[name], &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		actualPretty, _ := json.MarshalIndent(actual, "", "  ")
		expectedPretty, _ := json.MarshalIndent(expected, "", "  ")
		t.Fatalf("Go report JSON differs from JS 1.6.6\ngot: %s\nwant: %s", actualPretty, expectedPretty)
	}
}

func TestTBC20MergeValidationResolvesSharedAncestorsOnce(t *testing.T) {
	fixture := readValidatorTransactions(t)
	mint := mustValidatorTx(t, fixture["mintRaw"])
	split := mustValidatorTx(t, fixture["splitRaw"])
	root := mustValidatorTx(t, fixture["mergeRaw"])
	report, err := ValidateOnChainTransaction(context.Background(), TokenValidationOptions{
		Transaction: root, Network: "testnet", Fetcher: fixtureFetcher{mint.TxID(): mint, split.TxID(): split},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationValid || report.ResolvedTransactions != 2 || report.ParentsChecked != 2 || report.AncestorsChecked != 2 || len(report.AncestorEdges) != 2 {
		t.Fatalf("merge report = %+v issues=%+v", report, report.Issues)
	}
	assertReportJSONMatchesFixture(t, report, "validMerge")
}

func TestValidationIssuePrecedenceAndPolicy(t *testing.T) {
	fixture := readValidatorTransactions(t)
	source := mustValidatorTx(t, fixture["sourceRaw"])
	mint := mustValidatorTx(t, fixture["mintRaw"])
	root := mustValidatorTx(t, fixture["transferRaw"])

	wrongVersion := root.Clone()
	wrongVersion.Version = 9
	report, err := ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: wrongVersion, Network: "testnet", Fetcher: fixtureFetcher{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationInvalid || len(report.Issues) != 1 || report.Issues[0].Code != "INVALID_TRANSACTION_VERSION" || len(report.Source.QueriedTxIDs) != 0 {
		t.Fatalf("root invalid must take precedence: %+v", report)
	}

	report, err = ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: root, Network: "testnet", Fetcher: fixtureFetcher{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationUnknown || len(report.Issues) == 0 || report.Issues[0].Code != "PARENT_FETCH_FAILED" {
		t.Fatalf("unavailable positive parent must be UNKNOWN: %+v", report)
	}

	badCode := root.Clone()
	code := append([]byte(nil), badCode.Outputs[0].LockingScript.Bytes()...)
	code[100] ^= 1
	badCode.Outputs[0].LockingScript = bscript.NewFromBytes(code)
	report, err = ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: badCode, Network: "testnet", Fetcher: fixtureFetcher{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationInvalid || report.Issues[0].Code != "INVALID_TBC20_CODE" {
		t.Fatalf("damaged registered artifact must be INVALID: %+v", report)
	}

	badEnvelope := root.Clone()
	tape := append([]byte(nil), badEnvelope.Outputs[3].LockingScript.Bytes()...)
	tape[60] ^= 1
	badEnvelope.Outputs[3].LockingScript = bscript.NewFromBytes(tape)
	report, err = ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: badEnvelope, Network: "testnet", Fetcher: fixtureFetcher{source.TxID(): source, mint.TxID(): mint}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationInvalid || report.Issues[0].Code != "TAPE_ENVELOPE_MISMATCH" {
		t.Fatalf("strict envelope mismatch must be INVALID: %+v", report)
	}
	relaxed := false
	report, err = ValidateOnChainTransaction(context.Background(), TokenValidationOptions{Transaction: badEnvelope, Network: "testnet", Fetcher: fixtureFetcher{source.TxID(): source, mint.TxID(): mint}, Policy: TokenValidationPolicy{Preset: "relaxed-metadata", RequireExactTapeEnvelope: &relaxed}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != ValidationValid || len(report.Issues) == 0 || report.Issues[0].Severity != "warning" {
		t.Fatalf("relaxed metadata mismatch must remain valid with warning: %+v", report)
	}
	if bytes.Equal(badEnvelope.Outputs[1].LockingScript.Bytes(), badEnvelope.Outputs[3].LockingScript.Bytes()) {
		t.Fatal("test did not mutate the second envelope")
	}
}
