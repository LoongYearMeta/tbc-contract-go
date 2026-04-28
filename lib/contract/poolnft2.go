package contract

// Port of tbc-contract/lib/contract/poolNFT2.0.ts (4848 lines).
// Pool NFT 2.0 (linear pool) implementation.
// Legacy poolNFT (v1, non-linear) is intentionally not ported.
//
// Key design notes:
//   - Pool amounts are *big.Int.
//   - Service fee addresses mirror TS SERVICE_FEE_ADDRESS (lpPlan 1..5).
//   - GetPoolNftUnlockOffLine mirrors TS getPoolNftUnlockOffLine (offline signing).
//   - Swap/AddLP/RemoveLP/CreatePool methods require the full pool state to be
//     initialized via InitFromContractID or set manually.

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
)

const (
	poolNFT2Version       = 2
	poolNFT2DefaultFeeBPS = 35 // 万分之35
)

// PoolNFT2 线性池实例状态（对齐 poolNFT2.0 类字段）。
type PoolNFT2 struct {
	FtLpAmount      *big.Int
	FtAAmount       *big.Int
	TbcAmount       *big.Int
	FtLpPartialHash string
	FtAPartialHash  string
	FtAContractTxID string
	PoolNftCode     string
	PoolVersion     int
	ContractTxID    string
	Network         string

	ServiceFeeRate  int
	ServiceProvider string
	LpPlan          int
	WithLock        bool
	WithLockTime    bool
	TbcAmountFull   *big.Int
}

// PoolNFT2Config 构造参数（可选 contractTxid / network）。
type PoolNFT2Config struct {
	ContractTxID string
	Network      string
}

// NewPoolNFT2 创建 2.0 线性池句柄（不调用链上 API）。
func NewPoolNFT2(cfg *PoolNFT2Config) *PoolNFT2 {
	p := &PoolNFT2{
		FtLpAmount:      big.NewInt(0),
		FtAAmount:       big.NewInt(0),
		TbcAmount:       big.NewInt(0),
		TbcAmountFull:   big.NewInt(0),
		PoolVersion:     poolNFT2Version,
		ServiceFeeRate:  poolNFT2DefaultFeeBPS,
		FtAContractTxID: "",
		Network:         "mainnet",
	}
	if cfg != nil {
		p.ContractTxID = strings.TrimSpace(cfg.ContractTxID)
		if n := strings.TrimSpace(cfg.Network); n != "" {
			p.Network = n
		}
	}
	return p
}

// InitCreate 设置底层 FT 合约 txid（对齐 initCreate）。
func (p *PoolNFT2) InitCreate(ftContractTxid string) error {
	ftContractTxid = strings.TrimSpace(ftContractTxid)
	if !isSHA256Hex(ftContractTxid) {
		return fmt.Errorf("Invalid Input: ftContractTxid must be a 32-byte hash value")
	}
	p.FtAContractTxID = ftContractTxid
	return nil
}

// PoolNFT2ExtraInfo 自链上 pool NFT tape（output[1]）解析的扩展字段，对齐 getPoolNftExtraInfo。
type PoolNFT2ExtraInfo struct {
	ServiceFeeRate int
	LpPlan         int
	WithLock       bool
	WithLockTime   bool
}

// InitFromContractID 从 API + 合约交易 tape 拉取池状态（对齐 initfromContractId）。
func (p *PoolNFT2) InitFromContractID() error {
	if strings.TrimSpace(p.ContractTxID) == "" {
		return fmt.Errorf("contract txid is empty")
	}
	info, err := api.FetchPoolNFTInfo(p.ContractTxID, p.Network)
	if err != nil {
		return fmt.Errorf("fetch pool NFT info: %w", err)
	}
	p.FtLpAmount = mustDecimalBigPool(info.FtLpAmount)
	p.FtAAmount = mustDecimalBigPool(info.FtAAmount)
	p.TbcAmount = mustDecimalBigPool(info.TBCAmount)
	p.FtLpPartialHash = info.FtLpPartialHash
	p.FtAPartialHash = info.FtAPartialHash
	p.FtAContractTxID = info.FtAContractTxID
	p.PoolNftCode = info.PoolNftCode
	p.PoolVersion = info.PoolVersion
	p.ServiceProvider = info.ServiceProvider
	p.TbcAmountFull = big.NewInt(int64(info.CurrentContractSatoshi))

	if v := strings.TrimSpace(info.ServiceFeeRate); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.ServiceFeeRate = n
		}
	}

	tx, err := api.FetchTXRaw(p.ContractTxID, p.Network)
	if err != nil {
		return fmt.Errorf("fetch pool contract tx for tape: %w", err)
	}
	if len(tx.Outputs) < 2 {
		return fmt.Errorf("pool contract tx missing tape output")
	}
	tape := tx.Outputs[1].LockingScript
	extra, err := parsePoolNftTapeExtra(tape)
	if err != nil {
		return err
	}
	if extra.LpPlan > 0 {
		p.LpPlan = extra.LpPlan
	}
	p.WithLock = extra.WithLock
	p.WithLockTime = extra.WithLockTime
	if extra.ServiceFeeRate > 0 {
		p.ServiceFeeRate = extra.ServiceFeeRate
	}
	return nil
}

