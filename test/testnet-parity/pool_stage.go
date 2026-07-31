package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const (
	poolCreateStageFundingMinimumSatoshis = uint64(60_000)
	poolLockStageFundingMinimumSatoshis   = uint64(30_000)
)

type poolAmounts struct {
	LP  *big.Int
	FT  *big.Int
	TBC *big.Int
}

type poolSwapDirection uint8

const (
	poolSwapTBCToFT poolSwapDirection = iota + 1
	poolSwapFTToTBC
)

func clonePoolAmounts(value poolAmounts) poolAmounts {
	clone := func(input *big.Int) *big.Int {
		if input == nil {
			return new(big.Int)
		}
		return new(big.Int).Set(input)
	}
	return poolAmounts{
		LP:  clone(value.LP),
		FT:  clone(value.FT),
		TBC: clone(value.TBC),
	}
}

func validatePoolStateInput(tx *bt.Tx, previousTxID string, previousVout uint32) error {
	if tx == nil || len(tx.Inputs) == 0 {
		return fmt.Errorf("pool transition has no state input")
	}
	previousTxID = strings.ToLower(strings.TrimSpace(previousTxID))
	if len(previousTxID) != 64 {
		return fmt.Errorf("previous pool state txid is invalid")
	}
	gotTxID := hex.EncodeToString(tx.Inputs[0].PreviousTxID())
	gotVout := tx.Inputs[0].PreviousTxOutIndex
	if gotTxID != previousTxID || gotVout != previousVout {
		return fmt.Errorf(
			"pool state input=%s:%d want=%s:%d",
			gotTxID,
			gotVout,
			previousTxID,
			previousVout,
		)
	}
	for i := 1; i < len(tx.Inputs); i++ {
		if hex.EncodeToString(tx.Inputs[i].PreviousTxID()) == previousTxID &&
			tx.Inputs[i].PreviousTxOutIndex == previousVout {
			return fmt.Errorf("pool state outpoint is spent more than once")
		}
	}
	return nil
}

func decodePoolAmounts(tape *bscript.Script) (poolAmounts, error) {
	if tape == nil {
		return poolAmounts{}, fmt.Errorf("nil Pool NFT Tape")
	}
	chunks := tape.Chunks()
	if len(chunks) < 4 || len(chunks[3].Buf) != 24 {
		return poolAmounts{}, fmt.Errorf("Pool NFT Tape amount chunk must be 24 bytes")
	}
	data := chunks[3].Buf
	return poolAmounts{
		LP:  new(big.Int).SetUint64(binary.LittleEndian.Uint64(data[0:8])),
		FT:  new(big.Int).SetUint64(binary.LittleEndian.Uint64(data[8:16])),
		TBC: new(big.Int).SetUint64(binary.LittleEndian.Uint64(data[16:24])),
	}, nil
}

func validatePoolSwapDelta(
	before poolAmounts,
	after poolAmounts,
	direction poolSwapDirection,
) error {
	before = clonePoolAmounts(before)
	after = clonePoolAmounts(after)
	if after.LP.Cmp(before.LP) != 0 {
		return fmt.Errorf("pool swap changed LP supply from %s to %s", before.LP, after.LP)
	}
	switch direction {
	case poolSwapTBCToFT:
		if after.TBC.Cmp(before.TBC) <= 0 || after.FT.Cmp(before.FT) >= 0 {
			return fmt.Errorf(
				"TBC-to-FT reserve direction before=%s/%s after=%s/%s",
				before.TBC,
				before.FT,
				after.TBC,
				after.FT,
			)
		}
	case poolSwapFTToTBC:
		if after.TBC.Cmp(before.TBC) >= 0 || after.FT.Cmp(before.FT) <= 0 {
			return fmt.Errorf(
				"FT-to-TBC reserve direction before=%s/%s after=%s/%s",
				before.TBC,
				before.FT,
				after.TBC,
				after.FT,
			)
		}
	default:
		return fmt.Errorf("unknown pool swap direction %d", direction)
	}
	return nil
}

func validateLockedLPCostOutput(
	tx *bt.Tx,
	address string,
	amount uint64,
) error {
	if tx == nil {
		return fmt.Errorf("nil locked LP increase transaction")
	}
	wantScript, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return fmt.Errorf("locked LP cost address: %w", err)
	}
	matches := 0
	for _, output := range tx.Outputs {
		if output != nil && bytes.Equal(
			output.LockingScript.Bytes(),
			wantScript.Bytes(),
		) && output.Satoshis == amount {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf(
			"exact locked LP cost outputs=%d want=1",
			matches,
		)
	}
	return nil
}

type poolTransitionKind uint8

const (
	poolTransitionInit poolTransitionKind = iota + 1
	poolTransitionIncrease
	poolTransitionSwapTBCToFT
	poolTransitionSwapFTToTBC
	poolTransitionConsume
	poolTransitionMergeHeldFT
)

type poolStateRef struct {
	TxID     string
	Vout     uint32
	Satoshis uint64
	CodeHex  string
	Amounts  poolAmounts
}

func parsePoolAmount(value, label string) (*big.Int, error) {
	amount, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	if !ok || amount.Sign() < 0 {
		return nil, fmt.Errorf("indexed pool %s=%q is invalid", label, value)
	}
	return amount, nil
}

