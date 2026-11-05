import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { workflowHarness } from "./workflow-implicit-goals-harness";

// CC-001A: the provider discovery helper is the external deterministic boundary;
// the browser, proxy, router, catalog and SQLite remain real.
test("launch picker scrolls the full catalog and keeps search", async ({ page }) => {
  test.setTimeout(180_000);
  const f = await workflowHarness();
  try {
    const helper = join(f.router.root, "provider-fixture/index.mjs");
    const source = await readFile(helper, "utf8");
    const extra = Array.from({ length: 15 }, (_, i) =>
      `{ provider: "google", id: "fixture-${String(i).padStart(2, "0")}", name: "Fixture ${i}", available: true },`).join("\n");
    await writeFile(helper, source.replace('models: [', `models: [\n${extra}`));
    await f.router.restart();
    await expect.poll(async () => (await f.api("/v1/models")).models.some((m: { provider: string; id: string; available: boolean }) => m.provider === "google" && m.id === "fixture-14" && m.available), { timeout: 15000 }).toBe(true);
    await page.setViewportSize({ width: 900, height: 500 });
    await f.open(page);
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Launch Agent", exact: true }).click();
    const form = page.getByRole("dialog", { name: "Launch managed agent" });
    await form.getByRole("button", { name: "Use router default" }).click();
    const search = page.getByRole("searchbox", { name: "Search available models" });
    const options = page.getByRole("listbox");
    await expect.poll(() => options.getByRole("option").count()).toBeGreaterThan(11);
    const claude = options.getByRole("option").filter({ hasText: "claude-code/sonnet" });
    await expect(claude).toHaveCount(1);
    expect(await options.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true);
    await options.evaluate(el => { el.scrollTop = el.scrollHeight; });
    await claude.scrollIntoViewIfNeeded();
    expect(await options.evaluate(el => el.scrollTop)).toBeGreaterThan(0);
    const geometry = await claude.evaluate(el => {
      const option = el.getBoundingClientRect();
      const list = el.closest('[role="listbox"]')!.getBoundingClientRect();
      const menu = el.closest('[role="dialog"]')!.getBoundingClientRect();
      return { optionTop: option.top, optionBottom: option.bottom, listTop: list.top, listBottom: list.bottom, menuTop: menu.top, menuBottom: menu.bottom, viewportHeight: window.innerHeight };
    });
    expect(geometry.optionTop).toBeGreaterThanOrEqual(geometry.listTop - 1);
    expect(geometry.optionBottom).toBeLessThanOrEqual(geometry.listBottom + 1);
    expect(geometry.listTop).toBeGreaterThanOrEqual(geometry.menuTop - 1);
    expect(geometry.listBottom).toBeLessThanOrEqual(geometry.menuBottom + 1);
    expect(geometry.menuTop).toBeGreaterThanOrEqual(0);
    expect(geometry.menuBottom).toBeLessThanOrEqual(geometry.viewportHeight);
    await claude.click();
    await expect(form.getByRole("button", { name: "claude-code/sonnet" })).toBeVisible();
    await form.getByRole("button", { name: "claude-code/sonnet" }).click();
    await search.fill("fixture-14");
    await expect(options.getByRole("option").filter({ hasText: "google/fixture-14" })).toHaveCount(1);
    await expect(options.getByRole("option").filter({ hasText: "claude-code/sonnet" })).toHaveCount(0);
    await search.fill("no-such-model-xyz");
    await expect(options.getByText("No matching models")).toBeVisible();
    expect(f.violations).toEqual([]);
    expect(f.requests).toEqual([]);
  } finally { await f.close(); }
});
