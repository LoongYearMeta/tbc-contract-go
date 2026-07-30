package contract

import (
	"encoding/hex"
	"fmt"
	"math/big"

	contractapi "github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
)

// OrderBookAPI is the read-only discovery surface used by online Token Order
// composition. Injecting it keeps all transaction construction deterministic
// and independently testable.
type OrderBookAPI interface {
	FetchFtInfo(contractID, network string) (*contractapi.FtInfoResponse, error)
	FetchFtUTXOs(contractID, owner, code, network string, amount *big.Int) ([]*util.FtUTXO, error)
	FetchUTXOs(address, network string) ([]*bt.UTXO, error)
	FetchTXRaw(txid, network string) (*bt.Tx, error)
	FetchFtPrePreTxData(preTX *bt.Tx, vout int, network string) (string, error)
}

// DefaultOrderBookAPI delegates discovery to lib/api and supports both FT and
// StableCoin scripts through the same injected interface.
type DefaultOrderBookAPI struct{}

func (DefaultOrderBookAPI) FetchFtInfo(contractID, network string) (*contractapi.FtInfoResponse, error) {
	if coin, err := contractapi.FetchCoinInfo(contractID, network); err == nil {
		return &contractapi.FtInfoResponse{
			Name: coin.Name, Symbol: coin.Symbol, Decimal: coin.Decimal,
			TotalSupply: coin.TotalSupply, CodeScript: coin.CodeScript, TapeScript: coin.TapeScript,
		}, nil
	}
	return contractapi.FetchFtInfo(contractID, network)
}

func (DefaultOrderBookAPI) FetchFtUTXOs(
	contractID, owner, code, network string,
	amount *big.Int,
) ([]*util.FtUTXO, error) {
	info, err := util.ClassifyFTScriptHex(code)
	if err != nil {
		return nil, err
	}
	if info.IsCoin {
		return contractapi.FetchCoinUTXOs(contractID, owner, code, network, amount, 5)
	}
	return contractapi.FetchFtUTXOs(contractID, owner, code, network, amount)
}

func (DefaultOrderBookAPI) FetchUTXOs(address, network string) ([]*bt.UTXO, error) {
	utxo, err := contractapi.FetchUTXO(address, 0.01, network)
	if err != nil {
		return nil, err
	}
	return []*bt.UTXO{utxo}, nil
}

func (DefaultOrderBookAPI) FetchTXRaw(txid, network string) (*bt.Tx, error) {
	return contractapi.FetchTXRaw(txid, network)
}

func (DefaultOrderBookAPI) FetchFtPrePreTxData(preTX *bt.Tx, vout int, network string) (string, error) {
	return contractapi.FetchFtPrePreTxData(preTX, vout, network)
}

type OnlineOrderBook struct {
	Client  OrderBookAPI
	Network string
}

func NewOnlineOrderBook(client OrderBookAPI, network string) *OnlineOrderBook {
	if client == nil {
		client = DefaultOrderBookAPI{}
	}
	return &OnlineOrderBook{Client: client, Network: network}
}

func (o *OnlineOrderBook) mainnet() bool {
	return o.Network != "testnet"
}

func (o *OnlineOrderBook) addressForKey(privateKey *bec.PrivateKey) (string, error) {
	if privateKey == nil {
		return "", fmt.Errorf("private key is required")
	}
	address, err := bscript.NewAddressFromPublicKey(privateKey.PubKey(), o.mainnet())
	if err != nil {
		return "", err
	}
	return address.AddressString, nil
}

func (o *OnlineOrderBook) fetchTokenInfoPair(ftaID, ftbID string) (*contractapi.FtInfoResponse, *contractapi.FtInfoResponse, error) {
	fta, err := o.Client.FetchFtInfo(ftaID, o.Network)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch Token A info: %w", err)
	}
	ftb, err := o.Client.FetchFtInfo(ftbID, o.Network)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch Token B info: %w", err)
	}
	return fta, ftb, nil
}