func poolStateFromInfo(info *api.PoolNFTInfo) (poolStateRef, error) {
	if info == nil {
		return poolStateRef{}, fmt.Errorf("nil Pool NFT info")
	}
	lp, err := parsePoolAmount(info.FtLpAmount, "LP amount")
	if err != nil {
		return poolStateRef{}, err
	}
	ft, err := parsePoolAmount(info.FtAAmount, "FT amount")
	if err != nil {
		return poolStateRef{}, err
	}
	tbc, err := parsePoolAmount(info.TBCAmount, "TBC amount")
	if err != nil {
		return poolStateRef{}, err
	}
	if len(strings.TrimSpace(info.CurrentContractTxID)) != 64 {
		return poolStateRef{}, fmt.Errorf("indexed pool current txid is invalid")
	}
	if strings.TrimSpace(info.PoolNftCode) == "" {
		return poolStateRef{}, fmt.Errorf("indexed pool code is empty")
	}
	return poolStateRef{
		TxID:     strings.ToLower(info.CurrentContractTxID),
		Vout:     uint32(info.CurrentContractVout),
		Satoshis: info.CurrentContractSatoshi,
		CodeHex:  info.PoolNftCode,
		Amounts: poolAmounts{
			LP: lp, FT: ft, TBC: tbc,
		},
	}, nil
}

func validatePoolStateOutput(
	tx *bt.Tx,
	codeHex string,
) (poolAmounts, error) {
	if tx == nil || len(tx.Outputs) < 2 {
		return poolAmounts{}, fmt.Errorf("pool transition requires Code/Tape outputs")
	}
	wantCode, err := hex.DecodeString(codeHex)
	if err != nil {
		return poolAmounts{}, fmt.Errorf("decode expected pool code: %w", err)
	}
	if tx.Outputs[0].Satoshis < 1_000 {
		return poolAmounts{}, fmt.Errorf(
			"pool Code satoshis=%d want>=1000",
			tx.Outputs[0].Satoshis,
		)
	}
	if !bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), wantCode) {
		return poolAmounts{}, fmt.Errorf("pool Code script identity changed")
	}
	if tx.Outputs[1].Satoshis != 0 || !tx.Outputs[1].LockingScript.IsSafeDataOut() {
		return poolAmounts{}, fmt.Errorf("pool Tape is not zero-satoshi safe data")
	}
	amounts, err := decodePoolAmounts(tx.Outputs[1].LockingScript)
	if err != nil {
		return poolAmounts{}, err
	}
	if !amounts.TBC.IsUint64() {
		return poolAmounts{}, fmt.Errorf("pool TBC reserve exceeds uint64")
	}
	if amounts.TBC.Uint64() > tx.Outputs[0].Satoshis-1_000 {
		return poolAmounts{}, fmt.Errorf(
			"pool TBC reserve=%s exceeds state output backing=%d",
			amounts.TBC,
			tx.Outputs[0].Satoshis-1_000,
		)
	}
	return amounts, nil
}

func validatePoolTransition(
	tx *bt.Tx,
	before poolStateRef,
	kind poolTransitionKind,
) (poolAmounts, error) {
	if err := validatePoolStateInput(tx, before.TxID, before.Vout); err != nil {
		return poolAmounts{}, err
	}
	after, err := validatePoolStateOutput(tx, before.CodeHex)
	if err != nil {
		return poolAmounts{}, err
	}
	beforeAmounts := clonePoolAmounts(before.Amounts)
	switch kind {
	case poolTransitionInit:
		if beforeAmounts.LP.Sign() != 0 ||
			beforeAmounts.FT.Sign() != 0 ||
			beforeAmounts.TBC.Sign() != 0 {
			return poolAmounts{}, fmt.Errorf("pool init started from non-zero reserves")
		}
		if after.LP.Sign() <= 0 || after.FT.Sign() <= 0 || after.TBC.Sign() <= 0 {
			return poolAmounts{}, fmt.Errorf("pool init did not create positive reserves")
		}
	case poolTransitionIncrease:
		if after.LP.Cmp(beforeAmounts.LP) <= 0 ||
			after.FT.Cmp(beforeAmounts.FT) <= 0 ||
			after.TBC.Cmp(beforeAmounts.TBC) <= 0 {
			return poolAmounts{}, fmt.Errorf(
				"pool increase did not increase LP/FT/TBC reserves",
			)
		}
	case poolTransitionSwapTBCToFT:
		if err := validatePoolSwapDelta(
			beforeAmounts,
			after,
			poolSwapTBCToFT,
		); err != nil {
			return poolAmounts{}, err
		}
	case poolTransitionSwapFTToTBC:
		if err := validatePoolSwapDelta(
			beforeAmounts,
			after,
			poolSwapFTToTBC,
		); err != nil {
			return poolAmounts{}, err
		}
	case poolTransitionConsume:
		if after.LP.Cmp(beforeAmounts.LP) >= 0 ||
			after.FT.Cmp(beforeAmounts.FT) >= 0 ||
			after.TBC.Cmp(beforeAmounts.TBC) >= 0 {
			return poolAmounts{}, fmt.Errorf(
				"pool consume did not decrease LP/FT/TBC reserves",
			)
		}
	case poolTransitionMergeHeldFT:
		if after.LP.Cmp(beforeAmounts.LP) != 0 ||
			after.FT.Cmp(beforeAmounts.FT) != 0 ||
			after.TBC.Cmp(beforeAmounts.TBC) != 0 {
			return poolAmounts{}, fmt.Errorf("pool FT merge changed reserve accounting")
		}
	default:
		return poolAmounts{}, fmt.Errorf("unknown pool transition kind %d", kind)
	}
	return after, nil
}

