import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import { workflowHarness } from "./workflow-implicit-goals-harness";

for (const [name, width, height] of [["desktop", 1440, 900], ["narrow", 375, 812]] as const) {
  test(`${name}: original transparent wordmark fits sidebar`, async ({ page }) => {
    test.setTimeout(90_000);
    const f = await workflowHarness();
    try {
      await page.setViewportSize({ width, height });
      await f.open(page);
      const header = page.locator(".sidebar .brand");
      const logo = header.getByRole("img", { name: "Synapse", exact: true });
      await expect(logo).toHaveCount(1);
      await expect(logo).toBeVisible();
      await expect(logo).toHaveAttribute("src", "/synapse-logo.png");
      await expect(header.locator("svg, h1, .brand-sub-title")).toHaveCount(0);
      await expect(header).not.toContainText("Mecha-Developer");
      await expect(header).not.toContainText("AI AGENTS FOR REAL BUILDERS");
      await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth)).toBe(2172);
      const response = await page.request.get(new URL("/synapse-logo.png", page.url()).href);
      expect(response.status()).toBe(200);
      expect(response.headers()["content-type"]).toContain("image/png");
      // Pin the supplied original bytes, not a re-encoded or flattened substitute.
      expect(createHash("sha256").update(await response.body()).digest("hex"))
        .toBe("7f1fb149e05bb5bdaddf11367150629d1bc9d3425e025101934de0a6a9faed2b");
      const actual = await logo.evaluate((img: HTMLImageElement) => {
        const box = img.getBoundingClientRect();
        const header = img.parentElement!;
        const parent = header.getBoundingClientRect();
        const sidebar = img.closest(".sidebar")!.getBoundingClientRect();
        const styles = [img, header].map(el => {
          const s = getComputedStyle(el);
          return { filter: s.filter, background: s.backgroundColor, backgroundImage: s.backgroundImage, shadow: s.boxShadow };
        });
        // Inspect decoded alpha numerically only. No screenshot or image output.
        const canvas = document.createElement("canvas");
        canvas.width = img.naturalWidth;
        canvas.height = img.naturalHeight;
        const ctx = canvas.getContext("2d")!;
        ctx.drawImage(img, 0, 0);
        const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
        let minAlpha = 255, maxAlpha = 0;
        for (let i = 3; i < pixels.length; i += 4) {
          minAlpha = Math.min(minAlpha, pixels[i]!);
          maxAlpha = Math.max(maxAlpha, pixels[i]!);
        }
        return { width: box.width, height: box.height, left: box.left, right: box.right,
          parentWidth: parent.width, sidebarLeft: sidebar.left, sidebarRight: sidebar.right,
          naturalHeight: img.naturalHeight, styles, minAlpha, maxAlpha };
      });
      expect(actual.naturalHeight).toBe(724);
      expect(actual.width).toBeGreaterThan(0);
      expect(actual.width).toBeCloseTo(actual.parentWidth, 0);
      expect(actual.width / actual.height).toBeCloseTo(3, 2);
      expect(actual.left).toBeGreaterThanOrEqual(Math.max(0, actual.sidebarLeft));
      expect(actual.right).toBeLessThanOrEqual(Math.min(width, actual.sidebarRight));
      expect(actual.minAlpha).toBe(0);
      expect(actual.maxAlpha).toBeGreaterThan(0);
      for (const style of actual.styles) {
        expect(style).toEqual({ filter: "none", background: "rgba(0, 0, 0, 0)", backgroundImage: "none", shadow: "none" });
      }
      await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
      expect(f.violations).toEqual([]);
      expect(f.requests).toHaveLength(0);
    } finally { await f.close(); }
  });
}
