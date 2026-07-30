package main

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfigAllowsOnlyTestnet(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"TBC_TESTNET_WIF":       "runtime-only",
		"TBC_TESTNET_BROADCAST": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != "testnet" || !cfg.Broadcast {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadConfigRejectsNonTestnet(t *testing.T) {
	_, err := loadConfig(env(map[string]string{
		"TBC_TESTNET_NETWORK": "mainnet",
		"TBC_TESTNET_WIF":     "runtime-only",
	}))
	if err == nil || !strings.Contains(err.Error(), "refuses non-testnet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigRequiresRuntimeWIF(t *testing.T) {
	_, err := loadConfig(env(nil))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
