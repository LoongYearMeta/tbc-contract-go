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
    "tbc-contract 1.6.5 not found; set TBC_CONTRACT_JS_DIR to its checkout",
  );
}

const pkg = require(path.join(jsRoot, "package.json"));
if (pkg.version !== "1.6.5") {
  throw new Error(`expected tbc-contract 1.6.5, found ${pkg.version}`);
}
const sdk = require(jsRoot);
const tbc = require(path.join(jsRoot, "node_modules", "tbc-lib-js"));

const address = "1BitcoinEaterAddressDontSendf59kuE";
const txid = "11".repeat(32);
const codeHash = "22".repeat(32);
const adminPubHash = "33".repeat(20);
const compressedPublicKey = `02${"44".repeat(32)}`;
const tapeSize = 80;
const transferBalance = 123456n;
const transferInputBalance = 200000n;
const lockTime = 500000123;

const ft = new sdk.FT({
  name: "Parity",
  symbol: "PTY",
  amount: 1,
  decimal: 6,
});
const pool = new sdk.poolNFT2({ network: "testnet" });
const zeroAmount = "00".repeat(48);
const ftTapeTemplate = tbc.Script.fromASM(
  `OP_FALSE OP_RETURN ${zeroAmount} 06 506172697479 505459 4654617065`,
);
const { amountHex } = sdk.FT.buildTapeAmount(transferBalance, [
  transferInputBalance,
]);
const ftTransferTape = sdk.FT.buildFTtransferTape(
  ftTapeTemplate.toHex(),
  amountHex,
);
const nftData = {
  nftName: "Parity NFT",
  symbol: "PNFT",
  description: "JavaScript 1.6.5 fixture",
  attributes: "{\"level\":1}",
  file: "ipfs://parity",
};
const nftTape = sdk.NFT.buildTapeScript(nftData);
const stableCoinTapeTemplate = tbc.Script.fromASM(
  `OP_FALSE OP_RETURN ${zeroAmount} 06 506172697479436f696e 50434e 00000000 4654617065`,
);
const stableCoinTransferTape = sdk.FT.buildFTtransferTape(
  stableCoinTapeTemplate.toHex(),
  amountHex,
);
const stableCoinTape = sdk.stableCoin.setLockTimeInTape(
  stableCoinTransferTape,
  lockTime,
);

const order = new sdk.orderBook();
order.hold_address = address;
order.sale_volume = 123456n;
order.fee_rate = 10000n;
order.unit_price = 2000000n;
order.ft_a_contract_partialhash = codeHash;
order.ft_a_contract_id = txid;

const artifacts = {
  ftV3Mint: {
    script: ft.getFTmintCode(txid, 0, address, tapeSize),
    decoded: {
      kind: "ordinary-ft-v3",
      destination: address,
      codeBytes: "1884",
    },
  },
  ftTransferTape: {
    script: ftTransferTape,
    decoded: { balance: transferBalance.toString(), marker: "FTape" },
  },
  nftTape: {
    script: nftTape,
    decoded: {
      nftName: nftData.nftName,
      symbol: nftData.symbol,
      file: nftData.file,
    },
  },
  poolV3: {
    script: pool.getPoolNftCode(txid, 0, 2, 3, "parity", false),
    decoded: { version: "3", locked: "false", tokenID: txid },
  },
  poolV3Locked: {
    script: pool.getPoolNftCodeWithLock(
      txid,
      0,
      2,
      address,
      0.001,
      [compressedPublicKey],
      3,
      "parity",
      false,
    ),
    decoded: { version: "3", locked: "true", tokenID: txid },
  },
  ftlpV2: {
    script: pool.getFtlpCode(codeHash, address, tapeSize, false, 2),
    decoded: { version: "2", lockTime: "false" },
  },
  ftlpV3: {
    script: pool.getFtlpCode(codeHash, address, tapeSize, false, 3),
    decoded: { version: "3", lockTime: "false" },
  },
  ftlpV3LockTime: {
    script: pool.getFtlpCodeWithLockTime(
      codeHash,
      address,
      tapeSize,
      false,
      3,
    ),
    decoded: { version: "3", lockTime: "true" },
  },
  sellOrder: {
    script: order.getSellOrderCode(false, address),
    decoded: {
      side: "sell",
      holder: address,
      saleVolume: order.sale_volume.toString(),
      unitPrice: order.unit_price.toString(),
      feeRate: order.fee_rate.toString(),
      tokenID: txid,
      partialHash: codeHash,
    },
  },
  buyOrder: {
    script: order.getBuyOrderCode(false, address),
    decoded: {
      side: "buy",
      holder: address,
      saleVolume: order.sale_volume.toString(),
      unitPrice: order.unit_price.toString(),
      feeRate: order.fee_rate.toString(),
      tokenID: txid,
      partialHash: codeHash,
    },
  },
  stableCoinMint: {
    script: sdk.stableCoin.getCoinMintCode(
      adminPubHash,
      address,
      codeHash,
      tapeSize,
    ),
    decoded: {
      kind: "stablecoin",
      destination: address,
      codeBytes: "2012",
    },
  },
  stableCoinTape: {
    script: stableCoinTape,
    decoded: {
      balance: transferBalance.toString(),
      lockTime: sdk.stableCoin.getLockTimeFromTape(stableCoinTape).toString(),
      marker: "FTape",
    },
  },
};

const fixtures = {};
for (const [name, artifact] of Object.entries(artifacts)) {
  const { script, decoded } = artifact;
  const bytes = script.toBuffer();
  fixtures[name] = {
    length: bytes.length,
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
    decoded,
  };
}

const output = `${JSON.stringify(fixtures, null, 2)}\n`;
const outputPath = process.argv[2];
if (outputPath) {
  fs.writeFileSync(outputPath, output, { mode: 0o644 });
} else {
  process.stdout.write(output);
}
