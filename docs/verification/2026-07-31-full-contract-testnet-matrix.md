# Full Contract Testnet Matrix

Date: 2026-07-31
Network: TBC testnet only
Released compatibility target: tbc-contract JavaScript 1.6.5

## Scope

This run covers every economically distinct transaction path in the released
Go surface: native TBC, FT v3, NFT, MultiSig, base and token HTLC, PiggyBank,
StableCoin, Pool NFT 2.0 (standard and lock variants), and the ordinary FT/TBC
OrderBook.

The matrix intentionally excludes Pool NFT v1 and unreleased token-token
matching. No mainnet transaction was constructed or broadcast. Runtime WIFs,
ephemeral MultiSig/OrderBook private keys, HTLC preimages, raw signatures, and
unbroadcast raw transactions are not recorded here or elsewhere in git.

## Compatibility and verification rules

The committed fixture is generated from a sibling JavaScript checkout whose
package version is exactly 1.6.5. Go matches all 13 fixture lengths and SHA-256
hashes:

- FT v3 mint code and transfer tape
- NFT tape
- standard Pool v3, one-key locked Pool v3, and three-key locked Pool v3
- FTLp v2, FTLp v3, and lock-time FTLp v3
- ordinary sell and buy OrderBook scripts
- StableCoin mint code and tape

The Rust SDK snapshot uses the same signed-size fee rule,
max(80, ceil(bytes * 80 / 1000)), as the Go evidence collector. Rust is a
secondary implementation reference; released JavaScript 1.6.5 remains the
protocol compatibility authority.

Every transaction below was:

1. parsed and structurally checked before broadcast;
2. executed through the script interpreter for every contract input;
3. submitted to testnet with one POST and no POST retry;
4. refetched from the public testnet API byte-for-byte;
5. checked against its referenced parent outputs; and
6. independently re-fetched after the lifecycle and checked for serialized
   byte length, paid fee, and signed-size target fee.

All 69 final testnet transactions passed these checks.

## Live transaction evidence

### Native TBC, FT v3, and Token HTLC

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| Native P2PKH self-transfer | cee08256051764f7211eb833fc780c83515431342fde97c989f201a7fd86b402 | 225 | 80 / 80 |
| FT source | e41932ee77a10ef21fa654295296409ac4728527beb649d5630c1aa4328f22c9 | 323 | 80 / 80 |
| FT mint | a5c21ff13b45c0a9ca08ce87632c69a7c3df61663fc68ff9046f1573d199e8ee | 2172 | 174 / 174 |
| FT transfer | ce8b5439175a4956b2b4aa760d62c478b734b2143a56182ff3cac1c9e004b561 | 5179 | 415 / 415 |
| FT transfer with additional info | be1eac615e711c7a0f0967822329d664d3526304b1e308e36d8c9f9c5a4d3163 | 5398 | 432 / 432 |
| FT batch transfer | 532f310d192e57767b02dda2c49501acfe1d530e7a6cac16c0179ca21860572d | 7467 | 598 / 598 |
| FT merge | ddf491797fddb1b41585b049d5353ea19aeabfa3206623c8d81117475ad99eb5 | 5604 | 449 / 449 |
| Token HTLC withdraw deploy | edfaa694ed6834e7f96bb2a2615db2d803e00f2032d30f66180de7afce74e03f | 6409 | 518 / 513 |
| Token HTLC withdraw | a3dfc792e49c7ececfca01ae6ea56c84641345cb476358065ea219d369b5f8d5 | 3880 | 423 / 311 |
| Token HTLC refund deploy | d80783fb7d46421cce968e2f2a7e3d7561b8ba1811ba2769783ca85d3382ce92 | 5564 | 518 / 446 |
| Token HTLC refund | 32e82ad4df7dd2c6ba9ebafe3e9d8c4db99484d07017286845dc3b200d2cc86f | 3969 | 423 / 318 |

