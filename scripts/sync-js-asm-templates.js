"use strict";

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
const pkg = JSON.parse(
  fs.readFileSync(path.join(jsRoot, "package.json"), "utf8"),
);
if (pkg.version !== "1.6.6") {
  throw new Error(`expected tbc-contract 1.6.6, found ${pkg.version}`);
}
const sdk = require(jsRoot);

function extractTemplate(source, marker, declaration) {
  const markerAt = source.indexOf(marker);
  if (markerAt < 0) {
    throw new Error(`marker not found: ${marker}`);
  }
  const declarationAt = source.indexOf(declaration, markerAt);
  if (declarationAt < 0) {
    throw new Error(`declaration not found after ${marker}: ${declaration}`);
  }
  const start = source.indexOf("`", declarationAt);
  const end = source.indexOf("`", start + 1);
  if (start < 0 || end < 0) {
    throw new Error(`template literal not found after ${marker}`);
  }
  return source.slice(start + 1, end);
}

function writeTemplate(relativePath, template) {
  const output = path.join(repoRoot, relativePath);
  const normalized = template.replace(
    /\$\{"ff"\.repeat\(0x([0-9a-f]+)\)\}/gi,
    (_, count) => "ff".repeat(Number.parseInt(count, 16)),
  );
  fs.writeFileSync(output, `${normalized}\n`, { mode: 0o644 });
}

const ftSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/ft.js"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/ft_mint.asm",
  extractTemplate(ftSource, "getFTmintCode(txid, vout, address, tapeSize)", "new tbc.Script("),
);

const stableSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/stableCoin.js"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/stablecoin_mint.asm",
  extractTemplate(
    stableSource,
    "static getCoinMintCode(adminPubHashHex, receiveAddress, codeHash, tapeSize)",
    "new tbc.Script(",
  ),
);

const poolSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/poolNFT2.0.js"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/poolnft2_ftlp_code.asm",
  extractTemplate(
    poolSource,
    "    getFtlpCode(poolNftCodeHash, address, tapeSize, isCoin, ftVersion) {",
    "const ftlpCodePreTemplate =",
  ),
);
writeTemplate(
  "lib/contract/asm/poolnft2_ftlp_locktime_code.asm",
  extractTemplate(
    poolSource,
    "    getFtlpCodeWithLockTime(poolNftCodeHash, address, tapeSize, isCoin, ftVersion) {",
    "const ftlpCodePreTemplate =",
  ),
);

const nftFixtureTxid = "11".repeat(32);
const nftFixtureOutpoint = `${"11".repeat(32)}00000000`;
function nftTemplate(script) {
  const asm = script.toASM();
  if (!asm.includes(nftFixtureOutpoint)) {
    throw new Error("NFT template does not contain the fixed original outpoint");
  }
  return asm
    .split(" ")
    .map((token) => {
      if (token === nftFixtureOutpoint) {
        return "0x24 0x${utxoHex}";
      }
      if (token === "0") {
        return "OP_0";
      }
      if (/^[0-9a-f]+$/i.test(token) && token.length % 2 === 0) {
        const bytes = token.length / 2;
        return `0x${bytes.toString(16).padStart(2, "0")} 0x${token}`;
      }
      return token;
    })
    .join(" ");
}
writeTemplate(
  "lib/contract/asm/nft_code.asm",
  nftTemplate(sdk.NFT.buildCodeScript(nftFixtureTxid, 0)),
);
writeTemplate(
  "lib/contract/asm/nft_code_v1.asm",
  nftTemplate(sdk.NFT.buildCodeScript_v1(nftFixtureTxid, 0)),
);
