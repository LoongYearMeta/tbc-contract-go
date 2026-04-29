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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/util/partialsha256"
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
	lpHex, err := bigIntToUint64LEHexPool(p.FtLpAmount)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftTape FtLpAmount: %w", err)
	}
	aHex, err := bigIntToUint64LEHexPool(p.FtAAmount)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftTape FtAAmount: %w", err)
	}
	tbcHex, err := bigIntToUint64LEHexPool(p.TbcAmount)
	if err != nil {
		return nil, fmt.Errorf("GetPoolNftTape TbcAmount: %w", err)
	}
	amountData := lpHex + aHex + tbcHex
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

// bigIntToUint64LEHexPool serializes a non-negative big.Int as 8 LE bytes
// and returns its hex string. Returns an error if n is negative or exceeds
// uint64 — silently truncating would corrupt the pool tape.
var maxUint64BI = new(big.Int).SetUint64(^uint64(0))

// snapshotPoolAmounts captures pool reserve values for a deferred rollback
// on the error path of UpdatePoolNFT-using methods (Increase/Consume/Swap/...).
// Each PoolNFT2 instance is mutable through these methods; if any downstream
// API/build/sign step fails, the receiver would otherwise be left in a
// half-updated state and a retry would compound the error.
func snapshotPoolAmounts(p *PoolNFT2) (ftA, ftLp, tbc, tbcFull *big.Int) {
	if p.FtAAmount != nil {
		ftA = new(big.Int).Set(p.FtAAmount)
	}
	if p.FtLpAmount != nil {
		ftLp = new(big.Int).Set(p.FtLpAmount)
	}
	if p.TbcAmount != nil {
		tbc = new(big.Int).Set(p.TbcAmount)
	}
	if p.TbcAmountFull != nil {
		tbcFull = new(big.Int).Set(p.TbcAmountFull)
	}
	return
}

// restorePoolAmounts reverts the receiver to the snapshot taken by
// snapshotPoolAmounts. Skips fields whose snapshot was nil (the original
// receiver field was nil too).
func restorePoolAmounts(p *PoolNFT2, ftA, ftLp, tbc, tbcFull *big.Int) {
	if ftA != nil {
		p.FtAAmount = ftA
	}
	if ftLp != nil {
		p.FtLpAmount = ftLp
	}
	if tbc != nil {
		p.TbcAmount = tbc
	}
	if tbcFull != nil {
		p.TbcAmountFull = tbcFull
	}
}

func bigIntToUint64LEHexPool(n *big.Int) (string, error) {
	if n == nil {
		return "", fmt.Errorf("bigIntToUint64LEHexPool: nil amount")
	}
	if n.Sign() < 0 {
		return "", fmt.Errorf("bigIntToUint64LEHexPool: negative amount %s", n.String())
	}
	if n.Cmp(maxUint64BI) > 0 {
		return "", fmt.Errorf("bigIntToUint64LEHexPool: amount %s exceeds uint64", n.String())
	}
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
	return hex.EncodeToString(buf), nil
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

// --------------------------------------------------------------------------
// Service fee addresses (mirrors TS SERVICE_FEE_ADDRESS)
// --------------------------------------------------------------------------

var poolNFT2ServiceFeeAddresses = map[int]string{
	1: "13oCEJaqyyiC8iRrfup6PDL2GKZ3xQrsZL",
	2: "1Fa6Uy64Ub4qNdB896zX2pNMx4a8zMhtCy",
	3: "125fTLNsraQxTYqT4EeQNF2ggzcqicveKL",
	4: "19DetoaaohQkjFVJ6oGXd83xhZYQSbpE1g",
	5: "15EKrhuD8Yf3SfhjAgbizYqfnBbKh9ZMZ7",
}

// eaterAddress is the Bitcoin eater address for burning tokens.
const eaterAddress = "1BitcoinEaterAddressDontSendf59kuE"

func getServiceFeeAddress(lpPlan int) (string, error) {
	addr, ok := poolNFT2ServiceFeeAddresses[lpPlan]
	if !ok {
		return "", fmt.Errorf("invalid lpPlan: %d", lpPlan)
	}
	return addr, nil
}

// --------------------------------------------------------------------------
// FT length constants (mirrors TS ft_v1_length, ft_v2_length, coin_length)
// --------------------------------------------------------------------------

const (
	ftV2Length        = 1884
	coinLength        = 2012
	ftV1PartialOffset = 1536
	ftV2PartialOffset = 1856
	coinPartialOffset = 1984
)

// ftVersionFromCodeLen returns (ftVersion, isCoin) from codeScript byte length.
func ftVersionFromCodeLen(codeLen int) (ftVersion int, isCoin bool) {
	if codeLen == coinLength {
		return 2, true
	}
	if codeLen == ftV2Length {
		return 2, false
	}
	return 1, false
}

// ftCodeSizeHex returns the 2-byte LE hex for the FT code size used in pool script.
func ftCodeSizeHex(isCoin bool, ftVersion int) string {
	if isCoin {
		return "dc07"
	}
	if ftVersion == 1 {
		return "1c06"
	}
	return "5c07"
}

// poolNFTCodeSHA256 returns SHA256(poolNftCode) as hex.
func poolNFTCodeSHA256(poolNftCodeHex string) (string, error) {
	raw, err := hex.DecodeString(poolNftCodeHex)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(crypto.Sha256(raw)), nil
}

// poolNFTSHA256thenHash160 computes SHA256 first, then Hash160 of the SHA256.
// Mirrors TS: tbc.crypto.Hash.sha256ripemd160(tbc.crypto.Hash.sha256(buf))
func poolNFTSHA256thenHash160(poolNftCodeHex string) (string, error) {
	raw, err := hex.DecodeString(poolNftCodeHex)
	if err != nil {
		return "", err
	}
	h256 := crypto.Sha256(raw)
	h160 := crypto.Hash160(h256)
	return hex.EncodeToString(h160), nil
}

// isLockByCodeLen returns 1 if poolCode hex length > 6600 (mirrors TS isLock).
func isLockByCodeLen(poolNftCodeHex string) int {
	if len(poolNftCodeHex) > 6600 {
		return 1
	}
	return 0
}

// utxoHexFromTxIDVout encodes txid (reversed) + vout as 36-byte LE hex.
func utxoHexFromTxIDVout(txid string, vout uint32) (string, error) {
	txidBytes, err := hex.DecodeString(txid)
	if err != nil {
		return "", err
	}
	// reverse
	for i, j := 0, len(txidBytes)-1; i < j; i, j = i+1, j-1 {
		txidBytes[i], txidBytes[j] = txidBytes[j], txidBytes[i]
	}
	voutBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(voutBytes, vout)
	return hex.EncodeToString(append(txidBytes, voutBytes...)), nil
}

// pubKeyHashFromAddress extracts the 20-byte pubKeyHash hex from a TBC address.
func pubKeyHashFromAddress(address string) (string, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return "", err
	}
	return addr.PublicKeyHash, nil
}

// poolGetSize mirrors TS getSize(n) → 1-byte or 3-byte length encoding.
func poolGetSize(n int) []byte {
	if n < 0xfd {
		return []byte{byte(n)}
	}
	b := make([]byte, 3)
	b[0] = 0xfd
	binary.LittleEndian.PutUint16(b[1:], uint16(n))
	return b
}

// --------------------------------------------------------------------------
// getPoolNftCode — mirrors TS poolNFT2.getPoolNftCode
// --------------------------------------------------------------------------

// getPoolNftCode builds the pool NFT locking script.
// txid/vout identify the source UTXO; lpPlan 1..5; ftVersion 1 or 2;
// tag is an arbitrary UTF-8 tag (default "NULL"); isCoin for stablecoin FT.
func (p *PoolNFT2) getPoolNftCode(txid string, vout uint32, lpPlan, ftVersion int, tag string, isCoin bool) (*bscript.Script, error) {
	utxoHex, err := utxoHexFromTxIDVout(txid, vout)
	if err != nil {
		return nil, err
	}
	sfAddr, err := getServiceFeeAddress(lpPlan)
	if err != nil {
		return nil, err
	}
	pumpPKH, err := pubKeyHashFromAddress(sfAddr)
	if err != nil {
		return nil, err
	}
	codeSize := ftCodeSizeHex(isCoin, ftVersion)
	if tag == "" {
		tag = "NULL"
	}
	tagLenHex := fmt.Sprintf("%02x", len(tag))
	tagHex := hex.EncodeToString([]byte(tag))

	asm := fmt.Sprintf(
		`OP_4 OP_PICK OP_BIN2NUM OP_TOALTSTACK OP_1 OP_PICK OP_3 OP_SPLIT OP_NIP 0x01 0x20 OP_SPLIT 0x01 0x20 OP_SPLIT OP_1 OP_SPLIT OP_NIP OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_TOALTSTACK OP_BIN2NUM OP_TOALTSTACK OP_BIN2NUM OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_1 OP_PICK OP_TOALTSTACK OP_CAT OP_CAT OP_SHA256 OP_CAT OP_1 OP_PICK 0x01 0x24 OP_SPLIT OP_DROP OP_TOALTSTACK OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_HASH256 OP_6 OP_PUSH_META 0x01 0x20 OP_SPLIT OP_DROP OP_EQUALVERIFY OP_1 OP_PICK OP_TOALTSTACK OP_CAT OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP 0x01 0x20 OP_SPLIT OP_DROP OP_3 OP_ROLL OP_EQUALVERIFY OP_SWAP OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUAL OP_IF OP_DROP OP_ELSE 0x24 0x%s OP_EQUALVERIFY OP_ENDIF OP_DUP OP_1 OP_EQUAL OP_IF OP_DROP OP_DUP OP_0 OP_EQUAL OP_IF OP_TOALTSTACK OP_ELSE OP_DUP 0x01 0x19 OP_EQUALVERIFY OP_PARTIAL_HASH OP_CAT OP_TOALTSTACK OP_ENDIF OP_DUP OP_0 OP_EQUAL OP_IF OP_DROP OP_ELSE OP_2 OP_PICK 0x02 0x%s OP_EQUALVERIFY OP_4 OP_PICK OP_1 OP_SPLIT OP_NIP 0x01 0x14 OP_SPLIT OP_DROP OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_HASH160 OP_SWAP OP_TOALTSTACK OP_EQUAL OP_0 OP_EQUALVERIFY OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_CAT OP_CAT OP_TOALTSTACK OP_ENDIF OP_2 OP_PICK 0x02 0x%s OP_EQUALVERIFY OP_DUP OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_NIP OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_SWAP OP_BIN2NUM OP_ADD OP_SWAP OP_BIN2NUM OP_ADD OP_SWAP OP_BIN2NUM OP_ADD OP_SWAP OP_BIN2NUM OP_ADD OP_SWAP OP_BIN2NUM OP_ADD OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_7 OP_PICK OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_DROP OP_FROMALTSTACK OP_FROMALTSTACK OP_EQUALVERIFY OP_FROMALTSTACK OP_EQUALVERIFY OP_4 OP_PICK OP_HASH160 OP_FROMALTSTACK OP_EQUALVERIFY OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_TRUE OP_RETURN 0x%s 0x%s 0x05 0x32436f6465`,
		utxoHex, codeSize, codeSize, pumpPKH,
		tagLenHex, tagHex,
	)
	return bscript.NewFromASM(asm)
}

// --------------------------------------------------------------------------
// getFtlpCode — mirrors TS poolNFT2.getFtlpCode
// --------------------------------------------------------------------------

