package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"time"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const stableCoinStageFundingMinimumSatoshis = uint64(300_000)

const musigSignerScript = "test/testnet-parity/js-musig-sign.js"

func runMuSigSigner(arguments ...string) ([]byte, error) {
	command := exec.Command("node", append([]string{musigSignerScript}, arguments...)...)
	command.Env = os.Environ()
	output, err := command.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("external MuSig signer: %s", exitError.Stderr)
		}
		return nil, fmt.Errorf("external MuSig signer: %w", err)
	}
	return output, nil
}

func loadAggregateAdminKey() ([]byte, error) {
	output, err := runMuSigSigner("aggregate")
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(string(output))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("external MuSig signer returned invalid aggregate key")
	}
	return key, nil
}

func signAdminPrepared(prepared *contract.AdminPrepared) ([][]byte, error) {
	arguments := []string{"sign"}
	for _, item := range prepared.Sighashes {
		arguments = append(arguments, hex.EncodeToString(item.Sighash))
	}
	output, err := runMuSigSigner(arguments...)
	if err != nil {
		return nil, err
	}
	var encoded []string
	if err := json.Unmarshal(output, &encoded); err != nil {
		return nil, fmt.Errorf("decode external MuSig signatures: %w", err)
	}
	signatures := make([][]byte, len(encoded))
	for i, item := range encoded {
		signatures[i], err = hex.DecodeString(item)
		if err != nil {
			return nil, fmt.Errorf("decode external MuSig signature %d: %w", i, err)
		}
	}
	if err := validateAdminSignatures(signatures, len(prepared.Sighashes)); err != nil {
		return nil, err
	}
	return signatures, nil
}

func validateAdminSignatures(signatures [][]byte, expected int) error {
	if len(signatures) != expected {
		return fmt.Errorf(
			"external MuSig signature count %d, want %d",
			len(signatures),
			expected,
		)
	}
	for i, signature := range signatures {
		if len(signature) != 64 {
			return fmt.Errorf("external MuSig signature %d is not 64 bytes", i)
		}
	}
	return nil
}

func finalizeAdminPrepared(prepared *contract.AdminPrepared) ([]string, error) {
	signatures, err := signAdminPrepared(prepared)
	if err != nil {
		return nil, err
	}
	return prepared.Finalize(signatures)
}

func stableCoinInput(
	tx *bt.Tx,
	codeOutput int,
) (*contractutil.FtUTXO, error) {
	return ftUTXOFromTX(tx, codeOutput)
}

type adminPreparedFinalizer func(*contract.AdminPrepared) ([]string, error)

type stableCoinLifecyclePlan struct {
	Transactions []plannedTransaction
	StableCoin   *contract.StableCoin
	CoinNFT      *bt.Tx
	InitialMint  *bt.Tx
	AdminMint    *bt.Tx
	Transfer     *bt.Tx
	Batch        *bt.Tx
	Merge        *bt.Tx
	Freeze       *bt.Tx
	Unfreeze     *bt.Tx
	LockTime     uint32
}

func coinNFTTotalSupply(tx *bt.Tx) (*big.Int, error) {
	if tx == nil || len(tx.Outputs) < 3 {
		return nil, fmt.Errorf("Coin NFT transaction requires Code/Hold/Tape outputs")
	}
	data, err := contract.DecodeCoinNftTapeScript(tx.Outputs[2].LockingScript)
	if err != nil {
		return nil, fmt.Errorf("decode Coin NFT Tape: %w", err)
	}
	value, ok := data["coinTotalSupply"].(string)
	if !ok {
		return nil, fmt.Errorf("Coin NFT Tape coinTotalSupply is not a string")
	}
	supply, ok := new(big.Int).SetString(value, 10)
	if !ok || supply.Sign() < 0 {
		return nil, fmt.Errorf("Coin NFT Tape coinTotalSupply=%q is invalid", value)
	}
	return supply, nil
}

