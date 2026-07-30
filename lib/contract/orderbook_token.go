package contract

import (
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
)

const (
	tokenOrderPrefixLength = 1152
	tokenOrderDataLength   = 180
	tokenOrderLength       = tokenOrderPrefixLength + tokenOrderDataLength
	tokenOrderSizeHex      = "023405"
)

//go:embed asm/orderbook_token_sell.hex
var tokenSellOrderHexTemplate string

//go:embed asm/orderbook_token_buy.hex
var tokenBuyOrderHexTemplate string

// TokenOrderData is the decoded 180-byte data suffix shared by Token Order
// sell and buy contracts.
type TokenOrderData struct {
	HoldAddress    string
	SaleVolume     *big.Int
	FTAPartialHash string
	FTBPartialHash string
	FeeRate        *big.Int
	UnitPrice      *big.Int
	FTAID          string
	FTBID          string
}

func tokenOrderUint64(value *big.Int, field string) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("%s is required", field)
	}
	if value.Sign() < 0 || value.BitLen() > 64 {
		return nil, fmt.Errorf("%s must fit uint64", field)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, value.Uint64())
	return buf, nil
}

func tokenOrderHash(value, field string) ([]byte, error) {
	if !obSHA256HexPattern.MatchString(value) {
		return nil, fmt.Errorf("%s must be a 32-byte hex string", field)
	}
	return hex.DecodeString(value)
}

// BuildTokenOrderData mirrors buildTokenOrderData() in tbc-contract 1.6.5.
func (o *OrderBook) BuildTokenOrderData() (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(o.HoldAddress)
	if err != nil {
		return nil, fmt.Errorf("BuildTokenOrderData: hold address: %w", err)
	}
	holdHash, err := hex.DecodeString(addr.PublicKeyHash)
	if err != nil {
		return nil, fmt.Errorf("BuildTokenOrderData: hold address hash: %w", err)
	}
	saleVolume, err := tokenOrderUint64(o.TokenSaleVolume, "sale volume")
	if err != nil {
		return nil, err
	}
	feeRate, err := tokenOrderUint64(o.TokenFeeRate, "fee rate")
	if err != nil {
		return nil, err
	}
	unitPrice, err := tokenOrderUint64(o.TokenUnitPrice, "unit price")
	if err != nil {
		return nil, err
	}
	ftaPartial, err := tokenOrderHash(o.FtPartialHash, "FTA partial hash")
	if err != nil {
		return nil, err
	}
	ftbPartial, err := tokenOrderHash(o.FtBPartialHash, "FTB partial hash")
	if err != nil {
		return nil, err
	}
	ftaID, err := tokenOrderHash(o.FtID, "FTA ID")
	if err != nil {
		return nil, err
	}
	ftbID, err := tokenOrderHash(o.FtBID, "FTB ID")
	if err != nil {
		return nil, err
	}

	script := bscript.NewScript()
	for _, field := range [][]byte{
		holdHash,
		saleVolume,
		ftaPartial,
		ftbPartial,
		feeRate,
		unitPrice,
		ftaID,
		ftbID,
	} {
		if err := script.AppendPushData(field); err != nil {
			return nil, fmt.Errorf("BuildTokenOrderData: append field: %w", err)
		}
	}
	if script.Len() != tokenOrderDataLength {
		return nil, fmt.Errorf("BuildTokenOrderData: length %d, want %d", script.Len(), tokenOrderDataLength)
	}
	return script, nil
}

func (o *OrderBook) tokenOrderCode(template, taxAddress string) (*bscript.Script, error) {
	address, err := obAddrPKH(o.HoldAddress)
	if err != nil {
		return nil, fmt.Errorf("token order hold address: %w", err)
	}
	taxAddressHex, err := obAddrPKH(taxAddress)
	if err != nil {
		return nil, fmt.Errorf("token order tax address: %w", err)
	}
	codeHex := strings.NewReplacer(
		"${address}", address,
		"${taxAddressHex}", taxAddressHex,
		"${buyCodeSize}", tokenOrderSizeHex,
		"${sellCodeSize}", tokenOrderSizeHex,
	).Replace(strings.TrimSpace(template))
	code, err := bscript.NewFromHexString(codeHex)
	if err != nil {
		return nil, fmt.Errorf("token order code template: %w", err)
	}
	if code.Len() != tokenOrderPrefixLength {
		return nil, fmt.Errorf("token order prefix length %d, want %d", code.Len(), tokenOrderPrefixLength)
	}
	data, err := o.BuildTokenOrderData()
	if err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(code.Bytes(), data.Bytes()...)), nil
}