func (p *PoolNFT2) getFtlpCode(poolNftCodeHash, address string, tapeSize int, isCoin bool, ftVersion int) (*bscript.Script, error) {
	pkh, err := pubKeyHashFromAddress(address)
	if err != nil {
		return nil, err
	}
	hashField := pkh + "00"
	tapeSizeHex := hex.EncodeToString(poolGetSize(tapeSize))

	pre := fmt.Sprintf(
		`OP_9 OP_PICK OP_TOALTSTACK OP_1 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_5 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_4 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_3 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_2 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_1 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_0 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_FROMALTSTACK OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_3 OP_PICK OP_1 OP_SPLIT OP_NIP 0x01 0x14 OP_SPLIT OP_DROP OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_TOALTSTACK OP_SHA256 OP_FROMALTSTACK OP_CAT OP_CAT OP_HASH256 OP_6 OP_PUSH_META 0x01 0x20 OP_SPLIT OP_DROP OP_EQUALVERIFY OP_DUP OP_HASH160 OP_FROMALTSTACK OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUAL OP_IF OP_TOALTSTACK OP_PARTIAL_HASH OP_ELSE OP_TOALTSTACK OP_PARTIAL_HASH OP_DUP 0x20 0x%s OP_EQUALVERIFY OP_ENDIF OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_7 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_1 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_0 OP_EQUALVERIFY OP_DROP OP_FROMALTSTACK OP_FROMALTSTACK OP_SHA256 OP_7 OP_PUSH_META OP_EQUAL OP_NIP`,
		poolNftCodeHash,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
	)
	preScript, err := bscript.NewFromASM(pre)
	if err != nil {
		return nil, fmt.Errorf("getFtlpCode pre: %w", err)
	}

	// last part: padding + OP_DROP OP_RETURN 0x15 hash 0x05 0x02436f6465
	var paddingHex string
	var pushOpcode string
	if isCoin {
		// 577 bytes of 0xff → PUSHDATA2 0x0241
		paddingHex = strings.Repeat("ff", 577)
		pushOpcode = "4d0241"
	} else if ftVersion == 2 {
		// 449 bytes of 0xff → PUSHDATA2 0x01c1
		paddingHex = strings.Repeat("ff", 449)
		pushOpcode = "4d01c1"
	} else {
		// 130 bytes of 0xff → PUSHDATA1 0x82
		paddingHex = strings.Repeat("ff", 130)
		pushOpcode = "4c82"
	}

	// Build last part as raw hex:
	// pushOpcode + padding + 75(OP_DROP) 6a(OP_RETURN) 15 hash 05 02436f6465
	hashBytes := hashField
	lastHex := pushOpcode + paddingHex + "756a15" + hashBytes + "05" + "02436f6465"
	lastBytes, err := hex.DecodeString(lastHex)
	if err != nil {
		return nil, fmt.Errorf("getFtlpCode last hex decode: %w", err)
	}

	preBytes := preScript.Bytes()
	combined := append(preBytes, lastBytes...)
	return bscript.NewFromBytes(combined), nil
}

// --------------------------------------------------------------------------
// getFtlpCodeWithLockTime — mirrors TS poolNFT2.getFtlpCodeWithLockTime
// --------------------------------------------------------------------------

func (p *PoolNFT2) getFtlpCodeWithLockTime(poolNftCodeHash, address string, tapeSize int, isCoin bool, ftVersion int) (*bscript.Script, error) {
	pkh, err := pubKeyHashFromAddress(address)
	if err != nil {
		return nil, err
	}
	hashField := pkh + "00"
	tapeSizeHex := hex.EncodeToString(poolGetSize(tapeSize))

	pre := fmt.Sprintf(
		`OP_9 OP_PICK OP_TOALTSTACK OP_1 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_1 OP_SPLIT OP_NIP OP_4 OP_SPLIT OP_DROP OP_BIN2NUM OP_2 OP_PUSH_META OP_BIN2NUM OP_LESSTHANOREQUAL OP_1 OP_EQUALVERIFY OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_5 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_4 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_3 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_2 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_1 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_0 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x20 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_FROMALTSTACK OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_3 OP_PICK OP_1 OP_SPLIT OP_NIP 0x01 0x14 OP_SPLIT OP_DROP OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_TOALTSTACK OP_SHA256 OP_FROMALTSTACK OP_CAT OP_CAT OP_HASH256 OP_6 OP_PUSH_META 0x01 0x20 OP_SPLIT OP_4 OP_SPLIT OP_NIP OP_BIN2NUM 0x04 0xffffffff OP_BIN2NUM OP_NUMNOTEQUAL OP_1 OP_EQUALVERIFY OP_EQUALVERIFY OP_DUP OP_HASH160 OP_FROMALTSTACK OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUAL OP_IF OP_TOALTSTACK OP_PARTIAL_HASH OP_ELSE OP_TOALTSTACK OP_PARTIAL_HASH OP_DUP 0x20 0x%s OP_EQUALVERIFY OP_ENDIF OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_FROMALTSTACK OP_DUP OP_TOALTSTACK OP_EQUALVERIFY OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_HASH256 OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_SWAP OP_TOALTSTACK OP_EQUALVERIFY OP_ENDIF OP_7 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_FROMALTSTACK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_1 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_0 OP_EQUALVERIFY OP_DROP OP_FROMALTSTACK OP_FROMALTSTACK OP_SHA256 OP_7 OP_PUSH_META OP_EQUAL OP_NIP`,
		poolNftCodeHash,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
		tapeSizeHex, tapeSizeHex,
	)
	preScript, err := bscript.NewFromASM(pre)
	if err != nil {
		return nil, fmt.Errorf("getFtlpCodeWithLockTime pre: %w", err)
	}

	// Padding sizes differ from getFtlpCode (with lock time variant)
	var paddingHex string
	var pushOpcode string
	if isCoin {
		// 577 bytes → PUSHDATA2 0x0241
		paddingHex = strings.Repeat("ff", 577)
		pushOpcode = "4d0241"
	} else if ftVersion == 2 {
		// 425 bytes → PUSHDATA2 0x01a9
		paddingHex = strings.Repeat("ff", 425)
		pushOpcode = "4d01a9"
	} else {
		// 106 bytes → PUSHDATA1 0x6a
		paddingHex = strings.Repeat("ff", 106)
		pushOpcode = "4c6a"
	}

	hashBytes := hashField
	lastHex := pushOpcode + paddingHex + "756a15" + hashBytes + "05" + "02436f6465"
	lastBytes, err := hex.DecodeString(lastHex)
	if err != nil {
		return nil, fmt.Errorf("getFtlpCodeWithLockTime last hex decode: %w", err)
	}

	preBytes := preScript.Bytes()
	combined := append(preBytes, lastBytes...)
	return bscript.NewFromBytes(combined), nil
}

// --------------------------------------------------------------------------
// updatePoolNftTape — fetches live tape and replaces amount chunk
// --------------------------------------------------------------------------

// updatePoolNftTape fetches the live pool NFT tape script from chain
// (output[1] of contractTxID) and replaces ONLY the 24-byte amount chunk
// (chunk[3]) with the current FtLpAmount/FtAAmount/TbcAmount values.
// Chunks 4..N (lpPlan/withLock/withLockTime/serviceFeeRate/etc.) are
// preserved verbatim. Mirrors TS updatePoolNftTape, which patches the
// amount chunk in place rather than re-deriving the whole tape from
// instance state.
//
// Re-deriving the tape from `p.LpPlan`/`p.WithLock`/`p.WithLockTime` was
// unsafe: those fields are caches and can drift from the on-chain truth,
// producing a tape with a different sha256 than the one the pool's
// unlock script expects in OP_PARTIAL_HASH.
func (p *PoolNFT2) updatePoolNftTape() (*bscript.Script, error) {
	tx, err := api.FetchTXRaw(p.ContractTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("updatePoolNftTape fetchTX: %w", err)
	}
	if len(tx.Outputs) < 2 {
		return nil, fmt.Errorf("updatePoolNftTape: pool tx missing tape output")
	}
	lpHex, err := bigIntToUint64LEHexPool(p.FtLpAmount)
	if err != nil {
		return nil, fmt.Errorf("updatePoolNftTape FtLpAmount: %w", err)
	}
	aHex, err := bigIntToUint64LEHexPool(p.FtAAmount)
	if err != nil {
		return nil, fmt.Errorf("updatePoolNftTape FtAAmount: %w", err)
	}
	tbcHex, err := bigIntToUint64LEHexPool(p.TbcAmount)
	if err != nil {
		return nil, fmt.Errorf("updatePoolNftTape TbcAmount: %w", err)
	}
	amountBytes, err := hex.DecodeString(lpHex + aHex + tbcHex)
	if err != nil {
		return nil, err
	}
	if len(amountBytes) != 24 {
		return nil, fmt.Errorf("updatePoolNftTape: expected 24 amount bytes, got %d", len(amountBytes))
	}

	liveTape := tx.Outputs[1].LockingScript
	chunks := liveTape.Chunks()
	if len(chunks) < 4 {
		return nil, fmt.Errorf("updatePoolNftTape: tape has fewer than 4 chunks")
	}
	if len(chunks[3].Buf) != 24 {
		return nil, fmt.Errorf("updatePoolNftTape: tape chunk[3] is %d bytes, expected 24", len(chunks[3].Buf))
	}
	chunks[3].Buf = amountBytes
	chunks[3].Len = len(amountBytes)
	out, err := bscript.FromChunks(chunks)
	if err != nil {
		return nil, fmt.Errorf("updatePoolNftTape: re-serialize chunks: %w", err)
	}
	return out, nil
}

// --------------------------------------------------------------------------
// fetchFtlpUTXOList / fetchFtlpUTXO helpers
// --------------------------------------------------------------------------

// fetchFtlpUTXOList fetches all FT-LP UTXOs for the given address.
// Uses the pool's current FTA info to build the ftlpCode script hash for the query.
func (p *PoolNFT2) fetchFtlpUTXOList(address string) ([]*api.LpUTXO, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("fetchFtlpUTXOList FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	tapeLen := len(ftaInfo.TapeScript) / 2

	poolNftHash, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return nil, err
	}

	var ftlpCodeScript *bscript.Script
	if p.WithLockTime {
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftHash, address, tapeLen, isCoin, ftVersion)
	} else {
		ftlpCodeScript, err = p.getFtlpCode(poolNftHash, address, tapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return nil, err
	}

	ftlpCodeHex := hex.EncodeToString(ftlpCodeScript.Bytes())
	return api.FetchFtLpUTXOList(ftlpCodeHex, p.Network)
}

// FetchFtLpUTXOList is the exported wrapper for fetchFtlpUTXOList.
func (p *PoolNFT2) FetchFtLpUTXOList(address string) ([]*api.LpUTXO, error) {
	return p.fetchFtlpUTXOList(address)
}

