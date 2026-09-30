# tbc-contract-go

TBC 合约与索引 API 的 Go 实现，与 [tbc-contract](https://github.com/sCrypt-Inc/tbc-contract) 的 `lib/contract`、`lib/util` 等对齐。

当前本地版本候选为 **0.2.0**，新增合约以 npm **tbc-contract@1.7.2** 为基准；尚未发布 Git 标签。保留旧合约入口。详见 [兼容范围与真实测试网验证](docs/js-1.7.2-compatibility.md)。

Pool 参数矩阵与旧锁定 LP 拒绝调查见 [专项验证报告](docs/pool-parameter-coverage-172.md)。

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

- `github.com/LoongYearMeta/tbc-lib-go`：具体版本以 `go.mod` / `go.sum` 为准。

## 构建

```bash
go build ./...
```

## 说明

- 使用 `go test ./...` 运行库和测试工具的回归测试；真实广播证据见版本兼容说明。
