// StableCoin 测试程序占位 — 当前 Go 端 stablecoin 模块带 //go:build legacy_stablecoin
// 标签，默认不参与编译，未与新版 BIP327 / MuSig2 流程接通。
//
// 一旦 lib/contract/stablecoin.go 与 lib/api/api_stablecoin.go 重写完成
// （对照 ../tbc-contract/lib/contract/stableCoin.ts），请用对应的 AdminPrepared
// 两阶段流程在此补回真实测试代码。
package main

import "fmt"

func main() {
	fmt.Println("stablecoin 模块当前已冻结，详见 docs/test-cases/stablecoin.md")
}
