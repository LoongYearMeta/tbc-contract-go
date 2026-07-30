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
    "tbc-contract 1.6.5 not found; set TBC_CONTRACT_JS_DIR to its checkout",
  );
}
const pkg = JSON.parse(
  fs.readFileSync(path.join(jsRoot, "package.json"), "utf8"),
);
if (pkg.version !== "1.6.5") {
  throw new Error(`expected tbc-contract 1.6.5, found ${pkg.version}`);
}

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
  fs.writeFileSync(output, `${template}\n`, { mode: 0o644 });
}

const ftSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/ft.ts"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/ft_mint.asm",
  extractTemplate(ftSource, "getFTmintCode(txid: string", "new tbc.Script("),
);

const stableSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/stableCoin.ts"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/stablecoin_mint.asm",
  extractTemplate(
    stableSource,
    "  static getCoinMintCode(\n",
    "new tbc.Script(",
  ),
);

const poolSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/poolNFT2.0.ts"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/poolnft2_ftlp_code.asm",
  extractTemplate(
    poolSource,
    "  getFtlpCode(\n    poolNftCodeHash:",
    "const ftlpCodePreTemplate =",
  ),
);
writeTemplate(
  "lib/contract/asm/poolnft2_ftlp_locktime_code.asm",
  extractTemplate(
    poolSource,
    "  getFtlpCodeWithLockTime(\n",
    "const ftlpCodePreTemplate =",
  ),
);

const orderBookSource = fs.readFileSync(
  path.join(jsRoot, "lib/contract/orderBook.ts"),
  "utf8",
);
writeTemplate(
  "lib/contract/asm/orderbook_token_sell.hex",
  extractTemplate(
    orderBookSource,
    "  getTokenSellOrderCode(taxAddress: string)",
    "tbc.Script.fromHex(",
  ),
);
writeTemplate(
  "lib/contract/asm/orderbook_token_buy.hex",
  extractTemplate(
    orderBookSource,
    "  getTokenBuyOrderCode(taxAddress: string)",
    "tbc.Script.fromHex(",
  ),
);
