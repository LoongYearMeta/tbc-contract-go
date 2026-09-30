package contract

// TBC721CODE3, pinned to the compiler artifact published in tbc-contract 1.7.2.
import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-lib-go/crypto"
	"github.com/LoongYearMeta/tbc-lib-go/sighash"
	"strings"
)

//go:embed asm/tbc721_standard.hex
var tbc721StandardTemplate string

type TBC721Standard struct{}

func BuildTBC721StandardCode(txid string, vout uint32) (*bscript.Script, error) {
	root, err := nftUtxoHex(txid, vout)
	if err != nil {
		return nil, err
	}
	return bscript.NewFromHexString(strings.ReplaceAll(strings.TrimSpace(tbc721StandardTemplate), "<self.OriginalUTXO36>", "24"+root))
}
func ParseTBC721StandardCode(script *bscript.Script) (string, uint32, error) {
	fail := fmt.Errorf("TBC721Standard: Code does not match complete template")
	if script == nil {
		return "", 0, fail
	}
	parts := strings.Split(strings.TrimSpace(tbc721StandardTemplate), "<self.OriginalUTXO36>")
	prefix, _ := hex.DecodeString(parts[0])
	suffix, _ := hex.DecodeString(parts[1])
	data := script.Bytes()
	if len(data) != len(prefix)+37+len(suffix) || !bytes.HasPrefix(data, prefix) || !bytes.HasSuffix(data, suffix) || data[len(prefix)] != 36 {
		return "", 0, fail
	}
	root := append([]byte(nil), data[len(prefix)+1:len(prefix)+37]...)
	for i := 0; i < 16; i++ {
		root[i], root[31-i] = root[31-i], root[i]
	}
	return hex.EncodeToString(root[:32]), binary.LittleEndian.Uint32(root[32:]), nil
}
func nft721Inputs(tx *bt.Tx) []byte {
	result := []byte{}
	for _, input := range tx.Inputs {
		record := make([]byte, 40)
		id := input.PreviousTxID()
		for i := range id {
			record[i] = id[len(id)-1-i]
		}
		binary.LittleEndian.PutUint32(record[32:], input.PreviousTxOutIndex)
		binary.LittleEndian.PutUint32(record[36:], input.SequenceNumber)
		result = append(result, record...)
	}
	return result
}
func nft721Header(tx *bt.Tx) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b, tx.Version)
	binary.LittleEndian.PutUint32(b[4:], tx.LockTime)
	binary.LittleEndian.PutUint32(b[8:], uint32(len(tx.Inputs)))
	binary.LittleEndian.PutUint32(b[12:], uint32(len(tx.Outputs)))
	return b
}
func nft721UnlockHash(tx *bt.Tx) []byte {
	b := []byte{}
	for _, input := range tx.Inputs {
		var script []byte
		if input.UnlockingScript != nil {
			script = input.UnlockingScript.Bytes()
		}
		b = append(b, crypto.Sha256(script)...)
	}
	return crypto.Sha256(b)
}
func nft721Value(output *bt.Output) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, output.Satoshis)
	return b
}
func nft721Records(outputs []*bt.Output) []byte {
	b := []byte{}
	for _, output := range outputs {
		b = append(b, nft721Value(output)...)
		b = append(b, crypto.Sha256(output.LockingScript.Bytes())...)
	}
	return b
}

