import { test, expect } from "@playwright/test";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("conversation links collect shared links and support add, remove and clear", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  try {
    f.replies.push({ answer: "Ready" }); // the workflow start turn
    await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Ready").first()).toBeVisible();
    f.replies.push({ answer: "Read [Design Doc](https://example.com/design) and https://example.org/some/plain/link." });
    await userTurn(page, "Please look at https://mine.example/outgoing?x=1");
    // Both the outgoing and incoming links are counted on the header button.
    await expect(page.getByRole("button", { name: "Links (3)" })).toBeVisible();
    await page.getByRole("button", { name: "Links (3)" }).click();
    const dialog = page.locator("dialog.links-dialog[open]");
    const items = dialog.locator(".links-item");
    await expect(items).toHaveCount(3);
    // Markdown links show their label; plain URLs show a host/path preview; both directions are collected.
    await expect(dialog.getByRole("link", { name: /Design Doc/ })).toHaveAttribute("href", "https://example.com/design");
    await expect(dialog.getByText("example.org/some/plain/link", { exact: true })).toBeVisible();
    await expect(dialog.getByText("mine.example/outgoing?x=1", { exact: true })).toBeVisible();
    // The add form is a separate view, never shown alongside the list.
    await expect(dialog.getByLabel("Link URL")).toHaveCount(0);

    await dialog.getByRole("button", { name: "+ Add link" }).click();
    await expect(dialog.locator(".links-item")).toHaveCount(0);
    await dialog.getByLabel("Link URL").fill("not a url");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(dialog.getByRole("alert")).toContainText("valid http(s) URL");
    await dialog.getByLabel("Link URL").fill("https://manual.example.net/page");
    await dialog.getByLabel("Link label").fill("Manual Page");
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(items).toHaveCount(4);
    await expect(dialog.getByRole("link", { name: /Manual Page/ })).toBeVisible();

    await dialog.getByRole("button", { name: "Remove link Manual Page" }).click();
    await expect(items).toHaveCount(3);
    await dialog.getByRole("button", { name: "Clear all" }).click();
    await expect(dialog.getByText("No links yet.")).toBeVisible();
    await dialog.getByRole("button", { name: "Close dialog" }).click();
    await expect(page.getByRole("button", { name: "Links", exact: true })).toBeVisible();
    expect(f.violations).toEqual([]);
  } finally {
    await f.close();
  }
});
