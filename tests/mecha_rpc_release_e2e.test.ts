import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

test("the release command uses the Browser Console Bridge managed-RPC gate", async () => {
  const root = resolve(import.meta.dirname, "..");
  const packageJson = JSON.parse(await readFile(resolve(root, "package.json"), "utf8")) as { scripts?: Record<string, string> };
  const command = packageJson.scripts?.["test:mecha-rpc:release-e2e"];
  assert.equal(command, "node web/router-ui-react/tests/acceptance/bcb-runner.mjs --managed-rpc --release");
});
