import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { loadSentinelReceiverPolicies as loadExtensionReceiverPolicies } from "../pi-extensions/mecha-messaging.js";
import { loadSentinelReceiverPolicies, parseArguments } from "../tools/run_router.mjs";

test("router accepts an explicit fixed Sentinel receiver-policy file without enabling it by default", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-policy-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const policyPath = join(directory, "policies.json");
  await writeFile(policyPath, JSON.stringify({
    managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
  }));

  assert.equal(parseArguments(["--database", join(directory, "router.sqlite")]).sentinelReceiverPoliciesPath, undefined);
  assert.equal(parseArguments([
    "--database", join(directory, "router.sqlite"),
    "--sentinel-receiver-policies", policyPath,
  ]).sentinelReceiverPoliciesPath, policyPath);
  const sharedPolicy = {
    managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
  };
  assert.deepEqual(await loadSentinelReceiverPolicies(policyPath), sharedPolicy);
  assert.deepEqual(loadExtensionReceiverPolicies(policyPath), sharedPolicy);
  for (const tools of [["sentinel_reply"], ["other_tool"]]) {
    await writeFile(policyPath, JSON.stringify({
      managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools } },
    }));
    await assert.rejects(loadSentinelReceiverPolicies(policyPath), /invalid fixed profile/);
    assert.throws(() => loadExtensionReceiverPolicies(policyPath), /unavailable or malformed/);
  }
});

test("router rejects a local Sentinel sender not allowed by its receiver policy", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-policy-http-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({
    repository,
    sentinelReceiverPolicies: {
      managed: { receiver: "receiver", senders: ["supervisor"], models: ["gpt-5.6-terra"], execution: { supervisor: "supervisor", auth_path: "operator-auth", tools: [] } },
    },
  });
  t.after(async () => { await router.close(); repository.close(); });
  const url = new URL(await router.listen());
  url.protocol = "http:";
  const response = await fetch(`${url.origin}/v1/messages`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      id: "spoofed-sentinel", from: "untrusted", to: "receiver", body: "opaque",
      created_at: "2030-01-01T00:00:00.000Z",
      sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
    }),
  });
  assert.equal(response.status, 400);
  assert.equal((await response.json()).error.code, "invalid_sentinel_policy");
});

test("router denies Sentinel admission when no fixed profile is configured", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-sentinel-policy-default-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository });
  t.after(async () => {
    await router.close();
    repository.close();
  });
  const url = new URL(await router.listen());
  url.protocol = "http:";
  const response = await fetch(`${url.origin}/v1/messages`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      id: "denied-sentinel", from: "supervisor", to: "receiver", body: "opaque",
      created_at: "2030-01-01T00:00:00.000Z",
      sentinel: { receiver_policy: "managed", model: "gpt-5.6-terra" },
    }),
  });
  assert.equal(response.status, 400);
  assert.equal((await response.json()).error.code, "invalid_sentinel_policy");
});