// ParsePoolNftTapeExtra 从 pool NFT tape 脚本解析扩展信息（对齐 poolNFT2.getPoolNftExtraInfo 的 chunks[5..8]）。
func parsePoolNftTapeExtra(tape *bscript.Script) (PoolNFT2ExtraInfo, error) {
	var out PoolNFT2ExtraInfo
	if tape == nil {
		return out, fmt.Errorf("nil tape script")
	}
	ch := tape.Chunks()
	if len(ch) <= 8 {
		return out, nil
	}
	parseChunk := func(i int) (int, bool) {
		if i >= len(ch) || ch[i].Buf == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(hex.EncodeToString(ch[i].Buf), 16, 64)
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	if v, ok := parseChunk(5); ok {
		out.ServiceFeeRate = v
	}
	if v, ok := parseChunk(6); ok {
		out.LpPlan = v
	}
	if v, ok := parseChunk(7); ok {
		out.WithLock = (v == 1)
	}
	if v, ok := parseChunk(8); ok {
		out.WithLockTime = (v == 1)
	}
	return out, nil
}

// PoolNFTDifference 对齐 TS poolNFTDifference 接口。
type PoolNFTDifference struct {
	FtLpDifference      *big.Int
	FtADifference       *big.Int
	TbcAmountDifference *big.Int
	TbcAmountFullDiff   *big.Int
}

var poolNFT2Precision = big.NewInt(1_000_000)
var poolNFT2CodeDust = big.NewInt(1000)

// UpdatePoolNFT 对齐 TS poolNFT2.updatePoolNFT。
// option: 1=LP变化, 2=TBC变化, 3=FT-A变化。
func (p *PoolNFT2) UpdatePoolNFT(increment string, ftADecimal int, option int) (*PoolNFTDifference, error) {
	ftAOld := new(big.Int).Set(p.FtAAmount)
	ftLpOld := new(big.Int).Set(p.FtLpAmount)
	tbcOld := new(big.Int).Set(p.TbcAmount)
	tbcFullOld := new(big.Int).Set(p.TbcAmountFull)

	switch option {
	case 1:
		inc := parseDecimalToBigIntPool(increment, 6)
		if err := p.updateWhenFtLpChange(inc); err != nil {
			return nil, err
		}
	case 2:
		inc := parseDecimalToBigIntPool(increment, 6)
		if err := p.updateWhenTbcAmountChange(inc); err != nil {
			return nil, err
		}
	case 3:
		inc := parseDecimalToBigIntPool(increment, ftADecimal)
		if err := p.updateWhenFtAChange(inc); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid option: %d", option)
	}

	diff := &PoolNFTDifference{}
	if p.TbcAmount.Cmp(tbcOld) > 0 {
		diff.FtLpDifference = new(big.Int).Sub(p.FtLpAmount, ftLpOld)
		diff.FtADifference = new(big.Int).Sub(p.FtAAmount, ftAOld)
		diff.TbcAmountDifference = new(big.Int).Sub(p.TbcAmount, tbcOld)
		diff.TbcAmountFullDiff = new(big.Int).Sub(p.TbcAmountFull, tbcFullOld)
	} else {
		diff.FtLpDifference = new(big.Int).Sub(ftLpOld, p.FtLpAmount)
		diff.FtADifference = new(big.Int).Sub(ftAOld, p.FtAAmount)
		diff.TbcAmountDifference = new(big.Int).Sub(tbcOld, p.TbcAmount)
		diff.TbcAmountFullDiff = new(big.Int).Sub(tbcFullOld, p.TbcAmountFull)
	}
	return diff, nil
}

func (p *PoolNFT2) updateWhenFtLpChange(increment *big.Int) error {
	if increment.Sign() == 0 {
		return nil
	}
	if increment.Sign() <= 0 || increment.Cmp(p.FtLpAmount) > 0 {
		return fmt.Errorf("increment is invalid")
	}
	ratio := new(big.Int).Mul(p.FtLpAmount, poolNFT2Precision)
	ratio.Div(ratio, increment)

	p.FtLpAmount.Sub(p.FtLpAmount, increment)

	ftASub := new(big.Int).Mul(p.FtAAmount, poolNFT2Precision)
	ftASub.Div(ftASub, ratio)
	p.FtAAmount.Sub(p.FtAAmount, ftASub)

	tbcSub := new(big.Int).Mul(p.TbcAmount, poolNFT2Precision)
	tbcSub.Div(tbcSub, ratio)
	p.TbcAmount.Sub(p.TbcAmount, tbcSub)

	tbcFullMinusDust := new(big.Int).Sub(p.TbcAmountFull, poolNFT2CodeDust)
	tbcFullSub := new(big.Int).Mul(tbcFullMinusDust, poolNFT2Precision)
	tbcFullSub.Div(tbcFullSub, ratio)
	p.TbcAmountFull.Sub(p.TbcAmountFull, tbcFullSub)
	return nil
}

func (p *PoolNFT2) updateWhenFtAChange(increment *big.Int) error {
	if increment.Sign() == 0 {
		return nil
	}
	if increment.Sign() <= 0 {
		return fmt.Errorf("increment is invalid")
	}
	if increment.Cmp(p.FtAAmount) <= 0 {
		ratio := new(big.Int).Mul(p.FtAAmount, poolNFT2Precision)
		ratio.Div(ratio, increment)

		p.FtAAmount.Add(p.FtAAmount, increment)

		ftLpAdd := new(big.Int).Mul(p.FtLpAmount, poolNFT2Precision)
		ftLpAdd.Div(ftLpAdd, ratio)
		p.FtLpAmount.Add(p.FtLpAmount, ftLpAdd)

		tbcAdd := new(big.Int).Mul(p.TbcAmount, poolNFT2Precision)
		tbcAdd.Div(tbcAdd, ratio)
		p.TbcAmount.Add(p.TbcAmount, tbcAdd)

		tbcFullAdd := new(big.Int).Mul(p.TbcAmountFull, poolNFT2Precision)
		tbcFullAdd.Div(tbcFullAdd, ratio)
		p.TbcAmountFull.Add(p.TbcAmountFull, tbcFullAdd)
	} else {
		ratio := new(big.Int).Mul(increment, poolNFT2Precision)
		ratio.Div(ratio, p.FtAAmount)

		p.FtAAmount.Add(p.FtAAmount, increment)

		ftLpAdd := new(big.Int).Mul(p.FtLpAmount, ratio)
		ftLpAdd.Div(ftLpAdd, poolNFT2Precision)
		p.FtLpAmount.Add(p.FtLpAmount, ftLpAdd)

		tbcAdd := new(big.Int).Mul(p.TbcAmount, ratio)
		tbcAdd.Div(tbcAdd, poolNFT2Precision)
		p.TbcAmount.Add(p.TbcAmount, tbcAdd)

		tbcFullAdd := new(big.Int).Mul(p.TbcAmountFull, ratio)
		tbcFullAdd.Div(tbcFullAdd, poolNFT2Precision)
		p.TbcAmountFull.Add(p.TbcAmountFull, tbcFullAdd)
	}
	return nil
}

func (p *PoolNFT2) updateWhenTbcAmountChange(increment *big.Int) error {
	if increment.Sign() == 0 {
		return nil
	}
	if increment.Sign() <= 0 {
		return fmt.Errorf("increment is invalid")
	}
	tbcFullMinusDust := new(big.Int).Sub(p.TbcAmountFull, poolNFT2CodeDust)
	if increment.Cmp(p.TbcAmount) <= 0 {
		ratio := new(big.Int).Mul(tbcFullMinusDust, poolNFT2Precision)
		ratio.Div(ratio, increment)

		p.TbcAmount.Add(p.TbcAmount, increment)

		ftLpAdd := new(big.Int).Mul(p.FtLpAmount, poolNFT2Precision)
		ftLpAdd.Div(ftLpAdd, ratio)
		p.FtLpAmount.Add(p.FtLpAmount, ftLpAdd)

		ftAAdd := new(big.Int).Mul(p.FtAAmount, poolNFT2Precision)
		ftAAdd.Div(ftAAdd, ratio)
		p.FtAAmount.Add(p.FtAAmount, ftAAdd)

		p.TbcAmountFull.Add(p.TbcAmountFull, increment)
	} else {
		ratio := new(big.Int).Mul(increment, poolNFT2Precision)
		ratio.Div(ratio, tbcFullMinusDust)

		p.TbcAmount.Add(p.TbcAmount, increment)

		ftLpAdd := new(big.Int).Mul(p.FtLpAmount, ratio)
		ftLpAdd.Div(ftLpAdd, poolNFT2Precision)
		p.FtLpAmount.Add(p.FtLpAmount, ftLpAdd)

		ftAAdd := new(big.Int).Mul(p.FtAAmount, ratio)
		ftAAdd.Div(ftAAdd, poolNFT2Precision)
		p.FtAAmount.Add(p.FtAAmount, ftAAdd)

		p.TbcAmountFull.Add(p.TbcAmountFull, increment)
	}
	return nil
}

// GetPoolNftTape 对齐 TS poolNFT2.getPoolNftTape。
func (p *PoolNFT2) GetPoolNftTape(lpPlan int, withLock, withLockTime bool) (*bscript.Script, error) {
	amountData := bigIntToUint64LEHexPool(p.FtLpAmount) + bigIntToUint64LEHexPool(p.FtAAmount) + bigIntToUint64LEHexPool(p.TbcAmount)
	feeRateHex := fmt.Sprintf("%02x", p.ServiceFeeRate)
	lpPlanHex := fmt.Sprintf("%02x", lpPlan)
	withLockHex := "00"
	if withLock {
		withLockHex = "01"
	}
	withLockTimeHex := "00"
	if withLockTime {
		withLockTimeHex = "01"
	}
	asmStr := fmt.Sprintf("OP_FALSE OP_RETURN %s%s %s %s %s %s %s %s 4e54617065",
		p.FtLpPartialHash, p.FtAPartialHash,
		amountData, p.FtAContractTxID,
		feeRateHex, lpPlanHex, withLockHex, withLockTimeHex)
	return bscript.NewFromASM(asmStr)
}

// --------------------------------------------------------------------------
// GetPoolNftUnlockOffLine
// --------------------------------------------------------------------------

// GetPoolNftUnlockOffLine generates the pool NFT unlock script offline.
// Mirrors TS poolNFT2.getPoolNftUnlockOffLine.
//
// currentTX: the transaction being built.
// currentUnlockIndex: the input index of the pool NFT input.
// poolnftPreTX: the previous pool NFT transaction.
// poolnftPrePreTX: the pre-previous pool NFT transaction.
// inputsTXs: the transactions for each FT/TBC input (index 0 = tx for input 1).
// withLock: 1 if pool has a lock output; 0 otherwise.
// option: 1=addLP, 2=removeLP, 3=swapFTtoTBC, 4=swapTBCtoFT.
// swapOption: 1 or 2 (only used for option=3,4).
func (p *PoolNFT2) GetPoolNftUnlockOffLine(
	privKey *bec.PrivateKey,
	currentTX *bt.Tx,
	currentUnlockIndex int,
	poolnftPreTX *bt.Tx,
	poolnftPrePreTX *bt.Tx,
	inputsTXs []*bt.Tx,
	withLock int,
	option int,
	swapOption int,
) (*bscript.Script, error) {
	pretxdata, err := util.GetPoolNFTPreTxdata(poolnftPreTX)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftUnlockOffLine pretxdata: %w", err)
	}
	prepretxdata, err := util.GetPoolNFTPrePreTxdata(poolnftPrePreTX)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftUnlockOffLine prepretxdata: %w", err)
	}
	currentinputsdata, err := util.GetCurrentInputsdataPool(currentTX)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftUnlockOffLine currentinputsdata: %w", err)
	}

	currentinputstxdata := ""
	for i := 1; i < len(currentTX.Inputs); i++ {
		inputsTX := inputsTXs[i-1]
		vout := int(currentTX.Inputs[i].PreviousTxOutIndex)
		if option == 3 {
			d, err := util.GetInputsTxdataSwap(inputsTX, vout)
			if err != nil {
				return nil, fmt.Errorf("GetPoolNftUnlockOffLine swap inputstxdata[%d]: %w", i, err)
			}
			currentinputstxdata = d + currentinputstxdata
		} else {
			d, err := util.GetInputsTxdata(inputsTX, vout)
			if err != nil {
				return nil, fmt.Errorf("GetPoolNftUnlockOffLine inputstxdata[%d]: %w", i, err)
			}
			currentinputstxdata += d
		}
	}
	currentinputstxdata = "51" + currentinputstxdata

	currenttxoutputsdata, err := util.GetCurrentTxOutputsDataforPool2(currentTX, option, withLock, swapOption)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftUnlockOffLine outputsdata: %w", err)
	}

	optionHex := fmt.Sprintf("%02x", option+50) // TS: option + 50

	// Determine pool code type from output[0]
	poolCodeScript := currentTX.Outputs[0].LockingScript
	poolCodeBytes := poolCodeScript.Bytes()
	chunks := poolCodeScript.Chunks()

	isLargePool := false
	if len(chunks) >= 4 {
		sub := len(chunks[len(chunks)-2].Buf) + 1
		poolCodeLength := len(poolCodeBytes) - sub
		// Check for large pool code or OP_1 (0x51) opcode at chunks[-4]
		isLargePool = poolCodeLength > 3284 || chunks[len(chunks)-4].OpcodeNum == 0x51
	}

	var unlockHex string
	if isLargePool {
		switch option {
		case 1:
			unlockHex = currentinputstxdata + currentinputsdata + currenttxoutputsdata + optionHex + prepretxdata + pretxdata
		case 2:
			unlockHex = currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
		case 3:
			if withLock != 0 {
				sig, pubKey, err := poolNFTSign(currentTX, uint32(currentUnlockIndex), privKey)
				if err != nil {
					return nil, err
				}
				unlockHex = sig + pubKey + currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
			} else {
				unlockHex = currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
			}
		case 4:
			unlockHex = currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
		default:
			return nil, fmt.Errorf("GetPoolNftUnlockOffLine: invalid option %d", option)
		}
	} else {
		sig, pubKey, err := poolNFTSign(currentTX, uint32(currentUnlockIndex), privKey)
		if err != nil {
			return nil, err
		}
		switch option {
		case 1:
			unlockHex = sig + pubKey + currentinputstxdata + currentinputsdata + currenttxoutputsdata + optionHex + prepretxdata + pretxdata
		case 2:
			unlockHex = sig + pubKey + currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
		case 3:
			unlockHex = sig + pubKey + currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
		case 4:
			unlockHex = sig + pubKey + currenttxoutputsdata + currentinputstxdata + currentinputsdata + optionHex + prepretxdata + pretxdata
		default:
			return nil, fmt.Errorf("GetPoolNftUnlockOffLine: invalid option %d", option)
		}
	}

	return bscript.NewFromHexString(unlockHex)
}

