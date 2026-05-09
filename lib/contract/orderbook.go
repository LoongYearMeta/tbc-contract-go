package contract

// Port of tbc-contract/lib/contract/orderBook.ts.
// OrderBook supports sell/buy order creation, cancellation, signature filling, and match.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	obOrderDataEncodedLen = 114

	// FT code script lengths (from TS orderBook.ts)
	obCoinLength  = 2012
	obFTv2Partial = 1856
	obCoinPartial = 1984

	// Order code base hex templates (without taxAddress or orderData appended).
	// These match the hex literals in TS orderBook.ts getSellOrderCode / getBuyOrderCode.
	// The placeholder strings are replaced at runtime:
	//   ${taxHex}     → "14" + taxAddress pubKeyHash (21 bytes / 42 hex chars)
	//   ${ftCodeSize} → "dc07" (coin) or "5c07" (FT v2)
	//   ${addrHex}    → "14" + holdAddress pubKeyHash (21 bytes / 42 hex chars)
	sellOrderBaseHexFmt = "765187637556ba01207f77547f75817654958f01289351947901157f597f7701217f597f597f517f7701207f756b517f77816b517f77816b517f776b517f776b7654958f01289379816b7654958f0128935394796b54958f0127935294796b006b7600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575686ca87e6b007e7e7e7e7e7e7e7e7e7ea86c7e7eaa56ba01207f7588006b7600879163a86c7e7e6bbb6c7e7e6bbb6c7e7e6c6c75756b676d6d6d760087916378787e6c6c6c7e7b7c886c55798194547901157f597f5879527a517f77886c76537a517f77887c01217f6c76537a517f77887c597f6c76537a517f7781887c597f6c76537a517f7781887c517f7701207f756c7c886b6b6b6b6b6bbb6c7e7e6b676d6d6c6c6c75756b6868760119885279537f7701147f756c6c6c76547a8700886b6b6bbb6c7e7e6b760119885279537f7701147f756c6c567981008763527a75677b"
	// After taxHex insertion: ...8868766b557981946b6bbb6c7e7e6b760119885279537f7701147f756c6c6c6c76557a8700886b6b5579819400886bbb6c7e7e6b5279 02${ftCodeSize} 88768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a93 5979 02${ftCodeSize} 8857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f79885979517f7701147f75${taxHex}885f79517f7701147f75886c6c527a950340420f9676527a950340420f96547988537a947b886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9${addrHex}88ad68516a07ffffffffffffff

	buyOrderBaseHexFmt = "765187637556ba01207f77547f75817654958f01289351947901157f597f7701217f597f597f517f7701207f756b517f77816b517f77816b517f776b517f776b7654958f0128935394796b54958f0127935294796b006b7600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575686ca87e6b007e7e7e7e7e7e7e7e7e7ea86c7e7eaa56ba01207f7588006b760087636d6d6d7600879163bb6c7e7e676d6d6c686c6c75756b67577957797e6c6c6c7e7b7c88537902"
	// After ftCodeSize: 88788255947f054654617065886c6c765879886b6b537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935679517f7701147f756b6b6ba86c7e7e6bbb6c7e7e6b527901157f597f6c6c6c6c76577a517f7788547a01217f6c76537a517f77887c597f6c76537a517f7781887c597f6c76537a517f7781767c88527a517f7701207f756c7c88587a517f7781517a950340420f96567a7c886b6b6b6b6b6bbb6c6c5279a97c887e7e6b68760119885279537f7701147f756c6c76537a8700886b6bbb6c7e7e6b760119885279537f7701147f756c55798100876377677c${taxHex}88685479816b6bbb6c7e7e6b760119885279537f7701147f756c6c6c76547a8878577981936c6c5279950340420f96547a886c527a950340420f967c6b7c6b6b6bbb6c7e7e6b5279 02${ftCodeSize} 88768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a93 5979 02${ftCodeSize} 8857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f79885979517f7701147f75${taxHex}885f79517f7701147f75870088537a94527a9400886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9${addrHex}88ad68516a30ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
)

var obSHA256HexPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// OrderBook handles sell/buy order construction and matching.
// Mirrors TS class OrderBook.
type OrderBook struct {
	Type            string // "buy" or "sell"
	HoldAddress     string
	SaleVolume      uint64
	FeeRate         uint64
	UnitPrice       uint64
	FtPartialHash   string // 32-byte hex partial hash of the FT code script
	FtID            string // 32-byte hex txid of the FT contract
	ContractVersion int
	BuyCodeDust     uint64
}

// OrderData carries the decoded order parameters from an order code script.
type OrderData struct {
	HoldAddress   string
	SaleVolume    uint64
	FtPartialHash string
	FeeRate       uint64
	UnitPrice     uint64
	FtID          string
}

// NewOrderBook constructs an OrderBook with defaults matching TS constructor().
func NewOrderBook() *OrderBook {
	return &OrderBook{
		ContractVersion: 1,
		BuyCodeDust:     300,
	}
}

// ---------------------------------------------------------------------------
// Script builders
// ---------------------------------------------------------------------------

// obAddrPKH returns "14" + pubKeyHash hex for the given address.
func obAddrPKH(address string) (string, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return "", err
	}
	return "14" + addr.PublicKeyHash, nil
}

// GetSellOrderCode mirrors getSellOrderCode(isCoin, taxAddress) in TS.
func (o *OrderBook) GetSellOrderCode(isCoin bool, taxAddress string) (*bscript.Script, error) {
	return o.buildOrderCodeScript("sell", isCoin, taxAddress)
}

// GetBuyOrderCode mirrors getBuyOrderCode(isCoin, taxAddress) in TS.
func (o *OrderBook) GetBuyOrderCode(isCoin bool, taxAddress string) (*bscript.Script, error) {
	return o.buildOrderCodeScript("buy", isCoin, taxAddress)
}

