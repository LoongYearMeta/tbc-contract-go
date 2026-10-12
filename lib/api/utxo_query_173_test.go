package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUTXO173Pagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("limit") != "1000" {
			t.Error("missing page limit")
		}
		if calls == 1 {
			fmt.Fprintf(w, `{"code":200,"data":{"utxos":[{"txid":"%s","index":0},{"txid":"%s","index":1}],"next_cursor":"a+/=&?"}}`, strings.Repeat("ab", 32), strings.Repeat("ab", 32))
		} else {
			if r.URL.Query().Get("after") != "a+/=&?" {
				t.Error("corrupted cursor")
			}
			fmt.Fprintf(w, `{"code":200,"data":{"utxos":[{"txid":"%s","index":"01"},{"txid":"%s","index":2,"ft_value":9007199254740993}]}}`, strings.Repeat("AB", 32), strings.Repeat("ab", 32))
		}
	}))
	defer server.Close()
	rows, e := FetchUTXORecords("ft/utxo/test", server.URL, UTXOQueryOptions{})
	if e != nil || len(rows) != 3 || calls != 2 {
		t.Fatalf("rows=%d calls=%d err=%v", len(rows), calls, e)
	}
	if !strings.Contains(string(rows[2]), "9007199254740993") {
		t.Fatal("integer precision lost")
	}
}
func TestUTXO173RejectsDuplicateAndCyclicPages(t *testing.T) {
	for _, body := range []string{`{"code":200,"data":{"utxos":[{"txid":"aa","index":1},{"txid":"AA","index":"01"}]}}`, `{"code":200,"data":{"utxos":[],"next_cursor":null}}`, `{"code":200,"data":{"utxos":[],"next_cursor":"same"}}`} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) }))
		_, e := FetchUTXORecords("utxo/test", s.URL, UTXOQueryOptions{})
		s.Close()
		if e == nil || calls > 2 {
			t.Fatalf("calls=%d err=%v", calls, e)
		}
	}
}
func TestUTXO173TargetBatchAndIndexError(t *testing.T) {
	for _, target := range []*big.Int{nil, new(big.Int).SetUint64(9007199254740993)} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Query().Get("limit") != "1" {
				t.Error("limit missing")
			}
			if target != nil && r.URL.Query().Get("target") != target.String() {
				t.Error("target differs")
			}
			fmt.Fprint(w, `{"code":200,"data":{"utxos":[{},{}],"next_cursor":"unused"}}`)
		}))
		rows, e := FetchUTXORecords("utxo/test", s.URL, UTXOQueryOptions{Target: target, Limit: 1, SinglePage: target == nil})
		s.Close()
		want := 1
		if target != nil {
			want = 2
		}
		if e != nil || len(rows) != want || calls != 1 {
			t.Fatalf("rows=%d err=%v", len(rows), e)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 422, "error": "UTXO_TOO_FRAGMENTED", "request_id": "req173", "message": "merge inputs"})
	}))
	defer s.Close()
	_, e := FetchUTXORecords("utxo/test", s.URL, UTXOQueryOptions{})
	var index *IndexError
	if !errors.As(e, &index) || index.Code != "UTXO_TOO_FRAGMENTED" || index.Status != 422 || index.RequestID != "req173" {
		t.Fatalf("metadata lost: %v", e)
	}
}
