import { expect, test } from "@playwright/test";
import { join } from "node:path";
import { serveFrontendDist } from "./harness";

test.describe("Web UI Oxanium typography", () => {
  let frontend: Awaited<ReturnType<typeof serveFrontendDist>>;

  test.beforeAll(async () => {
    frontend = await serveFrontendDist(join(process.cwd(), "web/router-ui-react/dist"));
  });

  test.afterAll(async () => {
    await frontend?.close();
  });

  test("uses the local Oxanium face while preserving monospace code", async ({ page }) => {
    const fontRequests: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("Oxanium-Regular.ttf")) fontRequests.push(request.url());
    });
    await page.goto(`${frontend.url}/`);
    await page.evaluate(() => document.fonts.ready);
    expect(fontRequests).toContain(`${frontend.url}/fonts/Oxanium-Regular.ttf`);
    const bodyFont = await page.locator("body").evaluate((el) => ({
      family: getComputedStyle(el).fontFamily,
      loaded: document.fonts.check('16px "Oxanium"'),
    }));
    expect(bodyFont.family).toContain("Oxanium");
    expect(bodyFont.loaded).toBe(true);
    const code = await page.evaluate(() => {
      const element = document.createElement("code");
      element.textContent = "const x = 1";
      document.body.append(element);
      const family = getComputedStyle(element).fontFamily;
      element.remove();
      return family;
    });
    expect(code).toMatch(/monospace/i);
    expect(await page.request.get(`${frontend.url}/fonts/Oxanium-OFL.txt`)).toBeOK();
  });

  test("falls back to system sans-serif when the font request fails", async ({ page }) => {
    await page.route("**/fonts/Oxanium-Regular.ttf", (route) => route.abort());
    await page.goto(`${frontend.url}/`);
    await page.evaluate(() => document.fonts.ready);
    const family = await page.locator("body").evaluate((el) => getComputedStyle(el).fontFamily);
    expect(family).toMatch(/Oxanium.*system-ui.*sans-serif/i);
    await expect(page.locator("body")).toBeVisible();
  });
});
