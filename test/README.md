# test/

每个合约一份可单独运行的 `package main`，与 [`docs/test-cases/`](../docs/test-cases/) 一一对应。

每个程序在源码顶部有一段 **`const ( … )` 配置区**：把 WIF / 合约 txid / 阶段开关填好，然后跑：

```bash
go run ./test/ft
```

**所有写操作都会真实广播**到 `network`（默认 `testnet`），主网上请额外谨慎。

## 配置区约定

每个 main.go 顶端形如：

```go
const (
    network        = "testnet"
    wifA           = ""          // 必填：操作者 WIF
    addressB       = ""          // 仅 Transfer / BatchTransfer 需要
    ftContractTxid = ""          // 仅非 Mint 阶段需要

    doMint     = false
    doTransfer = false
    doBatch    = false
    doMerge    = false
)
```

把 WIF / txid 直接改成你的值；要跑哪个阶段就把对应的 `do*` 改成 `true`。`do*` 全部为 `false` 时程序什么都不做、直接退出。

## 目录索引

| 程序 | 对应文档 | 主要配置常量 |
|------|----------|--------------|
| [ft/](./ft) | [docs/test-cases/ft.md](../docs/test-cases/ft.md) | `wifA` `addressB` `ftContractTxid` `doMint/Transfer/Batch/Merge` |
| [nft/](./nft) | [docs/test-cases/nft.md](../docs/test-cases/nft.md) | `wifStr` `filePath` `transferTo` `doCollection/Mint/Batch/Transfer` |
| [multisig/](./multisig) | [docs/test-cases/multisig.md](../docs/test-cases/multisig.md) | `wifA/B/C` `ftContractTxid` `doCreate/SendTBC/SendFT/FTOut` |
| [htlc/](./htlc) | [docs/test-cases/htlc.md](../docs/test-cases/htlc.md) | `wifSender` `wifRecv` `secretInit` `htlcTXID` `doDeploy/Withdraw/Refund` |
| [orderbook/](./orderbook) | [docs/test-cases/orderbook.md](../docs/test-cases/orderbook.md) | `wifStr` `ftContractTxid` `taxAddress` `doSell` |
| [poolnft2/](./poolnft2) | [docs/test-cases/poolnft2.md](../docs/test-cases/poolnft2.md) | `wifA` `ftContractTxid` `poolContractTxidInit` 与 `doCreate/Init/Increase/Consume/SwapToFT/SwapToTBC/MergeLP/BurnLP/MergeFT/UnlockLP` |
| [piggybank/](./piggybank) | [docs/test-cases/piggybank.md](../docs/test-cases/piggybank.md) | `wifStr` `amountSat` `lockTime` `doFreeze/Unfreeze` |
| [stablecoin/](./stablecoin) | [docs/test-cases/stablecoin.md](../docs/test-cases/stablecoin.md) | — Go 侧已冻结，仅打印提示 |

## 设计约定

- **金额单位**：FT 走 `*big.Int`；Pool 走十进制字符串（如 `"30"`）；TBC / HTLC / PiggyBank 走 `uint64` satoshi。详见各文档顶部说明。
- **失败行为**：任意一步出错立即 `os.Exit(1)`，不静默吞错（与 lib 内部的 `(value, error)` 风格一致）。
- **OrderBook 撤销 / 撮合 / 买单**：这些路径需要预先存在的链上 UTXO + 多份前置交易，运行式测试很难自洽，本目录只覆盖卖单创建；手工流程参考文档对应 stub 段。

## 编译

```bash
# 不留产物（仓库根不会出现同名可执行文件）：
go build -o /dev/null ./test/...
# 或者用 vet（更快、纯静态）：
go vet ./test/...
```

直接 `go build ./test/...`（不加 `-o`）会在 **当前工作目录** 落下与子目录同名的可执行文件（比如 `ft`、`nft` 等），`.gitignore` 已经把这些文件名忽略；正常应避免这种构建方式。