// fetchFtlpUTXO finds a single FT-LP UTXO meeting the requested amount.
func (p *PoolNFT2) fetchFtlpUTXO(address string, amount *big.Int) (*api.LpUTXO, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("fetchFtlpUTXO FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	tapeLen := len(ftaInfo.TapeScript) / 2

	poolNftHash, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return nil, err
	}

	var ftlpCodeScript *bscript.Script
	if p.WithLockTime {
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftHash, address, tapeLen, isCoin, ftVersion)
	} else {
		ftlpCodeScript, err = p.getFtlpCode(poolNftHash, address, tapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return nil, err
	}

	ftlpCodeHex := hex.EncodeToString(ftlpCodeScript.Bytes())
	if !p.WithLockTime {
		return api.FetchFtLpUTXO(ftlpCodeHex, amount, p.Network)
	}

	// WithLockTime pools: filter out LP UTXOs whose tape lock_time is non-zero.
	// Mirrors TS' fetchFtlpUTXO + checkLockTime gate (poolNFT2.0.ts:2216-2266).
	all, err := p.fetchFtlpUTXOList(address)
	if err != nil {
		return nil, err
	}
	for _, candidate := range all {
		if candidate.FtBalance == nil || candidate.FtBalance.Cmp(amount) < 0 {
			continue
		}
		preTX, fErr := api.FetchTXRaw(candidate.TxID, p.Network)
		if fErr != nil {
			return nil, fmt.Errorf("fetchFtlpUTXO FetchTXRaw[%s]: %w", candidate.TxID, fErr)
		}
		tapeIdx := int(candidate.Vout) + 1
		if tapeIdx >= len(preTX.Outputs) {
			continue
		}
		chunks := preTX.Outputs[tapeIdx].LockingScript.Chunks()
		if len(chunks) < 4 || len(chunks[3].Buf) < 4 {
			continue
		}
		// chunks[3].Buf is at minimum the 24-byte amount block; the lock-time
		// 4-byte LE int32 is appended at offset 24..28 in the same chunk per
		// the WithLockTime tape layout. If the chunk is exactly 24 bytes (the
		// non-locktime variant), treat lock_time as 0.
		var lockTimeFromTape uint32
		if len(chunks[3].Buf) >= 28 {
			lockTimeFromTape = binary.LittleEndian.Uint32(chunks[3].Buf[24:28])
		}
		if lockTimeFromTape == 0 {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("fetchFtlpUTXO: no unlocked FT-LP UTXO with sufficient balance")
}

// lpUTXOTobtUTXO converts api.LpUTXO to *bt.UTXO for transaction building.
func lpUTXOTobtUTXO(lp *api.LpUTXO) (*bt.UTXO, error) {
	txidBytes, err := hex.DecodeString(lp.TxID)
	if err != nil {
		return nil, err
	}
	script, err := bscript.NewFromHexString(lp.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          lp.Vout,
		LockingScript: script,
		Satoshis:      lp.Satoshis,
	}, nil
}

// --------------------------------------------------------------------------
// getPoolNftUnlock — online variant (fetches pre/prepre txs from chain)
// --------------------------------------------------------------------------

func (p *PoolNFT2) getPoolNftUnlock(
	privKey *bec.PrivateKey,
	currentTX *bt.Tx,
	currentUnlockIndex int,
	preTxID string,
	preVout int,
	withLock int,
	option int,
	swapOption int,
) (*bscript.Script, error) {
	preTX, err := api.FetchTXRaw(preTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("getPoolNftUnlock fetchPreTX: %w", err)
	}
	if preVout >= len(preTX.Inputs) {
		return nil, fmt.Errorf("getPoolNftUnlock: preVout %d out of range", preVout)
	}
	prePreTxID := hex.EncodeToString(preTX.Inputs[preVout].PreviousTxID())
	prePreTX, err := api.FetchTXRaw(prePreTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("getPoolNftUnlock fetchPrePreTX: %w", err)
	}

	// Build inputsTXs: for each input after index 0, fetch the previous tx
	inputsTXs := make([]*bt.Tx, len(currentTX.Inputs)-1)
	for i := 1; i < len(currentTX.Inputs); i++ {
		prevTxID := hex.EncodeToString(currentTX.Inputs[i].PreviousTxID())
		inputsTXs[i-1], err = api.FetchTXRaw(prevTxID, p.Network)
		if err != nil {
			return nil, fmt.Errorf("getPoolNftUnlock fetchInputTX[%d]: %w", i, err)
		}
	}

	return p.GetPoolNftUnlockOffLine(privKey, currentTX, currentUnlockIndex, preTX, prePreTX, inputsTXs, withLock, option, swapOption)
}


// --------------------------------------------------------------------------
// CreatePoolNFT — mirrors TS poolNFT2.createPoolNFT
// --------------------------------------------------------------------------

// CreatePoolNFT builds two raw transactions: txSourceRaw (P2PKH flag output)
// and txMintRaw (pool NFT code+tape).
// Returns [txSourceRaw, txMintRaw].
func (p *PoolNFT2) CreatePoolNFT(
	privKey *bec.PrivateKey,
	utxo *bt.UTXO,
	tag string,
	serviceFeeRate int,
	lpPlan int,
	withLockTime bool,
) ([]string, error) {
	if lpPlan < 1 || lpPlan > 5 {
		lpPlan = 1
	}

	// --- txSource: P2PKH with flag ---
	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return nil, err
	}
	pkhHex := addr.PublicKeyHash
	flagHex := hex.EncodeToString([]byte("for poolnft mint"))
	flagASM := fmt.Sprintf("OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s", pkhHex, flagHex)
	flagScript, err := bscript.NewFromASM(flagASM)
	if err != nil {
		return nil, err
	}

	txSource := newFTTx()
	if err := txSource.FromUTXOs(utxo); err != nil {
		return nil, fmt.Errorf("txSource.FromUTXOs: %w", err)
	}
	txSource.AddOutput(&bt.Output{Satoshis: 9800, LockingScript: flagScript})
	changeScript, err := bscript.NewP2PKHFromAddress(addr.AddressString)
	if err != nil {
		return nil, err
	}
	txSource.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript}) // change placeholder

	// Estimate fee and set change
	estSize := txSource.JSEstimateSize()
	fee := uint64(80)
	if estSize >= 1000 {
		fee = uint64((int(estSize)/1000 + 1) * 80)
	}
	inputTotal := utxo.Satoshis
	outputTotal := uint64(9800) + fee
	if inputTotal <= outputTotal {
		return nil, fmt.Errorf("CreatePoolNFT: insufficient UTXO for source tx")
	}
	change := inputTotal - outputTotal
	txSource.Outputs[1].Satoshis = change

	// Sign txSource with P2PKH unlocker
	err = signP2PKH(txSource, privKey)
	if err != nil {
		return nil, fmt.Errorf("CreatePoolNFT sign source: %w", err)
	}
	txSourceRaw := txSource.String()
	txSourceTxID := txSource.TxID()

	// --- Fetch FT info for the token in the pool ---
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("CreatePoolNFT FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	tapeLen := len(ftaInfo.TapeScript) / 2

	// Build poolNft code
	poolNftCodeScript, err := p.getPoolNftCode(txSourceTxID, 0, lpPlan, ftVersion, tag, isCoin)
	if err != nil {
		return nil, fmt.Errorf("CreatePoolNFT getPoolNftCode: %w", err)
	}
	p.PoolNftCode = hex.EncodeToString(poolNftCodeScript.Bytes())

	// Build ftlp code
	poolNftSHA256, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return nil, err
	}
	var ftlpCodeScript *bscript.Script
	if withLockTime {
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftSHA256, addr.AddressString, tapeLen, isCoin, ftVersion)
	} else {
		ftlpCodeScript, err = p.getFtlpCode(poolNftSHA256, addr.AddressString, tapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return nil, fmt.Errorf("CreatePoolNFT getFtlpCode: %w", err)
	}

	// Compute partial hashes
	ftlpCodeBytes := ftlpCodeScript.Bytes()
	ftaCodeBytes, err := hex.DecodeString(ftaInfo.CodeScript)
	if err != nil {
		return nil, err
	}
	offset := ftV2PartialOffset
	if isCoin {
		offset = coinPartialOffset
	} else if ftVersion == 1 {
		offset = ftV1PartialOffset
	}
	p.FtLpPartialHash = calculatePartialHash(ftlpCodeBytes[:offset])
	p.FtAPartialHash = calculatePartialHash(ftaCodeBytes[:offset])

	if serviceFeeRate > 0 {
		p.ServiceFeeRate = serviceFeeRate
	}

	// Build tape
	poolnftTapeScript, err := p.GetPoolNftTape(lpPlan, false, withLockTime)
	if err != nil {
		return nil, err
	}

	// Build mint tx
	txMint := newFTTx()
	// bt.UTXO.TxID is forward (display-order) bytes per the convention used
	// throughout this repo (see ft.go and util/util.go); no reversal here.
	prevTxIDBytes, err := hex.DecodeString(txSourceTxID)
	if err != nil {
		return nil, fmt.Errorf("decode source txid: %w", err)
	}
	srcOut := txSource.Outputs[0]
	if err := txMint.FromUTXOs(&bt.UTXO{
		TxID:          prevTxIDBytes,
		Vout:          0,
		LockingScript: srcOut.LockingScript,
		Satoshis:      srcOut.Satoshis,
	}); err != nil {
		return nil, fmt.Errorf("txMint.FromUTXOs: %w", err)
	}
	txMint.AddOutput(&bt.Output{Satoshis: 1000, LockingScript: poolNftCodeScript}) // poolNft code
	txMint.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})    // poolNft tape
	// change output placeholder
	txMint.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})

	// fee estimation and change
	est2 := txMint.JSEstimateSize()
	fee2 := uint64(80)
	if est2 >= 1000 {
		fee2 = uint64((int(est2)/1000 + 1) * 80)
	}
	in2 := srcOut.Satoshis
	out2 := uint64(1000) + fee2
	if in2 <= out2 {
		txMint.Outputs[2].Satoshis = 0
	} else {
		txMint.Outputs[2].Satoshis = in2 - out2
	}

	if err := signP2PKH(txMint, privKey); err != nil {
		return nil, fmt.Errorf("CreatePoolNFT sign mint: %w", err)
	}
	_ = prevTxIDBytes // already wired into txMint via FromUTXOs upstream
	_ = srcOut

	txMintRaw := txMint.String()
	return []string{txSourceRaw, txMintRaw}, nil
}

// calculatePartialHash mirrors TS partial_sha256.calculate_partial_hash —
// returns the hex-encoded internal SHA-256 mid-state (8 × uint32) after
// processing prefix bytes. The pool script verifies this via OP_PARTIAL_HASH
// on chain, so the value MUST match what tbc-lib-js produces.
func calculatePartialHash(prefix []byte) string {
	return partialsha256.CalculatePartialHash(prefix)
}

// signP2PKH signs tx input at index 0 with a P2PKH unlock script.
// signP2PKH is a thin convenience wrapper around signP2PKHAtIdx for input 0.
func signP2PKH(tx *bt.Tx, privKey *bec.PrivateKey) error {
	sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return err
	}
	sigBytes := append(sig.Serialise(), byte(sighash.AllForkID))
	pubBytes := privKey.PubKey().SerialiseCompressed()
	unlockHex := fmt.Sprintf("%02x%s%02x%s",
		len(sigBytes), hex.EncodeToString(sigBytes),
		len(pubBytes), hex.EncodeToString(pubBytes))
	unlock, err := bscript.NewFromHexString(unlockHex)
	if err != nil {
		return err
	}
	tx.Inputs[0].UnlockingScript = unlock
	return nil
}

// --------------------------------------------------------------------------
// InitPoolNFT — mirrors TS poolNFT2.initPoolNFT
// --------------------------------------------------------------------------

