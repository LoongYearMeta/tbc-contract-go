package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sort"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

type config struct {
	Network   string
	Broadcast bool
	WIF       string
	Stage     stageName
	TokenA    string
	TokenB    string
	PoolID    string
}

func loadConfig(getenv func(string) string) (config, error) {
	network := getenv("TBC_TESTNET_NETWORK")
	if network == "" {
		network = "testnet"
	}
	stage, err := parseStage(getenv("TBC_TESTNET_STAGE"))
	if err != nil {
		return config{}, err
	}
	cfg := config{
		Network:   network,
		Broadcast: getenv("TBC_TESTNET_BROADCAST") == "1",
		WIF:       getenv("TBC_TESTNET_WIF"),
		Stage:     stage,
		TokenA:    getenv("TBC_TESTNET_TOKEN_A"),
		TokenB:    getenv("TBC_TESTNET_TOKEN_B"),
		PoolID:    getenv("TBC_TESTNET_POOL_ID"),
	}
	if cfg.Network != "testnet" {
		return config{}, fmt.Errorf("testnet-parity refuses non-testnet network")
	}
	if cfg.WIF == "" {
		return config{}, fmt.Errorf("TBC_TESTNET_WIF is required")
	}
	return cfg, nil
}

func dryRunFundingTransaction(address string, utxos []*bt.UTXO, decoded *wif.WIF) error {
	if len(utxos) == 0 {
		return fmt.Errorf("no spendable testnet UTXOs")
	}
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(utxos[0]); err != nil {
		return err
	}
	if err := tx.ChangeToAddress(address, bt.NewFeeQuote()); err != nil {
		return err
	}
	if err := tx.FillAllInputs(context.Background(), &unlocker.Getter{PrivateKey: decoded.PrivKey}); err != nil {
		return err
	}
	parsed, err := bt.NewTxFromString(tx.String())
	if err != nil {
		return err
	}
	if len(parsed.Inputs) != 1 || len(parsed.Outputs) != 1 {
		return fmt.Errorf("dry-run funding transaction shape mismatch")
	}
	return nil
}