// poolNFTSign computes the DER+sighash signature + pubkey hex for a pool NFT input.
// Returns (sigHex, pubKeyHex, error) where each includes the length prefix byte.
// Mirrors TS: `${(sig.length/2).toString(16).padStart(2,'0')}${sig}` pattern.
func poolNFTSign(tx *bt.Tx, inputIdx uint32, privKey *bec.PrivateKey) (sigHex, pubKeyHex string, err error) {
	sh, err := tx.CalcInputSignatureHash(inputIdx, sighash.AllForkID)
	if err != nil {
		return "", "", err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return "", "", err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	pubBytes := privKey.PubKey().SerialiseCompressed()

	sigHex = fmt.Sprintf("%02x", len(sigBytes)) + hex.EncodeToString(sigBytes)
	pubKeyHex = fmt.Sprintf("%02x", len(pubBytes)) + hex.EncodeToString(pubBytes)
	return
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

func bigIntToUint64LEHexPool(n *big.Int) string {
	buf := make([]byte, 8)
	val := n.Uint64()
	buf[0] = byte(val)
	buf[1] = byte(val >> 8)
	buf[2] = byte(val >> 16)
	buf[3] = byte(val >> 24)
	buf[4] = byte(val >> 32)
	buf[5] = byte(val >> 40)
	buf[6] = byte(val >> 48)
	buf[7] = byte(val >> 56)
	return hex.EncodeToString(buf)
}

func parseDecimalToBigIntPool(amount string, decimal int) *big.Int {
	parts := strings.SplitN(amount, ".", 2)
	intPart := parts[0]
	fracPart := ""
	if len(parts) > 1 {
		fracPart = parts[1]
	}
	if len(fracPart) > decimal {
		fracPart = fracPart[:decimal]
	}
	for len(fracPart) < decimal {
		fracPart += "0"
	}
	combined := intPart + fracPart
	result := new(big.Int)
	result.SetString(combined, 10)
	return result
}

func mustDecimalBigPool(s string) *big.Int {
	s = strings.TrimSpace(s)
	if s == "" {
		return big.NewInt(0)
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return big.NewInt(0)
	}
	return n
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}
