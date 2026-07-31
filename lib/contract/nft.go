package contract

// Port of tbc-contract/lib/contract/nft.ts.
// NFT collection creation, individual-NFT minting, and NFT transfers.

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LoongYearMeta/tbc-contract-go/lib/util"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"github.com/LoongYearMeta/tbc-lib-go/unlocker"
)

//go:embed asm/nft_code.asm
var nftCodeTemplateASM string

//go:embed asm/nft_code_v0.asm
var nftCodeV0TemplateASM string

const nftSatPerKB = 80

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// CollectionData is the createCollection parameter (JSON matches TS CollectionData).
type CollectionData struct {
	CollectionName string `json:"collectionName"`
	Description    string `json:"description"`
	Supply         int    `json:"supply"`
	File           string `json:"file"`
}

// NFTData is the createNFT / tape content.
type NFTData struct {
	NftName     string `json:"nftName"`
	Symbol      string `json:"symbol"`
	Description string `json:"description"`
	Attributes  string `json:"attributes"`
	File        string `json:"file"`
}

// NFT is the non-fungible token handle.
type NFT struct {
	CollectionID    string
	CollectionIndex int
	CollectionName  string
	TransferCount   int
	ContractID      string
	NftData         NFTData
}

// ---------------------------------------------------------------------------
// Constructor and initialization
// ---------------------------------------------------------------------------

// NewNFT constructs an NFT handle from the contract ID (typically the collection genesis txid).
func NewNFT(contractID string) *NFT {
	return &NFT{ContractID: contractID}
}

// Initialize populates the NFT from util.NFTInfo.
// Mirrors TS NFT.initialize(nftInfo).
func (n *NFT) Initialize(info *util.NFTInfo) {
	file := n.ContractID + "00000000"
	n.NftData = NFTData{
		NftName:     info.NFTName,
		Symbol:      info.NFTSymbol,
		Description: info.NFTDescription,
		Attributes:  info.NFTAttributes,
		File:        file,
	}
	n.CollectionID = info.CollectionID
	n.CollectionIndex = info.CollectionIndex
	n.CollectionName = info.CollectionName
	n.TransferCount = info.NFTTransferTimeCount
}

// ---------------------------------------------------------------------------
// Script builders (static, mirror TS static methods)
// ---------------------------------------------------------------------------

// nftUtxoHex converts a txHash (hex) + vout into the 36-byte reversed-txid + LE-uint32 hex.
func nftUtxoHex(txHash string, vout uint32) (string, error) {
	txidBytes, err := hex.DecodeString(txHash)
	if err != nil || len(txidBytes) != 32 {
		return "", fmt.Errorf("nftUtxoHex: invalid txid %q", txHash)
	}
	buf := make([]byte, 36)
	for i := 0; i < 32; i++ {
		buf[i] = txidBytes[31-i]
	}
	binary.LittleEndian.PutUint32(buf[32:], vout)
	return hex.EncodeToString(buf), nil
}

func parseNFTCodeASM(asm string) (*bscript.Script, error) {
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// BuildCodeScript mirrors NFT.buildCodeScript.
func BuildCodeScript(txHash string, outputIndex uint32) (*bscript.Script, error) {
	utxoHex, err := nftUtxoHex(txHash, outputIndex)
	if err != nil {
		return nil, err
	}
	asm := strings.ReplaceAll(nftCodeTemplateASM, "${utxoHex}", utxoHex)
	return parseNFTCodeASM(asm)
}

// BuildCodeScriptV0 mirrors NFT.buildCodeScript_v0.
func BuildCodeScriptV0(txHash string, outputIndex uint32) (*bscript.Script, error) {
	utxoHex, err := nftUtxoHex(txHash, outputIndex)
	if err != nil {
		return nil, err
	}
	asm := strings.ReplaceAll(nftCodeV0TemplateASM, "${utxoHex}", utxoHex)
	return parseNFTCodeASM(asm)
}

// BuildMintScript mirrors NFT.buildMintScript.
// "V0 MintNHold" marker: 0x0d 0x5630204d696e74204e486f6c64
func BuildMintScript(address string) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x0d 0x5630204d696e74204e486f6c64", addr.PublicKeyHash)
	return parseNFTCodeASM(asm)
}