// GetTokenSellOrderCode mirrors getTokenSellOrderCode() in JS 1.6.5.
func (o *OrderBook) GetTokenSellOrderCode(taxAddress string) (*bscript.Script, error) {
	return o.tokenOrderCode(tokenSellOrderHexTemplate, taxAddress)
}

// GetTokenBuyOrderCode mirrors getTokenBuyOrderCode() in JS 1.6.5.
func (o *OrderBook) GetTokenBuyOrderCode(taxAddress string) (*bscript.Script, error) {
	return o.tokenOrderCode(tokenBuyOrderHexTemplate, taxAddress)
}

// GetTokenOrderData decodes the eight pushed fields at the end of a Token
// Order script. It accepts either the complete 1332-byte contract or its
// standalone 180-byte data suffix.
func GetTokenOrderData(codeHex string, mainnet bool) (*TokenOrderData, error) {
	script, err := bscript.NewFromHexString(codeHex)
	if err != nil {
		return nil, fmt.Errorf("GetTokenOrderData: decode script: %w", err)
	}
	if script.Len() < tokenOrderDataLength {
		return nil, fmt.Errorf("GetTokenOrderData: script is shorter than %d bytes", tokenOrderDataLength)
	}
	chunks := script.Chunks()
	if len(chunks) < 8 {
		return nil, fmt.Errorf("GetTokenOrderData: expected at least 8 chunks")
	}
	fields := chunks[len(chunks)-8:]
	wantLengths := []int{20, 8, 32, 32, 8, 8, 32, 32}
	for i, want := range wantLengths {
		if len(fields[i].Buf) != want {
			return nil, fmt.Errorf("GetTokenOrderData: field %d length %d, want %d", i, len(fields[i].Buf), want)
		}
	}
	addr, err := bscript.NewAddressFromPublicKeyHash(fields[0].Buf, mainnet)
	if err != nil {
		return nil, fmt.Errorf("GetTokenOrderData: hold address: %w", err)
	}
	readAmount := func(field []byte) *big.Int {
		return new(big.Int).SetUint64(binary.LittleEndian.Uint64(field))
	}
	return &TokenOrderData{
		HoldAddress:    addr.AddressString,
		SaleVolume:     readAmount(fields[1].Buf),
		FTAPartialHash: hex.EncodeToString(fields[2].Buf),
		FTBPartialHash: hex.EncodeToString(fields[3].Buf),
		FeeRate:        readAmount(fields[4].Buf),
		UnitPrice:      readAmount(fields[5].Buf),
		FTAID:          hex.EncodeToString(fields[6].Buf),
		FTBID:          hex.EncodeToString(fields[7].Buf),
	}, nil
}

// UpdateTokenSaleVolume replaces only the uint64 sale-volume field and
// preserves the rest of the contract byte-for-byte.
func UpdateTokenSaleVolume(codeHex string, newVolume *big.Int) (string, error) {
	volume, err := tokenOrderUint64(newVolume, "sale volume")
	if err != nil {
		return "", err
	}
	if _, err := GetTokenOrderData(codeHex, true); err != nil {
		return "", err
	}
	code, err := hex.DecodeString(codeHex)
	if err != nil {
		return "", err
	}
	dataStart := len(code) - tokenOrderDataLength
	copy(code[dataStart+22:dataStart+30], volume)
	return hex.EncodeToString(code), nil
}

// GetTokenOrderUnlock mirrors the Token Order use of the common OrderBook
// current/pre-transaction serialization and selects match/cancel option 1.
func GetTokenOrderUnlock(currentTX, preTX *bt.Tx, preTxVout int) (string, error) {
	preTxData, err := util.GetPreTxdataOB(preTX, preTxVout, 1)
	if err != nil {
		return "", err
	}
	currentTxData, err := util.GetCurrentTxOutputsDataOBFixed(currentTX, 12)
	if err != nil {
		return "", err
	}
	return currentTxData + preTxData + "51", nil
}
