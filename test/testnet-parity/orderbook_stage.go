package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
	contractutil "github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
)

const (
	orderBookPrecision = uint64(1_000_000)
	orderBookTax       = "1BitcoinEaterAddressDontSendf59kuE"
)

type ordinaryOrderSpec struct {
	Side        string
	Holder      string
	TaxAddress  string
	Volume      uint64
	UnitPrice   uint64
	FeeRate     uint64
	TokenID     string
	PartialHash string
}

type orderBookEventExpectation struct {
	TxID    string
	Type    string
	TokenID string
}

func validateResidualSaleVolume(
	before *contract.OrderData,
	after *contract.OrderData,
	matched uint64,
) error {
	if before == nil || after == nil {
		return fmt.Errorf("residual order data is missing")
	}
	if matched > before.SaleVolume {
		return fmt.Errorf(
			"matched volume=%d exceeds order volume=%d",
			matched,
			before.SaleVolume,
		)
	}
	want := before.SaleVolume - matched
	if after.SaleVolume != want {
		return fmt.Errorf(
			"residual volume=%d want=%d",
			after.SaleVolume,
			want,
		)
	}
	return nil
}

func validateOrderBookFTCode(script *bscript.Script) error {
	if script == nil {
		return fmt.Errorf("nil OrderBook FT code")
	}
	if script.Len() != contractutil.FTV4CodeLength {
		return fmt.Errorf("OrderBook FT code length=%d want=%d", script.Len(), contractutil.FTV4CodeLength)
	}
	info, err := contractutil.ClassifyFTScript(script)
	if err != nil {
		return fmt.Errorf("classify OrderBook FT code: %w", err)
	}
	if info.Version != contractutil.FTVersion4 || info.IsCoin {
		return fmt.Errorf(
			"OrderBook FT code version=%d coin=%t want ordinary v4",
			info.Version,
			info.IsCoin,
		)
	}
	if contract.IsCoinScript(script.ToHex()) {
		return fmt.Errorf("OrderBook FT code selected StableCoin")
	}
	return nil
}

func checkedOrderAmount(volume, price uint64) (uint64, error) {
	value := new(big.Int).Mul(
		new(big.Int).SetUint64(volume),
		new(big.Int).SetUint64(price),
	)
	value.Div(value, new(big.Int).SetUint64(orderBookPrecision))
	if !value.IsUint64() {
		return 0, fmt.Errorf("order amount exceeds uint64")
	}
	return value.Uint64(), nil
}

func validateOrderOutput(
	tx *bt.Tx,
	vout int,
	spec ordinaryOrderSpec,
) (*contract.OrderData, error) {
	if tx == nil || vout < 0 || vout >= len(tx.Outputs) {
		return nil, fmt.Errorf("order output %d is out of range", vout)
	}
	if spec.Side != "sell" && spec.Side != "buy" {
		return nil, fmt.Errorf("unsupported order side %q", spec.Side)
	}
	output := tx.Outputs[vout]
	data, err := contract.GetOrderData(output.LockingScript.ToHex(), true)
	if err != nil {
		return nil, fmt.Errorf("decode %s order: %w", spec.Side, err)
	}
	if data.HoldAddress != spec.Holder ||
		data.SaleVolume != spec.Volume ||
		data.UnitPrice != spec.UnitPrice ||
		data.FeeRate != spec.FeeRate ||
		!strings.EqualFold(data.FtID, spec.TokenID) ||
		!strings.EqualFold(data.FtPartialHash, spec.PartialHash) {
		return nil, fmt.Errorf(
			"%s order data mismatch holder=%s volume=%d price=%d fee=%d token=%s partial=%s",
			spec.Side,
			data.HoldAddress,
			data.SaleVolume,
			data.UnitPrice,
			data.FeeRate,
			data.FtID,
			data.FtPartialHash,
		)
	}
	expected := contract.NewOrderBook()
	expected.HoldAddress = spec.Holder
	expected.SaleVolume = spec.Volume
	expected.UnitPrice = spec.UnitPrice
	expected.FeeRate = spec.FeeRate
	expected.FtID = spec.TokenID
	expected.FtPartialHash = spec.PartialHash
	var expectedScript *bscript.Script
	if spec.Side == "sell" {
		expectedScript, err = expected.GetSellOrderCode(false, spec.TaxAddress)
		if output.Satoshis != spec.Volume {
			return nil, fmt.Errorf(
				"sell order satoshis=%d want=%d",
				output.Satoshis,
				spec.Volume,
			)
		}
	} else {
		expectedScript, err = expected.GetBuyOrderCode(false, spec.TaxAddress)
		if output.Satoshis != contract.NewOrderBook().BuyCodeDust {
			return nil, fmt.Errorf(
				"buy order satoshis=%d want=%d",
				output.Satoshis,
				contract.NewOrderBook().BuyCodeDust,
			)
		}
	}
	if err != nil {
		return nil, err
	}
	if output.LockingScript.Len() != expectedScript.Len() {
		return nil, fmt.Errorf(
			"%s order script length=%d want=%d",
			spec.Side,
			output.LockingScript.Len(),
			expectedScript.Len(),
		)
	}
	if !bytes.Equal(output.LockingScript.Bytes(), expectedScript.Bytes()) {
		return nil, fmt.Errorf("%s order script differs from SDK template", spec.Side)
	}
	return data, nil
}

