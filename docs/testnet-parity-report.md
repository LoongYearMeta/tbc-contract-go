# JS 1.6.5 parity testnet report

Date: 2026-07-30  
Network: TBC testnet  
Funding/test address: `miZGyNMbgfWNZdfQFYHKiL4ytJymC3nizd`

The test key was supplied only through `TBC_TESTNET_WIF` at runtime. This
report and the test harness contain no WIF, raw private key, raw transaction,
or HTLC secret.

## Results

| Scenario | Build | Broadcast txid(s) | API verification |
|---|---:|---|---:|
| Token A FT v3 mint | pass | source `49adb0743b4d5f74945a8377b724b4c9df8da478d6240c728e48bc5f8dd48b38`; mint `62d680c6610889b282fc7bf1fe20b679781f937c858d550e4139aca34807c377` | pass |
| Token B FT v3 mint | pass | source `0db4f5b4bad0533ff9b8d4ec7727b7018051b1de8239fdb96866a6012ed91fbd`; mint `79f39043c1911c49cf7369166827cbd53184672a340de940b0fe3a7a2824d128` | pass |
| FT v3 self-transfer | pass | `c200cb3e51be5fb76ade9b218a95971a207491bd060ddc76c5799f8b58828818` | pass |
| Pool NFT v2 create | pass | source `f998f6cd4a44081f3c902eb3a415eb1bc533ace63d8204dc3cee10cf2d26c975`; mint `9a69535c7251e6a5b539c03a61104eefaca051b4a41b016df7cbb0e88f66c4b6` | pass |
| Pool v2 initialize / FTLP mint | pass | `e6507de38c2dec87009df1889503d8cc8326d4742bfebac61541369622a5081b` | pass |
| Pool v2 FTLP consume | pass | `e44c52ee2471fca7228a6f302a111f3f3b0e260e8a1eafd915ac819ac5bbd8b4` | pass |
| Token HTLC withdraw | pass | deploy `61fb6ca872572d51c2b3f85c302a6bd8bd5571531ae7098da9cb8447f09ffb6d`; withdraw `62b6d3c0de1821139a35a77c57899ce5204f33cbc2af971ce62f74cf0afb7298` | pass |
| Token HTLC refund | pass | deploy `46f389d1a22dcb1914d28fa87cd82a6d97462677c59f2a3a0367515058b35efd`; refund `7331a30f922e5b8d60ee00efb709fd19f3ed12c2f8e1adca3ba0a59bb5abf6a3` | pass |

All 13 accepted txids above returned HTTP 200 from the testnet
`txraw/txid` endpoint during the final read-only verification.

## Current-node compatibility

The current testnet node rejects the JavaScript 1.6.5 FT mint source value of
9,900 satoshis as dust. The Go builder uses 10,000 satoshis for this source
output; a regression test covers the current testnet policy.

No ephemeral participant keys were created. All scenarios used the single
approved testnet address, so there were no external participant balances to
return.
