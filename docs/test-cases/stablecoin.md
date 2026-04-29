# StableCoin 测试场景（Go）

> ⚠️ **当前 Go 端冻结**：`lib/contract/stablecoin.go` 与 `lib/api/api_stablecoin.go` 都带 **`//go:build legacy_stablecoin`** 构建标签，默认 `go build ./...` 不参与编译；其内部仍引用旧版 SDK 的符号（如 `bt.FtUTXO` / `adjustFTTransferChangeFee`），即便加上构建标签也不会通过编译。文件保留只是作为日后重写的参考材料。
>
> **TS v1.6 起的 BIP327 / MuSig2 n-of-n 签名仪式** 见 [`../tbc-contract/docs/stableCoin.md`](../../tbc-contract/docs/stableCoin.md)。Go 端尚未实现 MuSig2 工具与 `prepared.sighashes / prepared.finalize(sigs)` 流程，因此这一文档以 TS 版为权威源。

## 重写时的对齐目标

未来重写应直接对照 TS [`../tbc-contract/lib/contract/stableCoin.ts`](../../tbc-contract/lib/contract/stableCoin.ts) 与 [`../tbc-contract/docs/stableCoin.md`](../../tbc-contract/docs/stableCoin.md)，按本仓 FT/NFT 等模块从零落 Go：

1. **管理员鉴权**：`createCoin` / `mintCoin` / `freezeCoinUTXO` / `unfreezeCoinUTXO` 改为 BIP327 MuSig2 n-of-n Schnorr。第一个参数为 32 字节聚合公钥 `aggPubkey32`，第二个参数为可付费的 ECDSA `feePrivateKey`。
2. **两阶段返回**：上述四个方法不再直接返回 raw tx，而是 `AdminPrepared{ Tx, Sighashes, Finalize(schnorrSigs64) []string }`。`Finalize` 再生成最终 raw。
3. **不变接口**：`transfer` / `batchTransfer` / `mergeCoin` 接口与 TS 一致，金额参数走 `*big.Int`，与 FT 同口径。
4. **依赖**：BIP327 / Schnorr 工具应来自 `tbc-lib-go/crypto`（如已合并）；若尚未合并，应先在 `tbc-lib-go` 侧补齐 `MuSig2.PubkeyFromSk` / `KeyAgg` / `NonceGen` / `PartialSign` 等原语，再在本仓的 stablecoin 模块里调用。

## 重启编译的步骤（仅供参考，未来重写时使用）

1. 删除 `lib/contract/stablecoin.go` 与 `lib/api/api_stablecoin.go` 顶部的 `//go:build legacy_stablecoin` 行。
2. 改用新版接口实现 `createCoin / mintCoin / transfer / batchTransfer / freezeCoinUTXO / unfreezeCoinUTXO / mergeCoin`。
3. `go mod tidy` 后再次 `go build ./...`；这里也要核对 `tbc-lib-go` 里 MuSig2 / Schnorr 的导出。
4. 在本目录补一份与 TS 一致的可运行 Go 示例（`AdminPrepared` 三段：构造 → MuSig2 仪式 → finalize → 广播）。

> 在重写完成之前，请勿调用 `lib/contract/stablecoin.go` 或 `lib/api/api_stablecoin.go` 中的任何符号。