func BuildTBC721StandardUnlockWithSignature(signature, publicKey []byte, tx, parent, ancestor *bt.Tx) (*bscript.Script, error) {
	fail := func(message string) (*bscript.Script, error) { return nil, fmt.Errorf("TBC721Standard: %s", message) }
	for _, t := range []*bt.Tx{tx, parent, ancestor} {
		if t == nil || t.Version != 10 || len(t.Inputs) == 0 || len(t.Outputs) == 0 {
			return fail("invalid version 10 transaction")
		}
		for _, i := range t.Inputs {
			if i == nil || len(i.PreviousTxID()) != 32 {
				return fail("invalid input")
			}
		}
		for _, o := range t.Outputs {
			if o == nil || o.LockingScript == nil {
				return fail("invalid output")
			}
		}
	}
	if len(signature) < 2 || signature[len(signature)-1] != 0x41 || (len(publicKey) != 32 && len(publicKey) != 33 && len(publicKey) != 65) {
		return fail("invalid signature/public key")
	}
	if len(parent.Outputs) < 2 || tx.Inputs[0].PreviousTxIDStr() != parent.TxID() || tx.Inputs[0].PreviousTxOutIndex != 0 || !bytes.Equal(tx.Outputs[0].LockingScript.Bytes(), parent.Outputs[0].LockingScript.Bytes()) {
		return fail("Code must spend parent vout 0 and remain unchanged")
	}
	root, index, err := ParseTBC721StandardCode(parent.Outputs[0].LockingScript)
	if err != nil {
		return nil, err
	}
	source := parent.Inputs[0]
	vout := int(source.PreviousTxOutIndex)
	if source.PreviousTxIDStr() != ancestor.TxID() || vout >= len(ancestor.Outputs) {
		return fail("ancestor link mismatch")
	}
	previous := ancestor.Outputs[vout]
	if bytes.Equal(previous.LockingScript.Bytes(), parent.Outputs[0].LockingScript.Bytes()) {
		if vout != 0 {
			return fail("continued ancestry requires vout 0")
		}
	} else if root != source.PreviousTxIDStr() || index != source.PreviousTxOutIndex {
		return fail("original outpoint mismatch")
	}
	hold := parent.Outputs[1].LockingScript.Bytes()
	if len(hold) < 25 || !bytes.Equal(hold[:3], []byte{0x76, 0xa9, 0x14}) || !bytes.Equal(hold[23:25], []byte{0x88, 0xac}) || !bytes.Equal(crypto.Hash160(publicKey), hold[3:23]) {
		return fail("public key does not own Hold")
	}
	fields := [][]byte{signature, publicKey, nft721Value(tx.Outputs[0]), crypto.Sha256(tx.Outputs[0].LockingScript.Bytes()), nft721Records(tx.Outputs[1:]), nft721Inputs(tx),
		nft721Header(ancestor), append(crypto.Sha256(nft721Inputs(ancestor)), nft721UnlockHash(ancestor)...), nft721Records(ancestor.Outputs[:vout]), nft721Value(previous), crypto.Sha256(previous.LockingScript.Bytes()), nft721Records(ancestor.Outputs[vout+1:]),
		nft721Header(parent), nft721Inputs(parent), nft721UnlockHash(parent), nft721Value(parent.Outputs[0]), crypto.Sha256(parent.Outputs[0].LockingScript.Bytes()), nft721Value(parent.Outputs[1]), hold, nft721Records(parent.Outputs[2:])}
	script := &bscript.Script{}
	for _, field := range fields {
		var e error
		if len(field) == 1 && field[0] >= 1 && field[0] <= 16 {
			e = script.AppendOpcodes(0x50 + field[0])
		} else if len(field) == 1 && field[0] == 0x81 {
			e = script.AppendOpcodes(0x4f)
		} else {
			e = script.AppendPushData(field)
		}
		if e != nil {
			return nil, e
		}
	}
	return script, nil
}
func BuildTBC721StandardUnlock(key *bec.PrivateKey, tx, parent, ancestor *bt.Tx) (*bscript.Script, error) {
	digest, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	signature, err := key.Sign(digest)
	if err != nil {
		return nil, err
	}
	return BuildTBC721StandardUnlockWithSignature(append(signature.Serialise(), byte(sighash.AllForkID)), key.PubKey().SerialiseCompressed(), tx, parent, ancestor)
}
func nft721Funding(key *bec.PrivateKey, funding []*bt.UTXO, excluded ...string) error {
	owner, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		return err
	}
	script, err := bscript.NewP2PKHFromAddress(owner.AddressString)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, point := range excluded {
		seen[point] = true
	}
	for _, utxo := range funding {
		if utxo == nil || utxo.LockingScript == nil {
			return fmt.Errorf("TBC721Standard: nil funding UTXO")
		}
		point := fmt.Sprintf("%s:%d", utxo.TxIDStr(), utxo.Vout)
		if seen[point] || !bytes.Equal(utxo.LockingScript.Bytes(), script.Bytes()) {
			return fmt.Errorf("TBC721Standard: invalid or duplicate funding UTXO")
		}
		seen[point] = true
	}
	return nil
}
func (TBC721Standard) CreateCollection(address string, key *bec.PrivateKey, data *CollectionData, funding []*bt.UTXO) (string, error) {
	if data == nil || data.Supply <= 0 {
		return "", fmt.Errorf("TBC721Standard: supply must be positive")
	}
	if err := nft721Funding(key, funding); err != nil {
		return "", err
	}
	return CreateCollection(address, key, data, funding)
}

