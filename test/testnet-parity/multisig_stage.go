package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const multiSigStageFundingMinimumSatoshis = uint64(200_000)

type ephemeralMultiSigSet struct {
	Required   int
	Signers    []multiSigSigner
	PublicKeys []string
	Address    string
}

type multiSigLifecyclePlan struct {
	Transactions               []plannedTransaction
	Signers                    *ephemeralMultiSigSet
	DestinationSigners         *ephemeralMultiSigSet
	MultiSigAddress            string
	DestinationMultiSigAddress string
	Token                      *contract.FT
	Source                     *bt.Tx
	Mint                       *bt.Tx
	Wallet                     *bt.Tx
	TBCDeposit                 *bt.Tx
	TBCSpend                   *bt.Tx
	TBCForward                 *bt.Tx
	FTDeposit                  *bt.Tx
	FTSpend                    *bt.Tx
	FTForward                  *bt.Tx
	MainAddress                string
}

func newEphemeralMultiSigSet(
	approvedPrivateKey *bec.PrivateKey,
) (*ephemeralMultiSigSet, error) {
	if approvedPrivateKey == nil {
		return nil, fmt.Errorf("approved private key is required")
	}
	signers := []multiSigSigner{{
		privateKey: approvedPrivateKey,
		publicKey: hex.EncodeToString(
			approvedPrivateKey.PubKey().SerialiseCompressed(),
		),
	}}
	for len(signers) < 3 {
		privateKey, err := bec.NewPrivateKey(bec.S256())
		if err != nil {
			return nil, err
		}
		signers = append(signers, multiSigSigner{
			privateKey: privateKey,
			publicKey: hex.EncodeToString(
				privateKey.PubKey().SerialiseCompressed(),
			),
		})
	}
	sort.Slice(signers, func(i, j int) bool {
		return signers[i].publicKey < signers[j].publicKey
	})
	publicKeys := make([]string, len(signers))
	for i := range signers {
		publicKeys[i] = signers[i].publicKey
	}
	address, err := contract.GetMultiSigAddress(publicKeys, 2, 3)
	if err != nil {
		return nil, err
	}
	return &ephemeralMultiSigSet{
		Required:   2,
		Signers:    signers,
		PublicKeys: publicKeys,
		Address:    address,
	}, nil
}

func verifyEphemeralMultiSigSet(set *ephemeralMultiSigSet) (bool, error) {
	if set == nil {
		return false, fmt.Errorf("nil multisig signer set")
	}
	if set.Required != 2 || len(set.Signers) != 3 || len(set.PublicKeys) != 3 {
		return false, nil
	}
	return contract.VerifyMultiSigAddress(set.PublicKeys, set.Address)
}

func finishMultiSigTBCTransaction(
	unsigned *contract.MultiSigTxRaw,
	signers *ephemeralMultiSigSet,
) (string, error) {
	if unsigned == nil || signers == nil {
		return "", fmt.Errorf("unsigned TBC transaction and signers are required")
	}
	signatures := make([][]string, len(unsigned.Amounts))
	for signerIndex := 0; signerIndex < signers.Required; signerIndex++ {
		perInput, err := contract.SignMultiSigTransactionSendTBC(
			signers.Address,
			unsigned,
			signers.Signers[signerIndex].privateKey,
		)
		if err != nil {
			return "", fmt.Errorf("MultiSig TBC signer %d: %w", signerIndex, err)
		}
		if len(perInput) != len(signatures) {
			return "", fmt.Errorf(
				"MultiSig TBC signer %d returned %d signatures for %d inputs",
				signerIndex,
				len(perInput),
				len(signatures),
			)
		}
		for inputIndex := range perInput {
			signatures[inputIndex] = append(
				signatures[inputIndex],
				perInput[inputIndex],
			)
		}
	}
	return contract.FinishMultiSigTransactionSendTBC(
		unsigned.TxRaw,
		signatures,
		signers.PublicKeys,
	)
}

