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
    "tbc-lib-js was not found; set TBC_CONTRACT_JS_ROOT to the exact JS SDK package",
  );
}

function main() {
  const payload = JSON.parse(fs.readFileSync(0, "utf8"));
  if (typeof payload.raw !== "string" || !Array.isArray(payload.parents)) {
    throw new Error("raw transaction and parent transactions are required");
  }
  const tbc = loadTbcLib();
  const transaction = new tbc.Transaction(payload.raw);
  if (payload.parents.length !== transaction.inputs.length) {
    throw new Error("parent transaction count does not match input count");
  }
  for (let index = 0; index < transaction.inputs.length; index += 1) {
    const parent = new tbc.Transaction(payload.parents[index]);
    const input = transaction.inputs[index];
    if (!parent.outputs[input.outputIndex]) {
      throw new Error(`input ${index} parent output is out of range`);
    }
    input.output = parent.outputs[input.outputIndex];
    const result = transaction.verifyScript(index);
    if (!result || !result.success) {
      const reason = result && result.error ? result.error : "script verification failed";
      throw new Error(`input ${index}: ${reason}`);
    }
  }
  process.stdout.write(JSON.stringify({ success: true }));
}

try {
  main();
} catch (error) {
  console.error(error && error.message ? error.message : String(error));
  process.exit(1);
}