// Transfer preserves the raw metadata, including fields unknown to this SDK.
func (TBC721Standard) Transfer(key *bec.PrivateKey, recipient string, funding []*bt.UTXO, parent, ancestor *bt.Tx, paymentAddress string, paymentSat uint64) (string, error) {
	return (TBC721Standard{}).TransferWithOptions(key, recipient, funding, parent, ancestor, paymentAddress, paymentSat, false)
}

// TransferWithOptions supports JS transferNft's batch (no fee change) mode.
func (TBC721Standard) TransferWithOptions(key *bec.PrivateKey, recipient string, funding []*bt.UTXO, parent, ancestor *bt.Tx, paymentAddress string, paymentSat uint64, omitChange bool) (string, error) {
	if parent == nil || len(parent.Outputs) < 3 {
		return "", fmt.Errorf("TBC721Standard: missing Code/Hold/Tape")
	}
	if _, _, err := ParseTBC721StandardCode(parent.Outputs[0].LockingScript); err != nil {
		return "", err
	}
	owner, err := bscript.NewAddressFromPublicKey(key.PubKey(), true)
	if err != nil {
		return "", err
	}
	oldHold, err := BuildNFTHoldScript(owner.AddressString)
	if err != nil {
		return "", err
	}
	if parent.Outputs[0].Satoshis != 200 || parent.Outputs[1].Satoshis != 100 || parent.Outputs[2].Satoshis != 0 || !bytes.Equal(parent.Outputs[1].LockingScript.Bytes(), oldHold.Bytes()) || !bytes.HasPrefix(parent.Outputs[2].LockingScript.Bytes(), []byte{0, 0x6a}) || !bytes.HasSuffix(parent.Outputs[2].LockingScript.Bytes(), []byte("\x05NTape")) {
		return "", fmt.Errorf("TBC721Standard: invalid Code/Hold/Tape")
	}
	if err := nft721Funding(key, funding, parent.TxID()+":0", parent.TxID()+":1"); err != nil {
		return "", err
	}
	tx := newFTTx()
	for i := 0; i < 2; i++ {
		if err := tx.From(parent.TxID(), uint32(i), parent.Outputs[i].LockingScript.String(), parent.Outputs[i].Satoshis); err != nil {
			return "", err
		}
	}
	if err := tx.FromUTXOs(funding...); err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(recipient)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{Satoshis: 200, LockingScript: parent.Outputs[0].LockingScript})
	tx.AddOutput(&bt.Output{Satoshis: 100, LockingScript: hold})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: parent.Outputs[2].LockingScript})
	if paymentAddress != "" {
		if paymentSat < 24 {
			return "", fmt.Errorf("TBC721Standard: attached TBC below 24 sat")
		}
		script, err := bscript.NewP2PKHFromAddress(paymentAddress)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{Satoshis: paymentSat, LockingScript: script})
	}
	if omitChange {
		if err := tx.FillAllInputs(context.Background(), &nftTransferUnlockerGetter{priv: key, preTx: parent, prePre: ancestor}); err != nil {
			return "", err
		}
		target, err := contractTargetFee(len(tx.Bytes()))
		if err != nil {
			return "", err
		}
		if err := verifyPaidFee(tx, target); err != nil {
			return "", err
		}
		return tx.String(), nil
	}
	if err := tx.ChangeToAddress(owner.AddressString, nftFeeQuote80()); err != nil {
		return "", err
	}
	if err := finalizeSignedFee(tx, len(tx.Outputs)-1, func() error {
		return tx.FillAllInputs(context.Background(), &nftTransferUnlockerGetter{priv: key, preTx: parent, prePre: ancestor})
	}); err != nil {
		return "", err
	}
	return tx.String(), nil
}