### NFT

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| Collection | 6528bbac8141af23322e387a615e1489a71d42de354075b8cc509e368a9f6041 | 507 | 80 / 80 |
| Single mint | 58ec14b0909feb28b875e7d3c94b4f2e4f3407bf31304fdd09651059072bc7c5 | 721 | 80 / 80 |
| Batch mint 1 | 0d019bfcf8a7627b15fb89999b54be6e4866aa300087932eab690e8f30526c5c | 675 | 80 / 80 |
| Batch mint 2 | 8e7f48fc57d3ce59eb87b5f9f475c73a3278319be1369c2efd53dfbef76f2958 | 673 | 80 / 80 |
| Temporary transfer funding | eb9a5eea0d604ba6b63a4cf7f2824869575a1d7d135bcc6b3d9c7f140ef1c5b6 | 225 | 80 / 80 |
| Transfer | a0256ccc947f7745f2f00a7fad066b12b96b503912ce7e702c514fede01eda78 | 1667 | 134 / 134 |
| Transfer with atomic TBC payment | a091b42285f8e6f56381c8707f13ed702a1d07054f73bbb5e0ef2151419aba77 | 1700 | 137 / 136 |

### MultiSig

The public 2-of-3 test wallet address was
FRJMtisnUuocSsG7hLpkqXmwRQA6Ekh3Ei. Two temporary keys existed in memory
only.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| FT source | d35eb318b804e223ed340081a422d98437e5290ed33be8323f18858dbd0cd52a | 328 | 80 / 80 |
| FT mint | a4bf4d857203628dab121544633a29d72e7bb5f9a04651277e13b339204ec721 | 2176 | 175 / 175 |
| Wallet create | bd9e41a99b04ed31f3decfdf6e6ae6ac8e4b54b5306d268fccb36b2fe1a1f2c8 | 662 | 80 / 80 |
| TBC spend | 9915055cd0271d539c06ce6ac848fa41df1d11746ec0ae21a51286f132522bbe | 383 | 1000 / 80 |
| FT deposit | cef1991c0e2581e8b688f1a6a76b8c9b6d9a53519ff5b478202763f0de51e82c | 5200 | 416 / 416 |
| FT spend | e2fe2aa4b9250b089fcec5eee9781a47a5c01c3f0884b272d76e3d98e511922f | 3643 | 4000 / 292 |

### Base HTLC and PiggyBank

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| HTLC withdraw deploy | fcfeab27356a7b0b06b5668ab52e4bcba43ba642e5c85760679547e716281b74 | 338 | 80 / 80 |
| HTLC withdraw | 65a889641c833940877f2d231336629929b06015e65318f63230c8bc7fa2ae17 | 226 | 80 / 80 |
| HTLC refund deploy | 68e8ffc41a6cd7c03fa7f722fcbef226f14f3265cf83a7dd2c2470b95273db43 | 337 | 80 / 80 |
| HTLC refund | 5a1b75c93238a80b1177b584f6ff2969cbb599dccb19baeac1e5f79652301478 | 192 | 80 / 80 |
| PiggyBank freeze | e0bb0d545a1e83db99b449e676bb6c5f3d0c93d72f2ec3fd8d20f3c7fda0f177 | 254 | 80 / 80 |
| PiggyBank unfreeze | 6b1ece1a2ee98f1d3c8efd31e1d31b44dbfeea7b36676aaf22eb93d0b937f824 | 192 | 80 / 80 |

### StableCoin

The JavaScript-compatible token contract id is
0ad1f0ef15153f78d7815a4ee8a804452b6c74fdcd2e4362527d85032dc19b28.
The Coin NFT origin id used by the stablecoin API is
fa50139edc886d2fc9b71b66d90b9900bbe2fb1d9e861fd02201000e66540cec.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| Coin NFT create | fa50139edc886d2fc9b71b66d90b9900bbe2fb1d9e861fd02201000e66540cec | 740 | 80 / 80 |
| Initial mint | 0ad1f0ef15153f78d7815a4ee8a804452b6c74fdcd2e4362527d85032dc19b28 | 3941 | 316 / 316 |
| Administrator mint | 5d86f70219d6de280f8b4db75d4dbd47bda59c354b2959db07c20b2d26641a90 | 4225 | 338 / 338 |
| Owner transfer | e8d72e0f99f8970324b7092d7e1be544b11d95bca6e63bf265eac5f0ea9e335f | 5791 | 464 / 464 |
| Batch transfer | 62edf11c222e79996a67896e3989cc9f154b0ab8c19c2e08321013f7cffdc9aa | 8116 | 650 / 650 |
| Merge | 11e8b9a0bdc18651e5f84484edea6ce93b8d0062e0ad5da3d5228db3af005e41 | 6016 | 482 / 482 |
| Administrator freeze | 604ab49939a9f9058826a182c35b622146d3d10754641c465e6871893a143af3 | 4232 | 339 / 339 |
| Administrator unfreeze | 3dd371651f7534b8e936d041e8077084944db66b906465e8fc14107e74177e36 | 3267 | 262 / 262 |