func validatePoolCreationMint(
	tx *bt.Tx,
	publicKeys []string,
	withLockTime bool,
) error {
	if tx == nil || len(tx.Outputs) < 3 {
		return fmt.Errorf("Pool NFT mint has incomplete outputs")
	}
	if tx.Outputs[0].Satoshis != 1_000 {
		return fmt.Errorf("Pool NFT Code satoshis=%d want=1000", tx.Outputs[0].Satoshis)
	}
	if tx.Outputs[1].Satoshis != 0 || !tx.Outputs[1].LockingScript.IsSafeDataOut() {
		return fmt.Errorf("Pool NFT Tape is not zero-satoshi safe data")
	}
	amounts, err := decodePoolAmounts(tx.Outputs[1].LockingScript)
	if err != nil {
		return err
	}
	if amounts.LP.Sign() != 0 || amounts.FT.Sign() != 0 || amounts.TBC.Sign() != 0 {
		return fmt.Errorf("new Pool NFT reserves are not zero")
	}
	chunks := tx.Outputs[1].LockingScript.Chunks()
	if len(chunks) < 9 || len(chunks[7].Buf) != 1 || len(chunks[8].Buf) != 1 {
		return fmt.Errorf("Pool NFT Tape lock flags are missing")
	}
	wantLock := byte(0)
	if len(publicKeys) > 0 {
		wantLock = 1
		for i, publicKey := range publicKeys {
			raw, err := hex.DecodeString(publicKey)
			if err != nil {
				return fmt.Errorf("decode pool lock public key %d: %w", i, err)
			}
			if !bytes.Contains(tx.Outputs[0].LockingScript.Bytes(), raw) {
				return fmt.Errorf("Pool NFT code is missing lock public key %d", i)
			}
		}
	}
	wantLockTime := byte(0)
	if withLockTime {
		wantLockTime = 1
	}
	if chunks[7].Buf[0] != wantLock || chunks[8].Buf[0] != wantLockTime {
		return fmt.Errorf(
			"Pool NFT lock flags=%d/%d want=%d/%d",
			chunks[7].Buf[0],
			chunks[8].Buf[0],
			wantLock,
			wantLockTime,
		)
	}
	return nil
}

func decodeFtlpLockTime(tape *bscript.Script) (uint32, error) {
	if tape == nil {
		return 0, fmt.Errorf("nil FTLP Tape")
	}
	chunks := tape.Chunks()
	if len(chunks) < 4 || len(chunks[3].Buf) < 4 {
		return 0, fmt.Errorf("FTLP lock-time chunk is missing")
	}
	return binary.LittleEndian.Uint32(chunks[3].Buf[:4]), nil
}

func validateFTLPPair(
	tx *bt.Tx,
	codeVout int,
	wantLockTime *uint32,
) (*big.Int, error) {
	if tx == nil || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return nil, fmt.Errorf("FTLP Code/Tape outputs are out of range")
	}
	code := tx.Outputs[codeVout]
	tape := tx.Outputs[codeVout+1]
	if code.Satoshis != 500 {
		return nil, fmt.Errorf("FTLP Code satoshis=%d want=500", code.Satoshis)
	}
	if tape.Satoshis != 0 || !tape.LockingScript.IsSafeDataOut() {
		return nil, fmt.Errorf("FTLP Tape is not zero-satoshi safe data")
	}
	balance, err := contractutil.GetFtBalanceFromTape(tape.LockingScript.ToHex())
	if err != nil {
		return nil, fmt.Errorf("decode FTLP balance: %w", err)
	}
	if balance.Sign() <= 0 {
		return nil, fmt.Errorf("FTLP balance=%s want positive", balance)
	}
	if wantLockTime != nil {
		got, err := decodeFtlpLockTime(tape.LockingScript)
		if err != nil {
			return nil, err
		}
		if got != *wantLockTime {
			return nil, fmt.Errorf("FTLP lockTime=%d want=%d", got, *wantLockTime)
		}
	}
	return balance, nil
}

func waitForFTV3Info(contractID, network string) (*api.FtInfoResponse, error) {
	const attempts = 15
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		info, err := api.FetchFtInfo(contractID, network)
		if err == nil {
			classified, classifyErr := contractutil.ClassifyFTScriptHex(info.CodeScript)
			if classifyErr == nil &&
				classified.Version == contractutil.FTVersion3 &&
				!classified.IsCoin {
				return info, nil
			}
			if classifyErr != nil {
				err = classifyErr
			} else {
				err = fmt.Errorf("indexed pool token is not ordinary FT v3")
			}
		}
		lastErr = err
		if attempt < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, fmt.Errorf("FT indexer did not converge: %w", lastErr)
}

