# Pool 参数覆盖与旧版锁定 LP 拒绝调查

参考固定为 npm `tbc-contract@1.7.2`、`tbc-lib-js@1.0.31`。所有真实广播均为测试网。用户指定精度后，最终验收使用 **decimal=6**；此前 decimal=0/2 的数据保留为历史记录，不代替精度 6 的验收。

旧 PoolNFT v2 的最终验收范围为 plan 1～6，plan 6 仅使用当前 330 bp / LP 200 bp。用户确认旧 130 bp 配置尚未使用，不再保留兼容支持或追加测试；下方此前复现记录仅保留为调查证据。Go、Rust 禁止旧池稳定币创池，测试代币使用精度 6 的普通 FT。

## 最新复测：旧 FT v4 时间锁 LP 的 A3 已不再复现（2026-09-30）

用户告知索引可能已经更新后，重新构建当前 Rust / Go 测试程序，使用同一授权测试账户进行新建池与真实广播。**JS、Rust、Go 共 12 组完整流程通过，168 笔交易接受并逐笔原样回查，0 笔拒绝**。矿工费 148,819 sat（0.148819 TBC）。

覆盖普通 FT v4（精度 6）、plan 1 / 当前 plan 6、无公钥限制 / 3 个公钥限制、已到期的高度锁 1 / 时间戳锁 500000001。带公钥限制的 LP 成本为 1000 sat。每组都真实完成创建、时间锁 LP 初始化、两次加池、双向兑换、池内 FT 合并、解锁后减池及剩余 LP 销毁。LP 合并入口在已被直接减池预先合并后为空操作，未将其计作额外广播。

每笔广播前检查输入脚本，Rust / Go 的池操作同时对比 JS 合约输出。没有为本次复测修改 SDK 合约实现或索引代码；仅增加矩阵参数筛选和独立报告路径。线上索引服务的具体构建版本未核实，结论依据真实广播接受与 raw 回查，不声称区块确认。

[最新逐笔证据](verification/2026-09-30-082958-old-pool-locked-recheck.json)。下方 2026-09-29 的 A3 记录保留为历史证据，不能再作为当前服务仍失败的结论；本轮是针对性复测，没有把旧矩阵全部 63 组失败改写为通过。

## FT v1～v4 的旧池真实广播（按用户要求停止追加测试）

用户已要求停止剩余测试，未完成的报告标记为 `stopped-by-user`，不得视为完整通过。FT v1、v3 的各 144 组主矩阵已通过；其余最终进度以 JSON 为准。

此前 1,305 笔矩阵只使用 FT v4，时间锁拒绝结论不能推广到 v1～v3。当前新增 FT v1、v2、v3 的公开测试网矩阵，精度均为 6；以各版本 JSON 中的实际交易、状态和拒绝记录为准。

FT 引导铸造使用已发布 npm 1.4.0 / 1.5.0 / 1.6.5 的 `getFTmintCode`，分别核实脚本分类为 v1 / v2 / v3；资金与签名由最新 JS 公共 MintFT 构造器处理。随后各端独立创建和操作池子，Go/Rust 的池操作为原生构造并实际广播。这里不声称 Go/Rust 的 FT 铸造入口支持选择任意历史版本。

- [FT v1 广播](verification/2026-09-29-old-pool-matrix-d6-ftv1.json)
- [FT v2 广播](verification/2026-09-29-old-pool-matrix-d6-ftv2.json)
- [FT v3 广播](verification/2026-09-29-old-pool-matrix-d6-ftv3.json)
- [历史铸造模板来源](verification/2026-09-29-old-ft-mint-sources.json)

离线辅助检查另存于[版本模板识别报告](verification/2026-09-29-lp-version-template-inspection.json)，不计为真实广播成功。最新 JS 普通 FT 的哈希位置如下：

| 底层 FT | LP 长度 | 普通 LP 实际/索引偏移 | 时间锁 LP 实际/索引偏移 |
| --- | --- | --- | --- |
| v1 | 1564 | 234 / 234 | 258 / 258 |
| v2 | 1884 | 234 / 234 | 258 / 258 |
| v3 | 1884 | 256 / 256 | 280 / 280 |
| v4 | 2076 | 293 / 293 | 314 / **258（不匹配）** |