The indexed post-state was checked through the Coin NFT origin id: total
supply 150,000, owner balance 138,000, metadata present, and a final unfrozen
UTXO.

### Standard Pool NFT 2.0

Underlying FT id:
0ffd42cb87977f90edc852d0792f95a01a45bddd74f2298e0ed0ec4c65a54b9b.
Pool id:
cb985a93ad70e0e8ae8bf8bc0854a75b8a56e8714fbd74fbf88d3b21b771b1bf.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| FT source | 6362280306cf3211d2bf752fd636c8e3a37b7e09aa83b586b09546d7aae41d2a | 329 | 80 / 80 |
| FT mint | 0ffd42cb87977f90edc852d0792f95a01a45bddd74f2298e0ed0ec4c65a54b9b | 2177 | 175 / 175 |
| Pool source | 66d7706f6a0d1a418b1d13371cceabb7d98935011bca6bf0e5addf3dcb0006ea | 244 | 80 / 80 |
| Pool mint | cb985a93ad70e0e8ae8bf8bc0854a75b8a56e8714fbd74fbf88d3b21b771b1bf | 3643 | 292 / 292 |
| Initial liquidity | d8dd10fe6a6ed4607f13fbb40781ceb35ec5fe99add6c8655f5da3e4598f236b | 12729 | 1019 / 1019 |
| Increase LP 1 | 51dcf907185b7b17334cf9c7e9d8255c51a0cee1dc1468244fa82b3c6a7af5cc | 13719 | 1098 / 1098 |
| Increase LP 2 | 04d2af6446e762a3ed7ba3d3ae6328986a90c2296f596733a468188ad3c279ff | 14322 | 1146 / 1146 |
| Swap TBC to FT | 766304113e0055b3ea1f040fdf94f00c1f269197b359dfc9bb8d12f5ef6adb6d | 15305 | 1225 / 1225 |
| Swap FT to TBC | 4367e9e062fff3a5443320a6528dda6287a3413477d42a42ae1e6e5b9bee44d0 | 11965 | 958 / 958 |
| Consume LP | 29f34528cf3d3f86c649a6f79ce88c90041fc73db3674830f4103a73c49713a5 | 23215 | 1858 / 1858 |
| Merge FTLp | fa15d35f8d3325cf0fdb60bc6ddbb2e9361437eb1380fe5c47a8287183ebee3d | 6766 | 542 / 542 |
| Burn FTLp | f9befea0089ad47d21dcd008e1c0910280a6e628c115512d8a4505fe775accb6 | 4523 | 362 / 362 |
| Merge pool-held FT | e7a96034c32e7f2d205cc5b3b74099805bd6432fd09dafe652fd3928deb3b262 | 13919 | 1114 / 1114 |

### Locked Pool NFT 2.0

Locked Pool id:
065114e4462b14f121764041e5a90f5665c85ba78409ef496c895b4fd437a1ae.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| Locked Pool source | d88779702e1aaaa49a8dfab01bd4e63b1cfeacb05e8e9ec7ca3241c214e6aa36 | 243 | 80 / 80 |
| Three-key locked Pool mint | 065114e4462b14f121764041e5a90f5665c85ba78409ef496c895b4fd437a1ae | 3823 | 306 / 306 |
| Lock-time FTLp initialization | 512be13280ed05b5d83117049c855ac5532f7e457883c25b3f48b963a71f764e | 13648 | 1092 / 1092 |
| FTLp unlock | f12d2ceb42c5e4413b47cddd3680ced28a498d3719b2f7f6c715f9d71ee10068 | 3387 | 271 / 271 |
| Consume unlocked FTLp | 3d1e101922d38a67ad19153304a00171977c0c9dd875eb4c4736a27015d5ee17 | 19238 | 1540 / 1540 |