func waitForPoolState(
	poolID,
	currentTxID,
	network string,
) (*api.PoolNFTInfo, error) {
	const attempts = 15
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		info, err := api.FetchPoolNFTInfo(poolID, network)
		if err == nil {
			if strings.EqualFold(info.CurrentContractTxID, currentTxID) {
				return info, nil
			}
			err = fmt.Errorf(
				"indexed current pool state=%s want=%s",
				info.CurrentContractTxID,
				currentTxID,
			)
		}
		lastErr = err
		if attempt < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, fmt.Errorf("Pool NFT indexer did not converge: %w", lastErr)
}

func loadIndexedPool(
	poolID,
	network string,
) (*contract.PoolNFT2, poolStateRef, error) {
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{
		ContractTxID: poolID,
		Network:      network,
	})
	if err := pool.InitFromContractID(); err != nil {
		return nil, poolStateRef{}, err
	}
	info, err := api.FetchPoolNFTInfo(poolID, network)
	if err != nil {
		return nil, poolStateRef{}, err
	}
	state, err := poolStateFromInfo(info)
	if err != nil {
		return nil, poolStateRef{}, err
	}
	return pool, state, nil
}

type indexedPoolBuilder func(*contract.PoolNFT2) (string, error)

func buildIndexedPoolOperation(
	poolID,
	network string,
	build indexedPoolBuilder,
) (*contract.PoolNFT2, poolStateRef, string, error) {
	if build == nil {
		return nil, poolStateRef{}, "", fmt.Errorf("nil pool operation builder")
	}
	const attempts = 12
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		pool, before, err := loadIndexedPool(poolID, network)
		if err == nil {
			raw, buildErr := build(pool)
			if buildErr == nil {
				return pool, before, raw, nil
			}
			err = buildErr
		}
		lastErr = err
		if attempt < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, poolStateRef{}, "", fmt.Errorf(
		"pool operation could not be built after indexer retries: %w",
		lastErr,
	)
}

