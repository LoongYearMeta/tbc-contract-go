package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseStageDefaultsToFoundation(t *testing.T) {
	stage, err := parseStage("")
	if err != nil {
		t.Fatal(err)
	}
	if stage != stageFoundation {
		t.Fatalf("stage=%q want=%q", stage, stageFoundation)
	}
}

func TestParseStageAcceptsFullMatrixStages(t *testing.T) {
	for _, value := range []string{
		"foundation",
		"ft",
		"nft",
		"multisig",
		"base-htlc",
		"piggybank",
		"stablecoin",
		"pool-create",
		"pool-trade",
		"pool-lock",
		"orderbook",
		"tbc20",
	} {
		stage, err := parseStage(value)
		if err != nil {
			t.Fatalf("stage %q: %v", value, err)
		}
		if string(stage) != value {
			t.Fatalf("stage=%q want=%q", stage, value)
		}
	}
}

func TestParseStageRejectsUnknownStage(t *testing.T) {
	_, err := parseStage("mainnet-send")
	if err == nil || !strings.Contains(err.Error(), "unknown testnet stage") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPublicStateRejectsSecretFields(t *testing.T) {
	typ := reflect.TypeOf(publicState{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, forbidden := range []string{"wif", "private", "secret", "raw", "signature"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("public state field %q is forbidden", typ.Field(i).Name)
			}
		}
	}
}

func TestWritePublicStateEmitsMachineReadablePublicJSON(t *testing.T) {
	var output bytes.Buffer
	state := publicState{
		TokenID:  strings.Repeat("1", 64),
		LastTxID: strings.Repeat("2", 64),
		LastVout: 3,
	}
	if err := writePublicState(&output, state); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "STATE {") {
		t.Fatalf("unexpected state output %q", output.String())
	}
	for _, forbidden := range []string{"wif", "private", "secret", "raw", "signature"} {
		if strings.Contains(strings.ToLower(output.String()), forbidden) {
			t.Fatalf("state output leaked forbidden word %q", forbidden)
		}
	}
}