// BuildNFTHoldScript mirrors NFT.buildHoldScript.
// "V0 Curr NHold" marker: 0x0d 0x56302043757272204e486f6c64
func BuildNFTHoldScript(address string) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x0d 0x56302043757272204e486f6c64", addr.PublicKeyHash)
	return parseNFTCodeASM(asm)
}

// BuildNFTTapeScript mirrors NFT.buildTapeScript (CollectionData | NFTData → JSON hex).
func BuildNFTTapeScript(data interface{}) (*bscript.Script, error) {
	j, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", hex.EncodeToString(j))
	return bscript.NewFromASM(asm)
}

// EncodeNFTDataToHex mirrors NFT.encodeNFTDataToHex.
func EncodeNFTDataToHex(data interface{}) (string, error) {
	j, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(j), nil
}

// DecodeNFTDataFromHex mirrors NFT.decodeNFTDataFromHex.
func DecodeNFTDataFromHex(h string) (map[string]interface{}, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode nft json: %w", err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Unlock script builders
// ---------------------------------------------------------------------------

// BuildNFTUnlockScript mirrors NFT.buildUnlockScript.
func BuildNFTUnlockScript(priv *bec.PrivateKey, currentTX *bt.Tx, preTX *bt.Tx, prePreTx *bt.Tx, currentUnlockIndex uint32) (*bscript.Script, error) {
	cur, err := util.GetNFTCurrentTxdata(currentTX)
	if err != nil {
		return nil, err
	}
	prepre, err := util.GetNFTPrePreTxdata(prePreTx)
	if err != nil {
		return nil, err
	}
	pre, err := util.GetNFTPreTxdata(preTX)
	if err != nil {
		return nil, err
	}
	sh, err := currentTX.CalcInputSignatureHash(currentUnlockIndex, sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := append(sig.Serialise(), byte(sighash.AllForkID))
	pub := priv.PubKey().SerialiseCompressed()

	txdata, err := hex.DecodeString(cur + prepre + pre)
	if err != nil {
		return nil, err
	}
	var sb bscript.Script
	if err := sb.AppendPushData(sigBytes); err != nil {
		return nil, err
	}
	if err := sb.AppendPushData(pub); err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(sb.Bytes(), txdata...)), nil
}

// ---------------------------------------------------------------------------
// Fee helpers (no env vars: hardcoded 80 sat/KB to match TS feePerKb(80))
// ---------------------------------------------------------------------------

func nftFeeQuote80() *bt.FeeQuote {
	fq := bt.NewFeeQuote()
	fq.AddQuote(bt.FeeTypeStandard, &bt.Fee{
		FeeType:   bt.FeeTypeStandard,
		MiningFee: bt.FeeUnit{Satoshis: nftSatPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: nftSatPerKB, Bytes: 1000},
	})
	fq.AddQuote(bt.FeeTypeData, &bt.Fee{
		FeeType:   bt.FeeTypeData,
		MiningFee: bt.FeeUnit{Satoshis: nftSatPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: nftSatPerKB, Bytes: 1000},
	})
	return fq
}

// ---------------------------------------------------------------------------
// Internal unlocker types
// ---------------------------------------------------------------------------

// nftIn0Unlocker produces the full NFT unlock script for input 0 (code output).
type nftIn0Unlocker struct {
	priv          *bec.PrivateKey
	preTx, prePre *bt.Tx
	useV0         bool
}

func (u *nftIn0Unlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	shf := p.SigHashFlags
	if shf == 0 {
		shf = sighash.AllForkID
	}
	var cur, prepre, pre string
	var err error
	if u.useV0 {
		cur, err = util.GetNFTCurrentTxdataV0(tx)
		if err != nil {
			return nil, err
		}
		prepre, err = util.GetNFTPrePreTxdataV0(u.prePre)
		if err != nil {
			return nil, err
		}
		pre, err = util.GetNFTPreTxdataV0(u.preTx)
		if err != nil {
			return nil, err
		}
	} else {
		cur, err = util.GetNFTCurrentTxdata(tx)
		if err != nil {
			return nil, err
		}
		prepre, err = util.GetNFTPrePreTxdata(u.prePre)
		if err != nil {
			return nil, err
		}
		pre, err = util.GetNFTPreTxdata(u.preTx)
		if err != nil {
			return nil, err
		}
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, shf)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := append(sig.Serialise(), byte(shf))
	pub := u.priv.PubKey().SerialiseCompressed()
	txdata, err := hex.DecodeString(cur + prepre + pre)
	if err != nil {
		return nil, err
	}
	var sb bscript.Script
	if err := sb.AppendPushData(sigBytes); err != nil {
		return nil, err
	}
	if err := sb.AppendPushData(pub); err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(sb.Bytes(), txdata...)), nil
}

