# tbc-contract-go

TBC 合约与索引 API 的 Go 实现，与 [tbc-contract](https://github.com/sCrypt-Inc/tbc-contract) 的 `lib/contract`、`lib/util` 等对齐。

## 结构

```
tbc-contract-go/
├── docs/           # 说明文档、快速上手与测试场景（对齐 tbc-contract/docs 体系）
├── go.mod
├── lib/
│   ├── api/        # HTTP 客户端（余额、UTXO、广播等）
│   ├── contract/   # 合约与交易构造（FT、NFT、稳定币、订单簿等）
│   └── util/       # 工具函数
└── README.md
```

## 文档

- **索引**：[docs/README.md](./docs/README.md)
- **合约库说明**：[docs/合约库说明.md](./docs/合约库说明.md)
- **Go 快速开始**：[docs/quick-start-go.md](./docs/quick-start-go.md)
- **测试场景提纲**：[docs/test-cases/README.md](./docs/test-cases/README.md)

规范原文与脚本级说明仍以并列仓库 **`tbc-contract/docs/`** 为准（本地克隆时一般为 `../tbc-contract/docs/`）。

## 依赖

- `github.com/LoongYearMeta/tbc-lib-go`：本地开发时通过 `go.mod` 中的 `replace` 指向 sibling **`../tbc-lib-go`**（与团队 fork 对齐）。

## 构建

```bash
go build ./...
```

## 说明

- 本仓库当前**仅包含库源码**（`lib/`），不含包内 `*_test.go`；验证步骤见 **`docs/test-cases/`**，在业务仓库实现自动化测试即可。
