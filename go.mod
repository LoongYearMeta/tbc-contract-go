module github.com/LoongYearMeta/tbc-contract-go

go 1.17

require github.com/LoongYearMeta/tbc-lib-go v0.0.0-00010101000000-000000000000

require (
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/crypto v0.0.0-20210711020723-a769d52b0f97 // indirect
)

replace github.com/LoongYearMeta/tbc-lib-go => ../tbc-lib-go
