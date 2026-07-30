# tbc-contract-go 文档

本目录存放 **Go 合约库说明**、**快速上手** 与 **测试场景 / 用例说明**（手工或业务仓自动化验证时对照）。布局参考并列仓库 **`tbc-contract/docs`**：规范与脚本细节仍以 TypeScript 侧文档为准，此处侧重 **Go 引用方式** 与 **验证步骤**。

## 文档索引

| 文档 | 说明 |
|------|------|
| [合约库说明.md](./合约库说明.md) | 模块结构、`lib/contract` / `lib/api` / `lib/util` 能力、并行规约（parity discipline）、构建与引用示例 |
| [quick-start-go.md](./quick-start-go.md) | 开头 **参数表** + 可 `go run` 的最小脚本：`GetTBCBalance` / `FetchUTXO`（对齐 `快速开始.md`） |
| [test-cases/README.md](./test-cases/README.md) | 测试场景总览；各子页含 **可对照 TS 文档的 Go 代码示例**（复制到业务仓库后配置 WIF / 网络即可扩展） |

## 与 `tbc-contract/docs` 的对应（规范原文）

将本仓库与 `tbc-contract` **并列克隆**时，可直接打开下列路径（相对本仓库为 `../tbc-contract/docs/`）：

| TypeScript 规范（`tbc-contract/docs`） | Go 实现入口（本仓库） |
|----------------------------------------|------------------------|
| [快速开始.md](../tbc-contract/docs/快速开始.md)、[Quick Start.md](../tbc-contract/docs/Quick%20Start.md) | [quick-start-go.md](./quick-start-go.md) |
| [ft.md](../tbc-contract/docs/ft.md) | `lib/contract/ft.go`，场景见 [test-cases/ft.md](./test-cases/ft.md) |
| [stableCoin.md](../tbc-contract/docs/stableCoin.md) | `lib/contract/stablecoin.go`、`lib/api/api_stablecoin.go`，场景见 [test-cases/stablecoin.md](./test-cases/stablecoin.md)。管理员路径返回 `*AdminPrepared`，由调用方在外部跑 BIP327 / MuSig2 仪式产出 64 字节 Schnorr 签名后回填；tbc-lib-go 不内置 Schnorr |
| [nft.md](../tbc-contract/docs/nft.md) | `lib/contract/nft.go`，场景见 [test-cases/nft.md](./test-cases/nft.md) |
| [orderBook.md](../tbc-contract/docs/orderBook.md) | `lib/contract/orderbook.go`、`orderbook_token*.go`（含 Token-for-Token 订单），场景见 [test-cases/orderbook.md](./test-cases/orderbook.md) |
| [multiSIg.md](../tbc-contract/docs/multiSIg.md) | `lib/contract/multisig.go`，场景见 [test-cases/multisig.md](./test-cases/multisig.md) |
| [htlc.md](../tbc-contract/docs/htlc.md) | `lib/contract/htlc.go`、`htlc_token.go`，场景见 [test-cases/htlc.md](./test-cases/htlc.md) |
| [poolNFT2.0.md](../tbc-contract/docs/poolNFT2.0.md) | `lib/contract/poolnft2.go`，场景见 [test-cases/poolnft2.md](./test-cases/poolnft2.md) |
| [config.md](../tbc-contract/docs/config.md) | 网络与 API 基址见 `lib/api` 与 [合约库说明.md](./合约库说明.md) |
| [sample.md](../tbc-contract/docs/sample.md) | 通用示例仍以 TS 仓库为准 |

存钱罐（`piggybank.go`）在 `tbc-contract/docs` 中若无独立大文档，以 TS 源码 `lib/contract/piggyBank.ts` 及 [test-cases/piggybank.md](./test-cases/piggybank.md) 为准。

## 验证

```bash
go test ./...
go vet ./...
```

真实广播测试只允许使用明确标记为测试网的临时密钥与
`https://api.tbcdev.org/api/tbc/`；私钥必须通过运行时环境变量注入，
不得写入源码、测试夹具、命令历史或日志。主网广播不属于默认测试流程。
