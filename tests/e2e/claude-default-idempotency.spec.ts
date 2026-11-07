import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

// CD-001: exercise durable request replay through the browser, proxy and SQLite.
test("Claude default idempotency survives restart without reverting a later change", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const requestId = randomUUID();
    const sonnet = { provider: "claude-code", id: "sonnet" };
    const haiku = { provider: "claude-code", id: "haiku" };
    const put = (id: string, model: object | null) => page.evaluate(async ({ url, id, model }) => {
      const response = await fetch(url, {
        method: "PUT", credentials: "omit",
        headers: { "Content-Type": "application/json", "X-Request-ID": id },
        body: JSON.stringify({ model }),
      });
      return { status: response.status, body: await response.text() };
    }, { url: `${f.proxy.url}/v1/settings/default-model`, id, model });

    const first = await put(requestId, sonnet);
    expect(first.status).toBe(200);
    expect(await put(requestId, sonnet)).toEqual(first);
    expect((await put(randomUUID(), haiku)).status).toBe(200);
    await f.restart();
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(haiku);
    expect(await put(requestId, sonnet)).toEqual(first);
    expect((await put(requestId, haiku)).status).toBe(409);
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(haiku);
    expect((await put(randomUUID(), { provider: "claude-code", id: "not-a-model" })).status).toBe(422);
    expect((await f.request(page, "/v1/models")).body.default_model).toMatchObject(haiku);
    expect((await put(randomUUID(), null)).status).toBe(200);
    await f.restart();
    await page.reload();
    expect((await f.request(page, "/v1/models")).body.default_model).toBeNull();
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
