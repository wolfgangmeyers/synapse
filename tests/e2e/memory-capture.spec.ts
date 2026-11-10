import { test, expect } from "@playwright/test";
import { resolve } from "node:path";
import { readFileSync } from "node:fs";
const capturePolicy = JSON.parse(readFileSync(new URL("../fixtures/memory-capture-policy.json", import.meta.url), "utf8"));
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