func validateCoinNFTOutputs(tx *bt.Tx, wantSupply int64) error {
	if tx == nil || len(tx.Outputs) < 3 {
		return fmt.Errorf("Coin NFT transaction requires Code/Hold/Tape outputs")
	}
	if tx.Outputs[0].Satoshis != 200 {
		return fmt.Errorf("Coin NFT Code satoshis=%d want=200", tx.Outputs[0].Satoshis)
	}
	if tx.Outputs[1].Satoshis != 100 {
		return fmt.Errorf("Coin NFT Hold satoshis=%d want=100", tx.Outputs[1].Satoshis)
	}
	if tx.Outputs[2].Satoshis != 0 || !tx.Outputs[2].LockingScript.IsSafeDataOut() {
		return fmt.Errorf("Coin NFT Tape is not zero-satoshi safe data")
	}
	supply, err := coinNFTTotalSupply(tx)
	if err != nil {
		return err
	}
	if supply.Cmp(big.NewInt(wantSupply)) != 0 {
		return fmt.Errorf("Coin NFT supply=%s want=%d", supply, wantSupply)
	}
	return nil
}

func validateCoinOutput(
	tx *bt.Tx,
	codeVout int,
	wantAmount int64,
	wantLockTime uint32,
) error {
	if tx == nil || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return fmt.Errorf("Coin Code/Tape outputs %d/%d are out of range", codeVout, codeVout+1)
	}
	code := tx.Outputs[codeVout]
	tape := tx.Outputs[codeVout+1]
	if code.Satoshis != 500 {
		return fmt.Errorf("Coin Code output %d satoshis=%d want=500", codeVout, code.Satoshis)
	}
	if tape.Satoshis != 0 || !tape.LockingScript.IsSafeDataOut() {
		return fmt.Errorf("Coin Tape output %d is not zero-satoshi safe data", codeVout+1)
	}
	info, err := contractutil.ClassifyFTScript(code.LockingScript)
	if err != nil {
		return fmt.Errorf("classify Coin output %d: %w", codeVout, err)
	}
	if info.Version != contractutil.FTVersion4 || !info.IsCoin {
		return fmt.Errorf(
			"Coin output %d classified version=%d coin=%t",
			codeVout,
			info.Version,
			info.IsCoin,
		)
	}
	if len(code.LockingScript.Bytes()) != contractutil.FTV4CodeLength {
		return fmt.Errorf(
			"Coin output %d code length=%d want=%d",
			codeVout,
			len(code.LockingScript.Bytes()),
			contractutil.FTV4CodeLength,
		)
	}
	amount, err := contractutil.GetFtBalanceFromTape(tape.LockingScript.ToHex())
	if err != nil {
		return fmt.Errorf("decode Coin Tape output %d: %w", codeVout+1, err)
	}
	if amount.Cmp(big.NewInt(wantAmount)) != 0 {
		return fmt.Errorf(
			"Coin output %d amount=%s want=%d",
			codeVout,
			amount,
			wantAmount,
		)
	}
	lockTime, err := contract.GetLockTimeFromTape(tape.LockingScript)
	if err != nil {
		return fmt.Errorf("decode Coin lockTime output %d: %w", codeVout+1, err)
	}
	if lockTime != wantLockTime {
		return fmt.Errorf(
			"Coin output %d lockTime=%d want=%d",
			codeVout,
			lockTime,
			wantLockTime,
		)
	}
	return nil
}

func validateStableCoinLifecycleTransaction(
	label string,
	tx *bt.Tx,
	lockTime uint32,
) error {
	switch label {
	case "stablecoin-coin-nft-create":
		return validateCoinNFTOutputs(tx, 0)
	case "stablecoin-initial-mint":
		if err := validateCoinNFTOutputs(tx, 100_000); err != nil {
			return err
		}
		return validateCoinOutput(tx, 3, 100_000, 0)
	case "stablecoin-admin-mint":
		if err := validateCoinNFTOutputs(tx, 150_000); err != nil {
			return err
		}
		return validateCoinOutput(tx, 3, 50_000, 0)
	case "stablecoin-owner-transfer":
		if err := validateCoinOutput(tx, 0, 20_000, 0); err != nil {
			return err
		}
		return validateCoinOutput(tx, 2, 80_000, 0)
	case "stablecoin-batch-transfer":
		for codeVout, amount := range map[int]int64{
			0: 5_000,
			2: 7_000,
			4: 68_000,
		} {
			if err := validateCoinOutput(tx, codeVout, amount, 0); err != nil {
				return err
			}
		}
		return nil
	case "stablecoin-merge":
		return validateCoinOutput(tx, 0, 138_000, 0)
	case "stablecoin-freeze":
		return validateCoinOutput(tx, 0, 138_000, lockTime)
	case "stablecoin-unfreeze":
		return validateCoinOutput(tx, 0, 138_000, 0)
	default:
		return fmt.Errorf("unknown StableCoin lifecycle label %q", label)
	}
}

func buildStableCoinLifecyclePlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
	fundingParent *bt.Tx,
	aggregateKey []byte,
	finalize adminPreparedFinalizer,
	lockTime uint32,
) (*stableCoinLifecyclePlan, error) {
	if privateKey == nil {
		return nil, fmt.Errorf("StableCoin private key is required")
	}
	if funding == nil || fundingParent == nil {
		return nil, fmt.Errorf("StableCoin funding and parent transaction are required")
	}
	if len(aggregateKey) != 32 {
		return nil, fmt.Errorf("StableCoin aggregate key must be 32 bytes")
	}
	if finalize == nil {
		return nil, fmt.Errorf("StableCoin admin finalizer is required")
	}
	if lockTime < 500_000_000 {
		return nil, fmt.Errorf("StableCoin freeze lockTime must be a Unix timestamp")
	}

	stableCoin, err := contract.NewStableCoin(&contract.FtParams{
		Name: "GoFullMatrixStableCoin", Symbol: "GSC", Amount: 1_000, Decimal: 2,
	})
	if err != nil {
		return nil, err
	}
	create, err := stableCoin.PrepareCreateCoin(
		aggregateKey,
		privateKey,
		address,
		funding,
		fundingParent,
		"go-js-1.6.6 initial StableCoin mint",
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin create prepare: %w", err)
	}
	createRaws, err := finalize(create)
	if err != nil {
		return nil, fmt.Errorf("StableCoin create finalize: %w", err)
	}
	if len(createRaws) != 2 {
		return nil, fmt.Errorf("StableCoin create returned %d transactions, want=2", len(createRaws))
	}
	coinNFT, err := parsePlannedTransaction("stablecoin-coin-nft-create", createRaws[0])
	if err != nil {
		return nil, err
	}
	initialMint, err := parsePlannedTransaction("stablecoin-initial-mint", createRaws[1])
	if err != nil {
		return nil, err
	}
	if err := validateStableCoinLifecycleTransaction(
		"stablecoin-coin-nft-create", coinNFT, lockTime,
	); err != nil {
		return nil, err
	}
	if err := validateStableCoinLifecycleTransaction(
		"stablecoin-initial-mint", initialMint, lockTime,
	); err != nil {
		return nil, err
	}
	initialSupply, err := coinNFTTotalSupply(initialMint)
	if err != nil {
		return nil, err
	}
	stableCoin.TotalSupply = initialSupply
	stableCoin.ContractTxid = initialMint.TxID()

	adminFee, err := changeUTXO(initialMint)
	if err != nil {
		return nil, err
	}
	adminMintPrepared, err := stableCoin.PrepareMintCoin(
		aggregateKey,
		privateKey,
		address,
		big.NewInt(50_000),
		adminFee,
		initialMint,
		coinNFT,
		"go-js-1.6.6 additional StableCoin mint",
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin admin mint prepare: %w", err)
	}
	adminMintRaws, err := finalize(adminMintPrepared)
	if err != nil {
		return nil, fmt.Errorf("StableCoin admin mint finalize: %w", err)
	}
	if len(adminMintRaws) != 1 {
		return nil, fmt.Errorf(
			"StableCoin admin mint returned %d transactions, want=1",
			len(adminMintRaws),
		)
	}
	adminMint, err := parsePlannedTransaction("stablecoin-admin-mint", adminMintRaws[0])
	if err != nil {
		return nil, err
	}
	if err := validateStableCoinLifecycleTransaction(
		"stablecoin-admin-mint", adminMint, lockTime,
	); err != nil {
		return nil, err
	}
	totalSupply, err := coinNFTTotalSupply(adminMint)
	if err != nil {
		return nil, err
	}
	stableCoin.TotalSupply = totalSupply

	initialCoin, err := stableCoinInput(initialMint, 3)
	if err != nil {
		return nil, err
	}
	transferFee, err := changeUTXO(adminMint)
	if err != nil {
		return nil, err
	}
	initialPrePre, err := localPrePre(coinNFT, 0)
	if err != nil {
		return nil, fmt.Errorf("StableCoin initial mint ancestry: %w", err)
	}
	transferRaw, err := stableCoin.Transfer(
		privateKey,
		address,
		big.NewInt(20_000),
		[]*contractutil.FtUTXO{initialCoin},
		transferFee,
		[]*bt.Tx{initialMint},
		[]string{initialPrePre},
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin owner transfer build: %w", err)
	}
	transfer, err := parsePlannedTransaction("stablecoin-owner-transfer", transferRaw)
	if err != nil {
		return nil, err
	}

	batchCoin, err := stableCoinInput(transfer, 2)
	if err != nil {
		return nil, err
	}
	batchFee, err := changeUTXO(transfer)
	if err != nil {
		return nil, err
	}
	transferPrePre, err := localPrePre(initialMint, 3)
	if err != nil {
		return nil, fmt.Errorf("StableCoin transfer ancestry: %w", err)
	}
	recipientA, err := temporaryTestnetAddress()
	if err != nil {
		return nil, err
	}
	recipientB, err := temporaryTestnetAddress()
	if err != nil {
		return nil, err
	}
	batchRaws, err := stableCoin.BatchTransfer(
		privateKey,
		[]contract.AddressAmount{
			{Address: recipientA, Amount: big.NewInt(5_000)},
			{Address: recipientB, Amount: big.NewInt(7_000)},
		},
		[]*contractutil.FtUTXO{batchCoin},
		batchFee,
		[]*bt.Tx{transfer},
		[]string{transferPrePre},
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin batch transfer build: %w", err)
	}
	if len(batchRaws) != 1 {
		return nil, fmt.Errorf(
			"StableCoin batch returned %d transactions, want=1",
			len(batchRaws),
		)
	}
	batch, err := parsePlannedTransaction("stablecoin-batch-transfer", batchRaws[0])
	if err != nil {
		return nil, err
	}

	mergeTransferCoin, err := stableCoinInput(transfer, 0)
	if err != nil {
		return nil, err
	}
	mergeBatchCoin, err := stableCoinInput(batch, 4)
	if err != nil {
		return nil, err
	}
	mergeAdminCoin, err := stableCoinInput(adminMint, 3)
	if err != nil {
		return nil, err
	}
	mergeFee, err := changeUTXO(batch)
	if err != nil {
		return nil, err
	}
	mergeTransferPrePre, err := localPrePre(initialMint, 3)
	if err != nil {
		return nil, err
	}
	mergeBatchPrePre, err := localPrePre(transfer, 2)
	if err != nil {
		return nil, err
	}
	mergeAdminPrePre, err := localPrePre(initialMint, 0)
	if err != nil {
		return nil, err
	}
	mergeRaws, err := stableCoin.MergeCoin(
		privateKey,
		[]*contractutil.FtUTXO{
			mergeTransferCoin,
			mergeBatchCoin,
			mergeAdminCoin,
		},
		mergeFee,
		[]*bt.Tx{transfer, batch, adminMint},
		[]string{
			mergeTransferPrePre,
			mergeBatchPrePre,
			mergeAdminPrePre,
		},
		[]*bt.Tx{coinNFT, initialMint, adminMint, transfer, batch},
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin merge build: %w", err)
	}
	if len(mergeRaws) != 1 {
		return nil, fmt.Errorf("StableCoin merge returned %d transactions, want=1", len(mergeRaws))
	}
	merge, err := parsePlannedTransaction("stablecoin-merge", mergeRaws[0])
	if err != nil {
		return nil, err
	}

	mergedCoin, err := stableCoinInput(merge, 0)
	if err != nil {
		return nil, err
	}
	freezeFee, err := changeUTXO(merge)
	if err != nil {
		return nil, err
	}
	mergePrePre, err := contractutil.BuildFtPrePreTxData(
		merge,
		0,
		[]*bt.Tx{transfer, batch, adminMint},
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin merge ancestry: %w", err)
	}
	freezePrepared, err := stableCoin.PrepareFreezeCoinUTXO(
		aggregateKey,
		privateKey,
		lockTime,
		[]*contractutil.FtUTXO{mergedCoin},
		freezeFee,
		[]*bt.Tx{merge},
		[]string{mergePrePre},
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin freeze prepare: %w", err)
	}
	freezeRaws, err := finalize(freezePrepared)
	if err != nil {
		return nil, fmt.Errorf("StableCoin freeze finalize: %w", err)
	}
	if len(freezeRaws) != 1 {
		return nil, fmt.Errorf("StableCoin freeze returned %d transactions, want=1", len(freezeRaws))
	}
	freeze, err := parsePlannedTransaction("stablecoin-freeze", freezeRaws[0])
	if err != nil {
		return nil, err
	}

	frozenCoin, err := stableCoinInput(freeze, 0)
	if err != nil {
		return nil, err
	}
	unfreezeFee, err := changeUTXO(freeze)
	if err != nil {
		return nil, err
	}
	unfreezePrePre, err := localPrePre(merge, 0)
	if err != nil {
		return nil, fmt.Errorf("StableCoin freeze ancestry: %w", err)
	}
	unfreezePrepared, err := stableCoin.PrepareUnfreezeCoinUTXO(
		aggregateKey,
		privateKey,
		[]*contractutil.FtUTXO{frozenCoin},
		unfreezeFee,
		[]*bt.Tx{freeze},
		[]string{unfreezePrePre},
	)
	if err != nil {
		return nil, fmt.Errorf("StableCoin unfreeze prepare: %w", err)
	}
	unfreezeRaws, err := finalize(unfreezePrepared)
	if err != nil {
		return nil, fmt.Errorf("StableCoin unfreeze finalize: %w", err)
	}
	if len(unfreezeRaws) != 1 {
		return nil, fmt.Errorf(
			"StableCoin unfreeze returned %d transactions, want=1",
			len(unfreezeRaws),
		)
	}
	unfreeze, err := parsePlannedTransaction("stablecoin-unfreeze", unfreezeRaws[0])
	if err != nil {
		return nil, err
	}

	transactions := []plannedTransaction{
		{Label: "stablecoin-coin-nft-create", Raw: createRaws[0]},
		{Label: "stablecoin-initial-mint", Raw: createRaws[1]},
		{Label: "stablecoin-admin-mint", Raw: adminMintRaws[0]},
		{Label: "stablecoin-owner-transfer", Raw: transferRaw},
		{Label: "stablecoin-batch-transfer", Raw: batchRaws[0]},
		{Label: "stablecoin-merge", Raw: mergeRaws[0]},
		{Label: "stablecoin-freeze", Raw: freezeRaws[0]},
		{Label: "stablecoin-unfreeze", Raw: unfreezeRaws[0]},
	}
	for _, item := range transactions {
		tx, err := parsePlannedTransaction(item.Label, item.Raw)
		if err != nil {
			return nil, err
		}
		if err := validateStableCoinLifecycleTransaction(item.Label, tx, lockTime); err != nil {
			return nil, fmt.Errorf("%s: %w", item.Label, err)
		}
	}

	return &stableCoinLifecyclePlan{
		Transactions: transactions,
		StableCoin:   stableCoin,
		CoinNFT:      coinNFT,
		InitialMint:  initialMint,
		AdminMint:    adminMint,
		Transfer:     transfer,
		Batch:        batch,
		Merge:        merge,
		Freeze:       freeze,
		Unfreeze:     unfreeze,
		LockTime:     lockTime,
	}, nil
}