func finishMultiSigFTTransaction(
	unsigned *contract.MultiSigTxRaw,
	signers *ephemeralMultiSigSet,
) (string, error) {
	if unsigned == nil || signers == nil {
		return "", fmt.Errorf("unsigned FT transaction and signers are required")
	}
	signatures := make([][]string, 1)
	for signerIndex := 0; signerIndex < signers.Required; signerIndex++ {
		perInput, err := contract.SignMultiSigTransactionTransferFT(
			signers.Address,
			unsigned,
			signers.Signers[signerIndex].privateKey,
		)
		if err != nil {
			return "", fmt.Errorf("MultiSig FT signer %d: %w", signerIndex, err)
		}
		if len(perInput) != 1 {
			return "", fmt.Errorf(
				"MultiSig FT signer %d returned %d signatures",
				signerIndex,
				len(perInput),
			)
		}
		signatures[0] = append(signatures[0], perInput[0])
	}
	return contract.FinishMultiSigTransactionTransferFT(
		unsigned.TxRaw,
		signatures,
		signers.PublicKeys,
	)
}

func buildMultiSigLifecyclePlan(
	privateKey *bec.PrivateKey,
	address string,
	funding *bt.UTXO,
) (*multiSigLifecyclePlan, error) {
	signers, err := newEphemeralMultiSigSet(privateKey)
	if err != nil {
		return nil, err
	}
	destinationSigners, err := newEphemeralMultiSigSet(privateKey)
	if err != nil {
		return nil, err
	}
	if destinationSigners.Address == signers.Address {
		return nil, fmt.Errorf("ephemeral multisig destination equals source")
	}
	token, err := contract.NewFT(&contract.FtParams{
		Name: "GoMultiSigMatrix", Symbol: "GMS", Amount: 1_000_000, Decimal: 2,
	})
	if err != nil {
		return nil, err
	}
	mintRaws, err := token.MintFT(privateKey, address, funding)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT mint build: %w", err)
	}
	if len(mintRaws) != 2 {
		return nil, fmt.Errorf("MultiSig FT mint returned %d transactions", len(mintRaws))
	}
	source, err := parsePlannedTransaction("multisig-ft-source", mintRaws[0])
	if err != nil {
		return nil, err
	}
	mint, err := parsePlannedTransaction("multisig-ft-mint", mintRaws[1])
	if err != nil {
		return nil, err
	}
	walletFunding, err := changeUTXO(source)
	if err != nil {
		return nil, err
	}
	walletRaw, err := contract.CreateMultiSigWallet(
		address,
		signers.PublicKeys,
		2,
		3,
		100_000,
		[]*bt.UTXO{walletFunding},
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig wallet build: %w", err)
	}
	wallet, err := parsePlannedTransaction("multisig-wallet-create", walletRaw)
	if err != nil {
		return nil, err
	}
	walletChange, err := changeUTXO(wallet)
	if err != nil {
		return nil, err
	}
	tbcDepositRaw, err := contract.P2PKHToMultiSigSendTBC(
		address,
		signers.Address,
		20_000,
		[]*bt.UTXO{walletChange},
		privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig TBC deposit build: %w", err)
	}
	tbcDeposit, err := parsePlannedTransaction(
		"multisig-tbc-deposit",
		tbcDepositRaw,
	)
	if err != nil {
		return nil, err
	}

	multiSigTBC, err := outputUTXO(wallet, 0)
	if err != nil {
		return nil, err
	}
	unsignedTBC, err := contract.BuildMultiSigTransactionSendTBC(
		signers.Address,
		address,
		50_000,
		[]*bt.UTXO{multiSigTBC},
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig TBC spend build: %w", err)
	}
	tbcSpendRaw, err := finishMultiSigTBCTransaction(unsignedTBC, signers)
	if err != nil {
		return nil, fmt.Errorf("MultiSig TBC finish: %w", err)
	}
	tbcSpend, err := parsePlannedTransaction("multisig-tbc-spend", tbcSpendRaw)
	if err != nil {
		return nil, err
	}
	tbcDepositMultiSig, err := outputUTXO(tbcDeposit, 0)
	if err != nil {
		return nil, err
	}
	unsignedTBCForward, err := contract.BuildMultiSigTransactionSendTBC(
		signers.Address,
		destinationSigners.Address,
		10_000,
		[]*bt.UTXO{tbcDepositMultiSig},
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig TBC forward build: %w", err)
	}
	tbcForwardRaw, err := finishMultiSigTBCTransaction(
		unsignedTBCForward,
		signers,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig TBC forward finish: %w", err)
	}
	tbcForward, err := parsePlannedTransaction(
		"multisig-tbc-forward",
		tbcForwardRaw,
	)
	if err != nil {
		return nil, err
	}

	mintedFT, err := ftUTXOFromTX(mint, 0)
	if err != nil {
		return nil, err
	}
	depositFee, err := changeUTXO(tbcDeposit)
	if err != nil {
		return nil, err
	}
	mintPrePre, err := localPrePre(source, 0)
	if err != nil {
		return nil, err
	}
	depositRaw, err := contract.P2PKHToMultiSigTransferFT(
		address,
		signers.Address,
		token,
		big.NewInt(30_000_000),
		depositFee,
		[]*contractutil.FtUTXO{mintedFT},
		[]*bt.Tx{mint},
		[]string{mintPrePre},
		privateKey,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT deposit build: %w", err)
	}
	deposit, err := parsePlannedTransaction("multisig-ft-deposit", depositRaw)
	if err != nil {
		return nil, err
	}

	ftFee, err := outputUTXO(tbcSpend, 0)
	if err != nil {
		return nil, err
	}
	multiSigFT, err := ftUTXOFromTX(deposit, 0)
	if err != nil {
		return nil, err
	}
	depositPrePre, err := localPrePre(mint, 0)
	if err != nil {
		return nil, err
	}
	unsignedFT, err := contract.BuildMultiSigTransactionTransferFT(
		signers.Address,
		address,
		token,
		big.NewInt(10_000_000),
		ftFee,
		[]*contractutil.FtUTXO{multiSigFT},
		[]*bt.Tx{deposit},
		[]string{depositPrePre},
		tbcSpend,
		signers.Signers[0].privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT spend build: %w", err)
	}
	ftSpendRaw, err := finishMultiSigFTTransaction(unsignedFT, signers)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT finish: %w", err)
	}
	ftSpend, err := parsePlannedTransaction("multisig-ft-spend", ftSpendRaw)
	if err != nil {
		return nil, err
	}
	ftForwardFee, err := outputUTXO(ftSpend, 0)
	if err != nil {
		return nil, err
	}
	multiSigFTChange, err := ftUTXOFromTX(ftSpend, 3)
	if err != nil {
		return nil, err
	}
	ftSpendPrePre, err := localPrePre(deposit, 0)
	if err != nil {
		return nil, err
	}
	unsignedFTForward, err := contract.BuildMultiSigTransactionTransferFT(
		signers.Address,
		destinationSigners.Address,
		token,
		big.NewInt(10_000_000),
		ftForwardFee,
		[]*contractutil.FtUTXO{multiSigFTChange},
		[]*bt.Tx{ftSpend},
		[]string{ftSpendPrePre},
		ftSpend,
		signers.Signers[0].privateKey,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT forward build: %w", err)
	}
	ftForwardRaw, err := finishMultiSigFTTransaction(
		unsignedFTForward,
		signers,
	)
	if err != nil {
		return nil, fmt.Errorf("MultiSig FT forward finish: %w", err)
	}
	ftForward, err := parsePlannedTransaction(
		"multisig-ft-forward",
		ftForwardRaw,
	)
	if err != nil {
		return nil, err
	}

	return &multiSigLifecyclePlan{
		Transactions: []plannedTransaction{
			{Label: "multisig-ft-source", Raw: mintRaws[0]},
			{Label: "multisig-ft-mint", Raw: mintRaws[1]},
			{Label: "multisig-wallet-create", Raw: walletRaw},
			{Label: "multisig-tbc-deposit", Raw: tbcDepositRaw},
			{Label: "multisig-tbc-spend", Raw: tbcSpendRaw},
			{Label: "multisig-tbc-forward", Raw: tbcForwardRaw},
			{Label: "multisig-ft-deposit", Raw: depositRaw},
			{Label: "multisig-ft-spend", Raw: ftSpendRaw},
			{Label: "multisig-ft-forward", Raw: ftForwardRaw},
		},
		Signers:                    signers,
		DestinationSigners:         destinationSigners,
		MultiSigAddress:            signers.Address,
		DestinationMultiSigAddress: destinationSigners.Address,
		Token:                      token,
		Source:                     source,
		Mint:                       mint,
		Wallet:                     wallet,
		TBCDeposit:                 tbcDeposit,
		TBCSpend:                   tbcSpend,
		TBCForward:                 tbcForward,
		FTDeposit:                  deposit,
		FTSpend:                    ftSpend,
		FTForward:                  ftForward,
		MainAddress:                address,
	}, nil
}