func (TBC721Standard) CreateNFT(collectionID, address string, priv *bec.PrivateKey, data *NFTData, utxos []*bt.UTXO, nftUtxo *bt.UTXO) (string, error) {
	if data == nil || nftUtxo == nil {
		return "", fmt.Errorf("TBC721Standard: missing metadata/mint slot")
	}
	if err := nft721Funding(priv, utxos, fmt.Sprintf("%s:%d", nftUtxo.TxIDStr(), nftUtxo.Vout)); err != nil {
		return "", err
	}
	owner, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return "", err
	}
	expected, err := BuildMintScript(owner.AddressString)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(collectionID, nftUtxo.TxIDStr()) || nftUtxo.Satoshis != 100 || nftUtxo.LockingScript == nil || !bytes.Equal(nftUtxo.LockingScript.Bytes(), expected.Bytes()) {
		return "", fmt.Errorf("TBC721Standard: invalid mint slot")
	}
	copyData := *data
	data = &copyData

	if data.File == "" {
		voutBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(voutBuf, nftUtxo.Vout)
		data.File = collectionID + hex.EncodeToString(voutBuf)
	}
	code, err := BuildTBC721StandardCode(nftUtxo.TxIDStr(), nftUtxo.Vout)
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

// BatchCreateNFT builds an ordered chain, reusing each mint's funding change.
// It never broadcasts any of the resulting transactions.
func (n TBC721Standard) BatchCreateNFT(collectionID, recipient string, key *bec.PrivateKey, data []*NFTData, funding []*bt.UTXO, slots []*bt.UTXO) ([]string, error) {
	if len(data) != len(slots) {
		return nil, fmt.Errorf("TBC721Standard: metadata and mint slot counts must match")
	}
	seen := make(map[string]bool)
	for _, slot := range slots {
		if slot == nil {
			return nil, fmt.Errorf("TBC721Standard: missing mint slot")
		}
		point := fmt.Sprintf("%s:%d", strings.ToLower(slot.TxIDStr()), slot.Vout)
		if seen[point] {
			return nil, fmt.Errorf("TBC721Standard: duplicate mint slot")
		}
		seen[point] = true
	}
	result := make([]string, 0, len(data))
	for i, item := range data {
		raw, err := n.CreateNFT(collectionID, recipient, key, item, funding, slots[i])
		if err != nil {
			return nil, err
		}
		result = append(result, raw)
		if i+1 < len(data) {
			tx, err := bt.NewTxFromString(raw)
			if err != nil {
				return nil, err
			}
			if len(tx.Outputs) != 4 {
				return nil, fmt.Errorf("TBC721Standard: insufficient change to continue batch minting")
			}
			id, _ := hex.DecodeString(tx.TxID())
			funding = []*bt.UTXO{{TxID: id, Vout: 3, LockingScript: tx.Outputs[3].LockingScript, Satoshis: tx.Outputs[3].Satoshis}}
		}
	}
	return result, nil
}