// nftIn1Unlocker produces a plain P2PKH unlock script for input 1 (hold output).
type nftIn1Unlocker struct{ priv *bec.PrivateKey }

func (u *nftIn1Unlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	shf := p.SigHashFlags
	if shf == 0 {
		shf = sighash.AllForkID
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, shf)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	return bscript.NewP2PKHUnlockingScript(u.priv.PubKey().SerialiseCompressed(), sig.Serialise(), shf)
}

// nftTransferUnlockerGetter dispatches: input 0 → nftIn0Unlocker, input 1 → nftIn1Unlocker,
// any further inputs (fee UTXOs) → unlocker.Simple (P2PKH).
type nftTransferUnlockerGetter struct {
	priv          *bec.PrivateKey
	preTx, prePre *bt.Tx
	useV0         bool
	step          int
}

func (g *nftTransferUnlockerGetter) Unlocker(ctx context.Context, ls *bscript.Script) (bt.Unlocker, error) {
	i := g.step
	g.step++
	switch i {
	case 0:
		return &nftIn0Unlocker{priv: g.priv, preTx: g.preTx, prePre: g.prePre, useV0: g.useV0}, nil
	case 1:
		return &nftIn1Unlocker{priv: g.priv}, nil
	default:
		return &unlocker.Simple{PrivateKey: g.priv}, nil
	}
}

// p2pkhMintUnlocker handles the P2PKH + OP_RETURN suffix mint-hold input (input 0 for CreateNFT).
// The locking script is not IsP2PKH() because it has an OP_RETURN suffix, so
// unlocker.Simple would refuse it. We extract the PKH prefix and sign normally.
type p2pkhMintUnlocker struct{ priv *bec.PrivateKey }

func (u *p2pkhMintUnlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	if p.SigHashFlags == 0 {
		p.SigHashFlags = sighash.AllForkID
	}
	prevScript := tx.Inputs[p.InputIdx].PreviousTxScript
	pkh, err := prevScript.PublicKeyHash()
	if err != nil {
		// Script not PKH-based; skip (return empty).
		return bscript.NewFromBytes(nil), nil
	}
	keyPKH := crypto.Hash160(u.priv.PubKey().SerialiseCompressed())
	if !bytes.Equal(pkh, keyPKH) {
		return bscript.NewFromBytes(nil), nil
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, p.SigHashFlags)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	return bscript.NewP2PKHUnlockingScript(u.priv.PubKey().SerialiseCompressed(), sig.Serialise(), p.SigHashFlags)
}

// p2pkhMintUnlockerGetter is the UnlockerGetter for CreateNFT / BatchCreateNFT.
type p2pkhMintUnlockerGetter struct{ priv *bec.PrivateKey }

func (g *p2pkhMintUnlockerGetter) Unlocker(ctx context.Context, lockingScript *bscript.Script) (bt.Unlocker, error) {
	return &p2pkhMintUnlocker{priv: g.priv}, nil
}

// ---------------------------------------------------------------------------
// CreateCollection
// ---------------------------------------------------------------------------