其中 v2、v3 长度相同而内部布局不同，不能仅凭长度区分。稳定币模板只作历史识别审计，不进行稳定币创池，Go/Rust 禁止旧池稳定币创池的策略保持不变。

## 多版本补测发现并修复的 SDK 问题

- Rust 读取 Pool 来源时原来逐字节寻找 `36 字节 push + OP_EQUALVERIFY`，可能误把交易 ID 内的数据当成指令。FT v2 的真实池子触发了误判；现按真实脚本指令边界解析，并将该公开脚本保存为离线回归用例。修复后同一个池子已成功初始化并完成后续操作。
- Go 直接减少时间锁 LP 时，先生成解锁交易，但减少 LP 的交易仍读取旧的 LP 输出，并从接口查询尚未广播的解锁交易。现引用本地解锁交易的新 LP 输出，使用本地祖先数据签名；两笔按顺序广播，与 JS 合约输出比对。
- JS 的 `consumeLP` 会内部自动广播解锁前置交易。矩阵脚本现截获此调用，保留完整的“解锁→减少 LP”两笔交易，比较后统一广播，确保手续费输入跟踪正确。修正前两次部分流程及双花冲突保存在 `old-pool-ftv1/ftv3-harness-recovery.json`，不作为合约限制，也不计入最终完整矩阵总数。

2026-09-29 调查时索引代码未作修改，FT v4 时间锁初始化的 A3 拒绝单独保留。最新服务已通过上方复测，历史 SDK 修复与索引恢复分别记录。

## FT v4 的旧 PoolNFT v2 矩阵（已完成，含拒绝）

[逐笔广播与比对记录](verification/2026-09-29-old-pool-matrix-d6.json)使用精度 6 的普通 FT v4。基础组合为 plan 1～6 × 公共池/3 个公钥限制 × 普通 LP/时间锁 LP × JS/Rust/Go，共 72 组。基础 36 组普通 LP 流程全部通过，共 468 笔接受并按原始交易回读的广播；另外 36 组带时间锁 LP 均在初始化被 A3 拒绝。

每个普通 LP 流程实际执行 FT 源/铸造、Pool 源/铸造、初始化、两次增加 LP、双向 swap、池内 FT 合并、LP 合并、部分减少 LP、剩余 LP 销毁。所有广播前执行脚本解释器验证；Go/Rust 单笔操作与最新 JS 对比合约输出。两笔创建交易允许各 SDK 手续费及来源交易 ID 不同。

plan 6 的其余公钥数量（1、2、4～10）另有 54 组：27 组普通 LP 完整流程通过，27 组时间锁 LP 在初始化被 A3 拒绝。因此公钥数量 1～10 均已覆盖。全部 6 个方案的小数金额（FT `5.123456`、TBC `0.001001` 等）另有 18 组流程，全部通过。总计 **144 组：81 组完整流程通过、63 组时间锁初始化被拒绝**；接受并回读 **1,305 笔**（JS/Rust/Go 各 435 笔），手续费共 **909,266 sat**。

[144 个池子的只读模板审计](verification/2026-09-29-old-pool-template-audit-d6.json)按真实来源交易 ID 重建最新 JS Pool Code/Tape，全部一致，并核实代币精度 6。Rust 全目标测试、Go 全包测试通过。

时间锁 LP 初始化复现 `index-gate/A3: lp: neither mint nor transfer`。[当前方案的 63 笔拒绝诊断](verification/2026-09-29-old-pool-lock-diagnosis-d6.json)逐笔确认：2076 字节 LP 的 Pool 哈希在偏移 314，本地索引代码在实际命中的分支读取偏移 258，导致铸造识别失败；线上部署版本尚未核实。被拒绝的初始化及其后续依赖流程不计入成功；初始化失败后不能真实广播该池的加减 LP / swap。记录保留拒绝交易、错误及 JS 对照。

