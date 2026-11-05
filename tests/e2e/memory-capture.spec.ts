import { test, expect } from "@playwright/test";
import assert from "node:assert/strict";
import { resolve } from "node:path";
import { readFileSync } from "node:fs";
const capturePolicy = JSON.parse(readFileSync(new URL("../fixtures/memory-capture-policy.json", import.meta.url), "utf8"));
import { createMemoryCaptureLifecycle } from "../../pi-extensions/mecha-messaging/memory-capture";
import { startIsolatedRouter, startRouterProxy, type IsolatedRouter, type IsolatedProxy } from "./harness";

let router: IsolatedRouter;
let proxy: IsolatedProxy;
test.beforeAll(async () => {
  test.setTimeout(120_000);
  router = await startIsolatedRouter({ repoRoot: resolve("."), managed: false });
  try { proxy = await startRouterProxy({ repoRoot: resolve("."), upstreamPort: router.port }); }
  catch (error) { await router.close(); throw error; }
});
test.afterAll(async () => { try { await proxy?.close(); } finally { await router?.close(); } });

test("default router publishes policy without pretending catalog persistence", async ({ page }) => {
  // A real headless browser makes cross-origin calls through the real proxy.
  await page.goto(`${router.httpUrl}/v1/health`);
  const result = await page.evaluate(async (url) => {
    const policy = await fetch(`${url}/v1/memory/policy`, { credentials: "omit" }).then(r => r.json());
    const operations = [];
    for (const operation of ["lookup", "expand", "touch", "apply", "rebuild"]) {
      const response = await fetch(`${url}/v1/memory/${operation}`, {
        method: "POST", credentials: "omit", headers: { "content-type": "application/json" },
        body: JSON.stringify({ stores: ["default"], operations: [] }),
      });
      operations.push({ operation, status: response.status, body: await response.json() });
    }
    return { policy, operations, health: await fetch(`${url}/v1/health`).then(r => r.status) };
  }, proxy.url);
  expect(result.policy.recall.capture).toEqual(capturePolicy);
  for (const operation of result.operations) {
    expect(operation.status, operation.operation).toBe(404);
    expect(operation.body.error.code).toBe("not_found");
    expect(operation.body).not.toHaveProperty("records");
  }
  expect(result.health).toBe(200);
});

test("unavailable destination prevents extraction and apply without breaking router work", async ({ page }) => {
  const { recall: policy } = await fetch(`${proxy.url}/v1/memory/policy`).then(r => r.json());
  const diagnostics: Array<Record<string, unknown>> = [];
  let extracts = 0; let applies = 0;
  const capture = createMemoryCaptureLifecycle({ policy,
    context: async ({ signal }) => {
      const response = await fetch(`${proxy.url}/v1/memory/lookup`, { method: "POST", signal,
        headers: { "content-type": "application/json" }, body: JSON.stringify({ stores: ["default"] }) });
      if (!response.ok) throw new Error("capture destination unavailable");
      return (await response.json()).records;
    },
    extract: async () => { extracts++; throw new Error("unexpected extractor invocation"); },
    apply: async () => { applies++; throw new Error("unexpected apply invocation"); },
    diagnostics: event => { diagnostics.push(event); },
  });
  const source = [{ id: "tool-1", role: "toolResult", content: "Verified durable fixture text." }];
  try {
    await assert.rejects(capture.compact(source), /capture destination unavailable/);
    await assert.rejects(capture.shutdown(source), /capture destination unavailable/);
    expect(extracts).toBe(0); expect(applies).toBe(0);
    expect(diagnostics.map(({ trigger, status, reason }) => ({ trigger, status, reason }))).toEqual([
      { trigger: "compaction", status: "failed", reason: "destination-unavailable" },
      { trigger: "shutdown", status: "failed", reason: "destination-unavailable" },
    ]);
    expect(JSON.stringify(diagnostics)).not.toContain(source[0].content);
    await page.goto(`${proxy.url}/v1/health`);
    expect(await page.locator("body").innerText()).toContain('"status":"ok"');
  } finally { capture.close(); }
});
