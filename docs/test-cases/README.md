# 测试场景说明（test-cases）

本目录每个 `*.md` 均采用统一结构：

1. **参数定义**：环境变量与常量表（开头）。
2. **最小可执行脚本**：整段 **`package main`**，保存为业务仓库中的 `main.go`，在已配置 `replace` 与 `tbc-contract-go` 依赖的模块根目录执行 **`go run .`**。

规范原文仍以 **`../../tbc-contract/docs/`** 下各 Markdown 为准（与 `tbc-contract` 并列克隆时路径成立）。

## 场景索引

| 文件 | 对齐 TS 文档 |
|------|----------------|
| [ft.md](./ft.md) | `../../tbc-contract/docs/ft.md` |
| [stablecoin.md](./stablecoin.md) | `../../tbc-contract/docs/stableCoin.md` ⚠️ **Go 端已冻结**：`stablecoin.go` / `api_stablecoin.go` 带 `legacy_stablecoin` 构建标签，默认 `go build ./...` 不参与编译，场景文档仅作 TS 对照用 |
| [nft.md](./nft.md) | `../../tbc-contract/docs/nft.md` |
| [orderbook.md](./orderbook.md) | `../../tbc-contract/docs/orderBook.md` |
| [multisig.md](./multisig.md) | `../../tbc-contract/docs/multiSIg.md` |
| [htlc.md](./htlc.md) | `../../tbc-contract/docs/htlc.md` |
| [poolnft2.md](./poolnft2.md) | `../../tbc-contract/docs/poolNFT2.0.md` |
| [piggybank.md](./piggybank.md) | TS `piggyBank.ts` |
