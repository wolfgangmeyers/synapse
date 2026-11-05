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
    // Exercise a narrow, bordered and scrolled native dialog, not just the viewport.
    await form.evaluate(el => {
      const dialog = el as HTMLDialogElement;
      dialog.style.width = "300px";
      dialog.style.border = "7px solid transparent";
      const spacer = document.createElement("div");
      spacer.style.height = "400px";
      spacer.setAttribute("aria-hidden", "true");
      dialog.append(spacer);
      dialog.scrollTop = 45;
    });
    expect(await form.evaluate(el => el.scrollTop)).toBeGreaterThan(0);
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
    const bounds = await claude.evaluate(el => {
      const dialog = el.closest("dialog")!.getBoundingClientRect();
      const menu = el.closest<HTMLElement>(".model-menu")!.getBoundingClientRect();
      const label = el.querySelector("strong")!.getBoundingClientRect();
      const detail = el.querySelector("small")!.getBoundingClientRect();
      return { dialogTop: dialog.top, dialogBottom: dialog.bottom, menuTop: menu.top, menuBottom: menu.bottom, labelHeight: label.height, detailHeight: detail.height };
    });
    expect(bounds.menuTop).toBeGreaterThanOrEqual(bounds.dialogTop - 1);
    expect(bounds.menuBottom).toBeLessThanOrEqual(bounds.dialogBottom + 1);
    expect(bounds.labelHeight).toBeGreaterThan(5);
    expect(bounds.detailHeight).toBeGreaterThan(5);
    expect((await claude.locator("strong").innerText()).trim().length).toBeGreaterThan(0);
    await expect(claude.locator("small")).toHaveText("claude-code/sonnet");
    await expect.poll(() => claude.evaluate(el => {
      const dialog = el.closest("dialog")!;
      const d = dialog.getBoundingClientRect();
      const menu = el.closest<HTMLElement>(".model-menu")!.getBoundingClientRect();
      return menu.left >= Math.max(0, d.left + dialog.clientLeft) - 1 && menu.right <= Math.min(innerWidth, d.left + dialog.clientLeft + dialog.clientWidth) + 1;
    })).toBe(true);
    await expect(claude.locator("strong")).toBeVisible();
    await expect(claude.locator("small")).toBeVisible();
    await claude.click();
    await expect(form.getByRole("button", { name: "claude-code/sonnet" })).toBeVisible();
    await form.getByRole("button", { name: "claude-code/sonnet" }).click();
    await form.evaluate(el => { el.scrollTop += 25; });
    await expect.poll(async () => form.evaluate(el => {
      const dialog = el as HTMLDialogElement;
      const d = dialog.getBoundingClientRect();
      const m = dialog.querySelector(".model-menu")!.getBoundingClientRect();
      return m.top >= Math.max(0, d.top + dialog.clientTop) - 1 && m.bottom <= Math.min(innerHeight, d.top + dialog.clientTop + dialog.clientHeight) + 1;
    })).toBe(true);
    await search.fill("fixture-14");
    const fixture = options.getByRole("option").filter({ hasText: "google/fixture-14" });
    await expect(fixture).toHaveCount(1);
    await expect(fixture.locator("strong")).toHaveText("Fixture 14 (google)");
    await expect(fixture.locator("small")).toHaveText("google/fixture-14");
    await expect(options.getByRole("option").filter({ hasText: "claude-code/sonnet" })).toHaveCount(0);
    await search.fill("no-such-model-xyz");
    await expect(options.getByText("No matching models")).toBeVisible();
    // Force a visible above placement, then shrink the results and keep the bottom anchored.
    const trigger = form.getByRole("button", { name: "claude-code/sonnet" });
    await trigger.evaluate(el => {
      const dialog = el.closest("dialog")!;
      const bounds = dialog.getBoundingClientRect();
      const rect = el.getBoundingClientRect();
      el.style.transform = `translateY(${Math.min(innerHeight - 35, bounds.bottom - dialog.clientTop - 25) - rect.bottom}px)`;
      window.dispatchEvent(new Event("scroll"));
    });
    await search.fill("");
    const anchorGap = () => form.evaluate(el => {
      const menu = el.querySelector(".model-menu")!.getBoundingClientRect();
      const button = el.querySelector<HTMLButtonElement>('[aria-expanded="true"]')!.getBoundingClientRect();
      return button.top - menu.bottom;
    });
    await expect.poll(anchorGap).toBeCloseTo(8, 0);
    await search.fill("fixture-14");
    await expect.poll(anchorGap).toBeCloseTo(8, 0);
    // A moved trigger is tracked; once fully outside the visible dialog, close safely.
    await form.evaluate(el => { el.scrollTop += 20; });
    await expect.poll(anchorGap).toBeCloseTo(8, 0);
    await form.evaluate(el => { el.scrollTop = el.scrollHeight; });
    await expect(search).toHaveCount(0);
    expect(f.violations).toEqual([]);
    expect(f.requests).toEqual([]);
  } finally { await f.close(); }
});
