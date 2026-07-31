package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPGetWithRetryRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		if calls.Add(1) < 3 {
			http.Error(writer, "temporary upstream failure", http.StatusBadGateway)
			return
		}
		_, _ = writer.Write([]byte(`{"code":"200"}`))
	}))
	defer server.Close()

	body, err := httpGetWithRetry(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"code":"200"}` {
		t.Fatalf("body=%q", body)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("GET calls=%d want=3", got)
	}
}

func TestHTTPGetWithRetryRejectsNonRetryableStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		calls.Add(1)
		http.Error(writer, "missing test object", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := httpGetWithRetry(server.URL)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("GET calls=%d want=1", got)
	}
}