func broadcastPoolTransition(
	label,
	raw,
	network string,
	before poolStateRef,
	kind poolTransitionKind,
	extra func(*bt.Tx) error,
) (*bt.Tx, error) {
	accepted, _, err := broadcastAndVerify(
		label,
		raw,
		network,
		"pool-state-outpoint-integer-reserves",
		func(tx *bt.Tx) error {
			if _, err := validatePoolTransition(tx, before, kind); err != nil {
				return err
			}
			if extra != nil {
				return extra(tx)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return accepted, nil
}

func executeIndexedPoolOperation(
	cfg config,
	poolID,
	label string,
	kind poolTransitionKind,
	build indexedPoolBuilder,
	extra func(*bt.Tx) error,
) (*bt.Tx, error) {
	_, before, raw, err := buildIndexedPoolOperation(
		poolID,
		cfg.Network,
		build,
	)
	if err != nil {
		return nil, fmt.Errorf("%s build: %w", label, err)
	}
	accepted, err := broadcastPoolTransition(
		label,
		raw,
		cfg.Network,
		before,
		kind,
		extra,
	)
	if err != nil {
		return nil, err
	}
	if _, err := waitForPoolState(poolID, accepted.TxID(), cfg.Network); err != nil {
		return nil, fmt.Errorf("%s index: %w", label, err)
	}
	return accepted, nil
}

func waitForFTLPTransaction(
	pool *contract.PoolNFT2,
	address,
	txID string,
) error {
	const attempts = 15
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		utxos, err := pool.FetchFtLpUTXOList(address)
		if err == nil {
			for _, utxo := range utxos {
				if utxo != nil && strings.EqualFold(utxo.TxID, txID) {
					return nil
				}
			}
			err = fmt.Errorf("FTLP UTXO from %s is not indexed", txID)
		}
		lastErr = err
		if attempt < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return fmt.Errorf("FTLP indexer did not converge: %w", lastErr)
}

func poolCreationFunding(source, mint *bt.Tx) (*bt.UTXO, error) {
	if source == nil || mint == nil || len(mint.Inputs) == 0 {
		return nil, fmt.Errorf("pool FT mint ancestry is incomplete")
	}
	if err := validateOutpoint(mint, 0, source, 0); err != nil {
		return nil, fmt.Errorf("pool FT mint ancestry: %w", err)
	}
	return outputUTXO(source, 2)
}

func runPoolCreateStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(
		address,
		float64(poolCreateStageFundingMinimumSatoshis)/1_000_000,
		cfg.Network,
	)
	if err != nil {
		return fmt.Errorf("pool token funding: %w", err)
	}
	token, err := contract.NewFT(&contract.FtParams{
		Name: "GoPoolFullMatrix", Symbol: "GPFM", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		return err
	}
	mintRaws, err := token.MintFT(decoded.PrivKey, address, funding)
	if err != nil {
		return fmt.Errorf("pool token mint: %w", err)
	}
	if len(mintRaws) != 2 {
		return fmt.Errorf("pool token mint returned %d transactions", len(mintRaws))
	}
	source, err := parsePlannedTransaction("pool-token-source", mintRaws[0])
	if err != nil {
		return err
	}
	mint, err := parsePlannedTransaction("pool-token-mint", mintRaws[1])
	if err != nil {
		return err
	}
	for _, item := range []plannedTransaction{
		{Label: "pool-token-source", Raw: mintRaws[0]},
		{Label: "pool-token-mint", Raw: mintRaws[1]},
	} {
		label := item.Label
		_, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"ordinary-ft-v3-for-pool",
			func(tx *bt.Tx) error {
				if label == "pool-token-source" {
					return validateFTLifecycleTransaction("ft-source", tx)
				}
				return requireFTBalance(tx, 0, 100_000_000)
			},
		)
		if err != nil {
			return err
		}
	}
	if _, err := waitForFTV3Info(token.ContractTxid, cfg.Network); err != nil {
		return err
	}

	poolFunding, err := poolCreationFunding(source, mint)
	if err != nil {
		return err
	}
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: cfg.Network})
	if err := pool.InitCreate(token.ContractTxid); err != nil {
		return err
	}
	poolRaws, err := pool.CreatePoolNFT(
		decoded.PrivKey,
		poolFunding,
		"go-pool",
		35,
		2,
		false,
	)
	if err != nil {
		return fmt.Errorf("Pool NFT create: %w", err)
	}
	if len(poolRaws) != 2 {
		return fmt.Errorf("Pool NFT create returned %d transactions", len(poolRaws))
	}
	poolSource, err := parsePlannedTransaction("pool-source", poolRaws[0])
	if err != nil {
		return err
	}
	poolMint, err := parsePlannedTransaction("pool-mint", poolRaws[1])
	if err != nil {
		return err
	}
	if _, _, err := broadcastAndVerify(
		"pool-source",
		poolRaws[0],
		cfg.Network,
		"pool-mint-source",
		func(tx *bt.Tx) error {
			if len(tx.Outputs) != 2 || tx.Outputs[0].Satoshis != 9_800 {
				return fmt.Errorf("Pool NFT source output layout is invalid")
			}
			return nil
		},
	); err != nil {
		return err
	}
	if _, _, err := broadcastAndVerify(
		"pool-mint",
		poolRaws[1],
		cfg.Network,
		"pool-code-tape-zero-state",
		func(tx *bt.Tx) error {
			return validatePoolCreationMint(tx, nil, false)
		},
	); err != nil {
		return err
	}
	poolID := poolMint.TxID()
	if _, err := waitForPoolState(poolID, poolID, cfg.Network); err != nil {
		return err
	}

	initFunding, err := outputUTXO(poolSource, 1)
	if err != nil {
		return err
	}
	initTX, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-init",
		poolTransitionInit,
		func(indexed *contract.PoolNFT2) (string, error) {
			return indexed.InitPoolNFT(
				decoded.PrivKey,
				address,
				initFunding,
				"0.01",
				"100",
				0,
			)
		},
		func(tx *bt.Tx) error {
			_, err := validateFTLPPair(tx, 4, nil)
			return err
		},
	)
	if err != nil {
		return err
	}
	return writePublicState(os.Stdout, publicState{
		TokenID:  token.ContractTxid,
		PoolID:   poolID,
		LastTxID: initTX.TxID(),
		LastVout: 0,
	})
}

func fetchPoolFee(address, network string, minimum float64) (*bt.UTXO, error) {
	fee, err := api.FetchUTXO(address, minimum, network)
	if err != nil {
		return nil, fmt.Errorf("fetch pool fee UTXO: %w", err)
	}
	return fee, nil
}