// InitPoolNFT initialises a pool by depositing TBC and FT-A, minting FT-LP.
func (p *PoolNFT2) InitPoolNFT(
	privKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	tbcAmount string,
	ftA string,
	lockTime uint32,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	tapeLen := len(ftaInfo.TapeScript) / 2

	// parse amounts
	tbcAmountBN, err := util.ParseDecimalToBigInt(tbcAmount, 6)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT parse tbc_amount: %w", err)
	}
	ftAAmountBN, err := util.ParseDecimalToBigInt(ftA, ftaInfo.Decimal)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT parse ft_a: %w", err)
	}
	if tbcAmountBN.Sign() <= 0 || ftAAmountBN.Sign() <= 0 {
		return "", fmt.Errorf("InitPoolNFT: Invalid amount Input")
	}

	p.TbcAmount = new(big.Int).Set(tbcAmountBN)
	p.FtLpAmount = new(big.Int).Set(tbcAmountBN)
	p.FtAAmount = new(big.Int).Set(ftAAmountBN)

	if utxo.Satoshis < tbcAmountBN.Uint64() {
		return "", fmt.Errorf("InitPoolNFT: Insufficient TBC amount, please merge UTXOs")
	}

	// pool codehash160 for FT-A destination
	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	// fetch FT-A UTXO from sender
	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}
	ftaCodeScriptHex, err := buildFTTransferCodeHex(ftaInfo.CodeScript, addr.AddressString)
	if err != nil {
		return "", err
	}
	fttxoA, err := api.FetchFtUTXO(p.FtAContractTxID, addr.AddressString, ftaCodeScriptHex, p.Network, ftAAmountBN)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT FetchFtUTXO: %w", err)
	}
	if fttxoA.FtBalance.Cmp(ftAAmountBN) < 0 {
		return "", fmt.Errorf("InitPoolNFT: Insufficient FT-A amount, please merge FT-A UTXOs")
	}

	ftPreTX, err := api.FetchTXRaw(hex.EncodeToString(fttxoA.TxID), p.Network)
	if err != nil {
		return "", err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(fttxoA.Vout), p.Network)
	if err != nil {
		return "", err
	}

	tapeAmountSetIn := []*big.Int{new(big.Int).Set(fttxoA.FtBalance)}
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(ftAAmountBN, tapeAmountSetIn, 1)

	// pool tape (updated with current state)
	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", err
	}

	// Pool NFT UTXO
	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT FetchPoolNFTUTXO: %w", err)
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft, util.FtUTXOToUTXO(fttxoA), utxo); err != nil {
		return "", fmt.Errorf("InitPoolNFT tx.FromUTXOs: %w", err)
	}

	// poolNft outputs
	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 1000 + tbcAmountBN.Uint64(), LockingScript: poolNftCodeScript})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	// FT-A to pool (code+tape)
	ftaToPoolCode, err := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", err
	}
	ftaToPoolTape, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: ftaToPoolCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftaToPoolTape})

	// FT-LP outputs
	poolNftSHA256, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	var ftlpCodeScript *bscript.Script
	var ftlpTapeScript *bscript.Script

	if p.WithLockTime {
		// TS validates `lock_time < 0 || lock_time > 4294967295` because TS
		// number is unbounded; Go's uint32 already enforces the same range
		// at the type boundary, so no runtime check is needed.
		ftlpTapeScript = buildFtlpTapeWithLockTime(tbcAmountBN, tapeLen, lockTime)
		newTapeLen := len(ftlpTapeScript.Bytes())
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftSHA256, addressTo, newTapeLen, isCoin, ftVersion)
	} else {
		ftlpTapeScript = buildFtlpTape(tbcAmountBN, isCoin, ftaInfo.Name, ftaInfo.Symbol)
		newTapeLen := len(ftlpTapeScript.Bytes())
		ftlpCodeScript, err = p.getFtlpCode(poolNftSHA256, addressTo, newTapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT getFtlpCode: %w", err)
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftlpCodeScript})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftlpTapeScript})

	// FT-A change (if needed)
	if ftAAmountBN.Cmp(fttxoA.FtBalance) < 0 {
		changeCode, err2 := BuildFTtransferCode(ftaInfo.CodeScript, addr.AddressString)
		if err2 != nil {
			return "", err2
		}
		changeTape, err2 := BuildFTtransferTape(ftaInfo.TapeScript, changeHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: changeCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeTape})
	}

	// fee + change
	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	// sign pool NFT input
	poolUnlock, err := p.getPoolNftUnlock(privKey, tx, 0, hex.EncodeToString(poolnft.TxID), int(poolnft.Vout), 0, 1, 0)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT pool unlock: %w", err)
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	// sign FT-A input
	if isCoin {
		tx.Inputs[1].SequenceNumber = 4294967294
	}
	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	ftUnlock, err := ft.GetFTunlock(privKey, tx, ftPreTX, ftPrePreTxData, 1, int(fttxoA.Vout), isCoin)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT ft unlock: %w", err)
	}
	tx.Inputs[1].UnlockingScript = ftUnlock

	// sign P2PKH utxo
	err = signP2PKHAtIdx(tx, privKey, 2)
	if err != nil {
		return "", fmt.Errorf("InitPoolNFT sign utxo: %w", err)
	}

	return tx.String(), nil
}

// buildFtlpTape builds the FT-LP tape script (no lock time).
func buildFtlpTape(amount *big.Int, isCoin bool, name, symbol string) *bscript.Script {
	amtHex := bigIntToLE8Hex(amount)
	zero7 := strings.Repeat("0000000000000000", 5)
	tapeAmount := amtHex + zero7
	nameHex := hex.EncodeToString([]byte(name))
	symHex := hex.EncodeToString([]byte(symbol))
	var asm string
	if isCoin {
		asm = fmt.Sprintf("OP_FALSE OP_RETURN %s 06 %s %s 00000000 4654617065", tapeAmount, nameHex, symHex)
	} else {
		asm = fmt.Sprintf("OP_FALSE OP_RETURN %s 06 %s %s 4654617065", tapeAmount, nameHex, symHex)
	}
	s, _ := bscript.NewFromASM(asm)
	return s
}

// buildFtlpTapeWithLockTime builds the FT-LP tape script with lock time.
// isCoin/name/symbol are not embedded in the with-lock-time tape (TS uses an
// OP_0-padded layout); the caller owns them via the surrounding ftlp code.
func buildFtlpTapeWithLockTime(amount *big.Int, tapeLen int, lockTime uint32) *bscript.Script {
	amtHex := bigIntToLE8Hex(amount)
	zero5 := strings.Repeat("0000000000000000", 5)
	tapeAmount := amtHex + zero5
	lockTimeBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(lockTimeBytes, lockTime)
	lockTimeHex := hex.EncodeToString(lockTimeBytes)
	fillSize := tapeLen/2 - 62
	if fillSize < 0 {
		fillSize = 0
	}
	opZeros := strings.Repeat("OP_0 ", fillSize)
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s4654617065", tapeAmount, lockTimeHex, opZeros)
	s, _ := bscript.NewFromASM(strings.TrimSpace(asm))
	return s
}

// bigIntToLE8Hex encodes big.Int as 8-byte little-endian hex.
func bigIntToLE8Hex(n *big.Int) string {
	buf := make([]byte, 8)
	v := n.Uint64()
	binary.LittleEndian.PutUint64(buf, v)
	return hex.EncodeToString(buf)
}

// buildFTTransferCodeHex is a helper returning the hex string form.
func buildFTTransferCodeHex(codeHex, address string) (string, error) {
	s, err := BuildFTtransferCode(codeHex, address)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(s.Bytes()), nil
}

// signP2PKHAtIdx signs tx input at the given index.
func signP2PKHAtIdx(tx *bt.Tx, privKey *bec.PrivateKey, idx uint32) error {
	sh, err := tx.CalcInputSignatureHash(idx, sighash.AllForkID)
	if err != nil {
		return err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return err
	}
	sigBytes := append(sig.Serialise(), byte(sighash.AllForkID))
	pubBytes := privKey.PubKey().SerialiseCompressed()
	unlockHex := fmt.Sprintf("%02x%s%02x%s",
		len(sigBytes), hex.EncodeToString(sigBytes),
		len(pubBytes), hex.EncodeToString(pubBytes))
	unlock, err := bscript.NewFromHexString(unlockHex)
	if err != nil {
		return err
	}
	tx.Inputs[idx].UnlockingScript = unlock
	return nil
}

// poolDustAmount mirrors tbc-lib-js Transaction.DUST_AMOUNT (42 sat). Outputs
// with satoshis below this are pruned by tbc-lib-js's tx.change() helper —
// match that behavior for hex parity with JS.
const poolDustAmount uint64 = 42

// adjustFeeAndChange sets the last output's satoshis to input sum minus all
// other outputs and fee. Fee schedule mirrors TS exactly:
//
//	size < 1000 → fee = satPerKB (flat)
//	size ≥ 1000 → fee = ceil(size * satPerKB / 1000)
//
// (TS: `txSize < 1000 ? 80 : Math.ceil(txSize / 1000 * 80)`.)
//
// If the resulting last-output value would be below poolDustAmount, the
// output is removed entirely — matching tbc-lib-js .change() behavior.
func adjustFeeAndChange(tx *bt.Tx, satPerKB uint64) {
	est := uint64(tx.JSEstimateSize())
	var fee uint64
	if est < 1000 {
		fee = satPerKB
	} else {
		// ceil(est * satPerKB / 1000)
		fee = (est*satPerKB + 999) / 1000
	}
	inputSum := uint64(0)
	for _, in := range tx.Inputs {
		inputSum += in.PreviousTxSatoshis
	}
	outSum := uint64(0)
	for i, out := range tx.Outputs {
		if i < len(tx.Outputs)-1 {
			outSum += out.Satoshis
		}
	}
	if inputSum <= outSum+fee {
		// No change — drop the placeholder change output to mirror tbc-lib-js
		// (which doesn't add a change output if available - fee < dust).
		tx.Outputs = tx.Outputs[:len(tx.Outputs)-1]
		return
	}
	change := inputSum - outSum - fee
	if change < poolDustAmount {
		tx.Outputs = tx.Outputs[:len(tx.Outputs)-1]
		return
	}
	tx.Outputs[len(tx.Outputs)-1].Satoshis = change
}

// --------------------------------------------------------------------------
// IncreaseLP — mirrors TS poolNFT2.increaseLP
// --------------------------------------------------------------------------