func validateOutpoint(
	tx *bt.Tx,
	input int,
	parent *bt.Tx,
	vout int,
) error {
	if tx == nil || parent == nil || input < 0 || input >= len(tx.Inputs) {
		return fmt.Errorf("input outpoint arguments are invalid")
	}
	if vout < 0 || vout >= len(parent.Outputs) {
		return fmt.Errorf("parent output %d is out of range", vout)
	}
	gotTxID := hex.EncodeToString(tx.Inputs[input].PreviousTxID())
	if gotTxID != parent.TxID() ||
		tx.Inputs[input].PreviousTxOutIndex != uint32(vout) {
		return fmt.Errorf(
			"input %d outpoint=%s:%d want=%s:%d",
			input,
			gotTxID,
			tx.Inputs[input].PreviousTxOutIndex,
			parent.TxID(),
			vout,
		)
	}
	return nil
}

func p2pkhScript(address string) (*bscript.Script, error) {
	return bscript.NewP2PKHFromAddress(address)
}

func validateP2PKHOutput(
	tx *bt.Tx,
	vout int,
	address string,
	satoshis uint64,
) error {
	if tx == nil || vout < 0 || vout >= len(tx.Outputs) {
		return fmt.Errorf("P2PKH output %d is out of range", vout)
	}
	wantScript, err := p2pkhScript(address)
	if err != nil {
		return err
	}
	output := tx.Outputs[vout]
	if output.Satoshis != satoshis {
		return fmt.Errorf(
			"P2PKH output %d satoshis=%d want=%d",
			vout,
			output.Satoshis,
			satoshis,
		)
	}
	if !bytes.Equal(output.LockingScript.Bytes(), wantScript.Bytes()) {
		return fmt.Errorf("P2PKH output %d address mismatch", vout)
	}
	return nil
}

func validateFTAtAddress(
	tx *bt.Tx,
	codeVout int,
	template *bscript.Script,
	address string,
	amount uint64,
) error {
	if template == nil {
		return fmt.Errorf("nil FT template")
	}
	if err := validateOrderBookFTCode(template); err != nil {
		return err
	}
	balance, err := validateFTV4Outputs(tx, codeVout)
	if err != nil {
		return err
	}
	if !balance.IsUint64() || balance.Uint64() != amount {
		return fmt.Errorf(
			"FT output %d balance=%s want=%d",
			codeVout,
			balance,
			amount,
		)
	}
	wantCode, err := contract.BuildFTtransferCode(template.ToHex(), address)
	if err != nil {
		return err
	}
	if !bytes.Equal(tx.Outputs[codeVout].LockingScript.Bytes(), wantCode.Bytes()) {
		return fmt.Errorf("FT output %d holder mismatch", codeVout)
	}
	return nil
}

func validateFTLockedByOrder(
	tx *bt.Tx,
	orderVout int,
	codeVout int,
	template *bscript.Script,
	amount uint64,
) error {
	if tx == nil || orderVout < 0 || orderVout >= len(tx.Outputs) {
		return fmt.Errorf("order output %d is out of range", orderVout)
	}
	orderHash := hex.EncodeToString(
		crypto.Hash160(crypto.Sha256(tx.Outputs[orderVout].LockingScript.Bytes())),
	)
	return validateFTAtAddress(tx, codeVout, template, orderHash, amount)
}

func hasOrderOutput(tx *bt.Tx) bool {
	if tx == nil {
		return false
	}
	for _, output := range tx.Outputs {
		if _, err := contract.GetOrderData(output.LockingScript.ToHex(), true); err == nil {
			return true
		}
	}
	return false
}