func runPoolTradeStage(cfg config, decoded *wif.WIF, address string) error {
	if cfg.PoolID == "" {
		return fmt.Errorf("TBC_TESTNET_POOL_ID is required for pool-trade")
	}
	initialInfo, err := api.FetchPoolNFTInfo(cfg.PoolID, cfg.Network)
	if err != nil {
		return err
	}
	if cfg.TokenA != "" && !strings.EqualFold(initialInfo.FtAContractTxID, cfg.TokenA) {
		return fmt.Errorf(
			"pool token id=%s does not match configured token=%s",
			initialInfo.FtAContractTxID,
			cfg.TokenA,
		)
	}
	var last *bt.Tx
	for index, amount := range []string{"0.002", "0.003"} {
		label := "pool-increase-lp"
		if index > 0 {
			label = "pool-increase-lp-2"
		}
		last, err = executeIndexedPoolOperation(
			cfg,
			cfg.PoolID,
			label,
			poolTransitionIncrease,
			func(pool *contract.PoolNFT2) (string, error) {
				fee, err := fetchPoolFee(address, cfg.Network, 0.005)
				if err != nil {
					return "", err
				}
				return pool.IncreaseLP(decoded.PrivKey, address, fee, amount, 0)
			},
			func(tx *bt.Tx) error {
				_, err := validateFTLPPair(tx, 4, nil)
				return err
			},
		)
		if err != nil {
			return err
		}
	}

	last, err = executeIndexedPoolOperation(
		cfg,
		cfg.PoolID,
		"pool-swap-tbc-to-ft",
		poolTransitionSwapTBCToFT,
		func(pool *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return pool.SwapToToken(decoded.PrivKey, address, fee, "0.001", 2)
		},
		nil,
	)
	if err != nil {
		return err
	}

	last, err = executeIndexedPoolOperation(
		cfg,
		cfg.PoolID,
		"pool-swap-ft-to-tbc",
		poolTransitionSwapFTToTBC,
		func(pool *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return pool.SwapToTBC(decoded.PrivKey, address, fee, "10", 2)
		},
		nil,
	)
	if err != nil {
		return err
	}

	last, err = executeIndexedPoolOperation(
		cfg,
		cfg.PoolID,
		"pool-consume-lp",
		poolTransitionConsume,
		func(pool *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			raws, err := pool.ConsumeLP(
				decoded.PrivKey,
				address,
				fee,
				"0.001",
				nil,
			)
			if err != nil {
				return "", err
			}
			if len(raws) != 1 {
				return "", fmt.Errorf("standard pool ConsumeLP returned %d transactions", len(raws))
			}
			return raws[0], nil
		},
		nil,
	)
	if err != nil {
		return err
	}

	pool, _, err := loadIndexedPool(cfg.PoolID, cfg.Network)
	if err != nil {
		return err
	}
	mergeFee, err := fetchPoolFee(address, cfg.Network, 0.005)
	if err != nil {
		return err
	}
	mergeRaw, err := pool.MergeFTLP(decoded.PrivKey, mergeFee, nil)
	if err != nil {
		return fmt.Errorf("pool MergeFTLP: %w", err)
	}
	if mergeRaw == "" {
		return fmt.Errorf("pool MergeFTLP had fewer than two FTLP UTXOs")
	}
	mergeLP, _, err := broadcastAndVerify(
		"pool-merge-ftlp",
		mergeRaw,
		cfg.Network,
		"ftlp-pair-multi-input",
		func(tx *bt.Tx) error {
			if len(tx.Inputs) < 3 {
				return fmt.Errorf("FTLP merge has %d inputs want>=3", len(tx.Inputs))
			}
			_, err := validateFTLPPair(tx, 0, nil)
			return err
		},
	)
	if err != nil {
		return err
	}
	if err := waitForFTLPTransaction(pool, address, mergeLP.TxID()); err != nil {
		return err
	}

	pool, _, err = loadIndexedPool(cfg.PoolID, cfg.Network)
	if err != nil {
		return err
	}
	burnFee, err := fetchPoolFee(address, cfg.Network, 0.005)
	if err != nil {
		return err
	}
	burnRaw, err := pool.BurnFTLP(decoded.PrivKey, burnFee)
	if err != nil {
		return fmt.Errorf("pool BurnFTLP: %w", err)
	}
	_, _, err = broadcastAndVerify(
		"pool-burn-ftlp",
		burnRaw,
		cfg.Network,
		"ftlp-burn-pair",
		func(tx *bt.Tx) error {
			_, err := validateFTLPPair(tx, 0, nil)
			return err
		},
	)
	if err != nil {
		return err
	}

	pool, before, err := loadIndexedPool(cfg.PoolID, cfg.Network)
	if err != nil {
		return err
	}
	mergeHeldFee, err := fetchPoolFee(address, cfg.Network, 0.01)
	if err != nil {
		return err
	}
	mergeHeldRaws, err := pool.MergeFTinPool(decoded.PrivKey, mergeHeldFee, 1)
	if err != nil {
		return fmt.Errorf("pool MergeFTinPool: %w", err)
	}
	if len(mergeHeldRaws) != 1 {
		return fmt.Errorf("pool MergeFTinPool returned %d transactions, want=1", len(mergeHeldRaws))
	}
	last, err = broadcastPoolTransition(
		"pool-merge-held-ft",
		mergeHeldRaws[0],
		cfg.Network,
		before,
		poolTransitionMergeHeldFT,
		nil,
	)
	if err != nil {
		return err
	}
	if _, err := waitForPoolState(cfg.PoolID, last.TxID(), cfg.Network); err != nil {
		return err
	}

	return writePublicState(os.Stdout, publicState{
		TokenID:  initialInfo.FtAContractTxID,
		PoolID:   cfg.PoolID,
		LastTxID: last.TxID(),
		LastVout: 0,
	})
}