func (o *OnlineOrderBook) fetchTokenAncestors(ftUTXOs []*util.FtUTXO) ([]*bt.Tx, []string, error) {
	preTXs := make([]*bt.Tx, len(ftUTXOs))
	prePre := make([]string, len(ftUTXOs))
	for i, ftUTXO := range ftUTXOs {
		if ftUTXO == nil {
			return nil, nil, fmt.Errorf("nil FT UTXO %d", i)
		}
		txid := hex.EncodeToString(ftUTXO.TxID)
		preTX, err := o.Client.FetchTXRaw(txid, o.Network)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch FT parent %d: %w", i, err)
		}
		data, err := o.Client.FetchFtPrePreTxData(preTX, int(ftUTXO.Vout), o.Network)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch FT ancestry %d: %w", i, err)
		}
		preTXs[i] = preTX
		prePre[i] = data
	}
	return preTXs, prePre, nil
}

func tokenOrderSignatures(
	raw string,
	inputs []*bt.UTXO,
	privateKey *bec.PrivateKey,
	coinInputCount int,
) ([]string, error) {
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return nil, err
	}
	if len(inputs) != len(tx.Inputs) {
		return nil, fmt.Errorf("input metadata length %d, want %d", len(inputs), len(tx.Inputs))
	}
	for i, input := range inputs {
		if input == nil {
			return nil, fmt.Errorf("nil input metadata %d", i)
		}
		tx.Inputs[i].PreviousTxScript = input.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = input.Satoshis
		if i < coinInputCount {
			tx.Inputs[i].SequenceNumber = 0xfffffffe
		}
	}
	sigs := make([]string, len(inputs))
	for i := range inputs {
		hash, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return nil, err
		}
		sig, err := privateKey.Sign(hash)
		if err != nil {
			return nil, err
		}
		sigs[i] = hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
	}
	return sigs, nil
}

func tokenOrderOwnedCode(code, address string) (string, error) {
	owned, err := BuildFTtransferCode(code, address)
	if err != nil {
		return "", err
	}
	return owned.ToHex(), nil
}

func (o *OnlineOrderBook) makeTokenOrderWithSign(
	orderType string,
	privateKey *bec.PrivateKey,
	taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID string,
) (string, error) {
	if err := tokenOrderPositiveUint64(saleVolume, "sale volume"); err != nil {
		return "", err
	}
	if err := tokenOrderPositiveUint64(unitPrice, "unit price"); err != nil {
		return "", err
	}
	if _, err := tokenOrderUint64(feeRate, "fee rate"); err != nil {
		return "", err
	}
	if !obIsValidSHA256Hex(ftaID) || !obIsValidSHA256Hex(ftbID) {
		return "", fmt.Errorf("FTA ID and FTB ID must be 32-byte hex strings")
	}
	address, err := o.addressForKey(privateKey)
	if err != nil {
		return "", err
	}
	fta, ftb, err := o.fetchTokenInfoPair(ftaID, ftbID)
	if err != nil {
		return "", err
	}
	inputID := ftaID
	inputInfo := fta
	requiredAmount := new(big.Int).Set(saleVolume)
	if orderType == "buy" {
		inputID = ftbID
		inputInfo = ftb
		requiredAmount = tokenOrderMulDiv(saleVolume, unitPrice, big.NewInt(1_000_000))
	}
	ownedCode, err := tokenOrderOwnedCode(inputInfo.CodeScript, address)
	if err != nil {
		return "", err
	}
	ftUTXOs, err := o.Client.FetchFtUTXOs(inputID, address, ownedCode, o.Network, requiredAmount)
	if err != nil {
		return "", fmt.Errorf("fetch order FT inputs: %w", err)
	}
	preTXs, prePre, err := o.fetchTokenAncestors(ftUTXOs)
	if err != nil {
		return "", err
	}
	feeUTXOs, err := o.Client.FetchUTXOs(address, o.Network)
	if err != nil {
		return "", fmt.Errorf("fetch TBC fee inputs: %w", err)
	}

	book := NewOrderBook()
	var raw string
	if orderType == "sell" {
		raw, err = book.BuildTokenSellOrderTX(
			address, taxAddress, saleVolume, unitPrice, feeRate,
			ftaID, ftbID, fta.CodeScript, ftb.CodeScript,
			feeUTXOs, ftUTXOs, preTXs,
		)
	} else {
		raw, err = book.BuildTokenBuyOrderTX(
			address, taxAddress, saleVolume, unitPrice, feeRate,
			ftaID, ftbID, fta.CodeScript, ftb.CodeScript,
			feeUTXOs, ftUTXOs, preTXs,
		)
	}
	if err != nil {
		return "", err
	}
	info, err := util.ClassifyFTScript(ftUTXOs[0].LockingScript)
	if err != nil {
		return "", err
	}
	inputs := util.FtUTXOsToUTXOs(ftUTXOs)
	inputs = append(inputs, feeUTXOs...)
	coinCount := 0
	if info.IsCoin {
		coinCount = len(ftUTXOs)
	}
	sigs, err := tokenOrderSignatures(raw, inputs, privateKey, coinCount)
	if err != nil {
		return "", err
	}
	publicKey := hex.EncodeToString(privateKey.PubKey().SerialiseCompressed())
	if orderType == "sell" {
		return book.FillSigsMakeTokenSellOrder(raw, sigs, publicKey, preTXs, prePre)
	}
	return book.FillSigsMakeTokenBuyOrder(raw, sigs, publicKey, preTXs, prePre)
}