func signBuyOrderInputsForStage(
	rawHex string,
	privateKey *bec.PrivateKey,
	ftUTXOs []*contractutil.FtUTXO,
	feeUTXOs []*bt.UTXO,
	preTXs []*bt.Tx,
	prePreTXData []string,
	ftCodeScript string,
) (string, error) {
	rawBytes, err := hex.DecodeString(rawHex)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	if len(tx.Inputs) != len(ftUTXOs)+len(feeUTXOs) {
		return "", fmt.Errorf(
			"buy input count=%d want=%d",
			len(tx.Inputs),
			len(ftUTXOs)+len(feeUTXOs),
		)
	}
	if len(preTXs) != len(ftUTXOs) ||
		len(prePreTXData) != len(ftUTXOs) {
		return "", fmt.Errorf("buy FT ancestry length mismatch")
	}
	for i, input := range ftUTXOs {
		tx.Inputs[i].PreviousTxScript = input.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = input.Satoshis
	}
	for i, input := range feeUTXOs {
		index := len(ftUTXOs) + i
		tx.Inputs[index].PreviousTxScript = input.LockingScript
		tx.Inputs[index].PreviousTxSatoshis = input.Satoshis
	}
	pubKey := hex.EncodeToString(privateKey.PubKey().SerialiseCompressed())
	signatures := make([]string, len(tx.Inputs))
	for i := range tx.Inputs {
		hash, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return "", fmt.Errorf("buy sighash input %d: %w", i, err)
		}
		signature, err := privateKey.Sign(hash)
		if err != nil {
			return "", fmt.Errorf("buy sign input %d: %w", i, err)
		}
		signatures[i] = hex.EncodeToString(
			append(signature.Serialise(), byte(sighash.AllForkID)),
		)
	}
	return contract.NewOrderBook().FillSigsMakeBuyOrder(
		rawHex,
		signatures,
		pubKey,
		preTXs,
		prePreTXData,
		contract.IsCoinScript(ftCodeScript),
	)
}

func buildSignedBuyOrder(
	privateKey *bec.PrivateKey,
	holder string,
	spec ordinaryOrderSpec,
	ftUTXO *contractutil.FtUTXO,
	feeUTXO *bt.UTXO,
	ftPreTX *bt.Tx,
	ftPrePre string,
) (string, error) {
	order := contract.NewOrderBook()
	unsigned, err := order.BuildBuyOrderTX(
		holder,
		spec.TaxAddress,
		spec.Volume,
		spec.UnitPrice,
		spec.FeeRate,
		spec.TokenID,
		[]*bt.UTXO{feeUTXO},
		[]*contractutil.FtUTXO{ftUTXO},
		[]*bt.Tx{ftPreTX},
	)
	if err != nil {
		return "", err
	}
	return signBuyOrderInputsForStage(
		unsigned,
		privateKey,
		[]*contractutil.FtUTXO{ftUTXO},
		[]*bt.UTXO{feeUTXO},
		[]*bt.Tx{ftPreTX},
		[]string{ftPrePre},
		ftUTXO.LockingScript.ToHex(),
	)
}

func buildOrderBookFunding(
	privateKey *bec.PrivateKey,
	funding *bt.UTXO,
	sellerAddress string,
	buyerAddress string,
	matcherAddress string,
) (string, error) {
	tx := bt.NewTx()
	tx.Version = 10
	if err := tx.FromUTXOs(funding); err != nil {
		return "", err
	}
	for _, output := range []struct {
		Address  string
		Satoshis uint64
	}{
		{Address: sellerAddress, Satoshis: 60_000},
		{Address: buyerAddress, Satoshis: 25_000},
		{Address: matcherAddress, Satoshis: 25_000},
	} {
		if err := tx.PayToAddress(output.Address, output.Satoshis); err != nil {
			return "", err
		}
	}
	if err := tx.ChangeToAddress(buyerAddress, harnessFeeQuote80()); err != nil {
		return "", err
	}
	if err := tx.AdjustImplicitFeeToTarget(80); err != nil {
		return "", err
	}
	if err := tx.FillAllInputs(
		context.Background(),
		&unlocker.Getter{PrivateKey: privateKey},
	); err != nil {
		return "", err
	}
	return tx.String(), nil
}

func orderBookAddress(privateKey *bec.PrivateKey) (string, error) {
	address, err := bscript.NewAddressFromPublicKey(privateKey.PubKey(), true)
	if err != nil {
		return "", err
	}
	return address.AddressString, nil
}

func orderBookUTXO(tx *bt.Tx, vout int) (*bt.UTXO, error) {
	return outputUTXO(tx, vout)
}

func parseOrderBookRaw(label, raw string) (*bt.Tx, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s parse: %w", label, err)
	}
	return tx, nil
}

