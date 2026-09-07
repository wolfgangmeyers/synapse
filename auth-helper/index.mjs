#!/usr/bin/env node

import { createInterface } from "node:readline";
import { discoverAvailableModels } from "./providers.mjs";
import { encodeResult, parseRequest, safeError } from "./protocol.mjs";

const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
const lines = [];
let inputBytes = 0;
let oversized = false;
for await (const line of input) {
  inputBytes += Buffer.byteLength(line, "utf8") + 1;
  if (inputBytes <= 128 * 1024) lines.push(line);
  else oversized = true;
}

if (oversized || lines.length !== 1) {
  process.stdout.write(encodeResult(safeError()));
  process.exitCode = 1;
} else {
  try {
    parseRequest(lines[0]);
    const result = await discoverAvailableModels({
      authPath: process.env.MECHA_MANAGED_AUTH_PATH,
    });
    process.stdout.write(encodeResult(result));
  } catch {
    process.stdout.write(encodeResult(safeError()));
    process.exitCode = 1;
  }
}