### Ordinary FT/TBC OrderBook

Buyer, seller, and matcher used distinct identities. Temporary seller and
matcher keys existed in memory only.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| FT source | 932636f2a802c2afdfe7b519a44b77300290600e35cbc121db5ca42105fd9683 | 330 | 80 / 80 |
| FT mint | 49bc445be4be13fb9b05d30e0c730747f1bd6948736b26b336b4b6faf2f0b7bc | 2178 | 175 / 175 |
| Participant funding | 4e25b3929554eeb13e91f02cc48746f7f1c1f6aec3fd752153890ab5a2e72030 | 294 | 80 / 80 |
| Sell create | d09fc604c191f62de81aa09721a2e2ae6335831599d56fe2eb5bc5e7c30674e6 | 1148 | 95 / 92 |
| Sell cancel | 4443cbe4902e4e413a8b9548d7bedfebaabe84e3ba262d5d94fd3e4b5ab2053a | 374 | 80 / 80 |
| Buy create | 2f1a1446dc3976a3eb441e8f4b9fbd0ddeff72fdc0dbb56b0d317fb8bdacf718 | 6391 | 586 / 512 |
| Buy cancel | 6e3c356f4e89066d9fcd8ebee27b5889ff07a6f5c846942daad3a6d8e34b48a5 | 3864 | 310 / 310 |
| Full sell create | aa2143b786bd55ad73a7318c6b2b789513511b079737bd7f01168061849d33c3 | 1296 | 109 / 104 |
| Full buy create | 0eba568d50c9b1d775b73866635fd99b4da90ceff95d4f29e1b6bcf418b2c584 | 6616 | 586 / 530 |
| Full match | 0bdf45ba8651a5a99b4eb89a8f708e2479db14ea3ffa9fd225ca813a3f742ed5 | 8059 | 1769 / 645 |
| Partial sell create | 1ef8fc1d11c6ac2f4a2a8289c05775e729e338dca54bf1930116a92b93cbfaa7 | 1149 | 95 / 92 |
| Partial buy create | 20587d2229e496ebef4fdd3b05cd1c5a4225b6b2a002cffa09f73761c28385ef | 4421 | 427 / 354 |
| Partial match with residual order | ec824cd61b669631df094354ef5639aa34988bec964c7b150fa9c4392f935f17 | 9220 | 1769 / 738 |

## Defects found by live validation

1. Token HTLC final signed-size fee funding was too small.
2. NFT temporary transfer funding and final signed-size fees were incorrect.
3. MultiSig FT deposit, StableCoin coin-NFT, HTLC, PiggyBank, Pool creation,
   and OrderBook stage fees needed final signed-size handling or a minimum fee.
4. MultiSig FT combine-hash derivation did not match JavaScript/Rust.
5. An unlocked Pool tag could cross the 3,300-byte lock discriminator.
6. Lock-time FTLp creation divided an already-byte-normalized tape length by
   two, producing a 62-byte tape instead of 81 bytes and failing
   OP_EQUALVERIFY.
7. Locked Pool LP consumption duplicated the same unit error and could panic
   with a negative strings.Repeat count.
8. StableCoin's JavaScript-compatible token id and its Coin NFT/API id were
   treated as if they were one identifier.
9. The first OrderBook harness reused the matcher identity as the buyer,
   violating the contract participant checks. Preflight execution caught the
   failure before POST; the final lifecycle uses three distinct identities.
10. Read-only API GETs parsed Cloudflare 502 text as JSON instead of retrying
    transient status codes and returning an explicit HTTP error.

Every defect has a regression test. Broadcast POSTs remain non-retrying.

## Final static verification

The final working tree passed:

| Check | Command/result |
|---|---|
| JavaScript parity | regenerated from tbc-contract 1.6.5; exact cmp match |
| Full tests | go test ./... -count=1 |
| Repeated lifecycle tests | go test ./test/testnet-parity -count=10 |
| Race detector | go test -race ./... -count=1 |
| Static analysis | go vet ./... |
| Build | go build ./... |
| Patch hygiene | git diff --check |
| Evidence completeness | exactly 69 final transaction rows |
| Secret scan | no WIF-like material in the worktree |
