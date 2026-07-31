package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

func TestValidatePoolStateInputRequiresPreviousOutpoint(t *testing.T) {
	previous := strings.Repeat("11", 32)
	tx := bt.NewTx()
	if err := validatePoolStateInput(tx, previous, 0); err == nil {
		t.Fatal("expected missing pool-state input rejection")
	}

	txid, err := hex.DecodeString(previous)
	if err != nil {
		t.Fatal(err)
	}
	script, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.FromUTXOs(&bt.UTXO{
		TxID: txid, Vout: 0, LockingScript: script, Satoshis: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := validatePoolStateInput(tx, previous, 0); err != nil {
		t.Fatalf("valid pool-state input rejected: %v", err)
	}
	if err := validatePoolStateInput(tx, previous, 1); err == nil {
		t.Fatal("expected wrong pool-state vout rejection")
	}
}

func TestValidatePoolSwapDeltaUsesExactIntegers(t *testing.T) {
	before := poolAmounts{
		TBC: big.NewInt(1_000_000),
		FT:  big.NewInt(1_000_000),
		LP:  big.NewInt(1_000_000),
	}
	tbcToFT := poolAmounts{
		TBC: big.NewInt(1_100_000),
		FT:  big.NewInt(900_000),
		LP:  big.NewInt(1_000_000),
	}
	if err := validatePoolSwapDelta(before, tbcToFT, poolSwapTBCToFT); err != nil {
		t.Fatal(err)
	}
	ftToTBC := poolAmounts{
		TBC: big.NewInt(900_000),
		FT:  big.NewInt(1_100_000),
		LP:  big.NewInt(1_000_000),
	}
	if err := validatePoolSwapDelta(before, ftToTBC, poolSwapFTToTBC); err != nil {
		t.Fatal(err)
	}
	if err := validatePoolSwapDelta(before, tbcToFT, poolSwapFTToTBC); err == nil {
		t.Fatal("expected reversed reserve direction rejection")
	}
}

func TestDecodePoolAmountsFromTape(t *testing.T) {
	pool := contract.NewPoolNFT2(nil)
	pool.FtLpAmount = big.NewInt(123)
	pool.FtAAmount = big.NewInt(456)
	pool.TbcAmount = big.NewInt(789)
	pool.FtLpPartialHash = strings.Repeat("22", 32)
	pool.FtAPartialHash = strings.Repeat("33", 32)
	pool.FtAContractTxID = strings.Repeat("44", 32)
	tape, err := pool.GetPoolNftTape(2, false, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePoolAmounts(tape)
	if err != nil {
		t.Fatal(err)
	}
	if got.LP.Cmp(big.NewInt(123)) != 0 ||
		got.FT.Cmp(big.NewInt(456)) != 0 ||
		got.TBC.Cmp(big.NewInt(789)) != 0 {
		t.Fatalf("decoded pool amounts LP/FT/TBC=%s/%s/%s", got.LP, got.FT, got.TBC)
	}
}

func testPoolTape(
	t *testing.T,
	lp,
	ft,
	tbc int64,
	withLock,
	withLockTime bool,
) *bscript.Script {
	t.Helper()
	pool := contract.NewPoolNFT2(nil)
	pool.FtLpAmount = big.NewInt(lp)
	pool.FtAAmount = big.NewInt(ft)
	pool.TbcAmount = big.NewInt(tbc)
	pool.FtLpPartialHash = strings.Repeat("22", 32)
	pool.FtAPartialHash = strings.Repeat("33", 32)
	pool.FtAContractTxID = strings.Repeat("44", 32)
	tape, err := pool.GetPoolNftTape(2, withLock, withLockTime)
	if err != nil {
		t.Fatal(err)
	}
	return tape
}

func TestValidatePoolCreationMintChecksZeroStateAndFlags(t *testing.T) {
	code, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 1_000, LockingScript: code})
	tx.AddOutput(&bt.Output{
		Satoshis: 0, LockingScript: testPoolTape(t, 0, 0, 0, false, false),
	})
	tx.AddOutput(&bt.Output{Satoshis: 1_000, LockingScript: code})
	if err := validatePoolCreationMint(tx, nil, false); err != nil {
		t.Fatalf("standard Pool NFT mint rejected: %v", err)
	}

	tx.Outputs[1].LockingScript = testPoolTape(t, 1, 0, 0, false, false)
	if err := validatePoolCreationMint(tx, nil, false); err == nil {
		t.Fatal("expected non-zero creation reserve rejection")
	}
}

func TestValidatePoolInitTransition(t *testing.T) {
	previous := strings.Repeat("55", 32)
	code, err := bscript.NewFromASM("OP_TRUE")
	if err != nil {
		t.Fatal(err)
	}
	txid, err := hex.DecodeString(previous)
	if err != nil {
		t.Fatal(err)
	}
	tx := bt.NewTx()
	if err := tx.FromUTXOs(&bt.UTXO{
		TxID: txid, Vout: 0, Satoshis: 1_000, LockingScript: code,
	}); err != nil {
		t.Fatal(err)
	}
	tx.AddOutput(&bt.Output{Satoshis: 1_789, LockingScript: code})
	tx.AddOutput(&bt.Output{
		Satoshis: 0, LockingScript: testPoolTape(t, 123, 456, 789, false, false),
	})
	after, err := validatePoolTransition(
		tx,
		poolStateRef{
			TxID: previous, Vout: 0, Satoshis: 1_000,
			CodeHex: code.ToHex(),
			Amounts: poolAmounts{
				LP: new(big.Int), FT: new(big.Int), TBC: new(big.Int),
			},
		},
		poolTransitionInit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if after.LP.Cmp(big.NewInt(123)) != 0 ||
		after.FT.Cmp(big.NewInt(456)) != 0 ||
		after.TBC.Cmp(big.NewInt(789)) != 0 {
		t.Fatal("pool init amounts changed during validation")
	}
}

func TestPoolCreateStageUsesUnspentFTSourceChange(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = poolCreateStageFundingMinimumSatoshis
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Pool Matrix", Symbol: "PMX", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	raws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	source, err := bt.NewTxFromString(raws[0])
	if err != nil {
		t.Fatal(err)
	}
	mint, err := bt.NewTxFromString(raws[1])
	if err != nil {
		t.Fatal(err)
	}
	got, err := poolCreationFunding(source, mint)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got.TxID) != source.TxID() || got.Vout != 2 {
		t.Fatalf(
			"pool funding=%s:%d want unspent FT source change=%s:2",
			hex.EncodeToString(got.TxID),
			got.Vout,
			source.TxID(),
		)
	}
	if got.Satoshis <= 20_000 {
		t.Fatalf("pool funding=%d leaves no margin for create+init", got.Satoshis)
	}
}

func TestPoolCreationTransactionsPayFinalSignedSizeFee(t *testing.T) {
	privateKey, address, funding := ftStageFixture(t)
	funding.Satoshis = poolCreateStageFundingMinimumSatoshis
	token, err := contract.NewFT(&contract.FtParams{
		Name: "Pool Matrix", Symbol: "PMX", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	mintRaws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		t.Fatal(err)
	}
	ftSource, err := bt.NewTxFromString(mintRaws[0])
	if err != nil {
		t.Fatal(err)
	}
	ftMint, err := bt.NewTxFromString(mintRaws[1])
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/ft/info/contract/"+token.ContractTxid {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			response,
			`{"code":"200","data":{"code_script":%q,"tape_script":%q,`+
				`"amount":"100000000","decimal":2,"name":"Pool Matrix","symbol":"PMX"}}`,
			token.CodeScript,
			token.TapeScript,
		)
	}))
	defer server.Close()

	poolFunding, err := poolCreationFunding(ftSource, ftMint)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		build func(*contract.PoolNFT2) ([]string, error)
	}{
		{
			name: "ordinary",
			build: func(pool *contract.PoolNFT2) ([]string, error) {
				return pool.CreatePoolNFT(
					privateKey,
					poolFunding,
					"fee-test",
					35,
					2,
					false,
				)
			},
		},
		{
			name: "multisig-lock",
			build: func(pool *contract.PoolNFT2) ([]string, error) {
				return pool.CreatePoolNFTWithLock(
					privateKey,
					poolFunding,
					"fee-test-lock",
					address,
					0.0001,
					[]string{
						hex.EncodeToString(
							privateKey.PubKey().SerialiseCompressed(),
						),
					},
					35,
					2,
					true,
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{
				Network: server.URL + "/",
			})
			if err := pool.InitCreate(token.ContractTxid); err != nil {
				t.Fatal(err)
			}
			raws, err := test.build(pool)
			if err != nil {
				t.Fatal(err)
			}
			if len(raws) != 2 {
				t.Fatalf("transactions=%d want=2", len(raws))
			}
			parents := map[string]*bt.Tx{ftSource.TxID(): ftSource}
			for index, raw := range raws {
				tx, err := bt.NewTxFromString(raw)
				if err != nil {
					t.Fatal(err)
				}
				paid, err := feeFromParents(tx, func(txid string) (*bt.Tx, error) {
					parent, ok := parents[txid]
					if !ok {
						return nil, fmt.Errorf("missing parent %s", txid)
					}
					return parent, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				target := targetFee80(len(tx.Bytes()))
				if paid < target {
					t.Fatalf(
						"transaction %d fee=%d target=%d bytes=%d",
						index,
						paid,
						target,
						len(tx.Bytes()),
					)
				}
				parents[tx.TxID()] = tx
			}
		})
	}
}

func TestLiveLockedPoolInitScriptPreflight(t *testing.T) {
	poolID := os.Getenv("TBC_TESTNET_LOCKED_POOL_DEBUG_ID")
	sourceID := os.Getenv("TBC_TESTNET_LOCKED_POOL_SOURCE_DEBUG_ID")
	wifText := os.Getenv("TBC_TESTNET_WIF")
	if poolID == "" || sourceID == "" || wifText == "" {
		t.Skip("live locked-pool debug inputs are not configured")
	}
	decoded, err := wif.DecodeWIF(wifText)
	if err != nil {
		t.Fatal(err)
	}
	address, err := bscript.NewAddressFromPublicKey(
		decoded.PrivKey.PubKey(),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err := api.FetchTXRaw(sourceID, "testnet")
	if err != nil {
		t.Fatal(err)
	}
	funding, err := outputUTXO(source, 1)
	if err != nil {
		t.Fatal(err)
	}
	height, err := api.FetchTBCLockTime("testnet")
	if err != nil {
		t.Fatal(err)
	}
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{
		ContractTxID: poolID,
		Network:      "testnet",
	})
	if err := pool.InitFromContractID(); err != nil {
		t.Fatal(err)
	}
	raw, err := pool.InitPoolNFT(
		decoded.PrivKey,
		address.AddressString,
		funding,
		"0.005",
		"50",
		height-1,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInputScriptsFromParents(
		tx,
		func(txid string) (*bt.Tx, error) {
			return api.FetchTXRaw(txid, "testnet")
		},
	); err != nil {
		t.Fatal(err)
	}
}