func validateOrderCancellation(
	tx *bt.Tx,
	orderParent *bt.Tx,
	orderSpec ordinaryOrderSpec,
	ftTemplate *bscript.Script,
) error {
	if err := validateOutpoint(tx, 0, orderParent, 0); err != nil {
		return err
	}
	if hasOrderOutput(tx) {
		return fmt.Errorf("%s cancellation created another order", orderSpec.Side)
	}
	if orderSpec.Side == "sell" {
		return validateP2PKHOutput(
			tx,
			0,
			orderSpec.Holder,
			orderSpec.Volume,
		)
	}
	amount, err := checkedOrderAmount(orderSpec.Volume, orderSpec.UnitPrice)
	if err != nil {
		return err
	}
	return validateFTAtAddress(tx, 0, ftTemplate, orderSpec.Holder, amount)
}

func validateMatchTransaction(
	tx *bt.Tx,
	buyParent *bt.Tx,
	sellParent *bt.Tx,
	buySpec ordinaryOrderSpec,
	sellSpec ordinaryOrderSpec,
	ftTemplate *bscript.Script,
	ftFeeAddress string,
	tbcFeeAddress string,
	expectResidual bool,
) error {
	if err := validateOutpoint(tx, 0, buyParent, 0); err != nil {
		return err
	}
	if err := validateOutpoint(tx, 1, buyParent, 1); err != nil {
		return err
	}
	if err := validateOutpoint(tx, 2, sellParent, 0); err != nil {
		return err
	}
	matched := buySpec.Volume
	if sellSpec.Volume < matched {
		matched = sellSpec.Volume
	}
	ftPay, err := checkedOrderAmount(matched, sellSpec.UnitPrice)
	if err != nil {
		return err
	}
	ftTax, err := checkedOrderAmount(ftPay, sellSpec.FeeRate)
	if err != nil {
		return err
	}
	tbcTax, err := checkedOrderAmount(matched, buySpec.FeeRate)
	if err != nil {
		return err
	}
	if ftTax > ftPay || tbcTax > matched {
		return fmt.Errorf("OrderBook tax exceeds matched asset")
	}
	if err := validateFTAtAddress(
		tx,
		0,
		ftTemplate,
		sellSpec.Holder,
		ftPay-ftTax,
	); err != nil {
		return err
	}
	if err := validateFTAtAddress(
		tx,
		2,
		ftTemplate,
		ftFeeAddress,
		ftTax,
	); err != nil {
		return err
	}
	if err := validateP2PKHOutput(
		tx,
		4,
		buySpec.Holder,
		matched-tbcTax,
	); err != nil {
		return err
	}
	if err := validateP2PKHOutput(
		tx,
		5,
		tbcFeeAddress,
		tbcTax,
	); err != nil {
		return err
	}

	if !expectResidual {
		if hasOrderOutput(tx) {
			return fmt.Errorf("full match unexpectedly created residual order")
		}
		if len(tx.Outputs) != 7 {
			return fmt.Errorf("full match outputs=%d want=7", len(tx.Outputs))
		}
		return nil
	}
	if len(tx.Outputs) != 8 {
		return fmt.Errorf("partial sell match outputs=%d want=8", len(tx.Outputs))
	}
	before, err := contract.GetOrderData(
		sellParent.Outputs[0].LockingScript.ToHex(),
		true,
	)
	if err != nil {
		return err
	}
	afterSpec := sellSpec
	afterSpec.Volume = sellSpec.Volume - matched
	after, err := validateOrderOutput(tx, 7, afterSpec)
	if err != nil {
		return err
	}
	return validateResidualSaleVolume(before, after, matched)
}

type orderBookTxInfoEnvelope struct {
	Code string `json:"code"`
	Data struct {
		Type    string `json:"type"`
		TokenID string `json:"tokenId"`
	} `json:"data"`
}

func fetchOrderBookEvent(
	client *http.Client,
	txid string,
) (string, string, error) {
	url := "https://api.tbcdev.org/api/tbc/orderbook/txinfo/txid/" + txid
	response, err := client.Get(url)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	var envelope orderBookTxInfoEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return "", "", err
	}
	if envelope.Code != "200" || envelope.Data.Type == "" {
		return "", "", fmt.Errorf("indexer code=%q type=%q", envelope.Code, envelope.Data.Type)
	}
	return envelope.Data.Type, envelope.Data.TokenID, nil
}

