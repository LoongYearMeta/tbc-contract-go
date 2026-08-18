package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

func TestPoolPlan6AndV4ScriptsMatchJS166(t *testing.T) {
	const (
		txid     = "1111111111111111111111111111111111111111111111111111111111111111"
		codeHash = "2222222222222222222222222222222222222222222222222222222222222222"
		address  = "1BitcoinEaterAddressDontSendf59kuE"
		pubKey   = "024444444444444444444444444444444444444444444444444444444444444444"
	)
	p := NewPoolNFT2(&PoolNFT2Config{Network: "testnet"})
	fixtures := loadJS166Fixtures(t)
	tests := []struct {
		name  string
		build func() ([]byte, error)
	}{
		{name: "poolPlan6V4", build: func() ([]byte, error) {
			s, err := p.getPoolNftCode(txid, 0, 6, 4, "parity", false)
			if err != nil {
				return nil, err
			}
			return s.Bytes(), nil
		}},
		{name: "poolPlan6V4Locked", build: func() ([]byte, error) {
			s, err := p.getPoolNftCodeWithLock(txid, 0, 6, address, 0.001, []string{pubKey}, 4, "parity", false)
			if err != nil {
				return nil, err
			}
			return s.Bytes(), nil
		}},
		{name: "ftlpV4", build: func() ([]byte, error) {
			s, err := p.getFtlpCode(codeHash, address, 80, false, 4)
			if err != nil {
				return nil, err
			}
			return s.Bytes(), nil
		}},
		{name: "ftlpV4LockTime", build: func() ([]byte, error) {
			s, err := p.getFtlpCodeWithLockTime(codeHash, address, 80, false, 4)
			if err != nil {
				return nil, err
			}
			return s.Bytes(), nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			want := fixtures[test.name]
			hash := sha256.Sum256(got)
			if len(got) != want.Length || hex.EncodeToString(hash[:]) != want.SHA256 {
				t.Fatalf("length/hash = %d/%s, want %d/%s", len(got), hex.EncodeToString(hash[:]), want.Length, want.SHA256)
			}
			if test.name == "ftlpV4" || test.name == "ftlpV4LockTime" {
				if len(got) != util.FTV4CodeLength {
					t.Fatalf("FTLP v4 length = %d", len(got))
				}
			}
		})
	}
}

func TestResolvePoolFeeConfigJS166Plans(t *testing.T) {
	plans := []struct {
		plan, rate, lpRate int
		address            string
	}{
		{1, 35, 25, "13oCEJaqyyiC8iRrfup6PDL2GKZ3xQrsZL"},
		{2, 35, 5, "1Fa6Uy64Ub4qNdB896zX2pNMx4a8zMhtCy"},
		{3, 135, 5, "125fTLNsraQxTYqT4EeQNF2ggzcqicveKL"},
		{4, 335, 5, "19DetoaaohQkjFVJ6oGXd83xhZYQSbpE1g"},
		{5, 535, 5, "15EKrhuD8Yf3SfhjAgbizYqfnBbKh9ZMZ7"},
		{6, 130, 80, "1N7rf2AuAHB2aCrVgnbQhSWhaUVk3rGhjm"},
	}
	for _, want := range plans {
		config, err := resolvePoolFeeConfig(want.rate, want.plan)
		if err != nil {
			t.Fatal(err)
		}
		if config.ServiceFeeRate != want.rate || config.LpPlan != want.plan || getLpServiceFeeRate(want.plan, want.rate) != want.lpRate {
			t.Fatalf("plan %d config = %+v", want.plan, config)
		}
		address, err := getServiceFeeAddress(want.plan)
		if err != nil || address != want.address {
			t.Fatalf("plan %d address = %q, err=%v", want.plan, address, err)
		}
	}
	for _, invalid := range [][2]int{{0, 35}, {7, 35}, {6, 35}, {3, 35}} {
		if _, err := resolvePoolFeeConfig(invalid[1], invalid[0]); err == nil {
			t.Fatalf("accepted plan/rate %v", invalid)
		}
	}
}

func TestParsePoolTapeExtraAcceptsSmallIntegerOpcodesJS166(t *testing.T) {
	tape, err := bscript.NewFromASM("OP_FALSE OP_RETURN 00 00 00 00 OP_6 OP_1 OP_0 4e54617065")
	if err != nil {
		t.Fatal(err)
	}
	extra, err := parsePoolNftTapeExtra(tape)
	if err != nil {
		t.Fatal(err)
	}
	if extra.LpPlan != 6 || !extra.WithLock || extra.WithLockTime {
		t.Fatalf("extra = %+v", extra)
	}
}