// CreateCollection mirrors NFT.createCollection.
// Returns the signed raw tx hex.
func CreateCollection(address string, priv *bec.PrivateKey, data *CollectionData, utxos []*bt.UTXO) (string, error) {
	if data.Supply < 0 || data.Supply > 100000 {
		return "", fmt.Errorf("invalid supply %d", data.Supply)
	}
	tape, err := BuildNFTTapeScript(data)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	for i := 0; i < data.Supply; i++ {
		ms, err := BuildMintScript(address)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: ms, Satoshis: 100})
	}
	if err := tx.ChangeToAddress(address, nftFeeQuote80()); err != nil {
		return "", err
	}
	if err := nftApplyJSFeeAndSign(
		tx,
		&unlocker.Getter{PrivateKey: priv},
	); err != nil {
		return "", fmt.Errorf("CreateCollection: finalize fee: %w", err)
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// CreateNFT
// ---------------------------------------------------------------------------

// nftAddrMainnet reports whether a network string means mainnet.
func nftAddrMainnet(network string) bool {
	n := strings.TrimSpace(strings.ToLower(network))
	return n == "" || n == "mainnet"
}

// nftApplyJSFee performs the two-pass fee adjustment that mirrors TS's
// tx.getEstimateSize() → fee(80)/feePerKb(80) → change → sign → seal:
//  1. ChangeToAddress with 80 sat/KB (this inserts the change output).
//  2. Sign all inputs.
//  3. Compute actual byte length of the serialised tx.
//  4. AdjustImplicitFeeToTarget to the exact ceil(len*80/1000) target.
//  5. Sign again so signatures cover the updated change amount.
//
// The caller is responsible for inserting the correct unlock scripts via the
// unlockerGetter. Returns the raw tx hex.
func nftApplyJSFeeAndSign(tx *bt.Tx, ug bt.UnlockerGetter) error {
	ctx := context.Background()
	return finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		return tx.FillAllInputs(ctx, ug)
	})
}

// CreateNFT mirrors NFT.createNFT.
// Returns the signed raw tx hex.
func CreateNFT(collectionID, address string, priv *bec.PrivateKey, data *NFTData, utxos []*bt.UTXO, nftUtxo *bt.UTXO) (string, error) {
	if data.File == "" {
		voutBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(voutBuf, nftUtxo.Vout)
		data.File = collectionID + hex.EncodeToString(voutBuf)
	}
	code, err := BuildCodeScript(nftUtxo.TxIDStr(), nftUtxo.Vout)
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(address)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(data)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	// input 0: the mint-hold UTXO (P2PKH + OP_RETURN suffix)
	if err := tx.From(nftUtxo.TxIDStr(), nftUtxo.Vout, nftUtxo.LockingScript.String(), nftUtxo.Satoshis); err != nil {
		return "", err
	}
	// fee inputs
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := tx.ChangeToAddress(address, nftFeeQuote80()); err != nil {
		return "", err
	}

	ug := &p2pkhMintUnlockerGetter{priv: priv}
	if err := nftApplyJSFeeAndSign(tx, ug); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// BatchCreateNFT
// ---------------------------------------------------------------------------

// BatchCreateNFT mirrors NFT.batchCreateNFT.
// Each tx's change (output[3]) is chained as fee input for the next tx.
// network is "mainnet" or "testnet"; controls the change address derivation.
// Returns a slice of signed raw tx hex, one per NFT.
func BatchCreateNFT(collectionID, address string, priv *bec.PrivateKey, datas []NFTData, utxos []*bt.UTXO, nftUtxos []*bt.UTXO, network string) ([]string, error) {
	if len(datas) != len(nftUtxos) {
		return nil, fmt.Errorf("datas length (%d) must match nftUtxos length (%d)", len(datas), len(nftUtxos))
	}
	if len(datas) == 0 {
		return nil, fmt.Errorf("BatchCreateNFT: empty batch")
	}

	hold, err := BuildNFTHoldScript(address)
	if err != nil {
		return nil, err
	}

	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), nftAddrMainnet(network))
	if err != nil {
		return nil, err
	}
	changeAddr := addr.AddressString

	out := make([]string, 0, len(datas))
	var prevTx *bt.Tx

	for i := range datas {
		d := datas[i]
		voutBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(voutBuf, nftUtxos[i].Vout)
		if strings.TrimSpace(d.File) == "" {
			d.File = collectionID + hex.EncodeToString(voutBuf)
		}

		code, err := BuildCodeScript(nftUtxos[i].TxIDStr(), nftUtxos[i].Vout)
		if err != nil {
			return nil, err
		}
		tape, err := BuildNFTTapeScript(&d)
		if err != nil {
			return nil, err
		}

		tx := newFTTx()
		// input 0: mint-hold UTXO
		if err := tx.From(nftUtxos[i].TxIDStr(), nftUtxos[i].Vout, nftUtxos[i].LockingScript.String(), nftUtxos[i].Satoshis); err != nil {
			return nil, err
		}
		if i == 0 {
			if len(utxos) == 0 {
				return nil, fmt.Errorf("BatchCreateNFT: utxos required for first tx")
			}
			if err := tx.FromUTXOs(utxos...); err != nil {
				return nil, err
			}
		} else {
			// input 1: change output (output[3]) from previous tx
			if prevTx == nil || len(prevTx.Outputs) < 4 {
				return nil, fmt.Errorf("BatchCreateNFT: previous tx missing change output at index 3")
			}
			o := prevTx.Outputs[3]
			if err := tx.From(prevTx.TxID(), 3, o.LockingScript.String(), o.Satoshis); err != nil {
				return nil, err
			}
		}

		tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
		tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
		tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
		if err := tx.ChangeToAddress(changeAddr, nftFeeQuote80()); err != nil {
			return nil, err
		}

		ug := &p2pkhMintUnlockerGetter{priv: priv}
		if err := nftApplyJSFeeAndSign(tx, ug); err != nil {
			return nil, err
		}

		raw := hex.EncodeToString(tx.Bytes())
		out = append(out, raw)

		// Reconstruct prevTx from the serialised raw so subsequent iterations can
		// reference output satoshi / script correctly (FillAllInputs may have
		// modified the change output satoshis).
		newPrev, err := bt.NewTxFromString(raw)
		if err != nil {
			return nil, fmt.Errorf("BatchCreateNFT: parse self tx: %w", err)
		}
		prevTx = newPrev
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// TransferNFT
// ---------------------------------------------------------------------------

// TransferNFT mirrors NFT.transferNFT.
// batch=true skips adding a change output (caller chains the next tx manually).
func (n *NFT) TransferNFT(addressFrom, addressTo string, priv *bec.PrivateKey, utxos []*bt.UTXO, preTx, prePreTx *bt.Tx, batch bool) (string, error) {
	code, err := BuildCodeScript(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressTo)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(&n.NftData)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if len(utxos) > 0 {
		if err := tx.FromUTXOs(utxos...); err != nil {
			return "", err
		}
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})

	addChange := !batch
	if addChange {
		if err := tx.ChangeToAddress(addressFrom, nftFeeQuote80()); err != nil {
			return "", err
		}
	}

	ctx := context.Background()
	if addChange {
		if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
			ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: false}
			return tx.FillAllInputs(ctx, ug)
		}); err != nil {
			return "", fmt.Errorf("TransferNFT: finalize fee: %w", err)
		}
	} else {
		ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: false}
		if err := tx.FillAllInputs(ctx, ug); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// TransferNFTWithTBC