// IncreaseLP adds TBC liquidity to the pool and issues FT-LP tokens to addressTo.
func (p *PoolNFT2) IncreaseLP(
	privKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	amountTBC string,
	lockTime uint32,
) (string, error) {
	lockStatus := p.WithLock || isLockByCodeLen(p.PoolNftCode) == 1

	var lpCostAmount uint64
	if lockStatus {
		var err error
		lpCostAmount, err = util.GetLpCostAmount(p.PoolNftCode)
		if err != nil {
			return "", fmt.Errorf("IncreaseLP getLpCostAmount: %w", err)
		}
		// Subtract LP cost from tbcAmount
		costDec := fmt.Sprintf("0.%06d", lpCostAmount%1000000)
		if lpCostAmount >= 1000000 {
			costDec = fmt.Sprintf("%d.%06d", lpCostAmount/1000000, lpCostAmount%1000000)
		}
		// amountTBC minus cost
		tbcBN, err2 := util.ParseDecimalToBigInt(amountTBC, 6)
		if err2 != nil {
			return "", err2
		}
		costBN := new(big.Int).SetUint64(lpCostAmount)
		tbcBN.Sub(tbcBN, costBN)
		if tbcBN.Sign() <= 0 {
			return "", fmt.Errorf("IncreaseLP: TBC amount must be greater than LP cost of %s", costDec)
		}
		amountTBC = fmt.Sprintf("%d.%06d", tbcBN.Uint64()/1000000, tbcBN.Uint64()%1000000)
	}

	tbcBN, err := util.ParseDecimalToBigInt(amountTBC, 6)
	if err != nil || tbcBN.Sign() <= 0 {
		return "", fmt.Errorf("IncreaseLP: Invalid TBC amount input")
	}

	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("IncreaseLP FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	tapeLen := len(ftaInfo.TapeScript) / 2

	// Snapshot pool state for rollback on any downstream error.
	preFtA, preFtLp, preTbc, preTbcFull := snapshotPoolAmounts(p)
	lpSuccess := false
	defer func() {
		if !lpSuccess {
			restorePoolAmounts(p, preFtA, preFtLp, preTbc, preTbcFull)
		}
	}()
	changeData, err := p.UpdatePoolNFT(amountTBC, ftaInfo.Decimal, 2)
	if err != nil {
		return "", err
	}

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}

	ftaCodeScriptHex, err := buildFTTransferCodeHex(ftaInfo.CodeScript, addr.AddressString)
	if err != nil {
		return "", err
	}

	fttxoA, err := api.FetchFtUTXO(p.FtAContractTxID, addr.AddressString, ftaCodeScriptHex, p.Network, changeData.FtADifference)
	if err != nil {
		if strings.Contains(err.Error(), "Insufficient FTbalance") {
			return "", fmt.Errorf("Insufficient FT-A amount, please merge FT-A UTXOs")
		}
		return "", err
	}

	ftPreTX, err := api.FetchTXRaw(hex.EncodeToString(fttxoA.TxID), p.Network)
	if err != nil {
		return "", err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(fttxoA.Vout), p.Network)
	if err != nil {
		return "", err
	}

	if changeData.FtADifference.Cmp(fttxoA.FtBalance) > 0 {
		return "", fmt.Errorf("IncreaseLP: Insufficient balance, please merge FT UTXOs")
	}

	tapeAmountSetIn := []*big.Int{new(big.Int).Set(fttxoA.FtBalance)}
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(changeData.FtADifference, tapeAmountSetIn, 1)

	if utxo.Satoshis < tbcBN.Uint64() {
		return "", fmt.Errorf("IncreaseLP: Insufficient TBC amount, please merge UTXOs")
	}

	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft, util.FtUTXOToUTXO(fttxoA), utxo); err != nil {
		return "", fmt.Errorf("IncreaseLP tx.FromUTXOs: %w", err)
	}

	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	newSats := poolnft.Satoshis + changeData.TbcAmountDifference.Uint64()
	tx.AddOutput(&bt.Output{Satoshis: newSats, LockingScript: poolNftCodeScript})

	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	// FT-A to pool
	ftaToPoolCode, err := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", err
	}
	ftaToPoolTape, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: ftaToPoolCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftaToPoolTape})

	// FT-LP
	poolNftSHA256, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	var ftlpCodeScript *bscript.Script
	var ftlpTapeScript *bscript.Script
	if p.WithLockTime {
		ftlpTapeScript = buildFtlpTapeWithLockTime(changeData.FtLpDifference, tapeLen, lockTime)
		newTapeLen := len(ftlpTapeScript.Bytes())
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftSHA256, addressTo, newTapeLen, isCoin, ftVersion)
	} else {
		ftlpTapeScript = buildFtlpTape(changeData.FtLpDifference, isCoin, ftaInfo.Name, ftaInfo.Symbol)
		newTapeLen := len(ftlpTapeScript.Bytes())
		ftlpCodeScript, err = p.getFtlpCode(poolNftSHA256, addressTo, newTapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftlpCodeScript})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftlpTapeScript})

	// Lock cost output
	if lockStatus {
		lpCostAddr, err2 := util.GetLpCostAddress(p.PoolNftCode)
		if err2 != nil {
			return "", err2
		}
		lockCostScript, err2 := bscript.NewP2PKHFromAddress(lpCostAddr)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: lpCostAmount, LockingScript: lockCostScript})
	}

	// FT-A change
	if changeData.FtADifference.Cmp(fttxoA.FtBalance) < 0 {
		changeCode, err2 := BuildFTtransferCode(ftaInfo.CodeScript, addr.AddressString)
		if err2 != nil {
			return "", err2
		}
		changeTape, err2 := BuildFTtransferTape(ftaInfo.TapeScript, changeHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: changeCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeTape})
	}

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	withLockInt := 0
	if lockStatus {
		withLockInt = 1
	}
	poolUnlock, err := p.getPoolNftUnlock(privKey, tx, 0, hex.EncodeToString(poolnft.TxID), int(poolnft.Vout), withLockInt, 1, 0)
	if err != nil {
		return "", err
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	if isCoin {
		tx.Inputs[1].SequenceNumber = 4294967294
	}
	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	ftUnlock, err := ft.GetFTunlock(privKey, tx, ftPreTX, ftPrePreTxData, 1, int(fttxoA.Vout), isCoin)
	if err != nil {
		return "", err
	}
	tx.Inputs[1].UnlockingScript = ftUnlock

	err = signP2PKHAtIdx(tx, privKey, 2)
	if err != nil {
		return "", err
	}

	lpSuccess = true
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// ConsumeLP — mirrors TS poolNFT2.consumeLP
// --------------------------------------------------------------------------

// ConsumeLP burns FT-LP and withdraws proportional FT-A and TBC from the pool.
func (p *PoolNFT2) ConsumeLP(
	privKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	amountLP string,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("ConsumeLP FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)

	amountLPBN, err := util.ParseDecimalToBigInt(amountLP, 6)
	if err != nil {
		return "", fmt.Errorf("ConsumeLP parse amount_lp: %w", err)
	}
	if p.FtLpAmount.Cmp(amountLPBN) < 0 {
		return "", fmt.Errorf("ConsumeLP: Invalid FT-LP amount input")
	}

	preFtA, preFtLp, preTbc, preTbcFull := snapshotPoolAmounts(p)
	consumeSuccess := false
	defer func() {
		if !consumeSuccess {
			restorePoolAmounts(p, preFtA, preFtLp, preTbc, preTbcFull)
		}
	}()
	changeData, err := p.UpdatePoolNFT(amountLP, ftaInfo.Decimal, 1)
	if err != nil {
		return "", err
	}

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}

	// Fetch FT-LP UTXO
	fttxoLP, err := p.fetchFtlpUTXO(addr.AddressString, changeData.FtLpDifference)
	if err != nil {
		return "", fmt.Errorf("ConsumeLP fetchFtlpUTXO: %w", err)
	}
	lpPreTX, err := api.FetchTXRaw(fttxoLP.TxID, p.Network)
	if err != nil {
		return "", err
	}
	lpPrePreTxData, err := api.FetchFtPrePreTxData(lpPreTX, int(fttxoLP.Vout), p.Network)
	if err != nil {
		return "", err
	}

	// Fetch FT-A UTXOs from pool
	ftutxoCodeScript, err := buildFTTransferCodeHex(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", err
	}
	fttxosC, err := api.FetchFtUTXOsForPool(p.FtAContractTxID, poolCodeHash160, ftutxoCodeScript, p.Network, changeData.FtADifference, 3)
	if err != nil {
		if strings.Contains(err.Error(), "Insufficient FTbalance") {
			return "", fmt.Errorf("Insufficient PoolFT, please merge FT UTXOs")
		}
		return "", err
	}

	// Pre-tx data for FT-A
	ftPreTXs := make([]*bt.Tx, len(fttxosC))
	ftPrePreTxDatas := make([]string, len(fttxosC))
	tapeAmountSetIn := make([]*big.Int, len(fttxosC))
	tapeAmountSum := new(big.Int)
	for i, ftu := range fttxosC {
		ftPreTXs[i], err = api.FetchTXRaw(hex.EncodeToString(ftu.TxID), p.Network)
		if err != nil {
			return "", err
		}
		ftPrePreTxDatas[i], err = api.FetchFtPrePreTxData(ftPreTXs[i], int(ftu.Vout), p.Network)
		if err != nil {
			return "", err
		}
		tapeAmountSetIn[i] = new(big.Int).Set(ftu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, ftu.FtBalance)
	}

	lpTapeAmountSetIn := []*big.Int{new(big.Int).Set(fttxoLP.FtBalance)}
	ftAbyAHex, ftAbyCHex := BuildTapeAmountWithFtInputIndex(changeData.FtADifference, tapeAmountSetIn, 2)
	ftlpBurnHex, ftlpChangeHex := BuildTapeAmountWithFtInputIndex(changeData.FtLpDifference, lpTapeAmountSetIn, 1)

	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return "", err
	}
	contractTX, err := api.FetchTXRaw(hex.EncodeToString(poolnft.TxID), p.Network)
	if err != nil {
		return "", err
	}

	// Build FT-LP code script
	poolNftSHA256, err := poolNFTCodeSHA256(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	tapeLen := len(ftaInfo.TapeScript) / 2
	var ftlpCodeScript *bscript.Script
	if p.WithLockTime {
		ftlpCodeScript, err = p.getFtlpCodeWithLockTime(poolNftSHA256, addr.AddressString, tapeLen, isCoin, ftVersion)
	} else {
		ftlpCodeScript, err = p.getFtlpCode(poolNftSHA256, addr.AddressString, tapeLen, isCoin, ftVersion)
	}
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft); err != nil {
		return "", fmt.Errorf("ConsumeLP tx.FromUTXOs poolnft: %w", err)
	}
	lpUTXO, err := lpUTXOTobtUTXO(fttxoLP)
	if err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(lpUTXO); err != nil {
		return "", fmt.Errorf("ConsumeLP tx.FromUTXOs lpUTXO: %w", err)
	}
	for _, ftu := range fttxosC {
		if err := tx.FromUTXOs(util.FtUTXOToUTXO(ftu)); err != nil {
			return "", fmt.Errorf("ConsumeLP tx.FromUTXOs ftu: %w", err)
		}
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", fmt.Errorf("ConsumeLP tx.FromUTXOs utxo: %w", err)
	}

	// pool NFT output
	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	newSats := poolnft.Satoshis - changeData.TbcAmountFullDiff.Uint64()
	tx.AddOutput(&bt.Output{Satoshis: newSats, LockingScript: poolNftCodeScript})

	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	// FT-A to user
	ftAbyACode, err := BuildFTtransferCode(ftaInfo.CodeScript, addressTo)
	if err != nil {
		return "", err
	}
	ftAbyATape, err := BuildFTtransferTape(ftaInfo.TapeScript, ftAbyAHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftAbyACode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftAbyATape})

	// TBC to user (P2PKH)
	tbcToUserScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: changeData.TbcAmountFullDiff.Uint64(), LockingScript: tbcToUserScript})

	// FT-LP burn output
	eaterCode, err := BuildFTtransferCode(hex.EncodeToString(ftlpCodeScript.Bytes()), eaterAddress)
	if err != nil {
		return "", err
	}

	// FT-LP tape (zeroed amounts)
	var ftlpBurnTape *bscript.Script
	if p.WithLockTime {
		if isCoin {
			ftlpBurnTape, _ = bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s 00000000 %s4654617065",
				strings.Repeat("0000000000000000", 6), strings.Repeat("OP_0 ", tapeLen/2-62)))
		} else {
			ftlpBurnTape, _ = bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s 00000000 %s4654617065",
				strings.Repeat("0000000000000000", 6), strings.Repeat("OP_0 ", tapeLen/2-62)))
		}
	} else {
		nameHex := hex.EncodeToString([]byte(ftaInfo.Name))
		symHex := hex.EncodeToString([]byte(ftaInfo.Symbol))
		if isCoin {
			ftlpBurnTape, _ = bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s 06 %s %s 00000000 4654617065",
				strings.Repeat("0000000000000000", 6), nameHex, symHex))
		} else {
			ftlpBurnTape, _ = bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s 06 %s %s 4654617065",
				strings.Repeat("0000000000000000", 6), nameHex, symHex))
		}
	}

	eaterBurnTape, err := BuildFTtransferTape(hex.EncodeToString(ftlpBurnTape.Bytes()), ftlpBurnHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: fttxoLP.Satoshis, LockingScript: eaterCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: eaterBurnTape})

	// FT-LP change (if any)
	if fttxoLP.FtBalance.Cmp(changeData.FtLpDifference) > 0 {
		ftlpChangeCode, err2 := BuildFTtransferCode(hex.EncodeToString(ftlpCodeScript.Bytes()), addressTo)
		if err2 != nil {
			return "", err2
		}
		ftlpChangeTape, err2 := BuildFTtransferTape(hex.EncodeToString(ftlpBurnTape.Bytes()), ftlpChangeHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: fttxoLP.Satoshis, LockingScript: ftlpChangeCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftlpChangeTape})
	}

	// FT-A change back to pool (if any)
	if changeData.FtADifference.Cmp(tapeAmountSum) < 0 {
		ftAbyCCode, err2 := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
		if err2 != nil {
			return "", err2
		}
		ftAbyCTape, err2 := BuildFTtransferTape(ftaInfo.TapeScript, ftAbyCHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftAbyCCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftAbyCTape})
	}

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	withLockInt := isLockByCodeLen(p.PoolNftCode)
	poolUnlock, err := p.getPoolNftUnlock(privKey, tx, 0, hex.EncodeToString(poolnft.TxID), int(poolnft.Vout), withLockInt, 2, 0)
	if err != nil {
		return "", err
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	// TS poolNFT2.0.ts:1265-1266 sets sequence=0xFFFFFFFE on the LP input
	// (input 1) for with_lock_time pools so tx.LockTime is honored. Without
	// this, the consumed LP UTXO's lock-time gate isn't released even if the
	// caller has separately broadcast the unlockFTLP precursor.
	//
	// NOTE: TS also calls `unlockFTLP` BEFORE this method runs, broadcasts
	// it, and uses unlockTX.outputs[2] as the fee input. The Go port does
	// not (yet) implement that precursor flow — for with-lock-time pools
	// the caller must broadcast their own unlock tx and pass its
	// outputs[2] as `utxo` for ConsumeLP to work end-to-end.
	if p.WithLockTime {
		tx.Inputs[1].SequenceNumber = 4294967294
	}

	// LP input unlock
	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	lpUnlock, err := ft.GetFTunlock(privKey, tx, lpPreTX, lpPrePreTxData, 1, int(fttxoLP.Vout), false)
	if err != nil {
		return "", err
	}
	tx.Inputs[1].UnlockingScript = lpUnlock

	// FT-A from pool: swap unlock
	for i, ftu := range fttxosC {
		if isCoin {
			tx.Inputs[i+2].SequenceNumber = 4294967294
		}
		swapUnlock, err2 := ft.GetFTunlockSwap(privKey, tx, ftPreTXs[i], ftPrePreTxDatas[i], contractTX, i+2, int(ftu.Vout), ftVersion, isCoin)
		if err2 != nil {
			return "", err2
		}
		tx.Inputs[i+2].UnlockingScript = swapUnlock
	}

	// Fee input is always P2PKH; sign unconditionally.
	feeIdx := len(fttxosC) + 2
	if err := signP2PKHAtIdx(tx, privKey, uint32(feeIdx)); err != nil {
		return "", err
	}

	consumeSuccess = true
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// SwapToToken — mirrors TS poolNFT2.swaptoToken_baseTBC
// --------------------------------------------------------------------------

// SwapToToken swaps TBC to FT-A.
func (p *PoolNFT2) SwapToToken(
	privKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	amountTBC string,
	lpPlan int,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("SwapToToken FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)

	// TS precedence: instance value wins over caller param.
	if p.LpPlan >= 1 && p.LpPlan <= 5 {
		lpPlan = p.LpPlan
	} else if lpPlan < 1 || lpPlan > 5 {
		lpPlan = 1
	}

	amountTBCBN, err := util.ParseDecimalToBigInt(amountTBC, 6)
	if err != nil || amountTBCBN.Sign() <= 0 {
		return "", fmt.Errorf("SwapToToken: Invalid TBC amount input")
	}

	// Service fee calculation
	serviceFee := new(big.Int).Mul(amountTBCBN, big.NewInt(int64(p.ServiceFeeRate)))
	serviceFee.Div(serviceFee, big.NewInt(10000))

	lpRate := int64(p.ServiceFeeRate - 10)
	if lpPlan != 1 {
		lpRate = 5
	}
	serviceFeeLP := new(big.Int).Mul(amountTBCBN, big.NewInt(lpRate))
	serviceFeeLP.Div(serviceFeeLP, big.NewInt(10000))
	serviceFeeA := new(big.Int).Sub(serviceFee, serviceFeeLP)

	amountTBCswap := new(big.Int).Sub(amountTBCBN, serviceFee)
	amountTBCswapLP := new(big.Int).Set(amountTBCBN)
	if serviceFeeA.Cmp(big.NewInt(10)) >= 0 {
		amountTBCswapLP = new(big.Int).Sub(amountTBCBN, serviceFeeA)
	}

	// Update pool state. Save snapshots so we can roll back if any
	// downstream API/build step fails — leaving the receiver half-updated
	// would compound errors on retry.
	preFtAAmount := new(big.Int).Set(p.FtAAmount)
	preTbcAmount := new(big.Int).Set(p.TbcAmount)
	swapSuccess := false
	defer func() {
		if !swapSuccess {
			p.FtAAmount = preFtAAmount
			p.TbcAmount = preTbcAmount
		}
	}()
	ftAold := new(big.Int).Set(p.FtAAmount)
	poolMul := new(big.Int).Mul(p.FtAAmount, p.TbcAmount)
	p.TbcAmount.Add(p.TbcAmount, amountTBCswap)
	p.FtAAmount.Div(poolMul, p.TbcAmount)
	ftADecrement := new(big.Int).Sub(ftAold, p.FtAAmount)

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	// Fetch FT-A UTXOs from pool
	ftutxoCodeScript, err := buildFTTransferCodeHex(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", err
	}
	fttxosC, err := api.FetchFtUTXOsForPool(p.FtAContractTxID, poolCodeHash160, ftutxoCodeScript, p.Network, ftADecrement, 4)
	if err != nil {
		if strings.Contains(err.Error(), "Insufficient FTbalance") {
			return "", fmt.Errorf("Insufficient PoolFT, please merge FT UTXOs")
		}
		return "", err
	}

	ftPreTXs := make([]*bt.Tx, len(fttxosC))
	ftPrePreTxDatas := make([]string, len(fttxosC))
	tapeAmountSetIn := make([]*big.Int, len(fttxosC))
	tapeAmountSum := new(big.Int)
	for i, ftu := range fttxosC {
		ftPreTXs[i], err = api.FetchTXRaw(hex.EncodeToString(ftu.TxID), p.Network)
		if err != nil {
			return "", err
		}
		ftPrePreTxDatas[i], err = api.FetchFtPrePreTxData(ftPreTXs[i], int(ftu.Vout), p.Network)
		if err != nil {
			return "", err
		}
		tapeAmountSetIn[i] = new(big.Int).Set(ftu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, ftu.FtBalance)
	}

	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(ftADecrement, tapeAmountSetIn, 2)

	if utxo.Satoshis < amountTBCBN.Uint64() {
		return "", fmt.Errorf("SwapToToken: Insufficient TBC amount, please merge UTXOs")
	}

	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return "", err
	}
	contractTX, err := api.FetchTXRaw(hex.EncodeToString(poolnft.TxID), p.Network)
	if err != nil {
		return "", err
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft, utxo); err != nil {
		return "", fmt.Errorf("SwapToToken tx.FromUTXOs: %w", err)
	}
	for _, ftu := range fttxosC {
		if err := tx.FromUTXOs(util.FtUTXOToUTXO(ftu)); err != nil {
			return "", fmt.Errorf("SwapToToken tx.FromUTXOs ftu: %w", err)
		}
	}

	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: poolnft.Satoshis + amountTBCswapLP.Uint64(), LockingScript: poolNftCodeScript})

	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	// FT-A to user
	ftAtoUserCode, err := BuildFTtransferCode(ftaInfo.CodeScript, addressTo)
	if err != nil {
		return "", err
	}
	ftAtoUserTape, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftAtoUserCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftAtoUserTape})

	// Service fee
	if serviceFeeA.Cmp(big.NewInt(10)) >= 0 {
		sfAddr, err2 := getServiceFeeAddress(lpPlan)
		if err2 != nil {
			return "", err2
		}
		sfScript, err2 := bscript.NewP2PKHFromAddress(sfAddr)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: serviceFeeA.Uint64(), LockingScript: sfScript})
	}

	// FT-A change to pool
	if ftADecrement.Cmp(tapeAmountSum) < 0 {
		ftAbyCCode, err2 := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
		if err2 != nil {
			return "", err2
		}
		ftAbyCTape, err2 := BuildFTtransferTape(ftaInfo.TapeScript, changeHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: ftAbyCCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftAbyCTape})
	}

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	withLockInt := isLockByCodeLen(p.PoolNftCode)
	poolUnlock, err := p.getPoolNftUnlock(privKey, tx, 0, hex.EncodeToString(poolnft.TxID), int(poolnft.Vout), withLockInt, 3, 1)
	if err != nil {
		return "", err
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	for i, ftu := range fttxosC {
		if isCoin {
			tx.Inputs[i+2].SequenceNumber = 4294967294
		}
		swapUnlock, err2 := ft.GetFTunlockSwap(privKey, tx, ftPreTXs[i], ftPrePreTxDatas[i], contractTX, i+2, int(ftu.Vout), ftVersion, isCoin)
		if err2 != nil {
			return "", err2
		}
		tx.Inputs[i+2].UnlockingScript = swapUnlock
	}

	err = signP2PKHAtIdx(tx, privKey, 1)
	if err != nil {
		return "", err
	}

	swapSuccess = true
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// SwapToTBC — mirrors TS poolNFT2.swaptoTBC_baseToken
// --------------------------------------------------------------------------

// SwapToTBC swaps FT-A to TBC.
func (p *PoolNFT2) SwapToTBC(
	privKey *bec.PrivateKey,
	addressTo string,
	utxo *bt.UTXO,
	amountToken string,
	lpPlan int,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("SwapToTBC FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)
	_ = ftVersion

	// TS precedence: instance value wins over caller param.
	if p.LpPlan >= 1 && p.LpPlan <= 5 {
		lpPlan = p.LpPlan
	} else if lpPlan < 1 || lpPlan > 5 {
		lpPlan = 1
	}

	amountFTBN, err := util.ParseDecimalToBigInt(amountToken, ftaInfo.Decimal)
	if err != nil || amountFTBN.Sign() <= 0 {
		return "", fmt.Errorf("SwapToTBC: Invalid FT amount input")
	}

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", err
	}

	// Update pool state. Save snapshots so we can roll back if any
	// downstream API/build step fails — leaving the receiver half-updated
	// would compound errors on retry.
	preFtAAmount := new(big.Int).Set(p.FtAAmount)
	preTbcAmount := new(big.Int).Set(p.TbcAmount)
	swapSuccess := false
	defer func() {
		if !swapSuccess {
			p.FtAAmount = preFtAAmount
			p.TbcAmount = preTbcAmount
		}
	}()
	poolMul := new(big.Int).Mul(p.FtAAmount, p.TbcAmount)
	tbcOld := new(big.Int).Set(p.TbcAmount)
	p.FtAAmount.Add(p.FtAAmount, amountFTBN)
	p.TbcAmount.Div(poolMul, p.FtAAmount)
	tbcDecrement := new(big.Int).Sub(tbcOld, p.TbcAmount)

	// Service fee
	serviceFee := new(big.Int).Mul(tbcDecrement, big.NewInt(int64(p.ServiceFeeRate)))
	serviceFee.Div(serviceFee, big.NewInt(10000))
	lpRate := int64(p.ServiceFeeRate - 10)
	if lpPlan != 1 {
		lpRate = 5
	}
	serviceFeeLP := new(big.Int).Mul(tbcDecrement, big.NewInt(lpRate))
	serviceFeeLP.Div(serviceFeeLP, big.NewInt(10000))
	serviceFeeA := new(big.Int).Sub(serviceFee, serviceFeeLP)

	tbcDecrementSwap := new(big.Int).Sub(tbcDecrement, serviceFee)
	tbcDecrementSwapLP := new(big.Int).Set(tbcDecrementSwap)
	if serviceFeeA.Cmp(big.NewInt(10)) >= 0 {
		tbcDecrementSwapLP = new(big.Int).Sub(tbcDecrement, serviceFeeLP)
	}

	// Fetch FT-A UTXO from sender
	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}
	ftaCodeScriptHex, err := buildFTTransferCodeHex(ftaInfo.CodeScript, addr.AddressString)
	if err != nil {
		return "", err
	}
	fttxoA, err := api.FetchFtUTXO(p.FtAContractTxID, addr.AddressString, ftaCodeScriptHex, p.Network, amountFTBN)
	if err != nil {
		return "", fmt.Errorf("SwapToTBC FetchFtUTXO: %w", err)
	}
	ftPreTX, err := api.FetchTXRaw(hex.EncodeToString(fttxoA.TxID), p.Network)
	if err != nil {
		return "", err
	}
	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(fttxoA.Vout), p.Network)
	if err != nil {
		return "", err
	}

	tapeAmountSetIn := []*big.Int{new(big.Int).Set(fttxoA.FtBalance)}
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(amountFTBN, tapeAmountSetIn, 1)

	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft, util.FtUTXOToUTXO(fttxoA), utxo); err != nil {
		return "", fmt.Errorf("SwapToTBC tx.FromUTXOs: %w", err)
	}

	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: poolnft.Satoshis - tbcDecrementSwapLP.Uint64(), LockingScript: poolNftCodeScript})

	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	// TBC to user
	tbcToUserScript, _ := bscript.NewP2PKHFromAddress(addressTo)
	tx.AddOutput(&bt.Output{Satoshis: tbcDecrementSwap.Uint64(), LockingScript: tbcToUserScript})

	// FT-A to pool
	ftAtoPoolCode, err := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", err
	}
	ftAtoPoolTape, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: ftAtoPoolCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: ftAtoPoolTape})

	// Service fee
	if serviceFeeA.Cmp(big.NewInt(10)) >= 0 {
		sfAddr, err2 := getServiceFeeAddress(lpPlan)
		if err2 != nil {
			return "", err2
		}
		sfScript, err2 := bscript.NewP2PKHFromAddress(sfAddr)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: serviceFeeA.Uint64(), LockingScript: sfScript})
	}

	// FT-A change to sender
	if amountFTBN.Cmp(fttxoA.FtBalance) < 0 {
		changeCode, err2 := BuildFTtransferCode(ftaInfo.CodeScript, addr.AddressString)
		if err2 != nil {
			return "", err2
		}
		changeTape, err2 := BuildFTtransferTape(ftaInfo.TapeScript, changeHex)
		if err2 != nil {
			return "", err2
		}
		tx.AddOutput(&bt.Output{Satoshis: fttxoA.Satoshis, LockingScript: changeCode})
		tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeTape})
	}

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	withLockInt := isLockByCodeLen(p.PoolNftCode)
	poolUnlock, err := p.getPoolNftUnlock(privKey, tx, 0, hex.EncodeToString(poolnft.TxID), int(poolnft.Vout), withLockInt, 3, 2)
	if err != nil {
		return "", err
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	if isCoin {
		tx.Inputs[1].SequenceNumber = 4294967294
	}
	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	ftUnlock, err := ft.GetFTunlock(privKey, tx, ftPreTX, ftPrePreTxData, 1, int(fttxoA.Vout), isCoin)
	if err != nil {
		return "", err
	}
	tx.Inputs[1].UnlockingScript = ftUnlock

	err = signP2PKHAtIdx(tx, privKey, 2)
	if err != nil {
		return "", err
	}

	swapSuccess = true
	return tx.String(), nil
}