func runPoolLockStage(cfg config, decoded *wif.WIF, address string) error {
	if cfg.TokenA == "" {
		return fmt.Errorf("TBC_TESTNET_TOKEN_A is required for pool-lock")
	}
	if _, err := waitForFTV3Info(cfg.TokenA, cfg.Network); err != nil {
		return err
	}
	funding, err := api.FetchUTXO(
		address,
		float64(poolLockStageFundingMinimumSatoshis)/1_000_000,
		cfg.Network,
	)
	if err != nil {
		return err
	}
	signers, err := newEphemeralMultiSigSet(decoded.PrivKey)
	if err != nil {
		return err
	}
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: cfg.Network})
	if err := pool.InitCreate(cfg.TokenA); err != nil {
		return err
	}
	raws, err := pool.CreatePoolNFTWithLock(
		decoded.PrivKey,
		funding,
		"pool-lock",
		address,
		0.0001,
		signers.PublicKeys,
		35,
		2,
		true,
	)
	if err != nil {
		return fmt.Errorf("locked Pool NFT create: %w", err)
	}
	if len(raws) != 2 {
		return fmt.Errorf("locked Pool NFT create returned %d transactions", len(raws))
	}
	source, err := parsePlannedTransaction("pool-lock-source", raws[0])
	if err != nil {
		return err
	}
	mint, err := parsePlannedTransaction("pool-create-multisig-lock", raws[1])
	if err != nil {
		return err
	}
	if _, _, err := broadcastAndVerify(
		"pool-lock-source",
		raws[0],
		cfg.Network,
		"pool-lock-mint-source",
		func(tx *bt.Tx) error {
			if len(tx.Outputs) != 2 || tx.Outputs[0].Satoshis != 9_800 {
				return fmt.Errorf("locked Pool NFT source output layout is invalid")
			}
			return nil
		},
	); err != nil {
		return err
	}
	if _, _, err := broadcastAndVerify(
		"pool-create-multisig-lock",
		raws[1],
		cfg.Network,
		"pool-pubkey-lock-and-ftlp-locktime-flags",
		func(tx *bt.Tx) error {
			return validatePoolCreationMint(tx, signers.PublicKeys, true)
		},
	); err != nil {
		return err
	}
	poolID := mint.TxID()
	if _, err := waitForPoolState(poolID, poolID, cfg.Network); err != nil {
		return err
	}

	height, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return err
	}
	if height == 0 {
		return fmt.Errorf("testnet height is zero")
	}
	lockTime := height - 1
	initFunding, err := outputUTXO(source, 1)
	if err != nil {
		return err
	}
	init, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-create-ftlp-locktime",
		poolTransitionInit,
		func(indexed *contract.PoolNFT2) (string, error) {
			return indexed.InitPoolNFT(
				decoded.PrivKey,
				address,
				initFunding,
				"0.005",
				"50",
				lockTime,
			)
		},
		func(tx *bt.Tx) error {
			_, err := validateFTLPPair(tx, 4, &lockTime)
			return err
		},
	)
	if err != nil {
		return err
	}
	_ = init

	var lockedLPCostAddress string
	var lockedLPCostAmount uint64
	if _, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-lock-increase-lp",
		poolTransitionIncrease,
		func(indexed *contract.PoolNFT2) (string, error) {
			var err error
			lockedLPCostAddress, err = contractutil.GetLpCostAddress(
				indexed.PoolNftCode,
			)
			if err != nil {
				return "", err
			}
			lockedLPCostAmount, err = contractutil.GetLpCostAmount(
				indexed.PoolNftCode,
			)
			if err != nil {
				return "", err
			}
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return indexed.IncreaseLP(
				decoded.PrivKey,
				address,
				fee,
				"0.002",
				lockTime,
			)
		},
		func(tx *bt.Tx) error {
			if _, err := validateFTLPPair(tx, 4, &lockTime); err != nil {
				return err
			}
			return validateLockedLPCostOutput(
				tx,
				lockedLPCostAddress,
				lockedLPCostAmount,
			)
		},
	); err != nil {
		return err
	}

	if _, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-lock-swap-tbc-to-ft",
		poolTransitionSwapTBCToFT,
		func(indexed *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return indexed.SwapToToken(
				decoded.PrivKey,
				address,
				fee,
				"0.001",
				2,
			)
		},
		nil,
	); err != nil {
		return err
	}

	if _, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-lock-swap-ft-to-tbc",
		poolTransitionSwapFTToTBC,
		func(indexed *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return indexed.SwapToTBC(
				decoded.PrivKey,
				address,
				fee,
				"10",
				2,
			)
		},
		nil,
	); err != nil {
		return err
	}

	zeroLockTime := uint32(0)
	pool, _, err = loadIndexedPool(poolID, cfg.Network)
	if err != nil {
		return err
	}
	mergeLPFee, err := fetchPoolFee(address, cfg.Network, 0.005)
	if err != nil {
		return err
	}
	mergeLPRaw, err := pool.MergeFTLP(
		decoded.PrivKey,
		mergeLPFee,
		&lockTime,
	)
	if err != nil {
		return fmt.Errorf("locked pool MergeFTLP: %w", err)
	}
	if mergeLPRaw == "" {
		return fmt.Errorf("locked pool MergeFTLP had fewer than two FTLP UTXOs")
	}
	mergeLP, _, err := broadcastAndVerify(
		"pool-lock-merge-ftlp",
		mergeLPRaw,
		cfg.Network,
		"locked-ftlp-pair-multi-input",
		func(tx *bt.Tx) error {
			if len(tx.Inputs) < 3 {
				return fmt.Errorf("locked FTLP merge has %d inputs want>=3", len(tx.Inputs))
			}
			_, err := validateFTLPPair(tx, 0, &zeroLockTime)
			return err
		},
	)
	if err != nil {
		return err
	}
	if err := waitForFTLPTransaction(pool, address, mergeLP.TxID()); err != nil {
		return err
	}
	if _, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-lock-increase-lp-after-merge",
		poolTransitionIncrease,
		func(indexed *contract.PoolNFT2) (string, error) {
			var err error
			lockedLPCostAddress, err = contractutil.GetLpCostAddress(
				indexed.PoolNftCode,
			)
			if err != nil {
				return "", err
			}
			lockedLPCostAmount, err = contractutil.GetLpCostAmount(
				indexed.PoolNftCode,
			)
			if err != nil {
				return "", err
			}
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			return indexed.IncreaseLP(
				decoded.PrivKey,
				address,
				fee,
				"0.001",
				lockTime,
			)
		},
		func(tx *bt.Tx) error {
			if _, err := validateFTLPPair(tx, 4, &lockTime); err != nil {
				return err
			}
			return validateLockedLPCostOutput(
				tx,
				lockedLPCostAddress,
				lockedLPCostAmount,
			)
		},
	); err != nil {
		return err
	}

	pool, _, err = loadIndexedPool(poolID, cfg.Network)
	if err != nil {
		return err
	}
	unlockFee, err := fetchPoolFee(address, cfg.Network, 0.005)
	if err != nil {
		return err
	}
	unlockRaw, err := pool.UnlockFTLP(decoded.PrivKey, unlockFee, &lockTime)
	if err != nil {
		return fmt.Errorf("pool UnlockFTLP: %w", err)
	}
	if unlockRaw == "" {
		return fmt.Errorf("locked FTLP unexpectedly required no unlock")
	}
	unlock, _, err := broadcastAndVerify(
		"pool-unlock-ftlp",
		unlockRaw,
		cfg.Network,
		"ftlp-locktime-zero-and-finality",
		func(tx *bt.Tx) error {
			if tx.LockTime != lockTime {
				return fmt.Errorf("FTLP unlock nLockTime=%d want=%d", tx.LockTime, lockTime)
			}
			for i := 0; i < len(tx.Inputs)-1; i++ {
				if tx.Inputs[i].SequenceNumber != 0xfffffffe {
					return fmt.Errorf("FTLP unlock input %d sequence is final", i)
				}
			}
			_, err := validateFTLPPair(tx, 0, &zeroLockTime)
			return err
		},
	)
	if err != nil {
		return err
	}
	if err := waitForFTLPTransaction(pool, address, unlock.TxID()); err != nil {
		return err
	}

	consume, err := executeIndexedPoolOperation(
		cfg,
		poolID,
		"pool-consume-unlocked-ftlp",
		poolTransitionConsume,
		func(indexed *contract.PoolNFT2) (string, error) {
			fee, err := fetchPoolFee(address, cfg.Network, 0.005)
			if err != nil {
				return "", err
			}
			raws, err := indexed.ConsumeLP(
				decoded.PrivKey,
				address,
				fee,
				"0.001",
				&lockTime,
			)
			if err != nil {
				return "", err
			}
			if len(raws) != 1 {
				return "", fmt.Errorf(
					"ConsumeLP after explicit unlock returned %d transactions",
					len(raws),
				)
			}
			return raws[0], nil
		},
		nil,
	)
	if err != nil {
		return err
	}

	pool, _, err = loadIndexedPool(poolID, cfg.Network)
	if err != nil {
		return err
	}
	if err := waitForFTLPTransaction(pool, address, consume.TxID()); err != nil {
		return err
	}
	burnFee, err := fetchPoolFee(address, cfg.Network, 0.005)
	if err != nil {
		return err
	}
	burnRaw, err := pool.BurnFTLP(decoded.PrivKey, burnFee)
	if err != nil {
		return fmt.Errorf("locked pool BurnFTLP: %w", err)
	}
	if _, _, err := broadcastAndVerify(
		"pool-lock-burn-ftlp",
		burnRaw,
		cfg.Network,
		"locked-ftlp-burn-pair",
		func(tx *bt.Tx) error {
			_, err := validateFTLPPair(tx, 0, nil)
			return err
		},
	); err != nil {
		return err
	}

	pool, beforeMergeHeld, err := loadIndexedPool(poolID, cfg.Network)
	if err != nil {
		return err
	}
	mergeHeldFee, err := fetchPoolFee(address, cfg.Network, 0.01)
	if err != nil {
		return err
	}
	mergeHeldRaws, err := pool.MergeFTinPool(
		decoded.PrivKey,
		mergeHeldFee,
		1,
	)
	if err != nil {
		return fmt.Errorf("locked pool MergeFTinPool: %w", err)
	}
	if len(mergeHeldRaws) != 1 {
		return fmt.Errorf(
			"locked pool MergeFTinPool returned %d transactions, want=1",
			len(mergeHeldRaws),
		)
	}
	mergeHeld, err := broadcastPoolTransition(
		"pool-lock-merge-held-ft",
		mergeHeldRaws[0],
		cfg.Network,
		beforeMergeHeld,
		poolTransitionMergeHeldFT,
		nil,
	)
	if err != nil {
		return err
	}
	if _, err := waitForPoolState(poolID, mergeHeld.TxID(), cfg.Network); err != nil {
		return err
	}
	return writePublicState(os.Stdout, publicState{
		TokenID:  cfg.TokenA,
		PoolID:   poolID,
		LastTxID: mergeHeld.TxID(),
		LastVout: 0,
	})
}