func run(cfg config) error {
	decoded, err := wif.DecodeWIF(cfg.WIF)
	if err != nil {
		return fmt.Errorf("decode testnet key: %w", err)
	}
	address, err := bscript.NewAddressFromPublicKey(decoded.PrivKey.PubKey(), false)
	if err != nil {
		return err
	}
	balance, err := api.GetTBCBalance(address.AddressString, cfg.Network)
	if err != nil {
		return fmt.Errorf("read-only balance check: %w", err)
	}
	utxos, err := api.FetchUTXOs(address.AddressString, cfg.Network)
	if err != nil {
		return fmt.Errorf("read-only UTXO check: %w", err)
	}
	fmt.Printf("testnet address=%s balance_satoshis=%d spendable_utxos=%d\n",
		address.AddressString, balance, len(utxos))
	sort.Slice(utxos, func(i, j int) bool { return utxos[i].Satoshis > utxos[j].Satoshis })
	if err := dryRunFundingTransaction(address.AddressString, utxos, decoded); err != nil {
		return err
	}
	if !cfg.Broadcast {
		if len(utxos) > 0 {
			token, tokenErr := contract.NewFT(&contract.FtParams{
				Name: "GoParityDry", Symbol: "GPD", Amount: 1_000_000, Decimal: 2,
			})
			if tokenErr != nil {
				return tokenErr
			}
			raws, tokenErr := token.MintFT(decoded.PrivKey, address.AddressString, utxos[0])
			if tokenErr != nil {
				return tokenErr
			}
			source, tokenErr := bt.NewTxFromString(raws[0])
			if tokenErr != nil {
				return tokenErr
			}
			for i, output := range source.Outputs {
				fmt.Printf("dry-run mint-source output=%d satoshis=%d script_bytes=%d safe_data=%t\n",
					i, output.Satoshis, output.LockingScript.Len(), output.LockingScript.IsSafeDataOut())
			}
		}
		fmt.Println("dry-run pass; broadcast disabled")
		return nil
	}
	if cfg.Stage == "htlc" {
		if cfg.TokenA == "" {
			return fmt.Errorf("TBC_TESTNET_TOKEN_A is required for htlc stage")
		}
		return runFTAndHTLC(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stageFT {
		return runFTStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stageNFT {
		return runNFTStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stageMultiSig {
		return runMultiSigStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stageBaseHTLC {
		return runBaseHTLCStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stagePiggyBank {
		return runPiggyBankStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stagePoolCreate {
		return runPoolCreateStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stagePoolTrade {
		return runPoolTradeStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == stagePoolLock {
		return runPoolLockStage(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == "core-contracts" {
		return runCoreContracts(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == "stablecoin" {
		return runStableCoinLifecycle(cfg, decoded, address.AddressString)
	}
	if cfg.Stage == "pool-foundation" {
		if cfg.TokenA == "" {
			return fmt.Errorf("TBC_TESTNET_TOKEN_A is required for pool-foundation stage")
		}
		return runPoolFoundation(cfg, decoded)
	}
	if cfg.Stage == "pool-init" || cfg.Stage == "pool-consume" {
		if cfg.PoolID == "" {
			return fmt.Errorf("TBC_TESTNET_POOL_ID is required for %s stage", cfg.Stage)
		}
		if cfg.Stage == "pool-consume" {
			return runPoolConsume(cfg, decoded, address.AddressString)
		}
		return runPoolInit(cfg, decoded, address.AddressString)
	}
	if cfg.Stage != "" && cfg.Stage != "foundation" {
		return fmt.Errorf("unknown testnet stage %q", cfg.Stage)
	}
	if len(utxos) == 0 {
		return fmt.Errorf("at least one funding UTXO is required")
	}
	tokenA, err := contract.NewFT(&contract.FtParams{
		Name: "GoParityA", Symbol: "GPA", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		return err
	}
	tokenB, err := contract.NewFT(&contract.FtParams{
		Name: "GoParityB", Symbol: "GPB", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		return err
	}
	sourceA, _, err := mintAndBroadcast("token-a", tokenA, decoded, address.AddressString, utxos[0], cfg.Network)
	if err != nil {
		return err
	}
	sourceAID, err := hex.DecodeString(sourceA.TxID())
	if err != nil {
		return err
	}
	tokenBFunding := &bt.UTXO{
		TxID: sourceAID, Vout: 2,
		LockingScript: sourceA.Outputs[2].LockingScript,
		Satoshis:      sourceA.Outputs[2].Satoshis,
	}
	if _, _, err := mintAndBroadcast("token-b", tokenB, decoded, address.AddressString, tokenBFunding, cfg.Network); err != nil {
		return err
	}
	fmt.Printf("foundation pass token_a_contract=%s token_b_contract=%s\n",
		tokenA.ContractTxid, tokenB.ContractTxid)
	return nil
}

func runPoolFoundation(cfg config, decoded *wif.WIF) error {
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{Network: cfg.Network})
	if err := pool.InitCreate(cfg.TokenA); err != nil {
		return err
	}
	address, err := bscript.NewAddressFromPublicKey(decoded.PrivKey.PubKey(), false)
	if err != nil {
		return err
	}
	feeUTXO, err := api.FetchUTXO(address.AddressString, 0.01, cfg.Network)
	if err != nil {
		return err
	}
	raws, err := pool.CreatePoolNFT(decoded.PrivKey, feeUTXO, "goparity", 35, 2, false)
	if err != nil {
		return err
	}
	if len(raws) != 2 {
		return fmt.Errorf("pool foundation transaction count %d, want 2", len(raws))
	}
	if _, _, err := broadcastOne("pool-v2-source", raws[0], cfg.Network); err != nil {
		return err
	}
	if _, _, err := broadcastOne("pool-v2-mint", raws[1], cfg.Network); err != nil {
		return err
	}
	fmt.Println("pool-v2 foundation pass")
	return nil
}

func runPoolInit(cfg config, decoded *wif.WIF, address string) error {
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{
		ContractTxID: cfg.PoolID,
		Network:      cfg.Network,
	})
	if err := pool.InitFromContractID(); err != nil {
		return err
	}
	feeUTXO, err := api.FetchUTXO(address, 0.02, cfg.Network)
	if err != nil {
		return err
	}
	raw, err := pool.InitPoolNFT(decoded.PrivKey, address, feeUTXO, "0.01", "100", 0)
	if err != nil {
		return err
	}
	if _, _, err := broadcastOne("pool-v2-init-ftlp", raw, cfg.Network); err != nil {
		return err
	}
	fmt.Println("pool-v2 FTLP init pass")
	return nil
}

func runPoolConsume(cfg config, decoded *wif.WIF, address string) error {
	pool := contract.NewPoolNFT2(&contract.PoolNFT2Config{
		ContractTxID: cfg.PoolID,
		Network:      cfg.Network,
	})
	if err := pool.InitFromContractID(); err != nil {
		return err
	}
	feeUTXO, err := api.FetchUTXO(address, 0.01, cfg.Network)
	if err != nil {
		return err
	}
	raws, err := pool.ConsumeLP(decoded.PrivKey, address, feeUTXO, "0.001", nil)
	if err != nil {
		return err
	}
	for i, raw := range raws {
		if _, _, err := broadcastOne(
			fmt.Sprintf("pool-v2-consume-ftlp-%d", i), raw, cfg.Network,
		); err != nil {
			return err
		}
	}
	fmt.Println("pool-v2 FTLP consume pass")
	return nil
}

func utxoFromTX(tx *bt.Tx, vout int) (*bt.UTXO, error) {
	if tx == nil || vout < 0 || vout >= len(tx.Outputs) {
		return nil, fmt.Errorf("output %d is out of range", vout)
	}
	txid, err := hex.DecodeString(tx.TxID())
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID: txid, Vout: uint32(vout),
		LockingScript: tx.Outputs[vout].LockingScript,
		Satoshis:      tx.Outputs[vout].Satoshis,
	}, nil
}

func ftUTXOFromTX(tx *bt.Tx, codeVout int) (*contractutil.FtUTXO, error) {
	base, err := utxoFromTX(tx, codeVout)
	if err != nil {
		return nil, err
	}
	if codeVout+1 >= len(tx.Outputs) {
		return nil, fmt.Errorf("FT tape output is missing")
	}
	balance, err := contractutil.GetFtBalanceFromTape(tx.Outputs[codeVout+1].LockingScript.ToHex())
	if err != nil {
		return nil, err
	}
	return &contractutil.FtUTXO{
		TxID: base.TxID, Vout: base.Vout,
		LockingScript: base.LockingScript,
		Satoshis:      base.Satoshis,
		FtBalance:     balance,
	}, nil
}

func localPrePre(parent *bt.Tx, vout int) (string, error) {
	data, err := contractutil.GetPrePreTxdata(parent, vout)
	if err != nil {
		return "", err
	}
	return "57" + data, nil
}

func broadcastOne(label, raw, network string) (*bt.Tx, string, error) {
	tx, evidence, err := broadcastAndVerify(
		label, raw, network, "txid-raw-fee", nil,
	)
	if err != nil {
		return nil, "", err
	}
	return tx, evidence.TxID, nil
}

func runFTAndHTLC(cfg config, decoded *wif.WIF, address string) error {
	info, err := api.FetchFtInfo(cfg.TokenA, cfg.Network)
	if err != nil {
		return fmt.Errorf("fetch Token A info: %w", err)
	}
	ownedCode, err := contract.BuildFTtransferCode(info.CodeScript, address)
	if err != nil {
		return err
	}
	ftUTXO, err := api.FetchFtUTXO(
		cfg.TokenA, address, ownedCode.ToHex(), cfg.Network, big.NewInt(10_000_000),
	)
	if err != nil {
		return fmt.Errorf("fetch Token A UTXO: %w", err)
	}
	ftPreTX, err := api.FetchTXRaw(hex.EncodeToString(ftUTXO.TxID), cfg.Network)
	if err != nil {
		return err
	}
	ftPrePre, err := api.FetchFtPrePreTxData(ftPreTX, int(ftUTXO.Vout), cfg.Network)
	if err != nil {
		return err
	}
	feeUTXO, err := api.FetchUTXO(address, 0.01, cfg.Network)
	if err != nil {
		return err
	}
	token, err := contract.NewFT(cfg.TokenA)
	if err != nil {
		return err
	}
	token.Initialize(&contract.FtInfo{
		ContractTxid: cfg.TokenA,
		Name:         info.Name, Symbol: info.Symbol, Decimal: info.Decimal,
		TotalSupply: info.TotalSupply, CodeScript: info.CodeScript, TapeScript: info.TapeScript,
	})
	selfRaw, err := token.TransferWithAdditionalInfo(
		decoded.PrivKey, address, big.NewInt(10_000_000),
		[]*contractutil.FtUTXO{ftUTXO}, feeUTXO,
		[]*bt.Tx{ftPreTX}, []string{ftPrePre}, []byte("go-js-1.6.5-parity"),
	)
	if err != nil {
		return fmt.Errorf("FT v3 self-transfer build: %w", err)
	}
	selfTX, _, err := broadcastOne("ft-v3-self-transfer", selfRaw, cfg.Network)
	if err != nil {
		return err
	}

	htlcFT, err := ftUTXOFromTX(selfTX, 0)
	if err != nil {
		return err
	}
	htlcFee, err := utxoFromTX(selfTX, len(selfTX.Outputs)-1)
	if err != nil {
		return err
	}
	deployPrePre, err := localPrePre(ftPreTX, int(ftUTXO.Vout))
	if err != nil {
		return err
	}
	secretBytes := make([]byte, 32)
	if _, err := cryptorand.Read(secretBytes); err != nil {
		return fmt.Errorf("generate HTLC secret: %w", err)
	}
	secret := hex.EncodeToString(secretBytes)
	hashlock := hex.EncodeToString(crypto.Sha256(secretBytes))
	height, err := api.FetchTBCLockTime(cfg.Network)
	if err != nil {
		return err
	}
	timelock := height
	if timelock > 0 {
		timelock--
	}
	deployRaw, err := contract.DeployHTLCTokenWithSign(
		address, address, hashlock, timelock, big.NewInt(4_000_000),
		[]*contractutil.FtUTXO{htlcFT}, htlcFee,
		[]*bt.Tx{selfTX}, []string{deployPrePre}, decoded.PrivKey,
	)
	if err != nil {
		return fmt.Errorf("HTLC withdraw deploy build: %w", err)
	}
	deployTX, _, err := broadcastOne("token-htlc-withdraw-deploy", deployRaw, cfg.Network)
	if err != nil {
		return err
	}
	htlcContract, err := utxoFromTX(deployTX, 0)
	if err != nil {
		return err
	}
	lockedFT, err := ftUTXOFromTX(deployTX, 1)
	if err != nil {
		return err
	}
	withdrawFee, err := utxoFromTX(deployTX, len(deployTX.Outputs)-1)
	if err != nil {
		return err
	}
	withdrawPrePre, err := localPrePre(selfTX, 0)
	if err != nil {
		return err
	}
	withdrawRaw, err := contract.WithdrawHTLCTokenWithSign(
		decoded.PrivKey, address, htlcContract, lockedFT,
		deployTX, withdrawPrePre, withdrawFee, secret,
	)
	if err != nil {
		return fmt.Errorf("HTLC withdraw build: %w", err)
	}
	withdrawTX, _, err := broadcastOne("token-htlc-withdraw", withdrawRaw, cfg.Network)
	if err != nil {
		return err
	}

	refundInput, err := ftUTXOFromTX(deployTX, 3)
	if err != nil {
		return err
	}
	refundDeployFee, err := utxoFromTX(withdrawTX, len(withdrawTX.Outputs)-1)
	if err != nil {
		return err
	}
	refundDeployPrePre, err := localPrePre(selfTX, 0)
	if err != nil {
		return err
	}
	refundDeployRaw, err := contract.DeployHTLCTokenWithSign(
		address, address, hashlock, timelock, big.NewInt(2_000_000),
		[]*contractutil.FtUTXO{refundInput}, refundDeployFee,
		[]*bt.Tx{deployTX}, []string{refundDeployPrePre}, decoded.PrivKey,
	)
	if err != nil {
		return fmt.Errorf("HTLC refund deploy build: %w", err)
	}
	refundDeployTX, _, err := broadcastOne("token-htlc-refund-deploy", refundDeployRaw, cfg.Network)
	if err != nil {
		return err
	}
	refundContract, err := utxoFromTX(refundDeployTX, 0)
	if err != nil {
		return err
	}
	refundFT, err := ftUTXOFromTX(refundDeployTX, 1)
	if err != nil {
		return err
	}
	refundFee, err := utxoFromTX(refundDeployTX, len(refundDeployTX.Outputs)-1)
	if err != nil {
		return err
	}
	refundPrePre, err := localPrePre(deployTX, 3)
	if err != nil {
		return err
	}
	refundRaw, err := contract.RefundHTLCTokenWithSign(
		decoded.PrivKey, address, refundContract, refundFT,
		refundDeployTX, refundPrePre, refundFee, timelock,
	)
	if err != nil {
		return fmt.Errorf("HTLC refund build: %w", err)
	}
	if _, _, err := broadcastOne("token-htlc-refund", refundRaw, cfg.Network); err != nil {
		return err
	}
	fmt.Println("ft-v3 and token-htlc scenarios pass")
	return nil
}

func mintAndBroadcast(
	label string,
	token *contract.FT,
	decoded *wif.WIF,
	address string,
	funding *bt.UTXO,
	network string,
) (*bt.Tx, *bt.Tx, error) {
	raws, err := token.MintFT(decoded.PrivKey, address, funding)
	if err != nil {
		return nil, nil, fmt.Errorf("%s build: %w", label, err)
	}
	if len(raws) != 2 {
		return nil, nil, fmt.Errorf("%s build returned %d transactions", label, len(raws))
	}
	mintTX, err := bt.NewTxFromString(raws[1])
	if err != nil {
		return nil, nil, fmt.Errorf("%s parse mint: %w", label, err)
	}
	if len(mintTX.Outputs) < 2 {
		return nil, nil, fmt.Errorf("%s mint has %d outputs, want FT code and tape", label, len(mintTX.Outputs))
	}
	if got := mintTX.Outputs[0].LockingScript.Len(); got != 1884 {
		return nil, nil, fmt.Errorf("%s mint code length %d, want released FT v3 length 1884", label, got)
	}
	fmt.Printf("%s mint_code_bytes=%d\n", label, mintTX.Outputs[0].LockingScript.Len())
	refetchedSource, _, err := broadcastOne(label+"-source", raws[0], network)
	if err != nil {
		return nil, nil, err
	}
	refetchedMint, mintID, err := broadcastOne(label+"-mint", raws[1], network)
	if err != nil {
		return nil, nil, err
	}
	if mintID != mintTX.TxID() || token.ContractTxid != mintTX.TxID() {
		return nil, nil, fmt.Errorf("%s returned txid mismatch", label)
	}
	return refetchedSource, refetchedMint, nil
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err == nil {
		err = run(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "testnet-parity:", err)
		os.Exit(1)
	}
}