func (o *OnlineOrderBook) MakeTokenSellOrderWithSign(
	privateKey *bec.PrivateKey,
	taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID string,
) (string, error) {
	return o.makeTokenOrderWithSign(
		"sell", privateKey, taxAddress, saleVolume, unitPrice, feeRate, ftaID, ftbID,
	)
}

func (o *OnlineOrderBook) MakeTokenBuyOrderWithSign(
	privateKey *bec.PrivateKey,
	taxAddress string,
	saleVolume, unitPrice, feeRate *big.Int,
	ftaID, ftbID string,
) (string, error) {
	return o.makeTokenOrderWithSign(
		"buy", privateKey, taxAddress, saleVolume, unitPrice, feeRate, ftaID, ftbID,
	)
}

func tokenOrderLockedFT(orderUTXO *bt.UTXO, orderPreTX *bt.Tx) (*util.FtUTXO, error) {
	if orderUTXO == nil || orderPreTX == nil {
		return nil, fmt.Errorf("order UTXO and parent transaction are required")
	}
	vout := int(orderUTXO.Vout) + 1
	if vout+1 >= len(orderPreTX.Outputs) {
		return nil, fmt.Errorf("locked FT pair is outside order parent transaction")
	}
	balance, err := util.GetFtBalanceFromTape(orderPreTX.Outputs[vout+1].LockingScript.ToHex())
	if err != nil {
		return nil, err
	}
	return &util.FtUTXO{
		TxID: orderUTXO.TxID, Vout: uint32(vout),
		LockingScript: orderPreTX.Outputs[vout].LockingScript,
		Satoshis:      orderPreTX.Outputs[vout].Satoshis,
		FtBalance:     balance,
	}, nil
}

