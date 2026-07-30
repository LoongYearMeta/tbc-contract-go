"use strict";

const fs = require("fs");
const path = require("path");

function loadTbcLib() {
  const roots = [
    process.env.TBC_CONTRACT_JS_ROOT,
    path.resolve(process.cwd(), "../tbc-contract"),
    path.resolve(process.cwd(), "../../../tbc-contract"),
  ].filter(Boolean);
  for (const root of roots) {
    const packagePath = path.join(root, "node_modules", "tbc-lib-js");
    if (fs.existsSync(path.join(packagePath, "package.json"))) {
      return require(packagePath);
    }
  }
  throw new Error(
    "tbc-lib-js was not found; set TBC_CONTRACT_JS_ROOT to the JS SDK checkout",
  );
}

const tbc = loadTbcLib();

const { MuSig2, Schnorr } = tbc.crypto;

function signerContext() {
  const wif = process.env.TBC_TESTNET_WIF;
  if (!wif) {
    throw new Error("TBC_TESTNET_WIF is required");
  }
  const privateKey = tbc.PrivateKey.fromWIF(wif);
  const secretKey = privateKey.toBuffer();
  const publicKey = MuSig2.pubkeyFromSk(secretKey);
  const publicKeys = MuSig2.keySort([publicKey]);
  const keyAggCtx = MuSig2.keyAgg(publicKeys);
  const aggPubkey32 = MuSig2.getAggPubkey(keyAggCtx);
  return { secretKey, publicKey, keyAggCtx, aggPubkey32 };
}

function signOne(context, message) {
  const nonce = MuSig2.nonceGen({
    pk: context.publicKey,
    sk: context.secretKey,
    aggpk: context.aggPubkey32,
    msg: message,
  });
  const aggregateNonce = MuSig2.nonceAgg([nonce.pubnonce]);
  const session = MuSig2.buildSession(
    context.keyAggCtx,
    aggregateNonce,
    message,
  );
  const partial = MuSig2.partialSign(
    nonce.secnonce,
    context.secretKey,
    session,
  );
  const signature = MuSig2.partialSigAgg([partial], session);
  if (!Schnorr.verify(message, signature, context.aggPubkey32)) {
    throw new Error("local Schnorr verification failed");
  }
  return signature;
}

function main() {
  const context = signerContext();
  if (process.argv[2] === "aggregate") {
    process.stdout.write(context.aggPubkey32.toString("hex"));
    return;
  }
  if (process.argv[2] !== "sign" || process.argv.length < 4) {
    throw new Error("usage: js-musig-sign.js aggregate | sign <sighash>...");
  }
  const signatures = process.argv.slice(3).map((hex) => {
    const message = Buffer.from(hex, "hex");
    if (message.length !== 32) {
      throw new Error("each sighash must be 32 bytes");
    }
    return signOne(context, message).toString("hex");
  });
  process.stdout.write(JSON.stringify(signatures));
}

try {
  main();
} catch (error) {
  console.error(error && error.stack ? error.stack : error);
  process.exit(1);
}
