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

All 79 final testnet transactions passed these checks. Transactions from
diagnostic attempts superseded by a later complete lifecycle are intentionally
excluded from this final set.

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

The public source 2-of-3 test wallet address was
FLypxmWSDts3TTmMhUZHvUKUF9LTZpjzV6. The destination wallet was a separate
2-of-3 wallet. Ephemeral keys existed in memory only.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| FT source | a68d53e1142e984faf208b176cef25e92d6c5e9d42080512576e79dcd4f849b5 | 327 | 80 / 80 |
| FT mint | c64dc3aaf4decc551de0dd36d8bf9602b73f18f7a3e2dacbb3a63512cfefa32e | 2174 | 174 / 174 |
| Wallet create | ac9dfdb11f4cf8f4054cf6696559b821b1a85b688b708461d908eb5e26322b3d | 663 | 80 / 80 |
| P2PKH to MultiSig TBC | 4069048837098a7be1908f6b164db0b8b117994f0ac468c34341915ae1941467 | 241 | 80 / 80 |
| MultiSig to P2PKH TBC | c81fc105d447f418141eb7888dff3a083e081a9c5879891be84db20322c8c992 | 382 | 1000 / 80 |
| MultiSig to MultiSig TBC | f1a7cd9c1b60fd634dd5e179bfda73c555b9cef02d40e9f4bb14d04edb3566b0 | 398 | 1000 / 80 |
| P2PKH to MultiSig FT | 60b1e9e9dbcc72be7ea3aa19879df473bc2176d5e2eacf33683ace1dd2ff1d6d | 5199 | 416 / 416 |
| MultiSig to P2PKH FT | 31db3e360d3b5c7c1184140ecea6e9b88a4d79abe53c6171f4773139ec88aacb | 5792 | 2000 / 464 |
| MultiSig to MultiSig FT | d4358b3ed86f5d353d1cb592cf2a9d22254fa25097369bac43ea3cc4705dcc7e | 5993 | 2000 / 480 |

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
7c1a437174dba495620e56e870ea467598b21acfcdeab5ab853ec25c3dc56522.
It uses the fresh MultiSig-stage FT id
c64dc3aaf4decc551de0dd36d8bf9602b73f18f7a3e2dacbb3a63512cfefa32e.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| Locked Pool source | f2fec9f462eea0470a4e41fd92ecd37050e620002cdcda164a1e9ad1f9eecdd3 | 244 | 80 / 80 |
| Three-key locked Pool mint | 7c1a437174dba495620e56e870ea467598b21acfcdeab5ab853ec25c3dc56522 | 3823 | 306 / 306 |
| Lock-time FTLp initialization | af95386fb13918d11c09506f3dd34afe5d7a78aefb6436549311659857192a2b | 13759 | 1101 / 1101 |
| Locked Increase LP | 7a1edb59e12c6d357d249c60a40256ded7db6e02bf11a45d53fe47f281f0738e | 14274 | 1142 / 1142 |
| Locked swap TBC to FT | 19a649a14aa8a0df3b48cc018fee77b5cd11a3c2cc4d65870bbf7624f5f58e0a | 16040 | 1284 / 1284 |
| Locked swap FT to TBC | 07172e92c5a8e79d2e80663360cefa6da37137de35ca015ee031533d1e275536 | 12315 | 986 / 986 |
| Merge locked FTLp and clear expired lock | 2d4202f8b1037a328b6e624e4a6fb39f997b7b0c4caaf7805950835666017bb1 | 4873 | 390 / 390 |
| Locked Increase LP after merge | 4358aef630a86c75a9295ced383c8b46e776091ecaaaa89b9efc5d5b76adb9fc | 14475 | 1158 / 1158 |
| Unlock mixed FTLp set | 2ae78e63304d085aa1b273b16dd7015c32706e6e63a823dc99272338c5fee4f7 | 5366 | 430 / 430 |
| Consume unlocked FTLp | 0c47724c068fe2e8b3d7ac02d279def52044e25ea6cd3842277033f409f14de4 | 23982 | 1919 / 1919 |
| Burn remaining FTLp | b33fff99f89f721db2b52d1025aa2dd64ebe41475f1aa51cc795d492b262132c | 3596 | 288 / 288 |
| Merge locked-pool-held FT | 1156af6dbac49da0cec4a14ac539de9fd7ee57e7aeb7895040fe7f780a589c55 | 14433 | 1155 / 1155 |

### Ordinary FT/TBC OrderBook

Buyer, seller, and matcher used distinct identities. Temporary seller and
matcher keys existed in memory only.

| Stage | Testnet txid | Bytes | Paid/target fee (sat) |
|---|---|---:|---:|
| FT source | 4d219c3a8ac3fcb0b7cf26ea70c768b57cb99a7fffa137f019c444264de54de7 | 330 | 80 / 80 |
| FT mint | f784b303abfa32aa40efeb8e308fd48719591c6d3002da42f8efecaf69eed85b | 2177 | 175 / 175 |
| Participant funding | 4f7a4f071df54eacd4b783b1e064c3078ee7f1c7cd233e850ccbc0e9502b2ef6 | 294 | 80 / 80 |
| Sell create | 141c5e9589fa07c45e2e18fc54da8cbe5189eb2edaf366135429e6222006720f | 1148 | 95 / 92 |
| Sell cancel | 3c0cf593278040b27c34a14893321f754704cbdb01f2cb248df47c729ef9fd60 | 375 | 80 / 80 |
| Buy create | d8c1bb3282189fe036891a5a872203f8abe3e64828f1b176da0ecd630ad8220a | 6391 | 586 / 512 |
| Buy cancel | 793ec44a36c0291e1a1aa1cdf410f262ac6cca3a5d368ab6fa7b712c41dc000c | 3863 | 310 / 310 |
| Full sell create | d9738fd2be0afc28d767c7b4d1170759a24bdabeeb2a5aad512f77a4404d4f76 | 1296 | 109 / 104 |
| Full buy create | 7d84732c27dbd0e40b38b07f1a890cf3b827fa921714fef64d81729bce025e90 | 6615 | 586 / 530 |
| Full match | 3e3ac801924fac75b1645d59350a808c04243f68a1c19c0835ac5852a1770844 | 8061 | 1769 / 645 |
| Partial sell create | 6c1d858a599ac633cd5cfeef3931d913e1efd10f11829d561e65af7c0c65f398 | 1148 | 95 / 92 |
| Partial buy create | ab736c50a339af93aadb1a4020311947eb6df78382097ad7e8353e5080f8abd8 | 4422 | 427 / 354 |
| Partial match with residual order | 1ec93517d8c310dff97d54a9bc6abc429f4d989650c8b02338650793060e037a | 9220 | 1769 / 738 |

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
11. Pool operations and OrderBook cancellation assumed one fee adjustment and
    re-sign was sufficient, although DER signature length can change with the
    updated sighash. All such call sites now use bounded iterative convergence.
12. The initial MultiSig matrix omitted the released P2PKH-to-MultiSig TBC,
    MultiSig-to-MultiSig TBC, and MultiSig-to-MultiSig FT branches.
13. The initial locked-Pool matrix omitted locked IncreaseLP, both swap
    directions, locked merge/burn, and locked pool-held FT merge behavior.
14. MultiSig address construction accepted a declared public-key count that
    did not match the supplied compressed public keys.

Library defects have regression tests, and the coverage gaps are enforced by
the complete lifecycle tests and public evidence rows. Broadcast POSTs remain
non-retrying.

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
| Evidence completeness | exactly 79 final transaction rows |
| Secret scan | no WIF-like material in the worktree |