func validateMultiSigLockOutput(
	tx *bt.Tx,
	vout int,
	address string,
	satoshis uint64,
) error {
	if tx == nil || vout < 0 || vout >= len(tx.Outputs) {
		return fmt.Errorf("multisig output %d is missing", vout)
	}
	asm, err := contract.GetMultiSigLockScript(address)
	if err != nil {
		return err
	}
	script, err := bscript.NewFromASM(asm)
	if err != nil {
		return err
	}
	output := tx.Outputs[vout]
	if output.Satoshis != satoshis || !bytes.Equal(output.LockingScript.Bytes(), script.Bytes()) {
		return fmt.Errorf("multisig output %d script/value mismatch", vout)
	}
	return nil
}

func validateMultiSigFTPair(
	tx *bt.Tx,
	codeVout int,
	codeSatoshis uint64,
	wantBalance int64,
) error {
	if tx == nil || codeVout < 0 || codeVout+1 >= len(tx.Outputs) {
		return fmt.Errorf("multisig FT pair %d/%d is missing", codeVout, codeVout+1)
	}
	code := tx.Outputs[codeVout]
	tape := tx.Outputs[codeVout+1]
	if code.Satoshis != codeSatoshis || tape.Satoshis != 0 || !tape.LockingScript.IsSafeDataOut() {
		return fmt.Errorf("multisig FT pair %d has wrong dust/Tape", codeVout)
	}
	info, err := contractutil.ClassifyFTScript(code.LockingScript)
	if err != nil {
		return err
	}
	if info.Version != contractutil.FTVersion4 || info.IsCoin {
		return fmt.Errorf("multisig FT pair %d is not ordinary FT v4", codeVout)
	}
	balance, err := contractutil.GetFtBalanceFromTape(tape.LockingScript.ToHex())
	if err != nil {
		return err
	}
	if balance.Cmp(big.NewInt(wantBalance)) != 0 {
		return fmt.Errorf(
			"multisig FT pair %d balance=%s want=%d",
			codeVout,
			balance,
			wantBalance,
		)
	}
	return nil
}

