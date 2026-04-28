# 测试场景：NFT

**规范对照**：[tbc-contract/docs/nft.md](../../tbc-contract/docs/nft.md)  
**Go 实现**：`lib/contract/nft.go`；索引：`lib/api`。

---

## 参数定义

| 名称 | 必填 | 说明 | 默认值 |
|------|------|------|--------|
| `TBC_WIF` | 是 | 操作账户 WIF | — |
| `TBC_NETWORK` | 否 | `testnet` / `mainnet` | `testnet` |
| `NFT_ACTION` | 否 | `info`：仅拉元数据+`Initialize`；`create`：建合集+铸 1 枚；`transfer`：单笔转移 | `info` |
| `NFT_CONTRACT_ID` | `info`、`transfer` 时必填 | NFT 合约 id（单枚 txid） | — |
| `NFT_TO` | `transfer` 时必填 | 收款地址 | — |
| `NFT_COLLECTION_NAME` 等 | 否 | `create` 时合集字段 | `demo` / `demo collection` / `10` |

`create` 使用内置 **1×1 PNG** 的 data URL 作为 `CollectionData.File`（仅演示；大图请换成本地/对象存储 URL 并提高 `GetUTXOs` 金额）。

---

## 最小可执行脚本

```bash
export TBC_WIF='...'
export NFT_ACTION=info
export NFT_CONTRACT_ID='...'   # info / transfer
# create:
# export NFT_ACTION=create
# transfer:
# export NFT_ACTION=transfer
# export NFT_TO=1...
go run .
```

