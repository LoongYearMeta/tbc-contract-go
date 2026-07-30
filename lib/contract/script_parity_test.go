package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type scriptHashFixture struct {
	Length int    `json:"length"`
	SHA256 string `json:"sha256"`
}

func loadScriptHashFixtures(t *testing.T) map[string]scriptHashFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "js-1.6.5", "script-hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]scriptHashFixture
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func renderAllReferenceScripts(t *testing.T) map[string]*bscript.Script {
	t.Helper()
	const (
		address             = "1BitcoinEaterAddressDontSendf59kuE"
		tapeSize            = 80
		compressedPublicKey = "024444444444444444444444444444444444444444444444444444444444444444"
	)
	txid := "1111111111111111111111111111111111111111111111111111111111111111"
	codeHash := "2222222222222222222222222222222222222222222222222222222222222222"
	adminPubHash := "3333333333333333333333333333333333333333"
	pool := NewPoolNFT2(&PoolNFT2Config{Network: "testnet"})

	rendered := make(map[string]*bscript.Script)
	add := func(name string, build func() (*bscript.Script, error)) {
		t.Helper()
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rendered[name] = script
	}

	add("ftV3Mint", func() (*bscript.Script, error) {
		return getFTmintCode(txid, 0, address, tapeSize)
	})
	add("poolV3", func() (*bscript.Script, error) {
		return pool.getPoolNftCode(txid, 0, 2, 3, "parity", false)
	})
	add("poolV3Locked", func() (*bscript.Script, error) {
		return pool.getPoolNftCodeWithLock(
			txid, 0, 2, address, 0.001,
			[]string{compressedPublicKey}, 3, "parity", false,
		)
	})
	add("ftlpV2", func() (*bscript.Script, error) {
		return pool.getFtlpCode(codeHash, address, tapeSize, false, 2)
	})
	add("ftlpV3", func() (*bscript.Script, error) {
		return pool.getFtlpCode(codeHash, address, tapeSize, false, 3)
	})
	add("ftlpV3LockTime", func() (*bscript.Script, error) {
		return pool.getFtlpCodeWithLockTime(codeHash, address, tapeSize, false, 3)
	})
	add("stableCoinMint", func() (*bscript.Script, error) {
		return GetCoinMintCode(adminPubHash, address, codeHash, tapeSize)
	})
	return rendered
}

func TestRenderedScriptsMatchJS165(t *testing.T) {
	fixtures := loadScriptHashFixtures(t)
	rendered := renderAllReferenceScripts(t)
	if len(rendered) != len(fixtures) {
		t.Fatalf("rendered %d scripts, fixture has %d", len(rendered), len(fixtures))
	}
	for name, script := range rendered {
		want, ok := fixtures[name]
		if !ok {
			t.Errorf("%s missing from fixture", name)
			continue
		}
		gotHash := sha256.Sum256(script.Bytes())
		gotSHA256 := hex.EncodeToString(gotHash[:])
		if len(script.Bytes()) != want.Length || gotSHA256 != want.SHA256 {
			t.Errorf(
				"%s length/hash = %d/%s, want %d/%s",
				name, len(script.Bytes()), gotSHA256, want.Length, want.SHA256,
			)
		}
	}
}
