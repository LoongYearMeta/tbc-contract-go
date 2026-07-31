package main

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
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