// ---------------------------------------------------------------------------

// TransferNFTWithTBC mirrors NFT.transferNFTWithTBC.
// tbcAmountSat is the satoshi amount to send to addressToTbc alongside the NFT.
func (n *NFT) TransferNFTWithTBC(addressFrom, addressToNft, addressToTbc string, priv *bec.PrivateKey,
	utxos []*bt.UTXO, preTx, prePreTx *bt.Tx, tbcAmountSat uint64) (string, error) {
	code, err := BuildCodeScript(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressToNft)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(&n.NftData)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if tbcAmountSat > 0 {
		if err := tx.PayToAddress(addressToTbc, tbcAmountSat); err != nil {
			return "", err
		}
	}
	if err := tx.ChangeToAddress(addressFrom, nftFeeQuote80()); err != nil {
		return "", err
	}

	ctx := context.Background()
	if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: false}
		return tx.FillAllInputs(ctx, ug)
	}); err != nil {
		return "", fmt.Errorf("TransferNFTWithTBC: finalize fee: %w", err)
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// ---------------------------------------------------------------------------
// TransferNFTV0
// ---------------------------------------------------------------------------

// TransferNFTV0 mirrors NFT.transferNFT_v0 (legacy V0 code script + V0 txdata).
func (n *NFT) TransferNFTV0(addressFrom, addressTo string, priv *bec.PrivateKey, utxos []*bt.UTXO, preTx, prePreTx *bt.Tx) (string, error) {
	code, err := BuildCodeScriptV0(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressTo)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(&n.NftData)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if len(utxos) > 0 {
		if err := tx.FromUTXOs(utxos...); err != nil {
			return "", err
		}
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := tx.ChangeToAddress(addressFrom, nftFeeQuote80()); err != nil {
		return "", err
	}

	ctx := context.Background()
	if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: true}
		return tx.FillAllInputs(ctx, ug)
	}); err != nil {
		return "", fmt.Errorf("TransferNFTV0: finalize fee: %w", err)
	}
	return hex.EncodeToString(tx.Bytes()), nil
}