// --------------------------------------------------------------------------
// MergeFTLP — mirrors TS poolNFT2.mergeFTLP
// --------------------------------------------------------------------------

// MergeFTLP merges LP UTXOs. Returns "" if already merged (1 UTXO).
// MergeFTLP merges up to 5 (or 6 with_lock_time) FT-LP UTXOs at the sender
// address into one. lockTime is optional: when nil and p.WithLockTime, the
// derived lock time falls back to the max tape-lock-time observed in the
// merged set capped to current chain height (or current time for >=500m).
// Mirrors poolNFT2.0.ts mergeFTLP(privateKey, utxo, lock_time?).
func (p *PoolNFT2) MergeFTLP(
	privKey *bec.PrivateKey,
	utxo *bt.UTXO,
	lockTime *uint32,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("MergeFTLP FetchFtInfo: %w", err)
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}

	ftUtxoList, err := p.fetchFtlpUTXOList(addr.AddressString)
	if err != nil {
		return "", fmt.Errorf("MergeFTLP fetchFtlpUTXOList: %w", err)
	}
	if len(ftUtxoList) == 0 {
		return "", fmt.Errorf("no FT UTXO available")
	}
	if len(ftUtxoList) == 1 {
		return "", nil // already merged
	}

	// Select FT-LP UTXOs to merge: respects p.WithLockTime path per TS.
	var ftutxo []*api.LpUTXO
	var derivedLockTime uint32
	if p.WithLockTime {
		headers, hErr := api.FetchBlockHeaders(0, 1, p.Network)
		if hErr != nil || len(headers) == 0 {
			return "", fmt.Errorf("MergeFTLP FetchBlockHeaders: %w", hErr)
		}
		// TS: subtract 2 for safety; subtract 30 minutes from time.
		currentBlockHeight := uint32(headers[0].Height) - 2
		currentTime := uint32(time.Now().Unix()) - 1800
		var lockTimeMax uint32

		for i := 0; i < len(ftUtxoList) && len(ftutxo) < 6; i++ {
			ltape, lerr := api.FetchTXRaw(ftUtxoList[i].TxID, p.Network)
			if lerr != nil {
				return "", fmt.Errorf("MergeFTLP FetchTXRaw[%d]: %w", i, lerr)
			}
			tapeOutIdx := int(ftUtxoList[i].Vout) + 1
			if tapeOutIdx >= len(ltape.Outputs) {
				return "", fmt.Errorf("MergeFTLP: tape vout %d out of range", tapeOutIdx)
			}
			chunks := ltape.Outputs[tapeOutIdx].LockingScript.Chunks()
			if len(chunks) < 4 || len(chunks[3].Buf) < 4 {
				return "", fmt.Errorf("MergeFTLP: ftlp tape chunks[3] missing or short")
			}
			lockTimeFromTape := binary.LittleEndian.Uint32(chunks[3].Buf[:4])

			if lockTime != nil {
				lt := *lockTime
				switch {
				case lockTimeFromTape == 0:
					ftutxo = append(ftutxo, ftUtxoList[i])
				case lt < 500_000_000 && lockTimeFromTape <= lt:
					ftutxo = append(ftutxo, ftUtxoList[i])
				case lockTimeFromTape >= 500_000_000 && lockTimeFromTape <= lt:
					ftutxo = append(ftutxo, ftUtxoList[i])
				}
			} else {
				if lockTimeFromTape > lockTimeMax {
					lockTimeMax = lockTimeFromTape
				}
				switch {
				case lockTimeFromTape < 500_000_000 && lockTimeFromTape <= currentBlockHeight:
					ftutxo = append(ftutxo, ftUtxoList[i])
				case lockTimeFromTape >= 500_000_000 && lockTimeFromTape <= currentTime:
					ftutxo = append(ftutxo, ftUtxoList[i])
				}
			}
		}
		if len(ftutxo) == 0 {
			return "", fmt.Errorf("no unlockable FTLP UTXO")
		}
		if lockTime != nil {
			derivedLockTime = *lockTime
		} else if lockTimeMax < 500_000_000 {
			if lockTimeMax > currentBlockHeight {
				derivedLockTime = currentBlockHeight
			} else {
				derivedLockTime = lockTimeMax
			}
		} else {
			if lockTimeMax > currentTime {
				derivedLockTime = currentTime
			} else {
				derivedLockTime = lockTimeMax
			}
		}
	} else {
		count := len(ftUtxoList)
		if count > 5 {
			count = 5
		}
		ftutxo = ftUtxoList[:count]
	}

	count := len(ftutxo)
	ftutxoCodeScript := ftutxo[0].Script

	tapeAmountSetIn := make([]*big.Int, count)
	ftPreTXs := make([]*bt.Tx, count)
	ftPrePreTxDatas := make([]string, count)
	tapeAmountSum := new(big.Int)
	for i, u := range ftutxo {
		tapeAmountSetIn[i] = new(big.Int).Set(u.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, u.FtBalance)
		ftPreTXs[i], err = api.FetchTXRaw(u.TxID, p.Network)
		if err != nil {
			return "", err
		}
		ftPrePreTxDatas[i], err = api.FetchFtPrePreTxData(ftPreTXs[i], int(u.Vout), p.Network)
		if err != nil {
			return "", err
		}
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSetIn)
	zeroChange := strings.Repeat("00", 48)
	if changeHex != zeroChange {
		return "", fmt.Errorf("change amount is not zero")
	}

	tx := newFTTx()
	for _, u := range ftutxo {
		lpb, err2 := lpUTXOTobtUTXO(u)
		if err2 != nil {
			return "", err2
		}
		if err := tx.FromUTXOs(lpb); err != nil {
			return "", fmt.Errorf("MergeFTLP tx.FromUTXOs lpb: %w", err)
		}
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", fmt.Errorf("MergeFTLP tx.FromUTXOs utxo: %w", err)
	}

	// merged output
	mergedCode, err := BuildFTtransferCode(ftutxoCodeScript, addr.AddressString)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: mergedCode})

	// tape
	var tapeScript *bscript.Script
	if p.WithLockTime {
		fillSize := len(ftaInfo.TapeScript)/2 - 62
		opZeros := strings.Repeat("OP_0 ", fillSize)
		baseTape, asmErr := bscript.NewFromASM(fmt.Sprintf("OP_FALSE OP_RETURN %s 00000000 %s4654617065",
			strings.Repeat("0000000000000000", 6), opZeros))
		if asmErr != nil {
			return "", fmt.Errorf("MergeFTLP build with-lock-time tape: %w", asmErr)
		}
		tapeScript, err = BuildFTtransferTape(hex.EncodeToString(baseTape.Bytes()), amountHex)
		if err != nil {
			return "", err
		}
	} else {
		tapeScript, err = BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
		if err != nil {
			return "", err
		}
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: tapeScript})

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	// Set sequence numbers and lock time BEFORE signing — the sighash
	// preimage embeds tx.LockTime and each input's sequence number.
	// Setting these after signing (the previous bug) would produce an
	// invalid sigHash that the on-chain CHECKSIG would reject.
	if p.WithLockTime {
		for i := range ftutxo {
			tx.Inputs[i].SequenceNumber = 4294967294
		}
		tx.LockTime = derivedLockTime
	}

	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	for i, u := range ftutxo {
		unlock, err2 := ft.GetFTunlock(privKey, tx, ftPreTXs[i], ftPrePreTxDatas[i], i, int(u.Vout), false)
		if err2 != nil {
			return "", err2
		}
		tx.Inputs[i].UnlockingScript = unlock
	}

	if err := signP2PKHAtIdx(tx, privKey, uint32(count)); err != nil {
		return "", err
	}

	return tx.String(), nil
}