```go
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/LoongYearMeta/tbc-lib-go/bec"
	"github.com/LoongYearMeta/tbc-lib-go/wif"
	bt "github.com/LoongYearMeta/tbc-lib-go"
	"github.com/LoongYearMeta/tbc-lib-go/bscript"
	"github.com/LoongYearMeta/tbc-contract-go/lib/api"
	"github.com/LoongYearMeta/tbc-contract-go/lib/contract"
)

const tinyPNGDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func mustPriv() *bec.PrivateKey {
	d, err := wif.DecodeWIF(os.Getenv("TBC_WIF"))
	if err != nil {
		panic(err)
	}
	return d.PrivKey
}

func nw() string { return envOrDefault("TBC_NETWORK", "testnet") }

func apiNFTUTXOToBT(u *api.NFTUTXO) (*bt.UTXO, error) {
	tid, err := hex.DecodeString(strings.TrimSpace(u.TxID))
	if err != nil || len(tid) != 32 {
		return nil, fmt.Errorf("txid")
	}
	ls, err := bscript.NewFromHexString(u.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{TxID: tid, Vout: u.Vout, LockingScript: ls, Satoshis: u.Satoshis}, nil
}

func nftInfoAPIToContract(i *api.NFTInfo) *contract.NFTInfo {
	return &contract.NFTInfo{
		CollectionID: i.CollectionID, CollectionIndex: i.CollectionIndex, CollectionName: i.CollectionName,
		NftName: i.NftName, NftSymbol: i.NftSymbol, NftAttributes: i.NftAttributes,
		NftDescription: i.NftDescription, NftTransferTimeCount: i.NftTransferTimeCount, NftIcon: i.NftIcon,
	}
}

func runInfo() error {
	id := strings.TrimSpace(os.Getenv("NFT_CONTRACT_ID"))
	if id == "" {
		return fmt.Errorf("info 需要 NFT_CONTRACT_ID")
	}
	info, err := api.FetchNFTInfo(id, nw())
	if err != nil {
		return err
	}
	n := contract.NewNFT(id)
	n.Initialize(nftInfoAPIToContract(info))
	fmt.Printf("initialized NFT: %+v / collection=%s idx=%d\n", n.NftData, n.CollectionID, n.CollectionIndex)
	return nil
}

func runCreate(priv *bec.PrivateKey) error {
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return err
	}
	from := addr.AddressString
	utxos, err := api.GetUTXOs(from, 0.25, nw())
	if err != nil {
		return err
	}
	slice := make([]*bt.UTXO, len(utxos))
	for i := range utxos {
		slice[i] = utxos[i]
	}
	col := &contract.CollectionData{
		CollectionName: envOrDefault("NFT_COLLECTION_NAME", "demo"),
		Description:    envOrDefault("NFT_COLLECTION_DESC", "demo collection"),
		Supply:         10,
		File:           tinyPNGDataURL,
	}
	raw, err := contract.CreateCollection(from, priv, col, slice)
	if err != nil {
		return err
	}
	cid, err := api.BroadcastTXRaw(raw, nw())
	if err != nil {
		return err
	}
	fmt.Println("collection txid:", cid)

	ms, err := contract.BuildMintScript(from)
	if err != nil {
		return err
	}
	slot, err := api.FetchNFTUTXO(hex.EncodeToString(ms.Bytes()), cid, nw())
	if err != nil {
		return err
	}
	nutxo, err := apiNFTUTXOToBT(slot)
	if err != nil {
		return err
	}
	ut2, err := api.GetUTXOs(from, 0.03, nw())
	if err != nil {
		return err
	}
	s2 := make([]*bt.UTXO, len(ut2))
	for i := range ut2 {
		s2[i] = ut2[i]
	}
	nd := &contract.NFTData{NftName: "item1", Symbol: "IT1", Description: "d", Attributes: "", File: ""}
	raw2, err := contract.CreateNFT(cid, from, priv, nd, s2, nutxo)
	if err != nil {
		return err
	}
	nftid, err := api.BroadcastTXRaw(raw2, nw())
	fmt.Println("nft contract txid:", nftid)
	return err
}

func runTransfer(priv *bec.PrivateKey) error {
	nftid := strings.TrimSpace(os.Getenv("NFT_CONTRACT_ID"))
	to := strings.TrimSpace(os.Getenv("NFT_TO"))
	if nftid == "" || to == "" {
		return fmt.Errorf("transfer 需要 NFT_CONTRACT_ID 与 NFT_TO")
	}
	info, err := api.FetchNFTInfo(nftid, nw())
	if err != nil {
		return err
	}
	n := contract.NewNFT(nftid)
	n.Initialize(nftInfoAPIToContract(info))
	fromAddr, _ := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	from := fromAddr.AddressString

	code, err := contract.BuildCodeScript(info.CollectionID, uint32(info.CollectionIndex))
	if err != nil {
		return err
	}
	slot, err := api.FetchNFTUTXO(hex.EncodeToString(code.Bytes()), "", nw())
	if err != nil {
		return err
	}
	// 多候选时需按持有 NFT 过滤 slot；演示仅取索引首条。
	utxos, err := api.GetUTXOs(from, 0.03, nw())
	if err != nil {
		return err
	}
	us := make([]*bt.UTXO, len(utxos))
	for i := range utxos {
		us[i] = utxos[i]
	}
	preTx, err := api.FetchTXRaw(slot.TxID, nw())
	if err != nil {
		return err
	}
	prev := strings.TrimSpace(strings.ToLower(hex.EncodeToString(preTx.Inputs[0].PreviousTxID())))
	prePre, err := api.FetchTXRaw(prev, nw())
	if err != nil {
		return err
	}
	raw, err := n.TransferNFT(from, to, priv, us, preTx, prePre, false)
	if err != nil {
		return err
	}
	txid, err := api.BroadcastTXRaw(raw, nw())
	fmt.Println("transfer txid:", txid)
	return err
}

func main() {
	switch strings.ToLower(strings.TrimSpace(envOrDefault("NFT_ACTION", "info"))) {
	case "create":
		if err := runCreate(mustPriv()); err != nil {
			panic(err)
		}
	case "transfer":
		if err := runTransfer(mustPriv()); err != nil {
			panic(err)
		}
	default:
		if err := runInfo(); err != nil {
			panic(err)
		}
	}
}
```

## 预期

- `info`：不广播，仅验证 **`api.FetchNFTInfo` + `(*NFT).Initialize`** 与 TS `fetchNFTInfo` + `initialize` 一致。
- `create` / `transfer`：会广播；`transfer` 在索引返回多条 code 相同 UTXO 时需自行过滤到当前 NFT。