func executeStableCoinLifecyclePlan(
	plan *stableCoinLifecyclePlan,
	accept plannedTransactionAcceptor,
) (publicState, error) {
	if plan == nil || plan.StableCoin == nil {
		return publicState{}, fmt.Errorf("nil StableCoin lifecycle plan")
	}
	if accept == nil {
		return publicState{}, fmt.Errorf("nil StableCoin transaction acceptor")
	}
	state := publicState{
		TokenID: plan.StableCoin.ContractTxid,
		CoinID:  plan.CoinNFT.TxID(),
	}
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, err := accept(item, func(tx *bt.Tx) error {
			return validateStableCoinLifecycleTransaction(label, tx, plan.LockTime)
		})
		if err != nil {
			return publicState{}, fmt.Errorf("%s: %w", label, err)
		}
		if accepted == nil {
			return publicState{}, fmt.Errorf("%s: acceptor returned nil transaction", label)
		}
		state.LastTxID = accepted.TxID()
		state.LastVout = 0
	}
	return state, nil
}

func verifyStableCoinIndexedStateOnce(
	plan *stableCoinLifecyclePlan,
	address string,
	network string,
) error {
	if plan == nil || plan.StableCoin == nil || plan.Unfreeze == nil {
		return fmt.Errorf("StableCoin indexed-state verification requires a complete plan")
	}
	stableCoinID := plan.StableCoin.ContractTxid
	info, err := api.FetchCoinInfo(stableCoinID, network)
	if err != nil {
		return fmt.Errorf("FetchCoinInfo: %w", err)
	}
	if info.TotalSupply == nil || info.TotalSupply.Cmp(big.NewInt(150_000)) != 0 {
		return fmt.Errorf("indexed StableCoin supply=%v want=150000", info.TotalSupply)
	}
	if info.Name != "GoFullMatrixStableCoin" ||
		info.Symbol != "GSC" ||
		info.Decimal != 2 {
		return fmt.Errorf(
			"indexed StableCoin metadata=%q/%q/%d",
			info.Name,
			info.Symbol,
			info.Decimal,
		)
	}

	balance, err := api.GetCoinBalance(stableCoinID, address, network)
	if err != nil {
		return fmt.Errorf("GetCoinBalance: %w", err)
	}
	if balance.Cmp(big.NewInt(138_000)) != 0 {
		return fmt.Errorf("indexed StableCoin balance=%s want=138000", balance)
	}

	utxos, err := api.FetchCoinUTXOList(
		stableCoinID,
		address,
		info.CodeScript,
		network,
	)
	if err != nil {
		return fmt.Errorf("FetchCoinUTXOList: %w", err)
	}
	for _, utxo := range utxos {
		if utxo == nil ||
			utxo.Vout != 0 ||
			utxo.FtBalance == nil ||
			utxo.FtBalance.Cmp(big.NewInt(138_000)) != 0 {
			continue
		}
		if hex.EncodeToString(utxo.TxID) != plan.Unfreeze.TxID() {
			continue
		}
		return nil
	}
	return fmt.Errorf(
		"indexed StableCoin UTXO %s:0 amount=138000 not found",
		plan.Unfreeze.TxID(),
	)
}