// --------------------------------------------------------------------------
// BurnFTLP — mirrors TS poolNFT2.burnFTLP
// --------------------------------------------------------------------------

// BurnFTLP sends all FT-LP UTXOs to the eater address.
func (p *PoolNFT2) BurnFTLP(
	privKey *bec.PrivateKey,
	utxo *bt.UTXO,
) (string, error) {
	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return "", fmt.Errorf("BurnFTLP FetchFtInfo: %w", err)
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", err
	}

	ftUtxoList, err := p.fetchFtlpUTXOList(addr.AddressString)
	if err != nil {
		return "", fmt.Errorf("BurnFTLP fetchFtlpUTXOList: %w", err)
	}
	if len(ftUtxoList) == 0 {
		return "", fmt.Errorf("No FT UTXO available")
	}

	count := len(ftUtxoList)
	if count > 5 {
		count = 5
	}
	ftutxo := ftUtxoList[:count]
	ftutxoCodeScript := ftutxo[0].Script

	tapeAmountSetIn := make([]*big.Int, count)
	ftPreTXs := make([]*bt.Tx, count)
	ftPrePreTxDatas := make([]string, count)
	tapeAmountSum := new(big.Int)
	for i, u := range ftutxo {
		tapeAmountSetIn[i] = new(big.Int).Set(u.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, u.FtBalance)
		ftPreTXs[i], err = api.FetchTXRaw(u.TxID, p.Network)
		if err != nil {
			return "", err
		}
		ftPrePreTxDatas[i], err = api.FetchFtPrePreTxData(ftPreTXs[i], int(u.Vout), p.Network)
		if err != nil {
			return "", err
		}
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSetIn)
	zeroChange := strings.Repeat("00", 48)
	if changeHex != zeroChange {
		return "", fmt.Errorf("Change amount is not zero")
	}

	tx := newFTTx()
	for _, u := range ftutxo {
		lpb, err2 := lpUTXOTobtUTXO(u)
		if err2 != nil {
			return "", err2
		}
		if err := tx.FromUTXOs(lpb); err != nil {
			return "", fmt.Errorf("BurnFTLP tx.FromUTXOs lpb: %w", err)
		}
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", fmt.Errorf("BurnFTLP tx.FromUTXOs utxo: %w", err)
	}

	burnCode, err := BuildFTtransferCode(ftutxoCodeScript, eaterAddress)
	if err != nil {
		return "", err
	}
	tapeScript, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: burnCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: tapeScript})

	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})
	adjustFeeAndChange(tx, 80)

	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	for i, u := range ftutxo {
		if p.WithLockTime {
			tx.Inputs[i].SequenceNumber = 4294967294
		}
		unlock, err2 := ft.GetFTunlock(privKey, tx, ftPreTXs[i], ftPrePreTxDatas[i], i, int(u.Vout), false)
		if err2 != nil {
			return "", err2
		}
		tx.Inputs[i].UnlockingScript = unlock
	}

	err = signP2PKHAtIdx(tx, privKey, uint32(count))
	if err != nil {
		return "", err
	}

	return tx.String(), nil
}

