package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

type scriptHashFixture struct {
	Length  int               `json:"length"`
	SHA256  string            `json:"sha256"`
	Decoded map[string]string `json:"decoded"`
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

func loadJS166Fixtures(t *testing.T) map[string]scriptHashFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "js-1.6.6", "script-hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]scriptHashFixture
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func TestRenderedScriptsMatchJS166(t *testing.T) {
	fixtures := loadJS166Fixtures(t)
	for _, name := range []string{
		"ftV4Mint",
		"stableCoinV4Mint",
		"nftV2Code",
		"nftV1Code",
		"poolPlan6V4",
		"poolPlan6V4Locked",
		"ftlpV4",
		"ftlpV4LockTime",
		"sellOrderV4",
		"buyOrderV4",
		"tbc20Code",
		"tbc20Tape",
	} {
		if _, ok := fixtures[name]; !ok {
			t.Errorf("missing JavaScript 1.6.6 fixture %q", name)
		}
	}
}

type renderedParityArtifact struct {
	Script  *bscript.Script
	Decoded map[string]string
}

func renderAllReferenceArtifacts(
	t *testing.T,
) map[string]renderedParityArtifact {
	t.Helper()
	const (
		address             = "1BitcoinEaterAddressDontSendf59kuE"
		tapeSize            = 80
		compressedPublicKey = "024444444444444444444444444444444444444444444444444444444444444444"
	)
	compressedPublicKeys := []string{
		compressedPublicKey,
		"035555555555555555555555555555555555555555555555555555555555555555",
		"026666666666666666666666666666666666666666666666666666666666666666",
	}
	txid := "1111111111111111111111111111111111111111111111111111111111111111"
	codeHash := "2222222222222222222222222222222222222222222222222222222222222222"
	adminPubHash := "3333333333333333333333333333333333333333"
	pool := NewPoolNFT2(&PoolNFT2Config{Network: "testnet"})

	rendered := make(map[string]renderedParityArtifact)
	add := func(
		name string,
		decoded map[string]string,
		build func() (*bscript.Script, error),
	) {
		t.Helper()
		script, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rendered[name] = renderedParityArtifact{
			Script:  script,
			Decoded: decoded,
		}
	}

	add("ftV3Mint", map[string]string{
		"kind":        "ordinary-ft-v3",
		"destination": address,
		"codeBytes":   "1884",
	}, func() (*bscript.Script, error) {
		return getFTmintCode(txid, 0, address, tapeSize)
	})
	zeroAmount := strings.Repeat("00", 48)
	amountHex, _ := BuildTapeAmount(
		big.NewInt(123456),
		[]*big.Int{big.NewInt(200000)},
	)
	ftTapeTemplate, err := bscript.NewFromASM(
		fmt.Sprintf(
			"OP_FALSE OP_RETURN %s 06 506172697479 505459 4654617065",
			zeroAmount,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	add("ftTransferTape", map[string]string{
		"balance": "123456",
		"marker":  "FTape",
	}, func() (*bscript.Script, error) {
		return BuildFTtransferTape(ftTapeTemplate.ToHex(), amountHex)
	})
	nftData := NFTData{
		NftName:     "Parity NFT",
		Symbol:      "PNFT",
		Description: "JavaScript 1.6.5 fixture",
		Attributes:  `{"level":1}`,
		File:        "ipfs://parity",
	}
	add("nftTape", map[string]string{
		"nftName": nftData.NftName,
		"symbol":  nftData.Symbol,
		"file":    nftData.File,
	}, func() (*bscript.Script, error) {
		return BuildNFTTapeScript(nftData)
	})
	add("poolV3", map[string]string{
		"version": "3",
		"locked":  "false",
		"tokenID": txid,
	}, func() (*bscript.Script, error) {
		return pool.getPoolNftCode(txid, 0, 2, 3, "parity", false)
	})
	add("poolV3Locked", map[string]string{
		"version": "3",
		"locked":  "true",
		"tokenID": txid,
	}, func() (*bscript.Script, error) {
		return pool.getPoolNftCodeWithLock(
			txid, 0, 2, address, 0.001,
			[]string{compressedPublicKey}, 3, "parity", false,
		)
	})
	add("poolV3Locked3", map[string]string{
		"version":    "3",
		"locked":     "true",
		"tokenID":    txid,
		"publicKeys": "3",
	}, func() (*bscript.Script, error) {
		return pool.getPoolNftCodeWithLock(
			txid, 0, 2, address, 0.0001,
			compressedPublicKeys, 3, "pool-lock", false,
		)
	})
	add("ftlpV2", map[string]string{
		"version":  "2",
		"lockTime": "false",
	}, func() (*bscript.Script, error) {
		return pool.getFtlpCode(codeHash, address, tapeSize, false, 2)
	})
	add("ftlpV3", map[string]string{
		"version":  "3",
		"lockTime": "false",
	}, func() (*bscript.Script, error) {
		return pool.getFtlpCode(codeHash, address, tapeSize, false, 3)
	})
	add("ftlpV3LockTime", map[string]string{
		"version":  "3",
		"lockTime": "true",
	}, func() (*bscript.Script, error) {
		return pool.getFtlpCodeWithLockTime(codeHash, address, tapeSize, false, 3)
	})
	order := NewOrderBook()
	order.HoldAddress = address
	order.SaleVolume = 123456
	order.FeeRate = 10000
	order.UnitPrice = 2000000
	order.FtPartialHash = codeHash
	order.FtID = txid
	orderDecoded := func(side string) map[string]string {
		return map[string]string{
			"side":        side,
			"holder":      address,
			"saleVolume":  "123456",
			"unitPrice":   "2000000",
			"feeRate":     "10000",
			"tokenID":     txid,
			"partialHash": codeHash,
		}
	}
	add("sellOrder", orderDecoded("sell"), func() (*bscript.Script, error) {
		return order.GetSellOrderCode(false, address)
	})
	add("buyOrder", orderDecoded("buy"), func() (*bscript.Script, error) {
		return order.GetBuyOrderCode(false, address)
	})
	add("stableCoinMint", map[string]string{
		"kind":        "stablecoin",
		"destination": address,
		"codeBytes":   "2012",
	}, func() (*bscript.Script, error) {
		return GetCoinMintCode(adminPubHash, address, codeHash, tapeSize)
	})
	stableCoinTapeTemplate, err := bscript.NewFromASM(
		fmt.Sprintf(
			"OP_FALSE OP_RETURN %s 06 506172697479436f696e 50434e 00000000 4654617065",
			zeroAmount,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	add("stableCoinTape", map[string]string{
		"balance":  "123456",
		"lockTime": "500000123",
		"marker":   "FTape",
	}, func() (*bscript.Script, error) {
		transferTape, err := BuildFTtransferTape(
			stableCoinTapeTemplate.ToHex(),
			amountHex,
		)
		if err != nil {
			return nil, err
		}
		return SetLockTimeInTape(transferTape, 500000123)
	})

	rendered["ftV3Mint"].Decoded["codeBytes"] = fmt.Sprint(
		rendered["ftV3Mint"].Script.Len(),
	)
	rendered["stableCoinMint"].Decoded["codeBytes"] = fmt.Sprint(
		rendered["stableCoinMint"].Script.Len(),
	)
	rendered["ftTransferTape"].Decoded["balance"] = GetBalanceFromTape(
		rendered["ftTransferTape"].Script.ToHex(),
	).String()
	ftTapeChunks := rendered["ftTransferTape"].Script.Chunks()
	rendered["ftTransferTape"].Decoded["marker"] = string(
		ftTapeChunks[len(ftTapeChunks)-1].Buf,
	)
	nftChunks := rendered["nftTape"].Script.Chunks()
	decodedNFT, err := DecodeNFTDataFromHex(
		hex.EncodeToString(nftChunks[len(nftChunks)-2].Buf),
	)
	if err != nil {
		t.Fatal(err)
	}
	rendered["nftTape"].Decoded["nftName"] = decodedNFT["nftName"].(string)
	rendered["nftTape"].Decoded["symbol"] = decodedNFT["symbol"].(string)
	rendered["nftTape"].Decoded["file"] = decodedNFT["file"].(string)
	for _, name := range []string{"sellOrder", "buyOrder"} {
		decodedOrder, err := GetOrderData(
			rendered[name].Script.ToHex(),
			true,
		)
		if err != nil {
			t.Fatal(err)
		}
		rendered[name].Decoded["holder"] = decodedOrder.HoldAddress
		rendered[name].Decoded["saleVolume"] = fmt.Sprint(decodedOrder.SaleVolume)
		rendered[name].Decoded["unitPrice"] = fmt.Sprint(decodedOrder.UnitPrice)
		rendered[name].Decoded["feeRate"] = fmt.Sprint(decodedOrder.FeeRate)
		rendered[name].Decoded["tokenID"] = decodedOrder.FtID
		rendered[name].Decoded["partialHash"] = decodedOrder.FtPartialHash
	}
	rendered["stableCoinTape"].Decoded["balance"] = GetBalanceFromTape(
		rendered["stableCoinTape"].Script.ToHex(),
	).String()
	decodedLockTime, err := GetLockTimeFromTape(
		rendered["stableCoinTape"].Script,
	)
	if err != nil {
		t.Fatal(err)
	}
	rendered["stableCoinTape"].Decoded["lockTime"] = fmt.Sprint(decodedLockTime)
	stableTapeChunks := rendered["stableCoinTape"].Script.Chunks()
	rendered["stableCoinTape"].Decoded["marker"] = string(
		stableTapeChunks[len(stableTapeChunks)-1].Buf,
	)
	return rendered
}

func renderAllReferenceScripts(t *testing.T) map[string]*bscript.Script {
	t.Helper()
	artifacts := renderAllReferenceArtifacts(t)
	scripts := make(map[string]*bscript.Script, len(artifacts))
	for name, artifact := range artifacts {
		scripts[name] = artifact.Script
	}
	return scripts
}

func TestRenderedScriptsMatchJS165(t *testing.T) {
	fixtures := loadScriptHashFixtures(t)
	rendered := renderAllReferenceScripts(t)
	for name, script := range rendered {
		if name == "ftV3Mint" || name == "stableCoinMint" {
			continue
		}
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

func TestRenderedTransactionArtifactsMatchJS165(t *testing.T) {
	fixtures := loadScriptHashFixtures(t)
	rendered := renderAllReferenceArtifacts(t)
	for _, name := range []string{
		"ftTransferTape",
		"nftTape",
		"poolV3",
		"poolV3Locked",
		"poolV3Locked3",
		"ftlpV3",
		"ftlpV3LockTime",
		"sellOrder",
		"buyOrder",
		"stableCoinTape",
	} {
		if _, ok := fixtures[name]; !ok {
			t.Fatalf("missing JavaScript 1.6.5 fixture %q", name)
		}
		artifact, ok := rendered[name]
		if !ok {
			t.Fatalf("missing Go artifact %q", name)
		}
		if !reflect.DeepEqual(artifact.Decoded, fixtures[name].Decoded) {
			t.Errorf(
				"%s decoded=%v want=%v",
				name,
				artifact.Decoded,
				fixtures[name].Decoded,
			)
		}
	}
}