func (o *OrderBook) buildOrderCodeScript(orderType string, isCoin bool, taxAddress string) (*bscript.Script, error) {
	addrHex, err := obAddrPKH(o.HoldAddress)
	if err != nil {
		return nil, fmt.Errorf("buildOrderCodeScript: holdAddress: %w", err)
	}
	taxHex, err := obAddrPKH(taxAddress)
	if err != nil {
		return nil, fmt.Errorf("buildOrderCodeScript: taxAddress: %w", err)
	}
	ftCodeSize := "5c07" // FT v2 (1884 bytes = 0x075c)
	if isCoin {
		ftCodeSize = "dc07" // Coin (2012 bytes = 0x07dc)
	}

	var base string
	if orderType == "sell" {
		// Build sell order hex (see TS getSellOrderCode)
		base = sellOrderBaseHexFmt + taxHex +
			"8868766b557981946b6bbb6c7e7e6b760119885279537f7701147f756c6c6c6c76557a8700886b6b5579819400886bbb6c7e7e6b527902" + ftCodeSize +
			"88768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935979 02" + ftCodeSize +
			"8857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f79885979517f7701147f75" + taxHex +
			"885f79517f7701147f75886c6c527a950340420f9676527a950340420f96547988537a947b886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9" + addrHex +
			"88ad68516a07ffffffffffffff"
	} else {
		// Build buy order hex (see TS getBuyOrderCode)
		base = buyOrderBaseHexFmt + ftCodeSize +
			"88788255947f054654617065886c6c765879886b6b537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935679517f7701147f756b6b6ba86c7e7e6bbb6c7e7e6b527901157f597f6c6c6c6c76577a517f7788547a01217f6c76537a517f77887c597f6c76537a517f7781887c597f6c76537a517f7781767c88527a517f7701207f756c7c88587a517f7781517a950340420f96567a7c886b6b6b6b6b6bbb6c6c5279a97c887e7e6b68760119885279537f7701147f756c6c76537a8700886b6bbb6c7e7e6b760119885279537f7701147f756c55798100876377677c" + taxHex +
			"88685479816b6bbb6c7e7e6b760119885279537f7701147f756c6c6c76547a8878577981936c6c5279950340420f96547a886c527a950340420f967c6b7c6b6b6bbb6c7e7e6b527902" + ftCodeSize +
			"88768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935979 02" + ftCodeSize +
			"8857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f79885979517f7701147f75" + taxHex +
			"885f79517f7701147f75870088537a94527a9400886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9" + addrHex +
			"88ad68516a30ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	}

	// Strip any spaces added for readability in the string literals above.
	cleanHex := ""
	for _, c := range base {
		if c != ' ' {
			cleanHex += string(c)
		}
	}

	orderDataHex, err := o.BuildOrderDataHex()
	if err != nil {
		return nil, err
	}
	return bscript.NewFromHexString(cleanHex + orderDataHex)
}

// BuildOrderDataHex mirrors buildOrderData() in TS, returns the 114-byte order data as hex.
func (o *OrderBook) BuildOrderDataHex() (string, error) {
	if !obIsValidAddress(o.HoldAddress) {
		return "", fmt.Errorf("invalid hold address")
	}
	if !obIsValidSHA256Hex(o.FtID) || !obIsValidSHA256Hex(o.FtPartialHash) {
		return "", fmt.Errorf("invalid FT hash (ftID or ftPartialHash)")
	}
	addr, err := bscript.NewAddressFromString(o.HoldAddress)
	if err != nil {
		return "", err
	}
	addressHash, err := hex.DecodeString(addr.PublicKeyHash)
	if err != nil {
		return "", err
	}
	ftPartial, err := hex.DecodeString(o.FtPartialHash)
	if err != nil {
		return "", err
	}
	ftID, err := hex.DecodeString(o.FtID)
	if err != nil {
		return "", err
	}

	data := make([]byte, 0, obOrderDataEncodedLen)
	data = append(data, 0x14)
	data = append(data, addressHash...)
	data = append(data, 0x08)
	data = obWriteUint64LE(data, o.SaleVolume)
	data = append(data, 0x20)
	data = append(data, ftPartial...)
	data = append(data, 0x08)
	data = obWriteUint64LE(data, o.FeeRate)
	data = append(data, 0x08)
	data = obWriteUint64LE(data, o.UnitPrice)
	data = append(data, 0x20)
	data = append(data, ftID...)

	return hex.EncodeToString(data), nil
}

// GetOrderData mirrors static getOrderData(codeScript) in TS.
// Parses the last 6 script chunks (the appended order data).
func GetOrderData(codeScriptHex string, mainnet bool) (*OrderData, error) {
	s, err := bscript.NewFromHexString(codeScriptHex)
	if err != nil {
		return nil, err
	}
	chunks := s.Chunks()
	if len(chunks) < 6 {
		return nil, fmt.Errorf("GetOrderData: too few chunks (%d)", len(chunks))
	}
	base := len(chunks) - 6

	addrHash := chunks[base].Buf
	if len(addrHash) != 20 {
		return nil, fmt.Errorf("GetOrderData: holdAddress hash wrong length %d", len(addrHash))
	}
	addrObj, err := bscript.NewAddressFromPublicKeyHash(addrHash, mainnet)
	if err != nil {
		return nil, fmt.Errorf("GetOrderData: holdAddress: %w", err)
	}
	saleVolBuf := chunks[base+1].Buf
	if len(saleVolBuf) != 8 {
		return nil, fmt.Errorf("GetOrderData: saleVolume wrong length %d", len(saleVolBuf))
	}
	ftPartial := chunks[base+2].Buf
	feeRateBuf := chunks[base+3].Buf
	unitPriceBuf := chunks[base+4].Buf
	ftIDBytes := chunks[base+5].Buf

	if len(feeRateBuf) != 8 || len(unitPriceBuf) != 8 || len(ftPartial) != 32 || len(ftIDBytes) != 32 {
		return nil, fmt.Errorf("GetOrderData: unexpected chunk lengths")
	}

	return &OrderData{
		HoldAddress:   addrObj.AddressString,
		SaleVolume:    binary.LittleEndian.Uint64(saleVolBuf),
		FtPartialHash: hex.EncodeToString(ftPartial),
		FeeRate:       binary.LittleEndian.Uint64(feeRateBuf),
		UnitPrice:     binary.LittleEndian.Uint64(unitPriceBuf),
		FtID:          hex.EncodeToString(ftIDBytes),
	}, nil
}

// UpdateSaleVolume mirrors static updateSaleVolume(codeScript, newSaleVolume) in TS.
// Modifies the sale-volume chunk in place (2nd of the last-6 chunks = chunk[len-5]).
func UpdateSaleVolume(codeScriptHex string, newSaleVolume uint64) (string, error) {
	s, err := bscript.NewFromHexString(codeScriptHex)
	if err != nil {
		return "", err
	}
	chunks := s.Chunks()
	if len(chunks) < 6 {
		return "", fmt.Errorf("UpdateSaleVolume: too few chunks (%d)", len(chunks))
	}
	saleVolIdx := len(chunks) - 5 // dataStartIndex + 1
	newBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(newBuf, newSaleVolume)
	chunks[saleVolIdx].Buf = newBuf

	newScript, err := bscript.FromChunks(chunks)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(newScript.Bytes()), nil
}

// PlaceHolderP2PKHOutput mirrors static placeHolderP2PKHOutput() in TS.
func PlaceHolderP2PKHOutput() (*bscript.Script, error) {
	return bscript.NewFromASM("OP_FALSE OP_RETURN ffffffffffffffffffffffffffffffffffffffffffff")
}

// ---------------------------------------------------------------------------
// ComputeFtPartialHash computes the FT partial hash from the code script hex.
// isCoin = true → coin_partial_offset (1984); false → ft_v2_partial_offset (1856).
func ComputeFtPartialHash(ftCodeScriptHex string, isCoin bool) (string, error) {
	buf, err := hex.DecodeString(ftCodeScriptHex)
	if err != nil {
		return "", err
	}
	partialOffset := obFTv2Partial
	if isCoin {
		partialOffset = obCoinPartial
	}
	if len(buf) < partialOffset {
		return "", fmt.Errorf("ComputeFtPartialHash: script too short (%d < %d)", len(buf), partialOffset)
	}
	return partialsha256.CalculatePartialHash(buf[:partialOffset]), nil
}

// IsCoinScript reports whether a code script hex belongs to a stablecoin (2012 bytes vs 1884).
func IsCoinScript(codeScriptHex string) bool {
	return len(codeScriptHex)/2 == obCoinLength
}

// ---------------------------------------------------------------------------
// Order unlock
// ---------------------------------------------------------------------------

// GetOrderUnlock mirrors getOrderUnlock(currentTX, preTX, preTxVout) in TS.
// Returns the raw unlock script bytes (hex).
func GetOrderUnlock(currentTX *bt.Tx, preTX *bt.Tx, preTxVout int) (string, error) {
	preTxData, err := util.GetPreTxdataOB(preTX, preTxVout, 1)
	if err != nil {
		return "", err
	}
	currentTxData, err := util.GetCurrentTxOutputsDataOB(currentTX)
	if err != nil {
		return "", err
	}
	// optionHex = "51" (OP_1)
	return currentTxData + preTxData + "51", nil
}

// ---------------------------------------------------------------------------
// BuildSellOrderTX
// ---------------------------------------------------------------------------

// BuildSellOrderTX mirrors buildSellOrderTX(...) in TS (unsigned).
// ftCodeScriptHex is the full FT code script hex (used to compute partial hash).
// Returns unsigned tx hex.
func (o *OrderBook) BuildSellOrderTX(
	holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate uint64,
	ftID string,
	ftCodeScriptHex string,
	utxos []*bt.UTXO,
) (string, error) {
	if !obIsValidAddress(holdAddress) || !obIsValidAddress(taxAddress) {
		return "", fmt.Errorf("BuildSellOrderTX: invalid holdAddress or taxAddress")
	}
	if saleVolume == 0 || unitPrice == 0 {
		return "", fmt.Errorf("BuildSellOrderTX: saleVolume and unitPrice must be positive")
	}
	if !obIsValidSHA256Hex(ftID) {
		return "", fmt.Errorf("BuildSellOrderTX: ftID must be a valid SHA256 hash string")
	}

	isCoin := IsCoinScript(ftCodeScriptHex)
	partialHash, err := ComputeFtPartialHash(ftCodeScriptHex, isCoin)
	if err != nil {
		return "", err
	}

	o.Type = "sell"
	o.HoldAddress = holdAddress
	o.SaleVolume = saleVolume
	o.UnitPrice = unitPrice
	o.FeeRate = feeRate
	o.FtID = ftID
	o.FtPartialHash = partialHash

	sellCode, err := o.GetSellOrderCode(isCoin, taxAddress)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: sellCode, Satoshis: saleVolume})
	if err := tx.ChangeToAddress(holdAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	est := tx.JSEstimateSize()
	if adjustErr := tx.AdjustImplicitFeeToTarget(obTargetFee(est)); adjustErr != nil {
		return "", adjustErr
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// BuildCancelSellOrderTX
// ---------------------------------------------------------------------------

// BuildCancelSellOrderTX mirrors buildCancelSellOrderTX(...) in TS (unsigned).
func (o *OrderBook) BuildCancelSellOrderTX(
	sellUTXO *bt.UTXO,
	utxos []*bt.UTXO,
	mainnet bool,
) (string, error) {
	if sellUTXO == nil {
		return "", fmt.Errorf("BuildCancelSellOrderTX: sellUTXO cannot be nil")
	}
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(sellData.HoldAddress, sellUTXO.Satoshis); err != nil {
		return "", err
	}
	if err := tx.ChangeToAddress(sellData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	est := tx.JSEstimateSize()
	if adjustErr := tx.AdjustImplicitFeeToTarget(obTargetFee(est)); adjustErr != nil {
		return "", adjustErr
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// FillSigsSellOrder
// ---------------------------------------------------------------------------

// FillSigsSellOrder mirrors fillSigsSellOrder(txRaw, sigs, publicKey, type) in TS.
// orderType is "make" or "cancel".
func (o *OrderBook) FillSigsSellOrder(sellOrderTxRaw string, sigs []string, publicKey string, orderType string) (string, error) {
	rawBytes, err := hex.DecodeString(sellOrderTxRaw)
	if err != nil {
		return "", fmt.Errorf("FillSigsSellOrder: invalid tx hex")
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	for i, sig := range sigs {
		var asm string
		if orderType == "cancel" && i == 0 {
			asm = fmt.Sprintf("%s %s OP_2", sig, publicKey)
		} else {
			asm = fmt.Sprintf("%s %s", sig, publicKey)
		}
		s, err := bscript.NewFromASM(asm)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), s); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// MakeSellOrderWithSign
// ---------------------------------------------------------------------------

// MakeSellOrderWithSign mirrors makeSellOrder_privateKeyOnline (offline version):
// builds and signs a sell order using the provided private key.
func (o *OrderBook) MakeSellOrderWithSign(
	privKey *bec.PrivateKey,
	taxAddress string,
	saleVolume, unitPrice, feeRate uint64,
	ftID, ftCodeScriptHex string,
	utxos []*bt.UTXO,
) (string, error) {
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return "", err
	}
	holdAddress := addr.AddressString

	txRaw, err := o.BuildSellOrderTX(holdAddress, taxAddress, saleVolume, unitPrice, feeRate, ftID, ftCodeScriptHex, utxos)
	if err != nil {
		return "", err
	}
	rawBytes, err := hex.DecodeString(txRaw)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	// `tx.Bytes()` doesn't serialise PreviousTxScript / PreviousTxSatoshis
	// (they're not part of the wire format), so the round-tripped tx has
	// empty input metadata. unlocker.Simple dispatches on
	// PreviousTxScript.ScriptType() and bails with "currently only p2pkh
	// supported" when it's empty. Repopulate from the supplied UTXOs (caller
	// guarantees order matches) before signing.
	if len(tx.Inputs) != len(utxos) {
		return "", fmt.Errorf("MakeSellOrderWithSign: input count %d != utxos %d", len(tx.Inputs), len(utxos))
	}
	for i, u := range utxos {
		tx.Inputs[i].PreviousTxScript = u.LockingScript
		tx.Inputs[i].PreviousTxSatoshis = u.Satoshis
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// CancelSellOrderWithSign
// ---------------------------------------------------------------------------

// CancelSellOrderWithSign builds and signs a sell-order cancellation.
func (o *OrderBook) CancelSellOrderWithSign(
	privKey *bec.PrivateKey,
	sellUTXO *bt.UTXO,
	utxos []*bt.UTXO,
	mainnet bool,
) (string, error) {
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	if err := tx.PayToAddress(sellData.HoldAddress, sellUTXO.Satoshis); err != nil {
		return "", err
	}
	// First pass — provisional change so the unlock sighash commits to a
	// non-zero change output. The JS-aligned fee estimator counts the
	// sell-order custom-unlock input as 41 B, vastly under-counting the real
	// ~3-4 KB unlock; we adjust against actual signed bytes below.
	if err := tx.ChangeToAddress(sellData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}

	pubKeyHex := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	signAll := func() error {
		// Input 0: sell order unlock with OP_2 suffix
		sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
		if err != nil {
			return err
		}
		sig, err := privKey.Sign(sh)
		if err != nil {
			return err
		}
		sigHex := hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
		cancelASM := fmt.Sprintf("%s %s OP_2", sigHex, pubKeyHex)
		cancelScript, err := bscript.NewFromASM(cancelASM)
		if err != nil {
			return err
		}
		if err := tx.InsertInputUnlockingScript(0, cancelScript); err != nil {
			return err
		}
		// Sign remaining P2PKH inputs
		ctx := context.Background()
		for i := 1; i < len(tx.Inputs); i++ {
			su := &unlocker.Simple{PrivateKey: privKey}
			us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
				InputIdx:     uint32(i),
				SigHashFlags: sighash.AllForkID,
			})
			if err != nil {
				return err
			}
			if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
				return err
			}
		}
		return nil
	}
	if err := signAll(); err != nil {
		return "", err
	}

	// Second pass: real-bytes fee, re-sign every SIGHASH_ALL input. Unlock
	// byte length is deterministic across re-signs so this converges.
	if err := adjustFeeFromActualSize(tx, 80); err != nil {
		return "", err
	}
	if err := signAll(); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// BuildBuyOrderTX
// ---------------------------------------------------------------------------

// BuildBuyOrderTX mirrors buildBuyOrderTX(...) in TS (unsigned).
// ftutxos: FT UTXOs being locked into the buy order.
// preTXs: parent transactions of each FT UTXO (for tape script lookup).
// Returns unsigned tx hex.
func (o *OrderBook) BuildBuyOrderTX(
	holdAddress, taxAddress string,
	saleVolume, unitPrice, feeRate uint64,
	ftID string,
	utxos []*bt.UTXO,
	ftutxos []*util.FtUTXO,
	preTXs []*bt.Tx,
) (string, error) {
	if !obIsValidAddress(holdAddress) || !obIsValidAddress(taxAddress) {
		return "", fmt.Errorf("BuildBuyOrderTX: invalid holdAddress or taxAddress")
	}
	if saleVolume == 0 || unitPrice == 0 {
		return "", fmt.Errorf("BuildBuyOrderTX: saleVolume and unitPrice must be positive")
	}
	if len(ftutxos) == 0 {
		return "", fmt.Errorf("BuildBuyOrderTX: ftutxos required")
	}
	if !obIsValidSHA256Hex(ftID) {
		return "", fmt.Errorf("BuildBuyOrderTX: ftID must be a valid SHA256 hash string")
	}

	ftCodeScriptHex := hex.EncodeToString(ftutxos[0].LockingScript.Bytes())
	isCoin := IsCoinScript(ftCodeScriptHex)
	partialHash, err := ComputeFtPartialHash(ftCodeScriptHex, isCoin)
	if err != nil {
		return "", err
	}

	o.Type = "buy"
	o.HoldAddress = holdAddress
	o.SaleVolume = saleVolume
	o.UnitPrice = unitPrice
	o.FeeRate = feeRate
	o.FtID = ftID
	o.FtPartialHash = partialHash

	buyOrder, err := o.GetBuyOrderCode(isCoin, taxAddress)
	if err != nil {
		return "", err
	}

	precision := uint64(1000000)
	ftAmount := new(big.Int).Mul(new(big.Int).SetUint64(saleVolume), new(big.Int).SetUint64(unitPrice))
	ftAmount.Div(ftAmount, new(big.Int).SetUint64(precision))

	tapeAmountSetIn := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		tapeAmountSetIn[i] = new(big.Int).Set(fu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, fu.FtBalance)
	}
	amountHex, changeHex := BuildTapeAmount(ftAmount, tapeAmountSetIn)

	// Tape script is the output following the FT code output in preTX.
	if len(preTXs) == 0 || len(preTXs[0].Outputs) <= int(ftutxos[0].Vout)+1 {
		return "", fmt.Errorf("BuildBuyOrderTX: preTXs[0] has insufficient outputs")
	}
	ftTapeHex := hex.EncodeToString(preTXs[0].Outputs[int(ftutxos[0].Vout)+1].LockingScript.Bytes())

	// buyOrderHash160: sha256ripemd160(sha256(buyOrder)) — used as FT code "address"
	buyOrderHash160 := hex.EncodeToString(crypto.Hash160(crypto.Sha256(buyOrder.Bytes())))
	ftCodeBuy, err := BuildFTtransferCode(ftCodeScriptHex, buyOrderHash160)
	if err != nil {
		return "", err
	}
	ftTapeBuy, err := BuildFTtransferTape(ftTapeHex, amountHex)
	if err != nil {
		return "", err
	}
	ftCodeDust := ftutxos[0].Satoshis

	tx := newFTTx()
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs(ftutxos)...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: buyOrder, Satoshis: o.BuyCodeDust})
	tx.AddOutput(&bt.Output{LockingScript: ftCodeBuy, Satoshis: ftCodeDust})
	tx.AddOutput(&bt.Output{LockingScript: ftTapeBuy, Satoshis: 0})

	if ftAmount.Cmp(tapeAmountSum) < 0 {
		ftCodeChange, err := BuildFTtransferCode(ftCodeScriptHex, holdAddress)
		if err != nil {
			return "", err
		}
		ftTapeChange, err := BuildFTtransferTape(ftTapeHex, changeHex)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: ftCodeChange, Satoshis: ftCodeDust})
		tx.AddOutput(&bt.Output{LockingScript: ftTapeChange, Satoshis: 0})
	}

	if err := tx.ChangeToAddress(holdAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	// Fee estimate: TS does tx.getEstimateSize() + ftutxos.length * 2000
	est := tx.JSEstimateSize() + len(ftutxos)*2000
	if adjustErr := tx.AdjustImplicitFeeToTarget(obTargetFee(est)); adjustErr != nil {
		return "", adjustErr
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// BuildCancelBuyOrderTX
// ---------------------------------------------------------------------------

// BuildCancelBuyOrderTX mirrors buildCancelBuyOrderTX(...) in TS (unsigned).
func (o *OrderBook) BuildCancelBuyOrderTX(
	buyUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	ftPreTX *bt.Tx,
	utxos []*bt.UTXO,
	mainnet bool,
) (string, error) {
	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", err
	}

	tapeAmountSetIn := []*big.Int{new(big.Int).Set(ftUTXO.FtBalance)}
	tapeSum := new(big.Int).Set(ftUTXO.FtBalance)
	// FT input is at index 1 of the cancel tx (input 0 is the buy order),
	// so tape slot[1] holds the amount. TS calls buildTapeAmount(sum, set, 1)
	// for this reason — using the 0-default produced a tape mismatching
	// what the FT-v2 swap contract expects, failing OP_EQUALVERIFY at
	// broadcast.
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(tapeSum, tapeAmountSetIn, 1)
	zeroChange := make([]byte, 48)
	if changeHex != hex.EncodeToString(zeroChange) {
		return "", fmt.Errorf("BuildCancelBuyOrderTX: change amount is not zero")
	}

	if len(ftPreTX.Outputs) <= int(ftUTXO.Vout)+1 {
		return "", fmt.Errorf("BuildCancelBuyOrderTX: ftPreTX insufficient outputs")
	}
	ftTapeHex := hex.EncodeToString(ftPreTX.Outputs[int(ftUTXO.Vout)+1].LockingScript.Bytes())
	ftCodeScriptHex := hex.EncodeToString(ftUTXO.LockingScript.Bytes())
	ftCodeOut, err := BuildFTtransferCode(ftCodeScriptHex, buyData.HoldAddress)
	if err != nil {
		return "", err
	}
	ftTapeOut, err := BuildFTtransferTape(ftTapeHex, amountHex)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(buyUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs([]*util.FtUTXO{ftUTXO})...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftCodeOut, Satoshis: ftUTXO.Satoshis})
	tx.AddOutput(&bt.Output{LockingScript: ftTapeOut, Satoshis: 0})
	if err := tx.ChangeToAddress(buyData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	est := tx.JSEstimateSize() + 2000
	if adjustErr := tx.AdjustImplicitFeeToTarget(obTargetFee(est)); adjustErr != nil {
		return "", adjustErr
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// CancelBuyOrderWithSign — convenience: builds + signs cancel-buy in-place.
// ---------------------------------------------------------------------------

// CancelBuyOrderWithSign mirrors TS cancelBuyOrder_privateKeyOnline: builds
// the cancel-buy tx and signs all three input categories (buy-order custom
// unlock, FT swap unlock, P2PKH fee inputs) in-place, returning the final
// raw hex ready to broadcast.
//
// In-place signing avoids the BuildCancelBuyOrderTX → hex → re-parse round
// trip that drops PreviousTxScript/PreviousTxSatoshis (needed for BIP143
// sighash + unlocker dispatch). Uses the two-pass actual-bytes fee pattern:
// provisional change → sign → adjust against `len(tx.Bytes())` → re-sign.
func (o *OrderBook) CancelBuyOrderWithSign(
	privKey *bec.PrivateKey,
	buyUTXO *bt.UTXO,
	ftUTXO *util.FtUTXO,
	buyPreTX *bt.Tx,
	ftPrePreTxData string,
	utxos []*bt.UTXO,
	mainnet bool,
) (string, error) {
	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", err
	}

	tapeAmountSetIn := []*big.Int{new(big.Int).Set(ftUTXO.FtBalance)}
	tapeSum := new(big.Int).Set(ftUTXO.FtBalance)
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(tapeSum, tapeAmountSetIn, 1)
	zeroChange := make([]byte, 48)
	if changeHex != hex.EncodeToString(zeroChange) {
		return "", fmt.Errorf("CancelBuyOrderWithSign: change amount is not zero")
	}

	ftPreTX := buyPreTX
	if len(ftPreTX.Outputs) <= int(ftUTXO.Vout)+1 {
		return "", fmt.Errorf("CancelBuyOrderWithSign: ftPreTX insufficient outputs")
	}
	ftTapeHex := hex.EncodeToString(ftPreTX.Outputs[int(ftUTXO.Vout)+1].LockingScript.Bytes())
	ftCodeScriptHex := hex.EncodeToString(ftUTXO.LockingScript.Bytes())
	isCoin := IsCoinScript(ftCodeScriptHex)

	ftCodeOut, err := BuildFTtransferCode(ftCodeScriptHex, buyData.HoldAddress)
	if err != nil {
		return "", err
	}
	ftTapeOut, err := BuildFTtransferTape(ftTapeHex, amountHex)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(buyUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs([]*util.FtUTXO{ftUTXO})...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftCodeOut, Satoshis: ftUTXO.Satoshis})
	tx.AddOutput(&bt.Output{LockingScript: ftTapeOut, Satoshis: 0})

	// Provisional change so unlock sighashes commit to a non-zero output.
	if err := tx.ChangeToAddress(buyData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}

	if isCoin {
		tx.Inputs[1].SequenceNumber = 4294967294
	}
	pubKeyHex := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	ft := &FT{ContractTxid: buyData.FtID, CodeScript: ftCodeScriptHex, TapeScript: ftTapeHex}

	signAll := func() error {
		// Input 0: buy-order custom unlock (sig + pubKey + OP_2).
		sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
		if err != nil {
			return err
		}
		sig0, err := privKey.Sign(sh)
		if err != nil {
			return err
		}
		sig0Hex := hex.EncodeToString(append(sig0.Serialise(), byte(sighash.AllForkID)))
		cancelASM := fmt.Sprintf("%s %s OP_2", sig0Hex, pubKeyHex)
		cancelScript, err := bscript.NewFromASM(cancelASM)
		if err != nil {
			return err
		}
		if err := tx.InsertInputUnlockingScript(0, cancelScript); err != nil {
			return err
		}
		// Input 1: FT swap unlock (BUY ORDER as contractTX, FT v2).
		swapUnlock, err := ft.GetFTunlockSwap(privKey, tx, ftPreTX, ftPrePreTxData, buyPreTX, 1, int(ftUTXO.Vout), 2, isCoin)
		if err != nil {
			return err
		}
		if err := tx.InsertInputUnlockingScript(1, swapUnlock); err != nil {
			return err
		}
		// Inputs 2..: P2PKH fee inputs.
		ctx := context.Background()
		for i := 2; i < len(tx.Inputs); i++ {
			su := &unlocker.Simple{PrivateKey: privKey}
			us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
				InputIdx:     uint32(i),
				SigHashFlags: sighash.AllForkID,
			})
			if err != nil {
				return err
			}
			if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
				return err
			}
		}
		return nil
	}
	if err := signAll(); err != nil {
		return "", err
	}
	// Second pass: real-bytes fee, re-sign every SIGHASH_ALL input.
	if err := adjustFeeFromActualSize(tx, 80); err != nil {
		return "", err
	}
	if err := signAll(); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// FillSigsMakeBuyOrder / FillSigsCancelBuyOrder