Go 在本轮发现并修复旧池 Tape 对三位十六进制费率（335、535、330 bp）的编码问题，按 JS 补齐前导零。Rust 示例改为按脚本识别 Pool FT 输出，避免费率改变后可选服务费输出移动其索引。

## 早期旧 Pool v2 调查记录：JS 同样被拒绝

失败组合是旧代 **PoolNFT v2 + plan 6 历史 130 bp + 3 个公钥白名单 + 带时间锁 LP**。建池代币是本次测试新铸造的普通 FT v4，名称 `RustPoolLiveFlow` / `RPLF`，最终复现精度 **6**，总量 1,000,000 枚；不是新版 Standard，也不是稳定币。

- FT ID：`8fc1d69763a4a4a760aa0a47d5d260134ce8b2dbd857a10df3b600b55ffeaafb`
- Pool ID：`ba2f639ae2e8c36f767dcafe0758a0fd22f0b80baaa078b32727767bae21c7be`
- JS `initPoolNFT` 构造交易：`f5c0a3ddb66dba875cb6c43fb8220e279dc7f753c41e0ea6a9eb297dc3fd818d`

JS 使用相同 Pool 和 FT 输入、相同合约输出，替换了已被费用分流交易消费的原费用输入；3 个输入的脚本预检均通过。真实广播返回 `index-gate/A3: lp: neither mint nor transfer`，与 Rust 一致。失败交易没有被计入成功数。相同 FT 建立的普通旧 Pool v2 已完成初始化、加减流动性和双向兑换，不能把此故障推广到所有旧池。

[完整复现证据](verification/2026-09-29-legacy-locked-js-reproduction-d6.json)包含网关原始响应、输入/输出比对和脚本预检结果。复现器使用原始父交易强制选取相同 FT UTXO；`ftBalance` 从父交易 Tape 的六个整数槽求和，未改动 npm 包代码。

本地 `tbc-index/internal/contract/detect.go` 的 `extractLPPoolHash` 对锁定 LP 使用固定偏移 258 或 280，而该 2076 字节 FT v4 锁定 LP 的正确 Pool 哈希位于偏移 **314**。两种旧偏移读到的内容都不匹配输入 Pool 的脚本哈希，导致 `tryLPMint` 无法识别铸造；初始化又没有旧 LP 输入，因此转账路径也失败。这是与线上响应相符的具体索引识别缺陷，线上服务的构建版本尚未核实。修复点在索引服务的脚本变体识别及其回归测试；修改 SDK 输出去伪装其他 LP 模板会破坏合约约束。

[精度 6 的旧 Pool 回归](verification/2026-09-29-legacy-pool-d6.json)有 16 笔接受/回读交易，涵盖普通池完整流程和锁定池的源/铸造交易。此前精度 2 的[同输入对照](verification/2026-09-29-legacy-locked-js-reproduction.json)也得到相同拒绝，故不是 token 精度造成。

JS 已弃用的 `initPoolNFTWithLockTime` 在同一输入上生成 1564 字节 LP，脚本预检即失败，未广播。推荐通用方法 `initPoolNFT(..., lock_time)` 才是上述有效对照。

## 新版 TBCAMM 参数矩阵

上一轮 72 笔 AMM 交易只测试 plan 6，公开/普通 LP 与控制者/锁定 LP 两种组合，锁值为 0。它不代表完整参数覆盖。

最终矩阵使用 **TBC20Standard，精度 6，总量 1,000 枚，原始总量 1,000,000,000**。首次投入 500 枚（500,000,000 原始单位），FT 预算追加 100 枚，反向兑换投入 10 枚。LP 原始单位和 TBC satoshis 独立处理。

本轮的完整离散配置为：

| 维度 | 取值 |
| --- | --- |
| 费率方案 | plan 1、2、3、4、5、6 |
| 权限 | 公开，或白名单 1、2、3、4、5 个控制者 |
| LP 模板 | 普通、带时间锁 |
| 创建端 | JS、Rust、Go 分别创建 |