func (o *OnlineOrderBook) cancelTokenOrderWithSign(
	privateKey *bec.PrivateKey,
	orderUTXO *bt.UTXO,
	orderType string,
) (string, error) {
	address, err := o.addressForKey(privateKey)
	if err != nil {
		return "", err
	}
	orderPreTX, err := o.Client.FetchTXRaw(hex.EncodeToString(orderUTXO.TxID), o.Network)
	if err != nil {
		return "", err
	}
	ftUTXO, err := tokenOrderLockedFT(orderUTXO, orderPreTX)
	if err != nil {
		return "", err
	}
	prePre, err := o.Client.FetchFtPrePreTxData(orderPreTX, int(ftUTXO.Vout), o.Network)
	if err != nil {
		return "", err
	}
	feeUTXOs, err := o.Client.FetchUTXOs(address, o.Network)
	if err != nil {
		return "", err
	}
	book := NewOrderBook()
	var raw string
	if orderType == "sell" {
		raw, err = book.BuildCancelTokenSellOrderTX(orderUTXO, ftUTXO, orderPreTX, feeUTXOs, o.mainnet())
	} else {
		raw, err = book.BuildCancelTokenBuyOrderTX(orderUTXO, ftUTXO, orderPreTX, feeUTXOs, o.mainnet())
	}
	if err != nil {
		return "", err
	}
	info, err := util.ClassifyFTScript(ftUTXO.LockingScript)
	if err != nil {
		return "", err
	}
	inputs := []*bt.UTXO{orderUTXO, util.FtUTXOToUTXO(ftUTXO)}
	inputs = append(inputs, feeUTXOs...)
	sigs, err := tokenOrderSignatures(raw, inputs, privateKey, 0)
	if err != nil {
		return "", err
	}
	if info.IsCoin {
		// tokenOrderSignatures' prefix form cannot express only input 1.
		tx, parseErr := bt.NewTxFromString(raw)
		if parseErr != nil {
			return "", parseErr
		}
		tx.Inputs[1].SequenceNumber = 0xfffffffe
		for i, input := range inputs {
			tx.Inputs[i].PreviousTxScript = input.LockingScript
			tx.Inputs[i].PreviousTxSatoshis = input.Satoshis
			hash, hashErr := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
			if hashErr != nil {
				return "", hashErr
			}
			sig, signErr := privateKey.Sign(hash)
			if signErr != nil {
				return "", signErr
			}
			sigs[i] = hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
		}
	}
	publicKey := hex.EncodeToString(privateKey.PubKey().SerialiseCompressed())
	if orderType == "sell" {
		return book.FillSigsCancelTokenSellOrder(raw, sigs, publicKey, orderPreTX, orderPreTX, prePre)
	}
	return book.FillSigsCancelTokenBuyOrder(raw, sigs, publicKey, orderPreTX, orderPreTX, prePre)
}

func (o *OnlineOrderBook) CancelTokenSellOrderWithSign(privateKey *bec.PrivateKey, sellUTXO *bt.UTXO) (string, error) {
	return o.cancelTokenOrderWithSign(privateKey, sellUTXO, "sell")
}

func (o *OnlineOrderBook) CancelTokenBuyOrderWithSign(privateKey *bec.PrivateKey, buyUTXO *bt.UTXO) (string, error) {
	return o.cancelTokenOrderWithSign(privateKey, buyUTXO, "buy")
}

func (o *OnlineOrderBook) MatchTokenOrderWithSign(
	privateKey *bec.PrivateKey,
	buyUTXO, sellUTXO *bt.UTXO,
	ftaFeeAddress, ftbFeeAddress string,
) (string, error) {
	address, err := o.addressForKey(privateKey)
	if err != nil {
		return "", err
	}
	buyPreTX, err := o.Client.FetchTXRaw(hex.EncodeToString(buyUTXO.TxID), o.Network)
	if err != nil {
		return "", err
	}
	sellPreTX, err := o.Client.FetchTXRaw(hex.EncodeToString(sellUTXO.TxID), o.Network)
	if err != nil {
		return "", err
	}
	buyFT, err := tokenOrderLockedFT(buyUTXO, buyPreTX)
	if err != nil {
		return "", err
	}
	sellFT, err := tokenOrderLockedFT(sellUTXO, sellPreTX)
	if err != nil {
		return "", err
	}
	buyPrePre, err := o.Client.FetchFtPrePreTxData(buyPreTX, int(buyFT.Vout), o.Network)
	if err != nil {
		return "", err
	}
	sellPrePre, err := o.Client.FetchFtPrePreTxData(sellPreTX, int(sellFT.Vout), o.Network)
	if err != nil {
		return "", err
	}
	feeUTXOs, err := o.Client.FetchUTXOs(address, o.Network)
	if err != nil {
		return "", err
	}
	return NewOrderBook().MatchTokenOrder(
		privateKey,
		buyUTXO, buyPreTX, buyFT, buyPreTX, buyPrePre,
		sellUTXO, sellPreTX, sellFT, sellPreTX, sellPrePre,
		feeUTXOs, ftaFeeAddress, ftbFeeAddress, o.mainnet(),
	)
}
