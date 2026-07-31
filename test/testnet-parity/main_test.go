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

func TestLoadConfigReadsTokenOrderMatchV2Roles(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"TBC_TESTNET_WIF":             "buyer-runtime-only",
		"TBC_TESTNET_SELLER_WIF":      "seller-runtime-only",
		"TBC_TESTNET_MATCHER_WIF":     "matcher-runtime-only",
		"TBC_TESTNET_TAX_ADDRESS":     "tax-address",
		"TBC_TESTNET_STAGE":           "token-order-match-v2",
		"TBC_TESTNET_TOKEN_A":         strings.Repeat("a", 64),
		"TBC_TESTNET_TOKEN_B":         strings.Repeat("b", 64),
		"TBC_TESTNET_BROADCAST":       "1",
		"TBC_TESTNET_ORDER_SELL_TXID": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SellerWIF != "seller-runtime-only" ||
		cfg.MatcherWIF != "matcher-runtime-only" ||
		cfg.TaxAddress != "tax-address" {
		t.Fatalf("unexpected role config: %#v", cfg)
	}
}

func TestValidateTokenOrderMatchV2ConfigRequiresSeparateRuntimeRoles(t *testing.T) {
	cfg := config{TokenA: strings.Repeat("a", 64), TokenB: strings.Repeat("b", 64)}
	err := validateTokenOrderMatchV2Config(cfg)
	if err == nil || !strings.Contains(err.Error(), "SELLER_WIF") {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.SellerWIF = "seller"
	err = validateTokenOrderMatchV2Config(cfg)
	if err == nil || !strings.Contains(err.Error(), "MATCHER_WIF") {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.MatcherWIF = "matcher"
	err = validateTokenOrderMatchV2Config(cfg)
	if err == nil || !strings.Contains(err.Error(), "TAX_ADDRESS") {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg.TaxAddress = "tax"
	if err := validateTokenOrderMatchV2Config(cfg); err != nil {
		t.Fatal(err)
	}
}
