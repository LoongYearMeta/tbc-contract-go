"use strict";

const crypto = require("crypto");
const fs = require("fs");
const path = require("path");

const repoRoot = path.resolve(__dirname, "..");
const candidates = [
  process.env.TBC_CONTRACT_JS_DIR,
  path.resolve(repoRoot, "../tbc-contract"),
  path.resolve(repoRoot, "../../../tbc-contract"),
].filter(Boolean);
const jsRoot = candidates.find((candidate) =>
  fs.existsSync(path.join(candidate, "package.json")),
);
if (!jsRoot) {
  throw new Error(
    "tbc-contract 1.6.6 not found; set TBC_CONTRACT_JS_DIR to the extracted npm package",
  );
}

const pkg = require(path.join(jsRoot, "package.json"));
if (pkg.version !== "1.6.6") {
  throw new Error(`expected tbc-contract 1.6.6, found ${pkg.version}`);
}
const sdk = require(jsRoot);
const tbc = require(path.join(jsRoot, "node_modules", "tbc-lib-js"));

const address = "1BitcoinEaterAddressDontSendf59kuE";
const txid = "11".repeat(32);
const codeHash = "22".repeat(32);
const adminPubHash = "33".repeat(20);
const compressedPublicKey = `02${"44".repeat(32)}`;
const tapeSize = 80;

function fixture(script, decoded) {
  const bytes = script.toBuffer();
  return {
    length: bytes.length,
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
    decoded,
  };
}