func verifyOrderBookEvents(events []orderBookEventExpectation) error {
	pending := make(map[string]orderBookEventExpectation, len(events))
	for _, event := range events {
		pending[event.TxID] = event
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for attempt := 1; attempt <= 15 && len(pending) > 0; attempt++ {
		for txid, want := range pending {
			gotType, gotTokenID, err := fetchOrderBookEvent(client, txid)
			if err != nil {
				lastErr = fmt.Errorf("%s: %w", txid, err)
				continue
			}
			if gotType != want.Type ||
				!strings.EqualFold(gotTokenID, want.TokenID) {
				lastErr = fmt.Errorf(
					"%s type/token=%s/%s want=%s/%s",
					txid,
					gotType,
					gotTokenID,
					want.Type,
					want.TokenID,
				)
				continue
			}
			delete(pending, txid)
		}
		if len(pending) > 0 && attempt < 15 {
			time.Sleep(2 * time.Second)
		}
	}
	if len(pending) > 0 {
		txids := make([]string, 0, len(pending))
		for txid := range pending {
			txids = append(txids, txid)
		}
		return fmt.Errorf(
			"OrderBook index events did not converge for %s: %v",
			strings.Join(txids, ","),
			lastErr,
		)
	}
	return nil
}

func broadcastOrderBookTransaction(
	cfg config,
	label string,
	raw string,
	invariant string,
	validate func(*bt.Tx) error,
	eventType string,
	tokenID string,
	events *[]orderBookEventExpectation,
) (*bt.Tx, error) {
	tx, evidence, err := broadcastAndVerify(
		label,
		raw,
		cfg.Network,
		invariant,
		validate,
	)
	if err != nil {
		return nil, err
	}
	if eventType != "" {
		*events = append(*events, orderBookEventExpectation{
			TxID:    evidence.TxID,
			Type:    eventType,
			TokenID: tokenID,
		})
	}
	return tx, nil
}

func fetchOrderBookFunding(address, network string) (*bt.UTXO, error) {
	funding, err := api.FetchUTXO(address, 0.15, network)
	if err != nil {
		return nil, fmt.Errorf("fetch OrderBook funding: %w", err)
	}
	return funding, nil
}

func runOrderBookStage(
	cfg config,
	decoded *wif.WIF,
	testnetAddress string,
) error {
	if cfg.Network != "testnet" {
		return fmt.Errorf("OrderBook stage refuses network %q", cfg.Network)
	}
	approvedAddress, err := orderBookAddress(decoded.PrivKey)
	if err != nil {
		return err
	}
	sellerKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		return err
	}
	sellerAddress, err := orderBookAddress(sellerKey)
	if err != nil {
		return err
	}
	matcherKey, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		return err
	}
	matcherAddress, err := orderBookAddress(matcherKey)
	if err != nil {
		return err
	}

	funding, err := fetchOrderBookFunding(testnetAddress, cfg.Network)
	if err != nil {
		return err
	}
	token, err := contract.NewFT(&contract.FtParams{
		Name:    "GoOrderBookMatrix",
		Symbol:  "GOBM",
		Amount:  1_000_000,
		Decimal: 2,
	})
	if err != nil {
		return err
	}
	mintRaws, err := token.MintFT(
		decoded.PrivKey,
		testnetAddress,
		funding,
	)
	if err != nil {
		return fmt.Errorf("OrderBook FT mint: %w", err)
	}
	if len(mintRaws) != 2 {
		return fmt.Errorf("OrderBook FT mint returned %d transactions", len(mintRaws))
	}
	source, err := parseOrderBookRaw("orderbook-ft-source", mintRaws[0])
	if err != nil {
		return err
	}
	mint, err := parseOrderBookRaw("orderbook-ft-mint", mintRaws[1])
	if err != nil {
		return err
	}
	if token.ContractTxid != mint.TxID() {
		return fmt.Errorf(
			"OrderBook token id=%s want mint txid=%s",
			token.ContractTxid,
			mint.TxID(),
		)
	}
	if _, _, err := broadcastAndVerify(
		"orderbook-ft-source",
		mintRaws[0],
		cfg.Network,
		"ordinary-ft-v4-source",
		func(tx *bt.Tx) error {
			return validateFTLifecycleTransaction("ft-source", tx)
		},
	); err != nil {
		return err
	}
	if _, _, err := broadcastAndVerify(
		"orderbook-ft-mint",
		mintRaws[1],
		cfg.Network,
		"ordinary-ft-v4-mint",
		func(tx *bt.Tx) error {
			if err := requireFTBalance(tx, 0, 100_000_000); err != nil {
				return err
			}
			return validateOrderBookFTCode(tx.Outputs[0].LockingScript)
		},
	); err != nil {
		return err
	}
	sourceChange, err := outputUTXO(source, 2)
	if err != nil {
		return err
	}
	fundingRaw, err := buildOrderBookFunding(
		decoded.PrivKey,
		sourceChange,
		sellerAddress,
		approvedAddress,
		matcherAddress,
	)
	if err != nil {
		return fmt.Errorf("build OrderBook participant funding: %w", err)
	}
	fundingTX, _, err := broadcastAndVerify(
		"orderbook-participant-funding",
		fundingRaw,
		cfg.Network,
		"seller-buyer-matcher-funding",
		func(tx *bt.Tx) error {
			if len(tx.Outputs) != 4 {
				return fmt.Errorf("participant funding outputs=%d want=4", len(tx.Outputs))
			}
			if err := validateP2PKHOutput(tx, 0, sellerAddress, 60_000); err != nil {
				return err
			}
			if err := validateP2PKHOutput(tx, 1, approvedAddress, 25_000); err != nil {
				return err
			}
			return validateP2PKHOutput(tx, 2, matcherAddress, 25_000)
		},
	)
	if err != nil {
		return err
	}

	ftTemplate := mint.Outputs[0].LockingScript
	partialHash, err := contract.ComputeFtPartialHash(ftTemplate.ToHex(), false)
	if err != nil {
		return err
	}
	baseSpec := ordinaryOrderSpec{
		TaxAddress:  orderBookTax,
		UnitPrice:   2_000_000,
		FeeRate:     10_000,
		TokenID:     token.ContractTxid,
		PartialHash: partialHash,
	}
	events := make([]orderBookEventExpectation, 0, 8)

	cancelSellSpec := baseSpec
	cancelSellSpec.Side = "sell"
	cancelSellSpec.Holder = sellerAddress
	cancelSellSpec.Volume = 10_000
	sellerFunding, err := orderBookUTXO(fundingTX, 0)
	if err != nil {
		return err
	}
	cancelSellRaw, err := contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		cancelSellSpec.Volume,
		cancelSellSpec.UnitPrice,
		cancelSellSpec.FeeRate,
		cancelSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{sellerFunding},
	)
	if err != nil {
		return fmt.Errorf("make cancel-path sell order: %w", err)
	}
	cancelSellCreate, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-sell-create",
		cancelSellRaw,
		"ordinary-ft-sell-order-fixed-script",
		func(tx *bt.Tx) error {
			_, err := validateOrderOutput(tx, 0, cancelSellSpec)
			return err
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}
	cancelSellUTXO, err := orderBookUTXO(cancelSellCreate, 0)
	if err != nil {
		return err
	}
	cancelSellFee, err := changeUTXO(cancelSellCreate)
	if err != nil {
		return err
	}
	cancelSellCancelRaw, err := contract.NewOrderBook().CancelSellOrderWithSign(
		sellerKey,
		cancelSellUTXO,
		[]*bt.UTXO{cancelSellFee},
		true,
	)
	if err != nil {
		return fmt.Errorf("cancel sell order: %w", err)
	}
	cancelSellCancel, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-sell-cancel",
		cancelSellCancelRaw,
		"sell-order-cancel-restores-tbc",
		func(tx *bt.Tx) error {
			return validateOrderCancellation(
				tx,
				cancelSellCreate,
				cancelSellSpec,
				ftTemplate,
			)
		},
		"CANCEL",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	cancelBuySpec := baseSpec
	cancelBuySpec.Side = "buy"
	cancelBuySpec.Holder = approvedAddress
	cancelBuySpec.Volume = 20_000
	mintedFT, err := ftUTXOFromTX(mint, 0)
	if err != nil {
		return err
	}
	buyerFee, err := orderBookUTXO(fundingTX, 1)
	if err != nil {
		return err
	}
	mintPrePre, err := localPrePre(source, 0)
	if err != nil {
		return err
	}
	cancelBuyRaw, err := buildSignedBuyOrder(
		decoded.PrivKey,
		approvedAddress,
		cancelBuySpec,
		mintedFT,
		buyerFee,
		mint,
		mintPrePre,
	)
	if err != nil {
		return fmt.Errorf("make cancel-path buy order: %w", err)
	}
	cancelBuyAmount, err := checkedOrderAmount(
		cancelBuySpec.Volume,
		cancelBuySpec.UnitPrice,
	)
	if err != nil {
		return err
	}
	cancelBuyCreate, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-buy-create",
		cancelBuyRaw,
		"ordinary-ft-buy-order-and-locked-ft",
		func(tx *bt.Tx) error {
			if _, err := validateOrderOutput(tx, 0, cancelBuySpec); err != nil {
				return err
			}
			return validateFTLockedByOrder(
				tx,
				0,
				1,
				ftTemplate,
				cancelBuyAmount,
			)
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}
	cancelBuyOrderUTXO, err := orderBookUTXO(cancelBuyCreate, 0)
	if err != nil {
		return err
	}
	cancelBuyFTUTXO, err := ftUTXOFromTX(cancelBuyCreate, 1)
	if err != nil {
		return err
	}
	cancelBuyFee, err := changeUTXO(cancelBuyCreate)
	if err != nil {
		return err
	}
	cancelBuyPrePre, err := localPrePre(mint, 0)
	if err != nil {
		return err
	}
	cancelBuyCancelRaw, err := contract.NewOrderBook().CancelBuyOrderWithSign(
		decoded.PrivKey,
		cancelBuyOrderUTXO,
		cancelBuyFTUTXO,
		cancelBuyCreate,
		cancelBuyPrePre,
		[]*bt.UTXO{cancelBuyFee},
		true,
	)
	if err != nil {
		return fmt.Errorf("cancel buy order: %w", err)
	}
	cancelBuyCancel, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-buy-cancel",
		cancelBuyCancelRaw,
		"buy-order-cancel-restores-ft",
		func(tx *bt.Tx) error {
			return validateOrderCancellation(
				tx,
				cancelBuyCreate,
				cancelBuySpec,
				ftTemplate,
			)
		},
		"CANCEL",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	fullSellSpec := baseSpec
	fullSellSpec.Side = "sell"
	fullSellSpec.Holder = sellerAddress
	fullSellSpec.Volume = 12_000
	returnedSellTBC, err := orderBookUTXO(cancelSellCancel, 0)
	if err != nil {
		return err
	}
	returnedSellFee, err := changeUTXO(cancelSellCancel)
	if err != nil {
		return err
	}
	fullSellRaw, err := contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		fullSellSpec.Volume,
		fullSellSpec.UnitPrice,
		fullSellSpec.FeeRate,
		fullSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{returnedSellTBC, returnedSellFee},
	)
	if err != nil {
		return fmt.Errorf("make full-match sell order: %w", err)
	}
	fullSell, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-full-sell-create",
		fullSellRaw,
		"full-match-sell-order",
		func(tx *bt.Tx) error {
			_, err := validateOrderOutput(tx, 0, fullSellSpec)
			return err
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	fullBuySpec := baseSpec
	fullBuySpec.Side = "buy"
	fullBuySpec.Holder = approvedAddress
	fullBuySpec.Volume = fullSellSpec.Volume
	returnedBuyFT, err := ftUTXOFromTX(cancelBuyCancel, 0)
	if err != nil {
		return err
	}
	returnedBuyFee, err := changeUTXO(cancelBuyCancel)
	if err != nil {
		return err
	}
	fullBuyPrePre, err := localPrePre(cancelBuyCreate, 1)
	if err != nil {
		return err
	}
	fullBuyRaw, err := buildSignedBuyOrder(
		decoded.PrivKey,
		approvedAddress,
		fullBuySpec,
		returnedBuyFT,
		returnedBuyFee,
		cancelBuyCancel,
		fullBuyPrePre,
	)
	if err != nil {
		return fmt.Errorf("make full-match buy order: %w", err)
	}
	fullBuyAmount, err := checkedOrderAmount(
		fullBuySpec.Volume,
		fullBuySpec.UnitPrice,
	)
	if err != nil {
		return err
	}
	fullBuy, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-full-buy-create",
		fullBuyRaw,
		"full-match-buy-order",
		func(tx *bt.Tx) error {
			if _, err := validateOrderOutput(tx, 0, fullBuySpec); err != nil {
				return err
			}
			return validateFTLockedByOrder(
				tx,
				0,
				1,
				ftTemplate,
				fullBuyAmount,
			)
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	fullBuyOrderUTXO, err := orderBookUTXO(fullBuy, 0)
	if err != nil {
		return err
	}
	fullBuyFTUTXO, err := ftUTXOFromTX(fullBuy, 1)
	if err != nil {
		return err
	}
	fullSellOrderUTXO, err := orderBookUTXO(fullSell, 0)
	if err != nil {
		return err
	}
	matcherFee, err := orderBookUTXO(fundingTX, 2)
	if err != nil {
		return err
	}
	fullMatchPrePre, err := localPrePre(cancelBuyCancel, 0)
	if err != nil {
		return err
	}
	fullMatchRaw, err := contract.NewOrderBook().MatchOrder(
		matcherKey,
		fullBuyOrderUTXO,
		fullBuy,
		fullBuyFTUTXO,
		fullBuy,
		fullMatchPrePre,
		fullSellOrderUTXO,
		fullSell,
		[]*bt.UTXO{matcherFee},
		orderBookTax,
		orderBookTax,
		true,
	)
	if err != nil {
		return fmt.Errorf("full OrderBook match: %w", err)
	}
	fullMatch, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-full-match",
		fullMatchRaw,
		"full-match-exact-ft-tbc-tax",
		func(tx *bt.Tx) error {
			return validateMatchTransaction(
				tx,
				fullBuy,
				fullSell,
				fullBuySpec,
				fullSellSpec,
				ftTemplate,
				orderBookTax,
				orderBookTax,
				false,
			)
		},
		"TRADE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	partialSellSpec := baseSpec
	partialSellSpec.Side = "sell"
	partialSellSpec.Holder = sellerAddress
	partialSellSpec.Volume = 20_000
	partialSellFee, err := changeUTXO(fullSell)
	if err != nil {
		return err
	}
	partialSellRaw, err := contract.NewOrderBook().MakeSellOrderWithSign(
		sellerKey,
		orderBookTax,
		partialSellSpec.Volume,
		partialSellSpec.UnitPrice,
		partialSellSpec.FeeRate,
		partialSellSpec.TokenID,
		ftTemplate.ToHex(),
		[]*bt.UTXO{partialSellFee},
	)
	if err != nil {
		return fmt.Errorf("make partial-match sell order: %w", err)
	}
	partialSell, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-partial-sell-create",
		partialSellRaw,
		"partial-match-sell-order",
		func(tx *bt.Tx) error {
			_, err := validateOrderOutput(tx, 0, partialSellSpec)
			return err
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	partialBuySpec := baseSpec
	partialBuySpec.Side = "buy"
	partialBuySpec.Holder = approvedAddress
	partialBuySpec.Volume = 8_000
	fullBuyChangeFT, err := ftUTXOFromTX(fullBuy, 3)
	if err != nil {
		return err
	}
	fullBuyFee, err := changeUTXO(fullBuy)
	if err != nil {
		return err
	}
	partialBuyPrePre, err := localPrePre(cancelBuyCancel, 0)
	if err != nil {
		return err
	}
	partialBuyRaw, err := buildSignedBuyOrder(
		decoded.PrivKey,
		approvedAddress,
		partialBuySpec,
		fullBuyChangeFT,
		fullBuyFee,
		fullBuy,
		partialBuyPrePre,
	)
	if err != nil {
		return fmt.Errorf("make partial-match buy order: %w", err)
	}
	partialBuyAmount, err := checkedOrderAmount(
		partialBuySpec.Volume,
		partialBuySpec.UnitPrice,
	)
	if err != nil {
		return err
	}
	partialBuy, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-partial-buy-create",
		partialBuyRaw,
		"partial-match-buy-order",
		func(tx *bt.Tx) error {
			if _, err := validateOrderOutput(tx, 0, partialBuySpec); err != nil {
				return err
			}
			return validateFTLockedByOrder(
				tx,
				0,
				1,
				ftTemplate,
				partialBuyAmount,
			)
		},
		"PLACE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}

	partialBuyOrderUTXO, err := orderBookUTXO(partialBuy, 0)
	if err != nil {
		return err
	}
	partialBuyFTUTXO, err := ftUTXOFromTX(partialBuy, 1)
	if err != nil {
		return err
	}
	partialSellOrderUTXO, err := orderBookUTXO(partialSell, 0)
	if err != nil {
		return err
	}
	partialMatchFee, err := changeUTXO(fullMatch)
	if err != nil {
		return err
	}
	partialMatchPrePre, err := localPrePre(fullBuy, 3)
	if err != nil {
		return err
	}
	partialMatchRaw, err := contract.NewOrderBook().MatchOrder(
		matcherKey,
		partialBuyOrderUTXO,
		partialBuy,
		partialBuyFTUTXO,
		partialBuy,
		partialMatchPrePre,
		partialSellOrderUTXO,
		partialSell,
		[]*bt.UTXO{partialMatchFee},
		orderBookTax,
		orderBookTax,
		true,
	)
	if err != nil {
		return fmt.Errorf("partial OrderBook match: %w", err)
	}
	partialMatch, err := broadcastOrderBookTransaction(
		cfg,
		"orderbook-partial-match",
		partialMatchRaw,
		"partial-match-exact-residual-volume",
		func(tx *bt.Tx) error {
			return validateMatchTransaction(
				tx,
				partialBuy,
				partialSell,
				partialBuySpec,
				partialSellSpec,
				ftTemplate,
				orderBookTax,
				orderBookTax,
				true,
			)
		},
		"TRADE",
		token.ContractTxid,
		&events,
	)
	if err != nil {
		return err
	}
	if err := verifyOrderBookEvents(events); err != nil {
		return err
	}
	return writePublicState(
		os.Stdout,
		publicState{
			TokenID:  token.ContractTxid,
			LastTxID: partialMatch.TxID(),
			LastVout: 7,
		},
	)
}
