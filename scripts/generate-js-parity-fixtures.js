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

const address = "1BitcoinEaterAddressDontSendf59kuE";
const txid = "11".repeat(32);
const codeHash = "22".repeat(32);
const adminPubHash = "33".repeat(20);
const compressedPublicKey = `02${"44".repeat(32)}`;
const tapeSize = 80;

const ft = new sdk.FT({
  name: "Parity",
  symbol: "PTY",
  amount: 1,
  decimal: 6,
});
const pool = new sdk.poolNFT2({ network: "testnet" });

const scripts = {
  ftV3Mint: ft.getFTmintCode(txid, 0, address, tapeSize),
  poolV3: pool.getPoolNftCode(txid, 0, 2, 3, "parity", false),
  poolV3Locked: pool.getPoolNftCodeWithLock(
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
  ftlpV2: pool.getFtlpCode(codeHash, address, tapeSize, false, 2),
  ftlpV3: pool.getFtlpCode(codeHash, address, tapeSize, false, 3),
  ftlpV3LockTime: pool.getFtlpCodeWithLockTime(
    codeHash,
    address,
    tapeSize,
    false,
    3,
  ),
  stableCoinMint: sdk.stableCoin.getCoinMintCode(
    adminPubHash,
    address,
    codeHash,
    tapeSize,
  ),
};

const fixtures = {};
for (const [name, script] of Object.entries(scripts)) {
  const bytes = script.toBuffer();
  fixtures[name] = {
    length: bytes.length,
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
  };
}

const output = `${JSON.stringify(fixtures, null, 2)}\n`;
const outputPath = process.argv[2];
if (outputPath) {
  fs.writeFileSync(outputPath, output, { mode: 0o644 });
} else {
  process.stdout.write(output);
}