费率方案 1～6 的总费率分别为 **35、35、135、335、535、330 bp**；这里的新 AMM plan 6 不使用旧池的历史 130 bp。

共 **72 种配置 × 3 个创建端 = 216 条生命周期**。每条依次执行 Standard 源/铸造、AMM 源/铸造、首次/追加流动性、双向兑换、LP 转移/解锁、部分赎回、LP 合并、全部赎回；最后检查 Pool 保留 1500 sat。Rust/Go 的操作与同输入 JS 构造结果比对，并验证全部输入脚本。

锁定 LP 使用已到期的非零区块高度 `1` 或时间戳 `500000001`；其转移显式解锁为 0。追加流动性覆盖 TBC 预算和 FT 预算。每个有效交易都先预检，再广播，再回读核对原始字节。这是接收/回读证据，不等于区块确认。

| 创建端 | 精度 6 基础矩阵 | 精度 6 扩展矩阵 |
| --- | --- | --- |
| JS | [72 组](verification/2026-09-29-amm-parameter-matrix-testnet-d6-js.json) | [6 组](verification/2026-09-29-amm-parameter-matrix-extended-testnet-d6-js.json) |
| Rust | [72 组](verification/2026-09-29-amm-parameter-matrix-testnet-d6-rust.json) | [6 组](verification/2026-09-29-amm-parameter-matrix-extended-testnet-d6-rust.json) |
| Go | [72 组](verification/2026-09-29-amm-parameter-matrix-testnet-d6-go.json) | [6 组](verification/2026-09-29-amm-parameter-matrix-extended-testnet-d6-go.json) |

三组广播使用[预先拆分的独立费用输出](verification/2026-09-29-amm-d6-funding.json)。六份精度 6 报告均为 `passed`：基础 2,592 笔、扩展 432 笔，共 3,024 笔接受并回读。带时间锁的部分每端 42 条流程，三端共 126 条 / 1,728 笔。

额外扩展生命周期逐个测试 5 个白名单成员授权、两种预算方式，并对 **LP 输入数 1、2、3、4、5** 分别做真实转移/合并，包含带找零拆分、部分及全部赎回。六种费率各由三端创建，共 18 条扩展生命周期。

精度 6 广播前的[离线冒烟](verification/2026-09-29-amm-parameter-matrix-offline-d6-smoke.json)覆盖普通池和 5 控制者锁定池，三端共 72 笔构造/脚本校验。此前精度 0 的完整离线/广播基础矩阵也保留；其扩展广播在用户要求精度 6 后停止，只有 12 条完整生命周期及第 13 条的部分已接受交易，不计为精度 6 验收。

[三端只读查询证据](verification/2026-09-29-amm-queries-d6.json)进一步核对每个基础矩阵池子的最终 txid、祖先、余额、费率和保留值；三个读取端均用索引结果对照原始交易，并核对创世交易声明的精度确实为 6。

## 边界和负例

- 1,641 组共享 JS 数学向量：六种费率、0/1/9/10 等小金额、手续费支付/保留取整、大于 JS 安全整数的金额、63 位边界及越界、滑点恰好满足/不足、超额赎回；Rust/Go 都检查成功结果和拒绝分支。
- [权限及异常输入检查](verification/2026-09-29-amm-negative-d6.json)：非法 plan、重复/过多/格式错误白名单、逐个成员签名、非成员拒绝、无效首次流动性、超额余额、零额转移、重复/过多 LP 输入、缺祖先、普通 LP 指定锁、混合高度与时间戳锁。
- 精度 6 的权限/异常检查共 138 项，另含锁值 499999999/500000000 的类型边界、2147483647/2147483648 的符号边界和 uint32 最大值 4294967295。
- 未来锁 `2000000000` 的离线检查确认交易锁值和非最终 sequence 被保留，LP 找零继续锁定；不会把脚本预检通过误报成未到期交易可以上链。此类负例不作真实广播。