func verifyStableCoinIndexedState(
	plan *stableCoinLifecyclePlan,
	address string,
	network string,
) error {
	const attempts = 12
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = verifyStableCoinIndexedStateOnce(plan, address, network)
		if lastErr == nil {
			return nil
		}
		if attempt < attempts {
			time.Sleep(2 * time.Second)
		}
	}
	return fmt.Errorf(
		"StableCoin indexer did not converge after %d attempts: %w",
		attempts,
		lastErr,
	)
}

func runStableCoinLifecycle(cfg config, decoded *wif.WIF, address string) error {
	aggregateKey, err := loadAggregateAdminKey()
	if err != nil {
		return err
	}
	funding, err := api.FetchUTXO(
		address,
		float64(stableCoinStageFundingMinimumSatoshis)/1_000_000,
		cfg.Network,
	)
	if err != nil {
		return err
	}
	fundingParent, err := api.FetchTXRaw(hex.EncodeToString(funding.TxID), cfg.Network)
	if err != nil {
		return err
	}
	lockTime := uint32(time.Now().Unix() - 60)
	plan, err := buildStableCoinLifecyclePlan(
		decoded.PrivKey,
		address,
		funding,
		fundingParent,
		aggregateKey,
		finalizeAdminPrepared,
		lockTime,
	)
	if err != nil {
		return err
	}
	state, err := executeStableCoinLifecyclePlan(
		plan,
		func(item plannedTransaction, validate func(*bt.Tx) error) (*bt.Tx, error) {
			accepted, _, err := broadcastAndVerify(
				item.Label,
				item.Raw,
				cfg.Network,
				"stablecoin-js-1.6.6-layout-supply-locktime",
				validate,
			)
			return accepted, err
		},
	)
	if err != nil {
		return err
	}
	if err := verifyStableCoinIndexedState(plan, address, cfg.Network); err != nil {
		return err
	}
	return writePublicState(os.Stdout, state)
}