func validateMultiSigFTRecipient(
	tx *bt.Tx,
	codeVout int,
	token *contract.FT,
	multiSigAddress string,
) error {
	if tx == nil || token == nil || codeVout < 0 || codeVout >= len(tx.Outputs) {
		return fmt.Errorf("multisig FT recipient output %d is missing", codeVout)
	}
	combineHash, err := contract.GetCombineHash(multiSigAddress)
	if err != nil {
		return err
	}
	recipientHash := strings.TrimSuffix(combineHash, "01")
	if len(recipientHash) != 40 {
		return fmt.Errorf("invalid multisig FT recipient hash %q", combineHash)
	}
	expected, err := contract.BuildFTtransferCode(token.CodeScript, recipientHash)
	if err != nil {
		return err
	}
	if !bytes.Equal(tx.Outputs[codeVout].LockingScript.Bytes(), expected.Bytes()) {
		return fmt.Errorf("multisig FT recipient output %d has wrong owner", codeVout)
	}
	return nil
}

func validateMultiSigLifecycleTransaction(
	label string,
	tx *bt.Tx,
	plan *multiSigLifecyclePlan,
) error {
	switch label {
	case "multisig-ft-source":
		return validateFTLifecycleTransaction("ft-source", tx)
	case "multisig-ft-mint":
		return validateFTLifecycleTransaction("ft-mint", tx)
	case "multisig-wallet-create":
		if tx == nil || len(tx.Outputs) != 6 {
			return fmt.Errorf("multisig wallet output layout mismatch")
		}
		if err := validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 100_000); err != nil {
			return err
		}
		if tx.Outputs[4].Satoshis != 0 || !tx.Outputs[4].LockingScript.IsSafeDataOut() {
			return fmt.Errorf("multisig wallet Tape is invalid")
		}
		return nil
	case "multisig-tbc-deposit":
		return validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 20_000)
	case "multisig-tbc-spend":
		if err := validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 49_000); err != nil {
			return err
		}
		return validatePaymentOutput(tx, plan.MainAddress, 50_000)
	case "multisig-tbc-forward":
		if err := validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 9_000); err != nil {
			return err
		}
		return validateMultiSigLockOutput(
			tx,
			1,
			plan.DestinationMultiSigAddress,
			10_000,
		)
	case "multisig-ft-deposit":
		if err := validateMultiSigFTPair(tx, 0, 2_000, 30_000_000); err != nil {
			return err
		}
		if err := validateMultiSigFTRecipient(
			tx,
			0,
			plan.Token,
			plan.MultiSigAddress,
		); err != nil {
			return err
		}
		return validateMultiSigFTPair(tx, 2, 2_000, 70_000_000)
	case "multisig-ft-spend":
		if err := validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 45_000); err != nil {
			return err
		}
		if err := validateMultiSigFTPair(tx, 1, 2_000, 10_000_000); err != nil {
			return err
		}
		if err := validateMultiSigFTPair(tx, 3, 2_000, 20_000_000); err != nil {
			return err
		}
		return validateMultiSigFTRecipient(
			tx,
			3,
			plan.Token,
			plan.MultiSigAddress,
		)
	case "multisig-ft-forward":
		if err := validateMultiSigLockOutput(tx, 0, plan.MultiSigAddress, 41_000); err != nil {
			return err
		}
		if err := validateMultiSigFTPair(tx, 1, 2_000, 10_000_000); err != nil {
			return err
		}
		if err := validateMultiSigFTRecipient(
			tx,
			1,
			plan.Token,
			plan.DestinationMultiSigAddress,
		); err != nil {
			return err
		}
		if err := validateMultiSigFTPair(tx, 3, 2_000, 10_000_000); err != nil {
			return err
		}
		return validateMultiSigFTRecipient(
			tx,
			3,
			plan.Token,
			plan.MultiSigAddress,
		)
	default:
		return fmt.Errorf("unknown multisig lifecycle label %q", label)
	}
}

func runMultiSigStage(cfg config, decoded *wif.WIF, address string) error {
	funding, err := api.FetchUTXO(
		address,
		float64(multiSigStageFundingMinimumSatoshis)/1_000_000,
		cfg.Network,
	)
	if err != nil {
		return fmt.Errorf("MultiSig funding: %w", err)
	}
	plan, err := buildMultiSigLifecyclePlan(decoded.PrivKey, address, funding)
	if err != nil {
		return err
	}
	state := publicState{
		TokenID:  plan.Token.ContractTxid,
		MultiSig: plan.MultiSigAddress,
	}
	for _, item := range plan.Transactions {
		label := item.Label
		accepted, _, err := broadcastAndVerify(
			label,
			item.Raw,
			cfg.Network,
			"multisig-2-of-3-tbc-ft",
			func(tx *bt.Tx) error {
				return validateMultiSigLifecycleTransaction(label, tx, plan)
			},
		)
		if err != nil {
			return err
		}
		state.LastTxID = accepted.TxID()
		state.LastVout = 0
	}
	return writePublicState(os.Stdout, state)
}
