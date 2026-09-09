import assert from "node:assert/strict";
import test from "node:test";
import { resolve } from "node:path";
import { acceptanceRunDirectory, loadProviderScenarios } from "./support/managed_rpc_harness.ts";

const root = resolve(import.meta.dirname, "..");

test("provider fixture covers the three bounded shared-auth providers", async () => {
  const providers = await loadProviderScenarios(root);
  assert.deepEqual(providers.map((provider) => provider.provider), ["openai-codex", "google", "cursor"]);
  assert.equal(acceptanceRunDirectory("a".repeat(40), "run-1"), `/tmp/mecha-rpc-acceptance/${"a".repeat(40)}/run-1`);
});