“完整”限定为上述有限配置集合；金额、地址、任意锁值和每一层 API 可选项不是可穷举集合。JS 的自定义 feePolicy/changeAddress、expectedSnapshotHash、附加 Add/Remove 限额及 prepared-signing 接口尚不都是原生 SDK 的同形入口，本报告不声称这些接口已全部对齐或已真实广播。新版 AMM 的底层资产为 TBC20Standard；旧 Pool v2 仅使用普通 FT 创池，Go/Rust 稳定币创池明确禁止。

## 复现

在 Rust 仓库根目录安装固定 JS 依赖并构建两端 worker：

```sh
npm ci --prefix scripts/reference-172
cargo build --example interop_172
(cd ../tbc-contract-go && go build -o /tmp/tbc-go-interop-172 ./test/interop-172)
node scripts/amm-parameter-matrix-172.js --decimal=6 --smoke
node scripts/amm-negative-172.js --decimal=6
```

真实广播使用 `node scripts/run-amm-d6-172.js --broadcast`，从运行时环境读取 `TBC_TESTNET_WIF`。它先拆出三笔 12,000,000 sat 的独立费用输出，再并行运行三个创建端；同一分支的基础与扩展广播串行执行。保存旧报告后再启动新一轮。单独矩阵命令可用 `--decimal=6 --language=js|rust|go --broadcast`，并通过 `TBC_AMM_FUNDING_OUTPOINT=txid:vout` 指定独立分支；`--resume` 用已接受 txid 恢复。不要让多个广播器自行选择同一地址的费用 UTXO。离线使用固定无资金测试键，不读取广播私钥。

旧池测试命令（按顺序串行执行，后两项恢复同一报告）：

```sh
cargo build --example old_pool_172
(cd ../tbc-contract-go && go build -o /tmp/tbc-go-old-pool-172 ./test/old-pool-172)
node scripts/old-pool-matrix-172.js --broadcast
node scripts/old-pool-matrix-172.js --broadcast --resume --extended
node scripts/old-pool-matrix-172.js --broadcast --resume --fractional
node scripts/audit-old-pool-172.js
```

多版本补测使用 `scripts/run-old-pool-versions-172.js`（FT v1/v2/v3 分别独立费用分支），以及 `scripts/run-old-pool-costs-172.js`（LP 费用 1,000 sat）。历史包需先以 `npm pack tbc-contract@<版本> --ignore-scripts` 分别下载 1.4.0、1.5.0、1.6.5，解包到 `/tmp/tbc-history-packs/<版本>/package`，并将各包的 `node_modules` 链接到本仓库 `scripts/reference-172/node_modules`；具体文件摘要见铸造来源 JSON。驱动器支持 `--versions=1,3` 单独恢复指定分支，恢复必须带 `--resume`，不要对同一费用分支同时运行两份广播程序。

只读最终审计使用 `audit-old-pool-172.js --ft-version=N`，费用矩阵另加 `--costs`；`summarize-old-pool-172.js` 检查全部八份报告及模板审计，拒绝把未完成项、双花等意外拒绝计为通过。

广播私钥仅从 `TBC_TESTNET_WIF` 运行时读取，不写入代码或证据。旧池报告中的金额字段和操作标签应结合 `cases/steps` 阅读；`blocked` 表示初始化未上链，其后续依赖操作也未执行。

## 更新索引主分支后的核对

本地 `tbc-index` 已从落后 64 个提交的 `main` 快进到 `e7ef05c`（tag `0.9.2`），原 `explorer` 分支保留，工作区干净；未部署或重启服务。[最新主分支重放结果](verification/2026-09-29-index-main-lp-replay.json)使用实际 `extractLPPoolHash`、`tryLPMint` 和 `DecodeAndVerify`：63 个旧时间锁 LP 样本均继续复现 258/314 偏移错误及 mint/transfer 识别失败；一个已广播的新版时间锁 LP 对照样本正确提取哈希并识别铸造。

新版在 `detect.go:490` 的 `LPTBC20CODE2` 分支中，根据脚本标记提取 32 字节 push；旧版时间锁路径仍使用固定偏移。因此新版成功并不表示旧版路径已修复。离线合约测试通过；5 项依赖主网历史交易的回放测试因本地节点无对应交易而失败，未计为通过。