// --------------------------------------------------------------------------
// MergeFTinPool — mirrors TS poolNFT2.mergeFTinPool / _mergeFTinPool
// --------------------------------------------------------------------------

// MergeFTinPool merges FT-A UTXOs inside the pool. Returns raw tx array.
func (p *PoolNFT2) MergeFTinPool(
	privKey *bec.PrivateKey,
	utxo *bt.UTXO,
	times int,
) ([]string, error) {
	if times < 1 {
		times = 1
	}

	ftaInfo, err := api.FetchFtInfo(p.FtAContractTxID, p.Network)
	if err != nil {
		return nil, fmt.Errorf("MergeFTinPool FetchFtInfo: %w", err)
	}
	codeLen := len(ftaInfo.CodeScript) / 2
	ftVersion, isCoin := ftVersionFromCodeLen(codeLen)

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return nil, err
	}

	ftutxoCodeScript, err := buildFTTransferCodeHex(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return nil, err
	}

	ftutxoList, err := api.FetchFtUTXOList(p.FtAContractTxID, poolCodeHash160, ftutxoCodeScript, p.Network)
	if err != nil {
		return nil, fmt.Errorf("MergeFTinPool FetchFtUTXOList: %w", err)
	}

	// Sort descending by FtBalance
	sort.Slice(ftutxoList, func(i, j int) bool {
		return ftutxoList[i].FtBalance.Cmp(ftutxoList[j].FtBalance) > 0
	})

	poolnft, err := api.FetchPoolNFTUTXO(p.ContractTxID, p.Network)
	if err != nil {
		return nil, err
	}

	poolnftPreTX, err := api.FetchTXRaw(hex.EncodeToString(poolnft.TxID), p.Network)
	if err != nil {
		return nil, err
	}
	prePreTxID := hex.EncodeToString(poolnftPreTX.Inputs[0].PreviousTxID())
	poolnftPrePreTX, err := api.FetchTXRaw(prePreTxID, p.Network)
	if err != nil {
		return nil, err
	}

	var txsraw []string
	var lastTX *bt.Tx

	for i := 0; i < times; i++ {
		start := i * 4
		end := start + 4
		if end > len(ftutxoList) {
			end = len(ftutxoList)
		}
		ftutxos := ftutxoList[start:end]
		if len(ftutxos) <= 1 {
			break
		}

		var feeUTXO *bt.UTXO
		if i == 0 {
			feeUTXO = utxo
		} else if lastTX != nil {
			// use last output of previous tx; TxID is forward bytes (no reverse)
			lastOut := lastTX.Outputs[len(lastTX.Outputs)-1]
			lastTxIDBytes, err := hex.DecodeString(lastTX.TxID())
			if err != nil {
				return nil, fmt.Errorf("decode lastTX txid: %w", err)
			}
			feeUTXO = &bt.UTXO{
				TxID:          lastTxIDBytes,
				Vout:          uint32(len(lastTX.Outputs) - 1),
				LockingScript: lastOut.LockingScript,
				Satoshis:      lastOut.Satoshis,
			}
		}

		rawTX, newTX, err2 := p.mergeFTinPoolSingle(privKey, poolnftPreTX, poolnftPrePreTX, ftutxos, ftaInfo, ftVersion, isCoin, feeUTXO)
		if err2 != nil {
			if len(txsraw) > 0 {
				break // return what we have
			}
			return nil, err2
		}
		txsraw = append(txsraw, rawTX)
		poolnftPrePreTX = poolnftPreTX
		poolnftPreTX = newTX
		lastTX = newTX
	}

	return txsraw, nil
}

// mergeFTinPoolSingle builds a single FT merge transaction inside the pool.
func (p *PoolNFT2) mergeFTinPoolSingle(
	privKey *bec.PrivateKey,
	poolnftPreTX *bt.Tx,
	poolnftPrePreTX *bt.Tx,
	ftutxos []*util.FtUTXO,
	ftaInfo *api.FtInfoResponse,
	ftVersion int,
	isCoin bool,
	feeUTXO *bt.UTXO,
) (string, *bt.Tx, error) {
	poolnft := &bt.UTXO{
		TxID:     poolnftPreTX.TxIDBytes(),
		Vout:     0,
		Satoshis: poolnftPreTX.Outputs[0].Satoshis,
	}
	poolNftCodeScript, err := bscript.NewFromHexString(p.PoolNftCode)
	if err != nil {
		return "", nil, err
	}
	poolnft.LockingScript = poolNftCodeScript

	poolCodeHash160, err := poolNFTSHA256thenHash160(p.PoolNftCode)
	if err != nil {
		return "", nil, err
	}

	tapeAmountSetIn := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, ftu := range ftutxos {
		tapeAmountSetIn[i] = new(big.Int).Set(ftu.FtBalance)
		tapeAmountSum.Add(tapeAmountSum, ftu.FtBalance)
	}

	ftPreTXs := make([]*bt.Tx, len(ftutxos))
	ftPrePreTxDatas := make([]string, len(ftutxos))
	for i, ftu := range ftutxos {
		ftPreTXs[i], err = api.FetchTXRaw(hex.EncodeToString(ftu.TxID), p.Network)
		if err != nil {
			return "", nil, err
		}
		ftPrePreTxDatas[i], err = api.FetchFtPrePreTxData(ftPreTXs[i], int(ftu.Vout), p.Network)
		if err != nil {
			return "", nil, err
		}
	}

	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(tapeAmountSum, tapeAmountSetIn, 1)
	zeroChange := strings.Repeat("00", 48)
	if changeHex != zeroChange {
		return "", nil, fmt.Errorf("mergeFTinPool: Change amount is not zero")
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(poolnft); err != nil {
		return "", nil, fmt.Errorf("mergeFTinPoolSingle tx.FromUTXOs poolnft: %w", err)
	}
	for _, ftu := range ftutxos {
		if err := tx.FromUTXOs(util.FtUTXOToUTXO(ftu)); err != nil {
			return "", nil, fmt.Errorf("mergeFTinPoolSingle tx.FromUTXOs ftu: %w", err)
		}
	}
	if feeUTXO != nil {
		if err := tx.FromUTXOs(feeUTXO); err != nil {
			return "", nil, fmt.Errorf("mergeFTinPoolSingle tx.FromUTXOs feeUTXO: %w", err)
		}
	} else {
		// add last output of poolnftPreTX as fee input
		lastIdx := len(poolnftPreTX.Outputs) - 1
		lastTxIDBytes := poolnftPreTX.TxIDBytes()
		if err := tx.FromUTXOs(&bt.UTXO{
			TxID:          lastTxIDBytes,
			Vout:          uint32(lastIdx),
			LockingScript: poolnftPreTX.Outputs[lastIdx].LockingScript,
			Satoshis:      poolnftPreTX.Outputs[lastIdx].Satoshis,
		}); err != nil {
			return "", nil, fmt.Errorf("mergeFTinPoolSingle tx.FromUTXOs lastOut: %w", err)
		}
	}

	tx.AddOutput(&bt.Output{Satoshis: poolnft.Satoshis, LockingScript: poolNftCodeScript})

	poolnftTapeScript, err := p.updatePoolNftTape()
	if err != nil {
		return "", nil, err
	}
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: poolnftTapeScript})

	mergedCode, err := BuildFTtransferCode(ftaInfo.CodeScript, poolCodeHash160)
	if err != nil {
		return "", nil, err
	}
	mergedTape, err := BuildFTtransferTape(ftaInfo.TapeScript, amountHex)
	if err != nil {
		return "", nil, err
	}
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: mergedCode})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: mergedTape})

	addr, err := bscript.NewAddressFromPublicKeyHash(crypto.Hash160(privKey.PubKey().SerialiseCompressed()), true)
	if err != nil {
		return "", nil, err
	}
	changeScript, _ := bscript.NewP2PKHFromAddress(addr.AddressString)
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: changeScript})

	// Build inputsTXs for offline unlock: ftPreTXs + (feeUTXO tx or poolnftPreTX)
	inputsTXs := make([]*bt.Tx, len(ftutxos)+1)
	copy(inputsTXs, ftPreTXs)
	if feeUTXO != nil {
		// feeUTXO.TxID is already forward bytes — encode straight to hex.
		inputsTXs[len(ftutxos)], err = api.FetchTXRaw(hex.EncodeToString(feeUTXO.TxID), p.Network)
		if err != nil {
			return "", nil, err
		}
	} else {
		inputsTXs[len(ftutxos)] = poolnftPreTX
	}

	adjustFeeAndChange(tx, 80)

	withLockInt := isLockByCodeLen(p.PoolNftCode)
	poolUnlock, err := p.GetPoolNftUnlockOffLine(privKey, tx, 0, poolnftPreTX, poolnftPrePreTX, inputsTXs, withLockInt, 4, 0)
	if err != nil {
		return "", nil, err
	}
	tx.Inputs[0].UnlockingScript = poolUnlock

	ft := &FT{CodeScript: ftaInfo.CodeScript, TapeScript: ftaInfo.TapeScript}
	for i, ftu := range ftutxos {
		if isCoin {
			tx.Inputs[i+1].SequenceNumber = 4294967294
		}
		swapUnlock, err2 := ft.GetFTunlockSwap(privKey, tx, ftPreTXs[i], ftPrePreTxDatas[i], poolnftPreTX, i+1, int(ftu.Vout), ftVersion, isCoin)
		if err2 != nil {
			return "", nil, err2
		}
		tx.Inputs[i+1].UnlockingScript = swapUnlock
	}

	// The fee input at index len(ftutxos)+1 is always P2PKH and must be
	// signed regardless of whether feeUTXO was supplied or sourced from
	// poolnftPreTX.outputs[last] (chained-merge case). TS sealAsync's
	// tx.sign(privateKey) signs all P2PKH inputs unconditionally.
	if err = signP2PKHAtIdx(tx, privKey, uint32(len(ftutxos)+1)); err != nil {
		return "", nil, err
	}

	return tx.String(), tx, nil
}