// ---------------------------------------------------------------------------

// FillSigsMakeBuyOrder mirrors fillSigsMakeBuyOrder in TS.
// sigs[i] covers ftutxos[i] (FT inputs); remaining sigs cover P2PKH fee inputs.
func (o *OrderBook) FillSigsMakeBuyOrder(
	buyOrderTxRaw string,
	sigs []string,
	publicKey string,
	preTXs []*bt.Tx,
	prepreTxData []string,
	isCoin bool,
) (string, error) {
	rawBytes, err := hex.DecodeString(buyOrderTxRaw)
	if err != nil {
		return "", fmt.Errorf("FillSigsMakeBuyOrder: invalid tx hex")
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	if len(preTXs) == 0 {
		return "", fmt.Errorf("FillSigsMakeBuyOrder: preTXs required")
	}

	for i := 0; i < len(preTXs) && i < len(sigs); i++ {
		vout := int(tx.Inputs[i].PreviousTxOutIndex)
		us, err := StaticGetFTunlock(sigs[i], publicKey, tx, preTXs[i], prepreTxData[i], i, vout, isCoin)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}
	// Remaining inputs (P2PKH fee inputs)
	for i := len(preTXs); i < len(tx.Inputs) && i < len(sigs); i++ {
		asm := fmt.Sprintf("%s %s", sigs[i], publicKey)
		s, err := bscript.NewFromASM(asm)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), s); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// FillSigsCancelBuyOrder mirrors fillSigsCancelBuyOrder in TS.
func (o *OrderBook) FillSigsCancelBuyOrder(
	buyOrderTxRaw string,
	sigs []string,
	publicKey string,
	buyPreTX *bt.Tx,
	ftPreTX *bt.Tx,
	ftPrePreTxData string,
	isCoin bool,
) (string, error) {
	rawBytes, err := hex.DecodeString(buyOrderTxRaw)
	if err != nil {
		return "", fmt.Errorf("FillSigsCancelBuyOrder: invalid tx hex")
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	if len(sigs) < 2 {
		return "", fmt.Errorf("FillSigsCancelBuyOrder: need at least 2 sigs")
	}

	// Input 0: buy order cancel (OP_2 suffix)
	cancelASM := fmt.Sprintf("%s %s OP_2", sigs[0], publicKey)
	cancelScript, err := bscript.NewFromASM(cancelASM)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, cancelScript); err != nil {
		return "", err
	}

	// Input 1: FT swap unlock
	vout1 := int(tx.Inputs[1].PreviousTxOutIndex)
	us1, err := StaticGetFTunlockSwap(sigs[1], publicKey, tx, ftPreTX, ftPrePreTxData, buyPreTX, 1, vout1, 2, isCoin)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(1, us1); err != nil {
		return "", err
	}

	// Remaining P2PKH inputs
	for i := 2; i < len(tx.Inputs) && i < len(sigs); i++ {
		asm := fmt.Sprintf("%s %s", sigs[i], publicKey)
		s, err := bscript.NewFromASM(asm)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), s); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// MatchOrder
// ---------------------------------------------------------------------------

// MatchOrder mirrors matchOrder(...) in TS.
// utxos: P2PKH fee inputs; the function handles partial fills.
// ftPreTX: parent tx of ftUTXO (for tape); ftPrePreTxData: pre-pre txdata string.
func (o *OrderBook) MatchOrder(
	privKey *bec.PrivateKey,
	buyUTXO *bt.UTXO, buyPreTX *bt.Tx,
	ftUTXO *util.FtUTXO, ftPreTX *bt.Tx, ftPrePreTxData string,
	sellUTXO *bt.UTXO, sellPreTX *bt.Tx,
	utxos []*bt.UTXO,
	ftFeeAddress, tbcFeeAddress string,
	mainnet bool,
) (string, error) {
	precision := uint64(1000000)

	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", fmt.Errorf("MatchOrder: parse buy order: %w", err)
	}
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), mainnet)
	if err != nil {
		return "", fmt.Errorf("MatchOrder: parse sell order: %w", err)
	}

	// Calculate matched amounts (mirror TS matchOrder math)
	matchedTBC := buyData.SaleVolume
	if sellData.SaleVolume < matchedTBC {
		matchedTBC = sellData.SaleVolume
	}
	tbcTax := matchedTBC * buyData.FeeRate / precision
	tbcBuyer := matchedTBC - tbcTax
	newSellVolume := sellData.SaleVolume - matchedTBC

	ftPay := matchedTBC * sellData.UnitPrice / precision
	ftTax := ftPay * sellData.FeeRate / precision
	ftSeller := ftPay - ftTax
	newBuyVolume := buyData.SaleVolume - matchedTBC

	ftBalance := ftUTXO.FtBalance
	ftCodeScriptHex := hex.EncodeToString(ftUTXO.LockingScript.Bytes())
	isCoin := IsCoinScript(ftCodeScriptHex)

	if len(ftPreTX.Outputs) <= int(ftUTXO.Vout)+1 {
		return "", fmt.Errorf("MatchOrder: ftPreTX insufficient outputs")
	}
	ftTapeHex := hex.EncodeToString(ftPreTX.Outputs[int(ftUTXO.Vout)+1].LockingScript.Bytes())

	// FT Seller amounts
	tapeAmountSetIn := []*big.Int{new(big.Int).Set(ftBalance)}
	ftSellerAmountHex, _ := BuildTapeAmountWithFtInputIndex(new(big.Int).SetUint64(ftSeller), tapeAmountSetIn, 1)
	// FT Tax amounts (remaining after seller)
	remaining := new(big.Int).Sub(ftBalance, new(big.Int).SetUint64(ftSeller))
	tapeAmountSetIn2 := []*big.Int{new(big.Int).Set(remaining)}
	ftTaxAmountHex, changeHex := BuildTapeAmountWithFtInputIndex(new(big.Int).SetUint64(ftTax), tapeAmountSetIn2, 1)

	tx := newFTTx()
	if err := tx.FromUTXOs(buyUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(util.FtUTXOsToUTXOs([]*util.FtUTXO{ftUTXO})...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}

	// FT Seller output
	ftSellerCode, err := BuildFTtransferCode(ftCodeScriptHex, sellData.HoldAddress)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftSellerCode, Satoshis: ftUTXO.Satoshis})
	ftSellerTape, err := BuildFTtransferTape(ftTapeHex, ftSellerAmountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftSellerTape, Satoshis: 0})

	// FT Tax output
	ftTaxCode, err := BuildFTtransferCode(ftCodeScriptHex, ftFeeAddress)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftTaxCode, Satoshis: ftUTXO.Satoshis})
	ftTaxTape, err := BuildFTtransferTape(ftTapeHex, ftTaxAmountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ftTaxTape, Satoshis: 0})

	// TBC Buyer output
	if err := tx.PayToAddress(buyData.HoldAddress, tbcBuyer); err != nil {
		return "", err
	}

	// TBC Tax output
	if buyData.FeeRate == 0 && tbcTax == 0 {
		placeholder, _ := PlaceHolderP2PKHOutput()
		tx.AddOutput(&bt.Output{LockingScript: placeholder, Satoshis: 0})
	} else if tbcTax < 10 {
		return "", fmt.Errorf("MatchOrder: TBC tax amount below dust limit")
	} else {
		if err := tx.PayToAddress(tbcFeeAddress, tbcTax); err != nil {
			return "", err
		}
	}

	// Fee change: TS computes inputsFee - fee - 1300 manually
	var inputsFee uint64
	for _, u := range utxos {
		inputsFee += u.Satoshis
	}
	est := tx.JSEstimateSize() + 2*1000 + 2000
	fee := obTargetFee(est)
	feeChangeAddr, err := obAddressFromUTXO(utxos[0], mainnet)
	if err != nil {
		return "", fmt.Errorf("MatchOrder: derive fee change address: %w", err)
	}
	// TS emits this output unconditionally (`tx.to(addr, inputsFee-fee-1300)`).
	// Skipping it on feeChange<=0 would change the output count and thus the
	// sighash for the FT/orderbook custom-unlock inputs. If feeChange is not
	// positive, the caller hasn't supplied enough fee inputs; surface that.
	feeChange := int64(inputsFee) - int64(fee) - 1300
	if feeChange <= 0 {
		return "", fmt.Errorf("MatchOrder: insufficient fee inputs (inputsFee=%d, fee=%d, change=%d)", inputsFee, fee, feeChange)
	}
	if err := tx.PayToAddress(feeChangeAddr, uint64(feeChange)); err != nil {
		return "", err
	}

	// Partial fill
	if newSellVolume > 0 {
		newSellCodeHex, err := UpdateSaleVolume(sellUTXO.LockingScript.String(), newSellVolume)
		if err != nil {
			return "", err
		}
		newSellScript, err := bscript.NewFromHexString(newSellCodeHex)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: newSellScript, Satoshis: newSellVolume})
	} else if newBuyVolume > 0 {
		ftPayBig := new(big.Int).SetUint64(ftPay)
		ftBalCopy := new(big.Int).Set(ftBalance)
		if ftBalCopy.Cmp(ftPayBig) > 0 {
			newBuyCodeHex, err := UpdateSaleVolume(buyUTXO.LockingScript.String(), newBuyVolume)
			if err != nil {
				return "", err
			}
			newBuyScript, err := bscript.NewFromHexString(newBuyCodeHex)
			if err != nil {
				return "", err
			}
			tx.AddOutput(&bt.Output{LockingScript: newBuyScript, Satoshis: o.BuyCodeDust})

			// FT change to buy-order hash160
			buyHash160 := hex.EncodeToString(crypto.Hash160(crypto.Sha256(newBuyScript.Bytes())))
			ftChangeCode, err := BuildFTtransferCode(ftCodeScriptHex, buyHash160)
			if err != nil {
				return "", err
			}
			tx.AddOutput(&bt.Output{LockingScript: ftChangeCode, Satoshis: ftUTXO.Satoshis})
			ftChangeTape, err := BuildFTtransferTape(ftTapeHex, changeHex)
			if err != nil {
				return "", err
			}
			tx.AddOutput(&bt.Output{LockingScript: ftChangeTape, Satoshis: 0})
		}
	}

	// Set order unlock scripts
	buyUnlockHex, err := GetOrderUnlock(tx, buyPreTX, int(buyUTXO.Vout))
	if err != nil {
		return "", err
	}
	buyUnlockScript, err := bscript.NewFromHexString(buyUnlockHex)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, buyUnlockScript); err != nil {
		return "", err
	}

	// FT swap unlock (input 1): use buyData.FtID as the FT contract ID (mirrors TS: new FT(buyData.ftID))
	ftInstance := &FT{ContractTxid: buyData.FtID}
	ftSwapUnlock, err := ftInstance.GetFTunlockSwap(privKey, tx, ftPreTX, ftPrePreTxData, buyPreTX, 1, int(ftUTXO.Vout), 2, isCoin)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(1, ftSwapUnlock); err != nil {
		return "", err
	}

	sellUnlockHex, err := GetOrderUnlock(tx, sellPreTX, int(sellUTXO.Vout))
	if err != nil {
		return "", err
	}
	sellUnlockScript, err := bscript.NewFromHexString(sellUnlockHex)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(2, sellUnlockScript); err != nil {
		return "", err
	}

	// Sign remaining P2PKH fee inputs
	ctx := context.Background()
	for i := 3; i < len(tx.Inputs); i++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(i),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// obTargetFee mirrors TS `txSize < 1000 ? 80 : Math.ceil(txSize/1000*80)`.
// Uses integer ceiling (sz*80+999)/1000 instead of math.Ceil(float64) so that
// satoshi-precision arithmetic never goes through a float64 representation
// (which only has 53-bit mantissa precision).
func obTargetFee(estimatedBytes int) int {
	if estimatedBytes < 1000 {
		return 80
	}
	return (estimatedBytes*80 + 999) / 1000
}

func obIsValidSHA256Hex(s string) bool {
	return obSHA256HexPattern.MatchString(s)
}

func obIsValidAddress(addr string) bool {
	ok, _ := bscript.ValidateAddress(addr)
	return ok
}

func obWriteUint64LE(dst []byte, n uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, n)
	return append(dst, b...)
}

// obAddressFromUTXO extracts the P2PKH address from a fee UTXO's locking script.
func obAddressFromUTXO(u *bt.UTXO, mainnet bool) (string, error) {
	if u == nil || u.LockingScript == nil {
		return "", fmt.Errorf("nil UTXO or locking script")
	}
	pkh, err := u.LockingScript.PublicKeyHash()
	if err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromPublicKeyHash(pkh, mainnet)
	if err != nil {
		return "", err
	}
	return addr.AddressString, nil
}