function asJSON(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

async function main() {
  const ft = new sdk.FT({ name: "Parity", symbol: "PTY", amount: 1, decimal: 6 });
  const pool = new sdk.poolNFT2({ network: "testnet" });
  const order = new sdk.orderBook();
  order.hold_address = address;
  order.sale_volume = 123456n;
  order.fee_rate = 10000n;
  order.unit_price = 2000000n;
  order.ft_a_contract_partialhash = codeHash;
  order.ft_a_contract_id = txid;

  const ftV4Mint = ft.getFTmintCode(txid, 0, address, tapeSize);
  const stableCoinV4Mint = sdk.stableCoin.getCoinMintCode(
    adminPubHash,
    address,
    codeHash,
    tapeSize,
  );
  const nftV2Code = sdk.NFT.buildCodeScript(txid, 0);
  const nftV1Code = sdk.NFT.buildCodeScript_v1(txid, 0);
  const poolPlan6V4 = pool.getPoolNftCode(txid, 0, 6, 4, "parity", false);
  const poolPlan6V4Locked = pool.getPoolNftCodeWithLock(
    txid,
    0,
    6,
    address,
    0.001,
    [compressedPublicKey],
    4,
    "parity",
    false,
  );
  const ftlpV4 = pool.getFtlpCode(codeHash, address, tapeSize, false, 4);
  const ftlpV4LockTime = pool.getFtlpCodeWithLockTime(
    codeHash,
    address,
    tapeSize,
    false,
    4,
  );
  // getFTCodeSizeHex(2076) is the little-endian uint16 value 0x081c.
  const sellOrderV4 = order.getSellOrderCode(false, address, "1c08");
  const buyOrderV4 = order.getBuyOrderCode(false, address, "1c08");

  const tbc20Tape = sdk.TBC20.buildTape(
    [123456n, 7n, 0n, 0n, 0n, 0n],
    sdk.TBC20.minTapeBytes,
  );
  const tbc20Code = sdk.TBC20.instantiateCode({
    originalUTXO: { txId: txid, outputIndex: 3 },
    tapeSize: sdk.TBC20.minTapeBytes,
    controller: sdk.TBC20.addressController(address),
  });
  const parsedTape = sdk.TBC20.parseTape(tbc20Tape);

  const tbc20PrivateKey = new tbc.PrivateKey("11".repeat(32));
  const tbc20RecipientKey = new tbc.PrivateKey("22".repeat(32));
  const tbc20Owner = tbc20PrivateKey.toAddress().toString();
  const tbc20P2PKH = tbc.Script.buildPublicKeyHashOut(
    tbc20PrivateKey.toAddress(),
  ).toHex();
  const tbc20 = new sdk.TBC20({
    name: "Parity Token",
    symbol: "PTY",
    supply: "1000.00000000",
    decimal: 8,
  });
  const tbc20Mint = tbc20.mint(tbc20PrivateKey, tbc20Owner, {
    txId: "aa".repeat(32),
    outputIndex: 0,
    script: tbc20P2PKH,
    satoshis: 300000,
  });
  const tbc20Transfer = tbc20.transfer(
    tbc20PrivateKey,
    tbc20RecipientKey.toAddress().toString(),
    "123.45600000",
    [
      {
        txId: tbc20Mint.transaction.hash,
        outputIndex: 0,
        script: tbc20Mint.transaction.outputs[0].script.toHex(),
        satoshis: tbc20Mint.transaction.outputs[0].satoshis,
      },
    ],
    {
      txId: "bb".repeat(32),
      outputIndex: 1,
      script: tbc20P2PKH,
      satoshis: 100000,
    },
    [tbc20Mint.transaction],
    [[tbc20Mint.sourceTransaction]],
    { verify: true },
  );
  const tbc20Split = tbc20.transfer(
    tbc20PrivateKey,
    tbc20Owner,
    "400.00000000",
    [
      {
        txId: tbc20Mint.transaction.hash,
        outputIndex: 0,
        script: tbc20Mint.transaction.outputs[0].script.toHex(),
        satoshis: tbc20Mint.transaction.outputs[0].satoshis,
      },
    ],
    {
      txId: "bb".repeat(32),
      outputIndex: 2,
      script: tbc20P2PKH,
      satoshis: 100000,
    },
    [tbc20Mint.transaction],
    [[tbc20Mint.sourceTransaction]],
    { verify: true },
  );
  const tbc20Merge = tbc20.merge(
    tbc20PrivateKey,
    [0, 2].map((outputIndex) => ({
      txId: tbc20Split.transaction.hash,
      outputIndex,
      script: tbc20Split.transaction.outputs[outputIndex].script.toHex(),
      satoshis: tbc20Split.transaction.outputs[outputIndex].satoshis,
    })),
    {
      txId: "cc".repeat(32),
      outputIndex: 0,
      script: tbc20P2PKH,
      satoshis: 100000,
    },
    [tbc20Split.transaction, tbc20Split.transaction],
    [[tbc20Mint.transaction], [tbc20Mint.transaction]],
    { verify: true },
  );
  const tbc20UnlockChunks = tbc20Transfer.transaction.inputs[0].script.chunks;
  if (tbc20UnlockChunks.length !== 123) {
    throw new Error(`expected 123 TBC20 unlock chunks, got ${tbc20UnlockChunks.length}`);
  }

  const scripts = {
    ftV4Mint: fixture(ftV4Mint, {
      kind: "ordinary-ft-v4",
      destination: address,
      codeBytes: String(ftV4Mint.toBuffer().length),
    }),
    stableCoinV4Mint: fixture(stableCoinV4Mint, {
      kind: "stablecoin-v4",
      destination: address,
      codeBytes: String(stableCoinV4Mint.toBuffer().length),
    }),
    nftV2Code: fixture(nftV2Code, { version: "2", marker: "3Code" }),
    nftV1Code: fixture(nftV1Code, { version: "1", marker: "ac6a" }),
    poolPlan6V4: fixture(poolPlan6V4, {
      plan: "6",
      version: "4",
      locked: "false",
    }),
    poolPlan6V4Locked: fixture(poolPlan6V4Locked, {
      plan: "6",
      version: "4",
      locked: "true",
    }),
    ftlpV4: fixture(ftlpV4, { version: "4", lockTime: "false" }),
    ftlpV4LockTime: fixture(ftlpV4LockTime, {
      version: "4",
      lockTime: "true",
    }),
    sellOrderV4: fixture(sellOrderV4, { side: "sell", version: "4" }),
    buyOrderV4: fixture(buyOrderV4, { side: "buy", version: "4" }),
    tbc20Code: fixture(tbc20Code, {
      version: "1",
      codeBytes: String(sdk.TBC20.codeBytes),
      partialOffset: String(sdk.TBC20.partialOffset),
    }),
    tbc20Tape: fixture(tbc20Tape, {
      version: "1",
      tapeBytes: String(parsedTape.size),
      balance: parsedTape.balance.toString(),
    }),
  };

  const vectors = {
    artifactSha256: sdk.TBC20.artifactSha256,
    codeBytes: sdk.TBC20.codeBytes,
    partialOffset: sdk.TBC20.partialOffset,
    minTapeBytes: sdk.TBC20.minTapeBytes,
    maxTapeBytes: sdk.TBC20.maxTapeBytes,
    maxSlotAmount: sdk.TBC20.maxSlotAmount.toString(),
    originalUTXOHex: sdk.TBC20.encodeOriginalUTXO({
      txId: txid,
      outputIndex: 3,
    }).toString("hex"),
    controllerHex: sdk.TBC20.addressController(address).toString("hex"),
    codeHex: tbc20Code.toHex(),
    codeIdentityHex: require(path.join(jsRoot, "lib/util/tbc20unlock.js"))
      .getTBC20CodeIdentity(tbc20Code)
      .toString("hex"),
    tapeHex: tbc20Tape.toHex(),
    tapeAmounts: parsedTape.amounts.map((amount) => amount.toString()),
    tapeBalance: parsedTape.balance.toString(),
    transactionFixture: {
      privateKeyHex: "11".repeat(32),
      recipient: tbc20RecipientKey.toAddress().toString(),
      fundingScriptHex: tbc20P2PKH,
      sourceRaw: tbc20Mint.sourceTxraw,
      sourceFeeSatoshis: tbc20Mint.sourceFeeSatoshis,
      mintRaw: tbc20Mint.txraw,
      mintFeeSatoshis: tbc20Mint.feeSatoshis,
      transferRaw: tbc20Transfer.txraw,
      transferFeeSatoshis: tbc20Transfer.feeSatoshis,
      transferUnlockHex: tbc20Transfer.transaction.inputs[0].script.toHex(),
      transferUnlockChunks: tbc20UnlockChunks.length,
      transferSignatureHex: tbc20UnlockChunks[98].buf.toString("hex"),
      publicKeyHex: tbc20PrivateKey.toPublicKey().toBuffer().toString("hex"),
      transferTokenAmounts: tbc20Transfer.tokenOutputs.map((output) =>
        output.amount.toString(),
      ),
      splitRaw: tbc20Split.txraw,
      splitFeeSatoshis: tbc20Split.feeSatoshis,
      mergeRaw: tbc20Merge.txraw,
      mergeFeeSatoshis: tbc20Merge.feeSatoshis,
      mergeTokenAmounts: tbc20Merge.tokenOutputs.map((output) =>
        output.amount.toString(),
      ),
    },
  };

  const invalidPolicy = await sdk.TokenValidator.validateOnChainTransaction({});
  const invalidRoot = await sdk.TokenValidator.validateOnChainTransaction({
    transaction: "zz",
    network: "testnet",
  });
  const validatorAPI = require(path.join(jsRoot, "lib/api/api.js"));
  const validatorTransactions = new Map(
    [
      tbc20Mint.sourceTransaction,
      tbc20Mint.transaction,
      tbc20Split.transaction,
    ].map((transaction) => [transaction.hash, transaction]),
  );
  validatorAPI.fetchTXraw = async (requestedTxid) => {
    const transaction = validatorTransactions.get(requestedTxid.toLowerCase());
    if (!transaction) throw new Error(`missing fixture transaction ${requestedTxid}`);
    return transaction;
  };
  const validTransfer = await sdk.TokenValidator.validateOnChainTransaction({
    transaction: tbc20Transfer.transaction,
    network: "testnet",
  });
  const validMerge = await sdk.TokenValidator.validateOnChainTransaction({
    transaction: tbc20Merge.transaction,
    network: "testnet",
  });
  const reports = {
    invalidPolicy: invalidPolicy.toJSON(),
    invalidRoot: invalidRoot.toJSON(),
    validTransfer: validTransfer.toJSON(),
    validMerge: validMerge.toJSON(),
  };

  const outputPath = process.argv[2];
  if (outputPath) {
    fs.writeFileSync(outputPath, asJSON(scripts), { mode: 0o644 });
    return;
  }
  const contractDir = path.join(repoRoot, "lib/contract/testdata/js-1.6.6");
  const utilDir = path.join(repoRoot, "lib/util/testdata/js-1.6.6");
  const validatorDir = path.join(repoRoot, "lib/validator/testdata/js-1.6.6");
  fs.mkdirSync(contractDir, { recursive: true });
  fs.mkdirSync(utilDir, { recursive: true });
  fs.mkdirSync(validatorDir, { recursive: true });
  fs.writeFileSync(path.join(contractDir, "script-hashes.json"), asJSON(scripts), { mode: 0o644 });
  fs.writeFileSync(path.join(contractDir, "tbc20-vectors.json"), asJSON(vectors), { mode: 0o644 });
  fs.writeFileSync(path.join(utilDir, "ft-v4.hex"), `${ftV4Mint.toHex()}\n`, { mode: 0o644 });
  fs.writeFileSync(path.join(utilDir, "coin-v4.hex"), `${stableCoinV4Mint.toHex()}\n`, { mode: 0o644 });
  fs.writeFileSync(path.join(validatorDir, "reports.json"), asJSON(reports), { mode: 0o644 });
}

main().catch((error) => {
  process.stderr.write(`${error.stack || error}\n`);
  process.exitCode = 1;
});
