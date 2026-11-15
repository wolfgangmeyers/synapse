import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { providerFixture, selections } from "./antigravity-provider-fixture";

// ETS-001: these are direct provider calls, not multi_tool_use.parallel.
test("Google and Vertex advertised read pagination executes at the provider boundary", async ({ page }) => {
  test.setTimeout(180_000);
  const f = await providerFixture();
  try {
    await writeFile(join(f.work, "pagination.txt"), "first\nsecond\nthird\n");
    await f.open(page);
    for (const model of selections.filter(m => m.provider === "google" || m.provider === "google-vertex")) {
      for (const [variant, pagination] of [["omitted", {}], ["null", { offset: null, limit: null }], ["integer", { offset: 2, limit: 1 }]] as const) {
        const name = `schema-${model.provider}-${variant}`;
        const answer = `completed ${name}`;
        const start = f.turns.length;
        f.replies.push({ tool: "read", args: { path: "pagination.txt", ...pagination } }, { answer });
        await f.api("/v1/managed-agents", { name, cwd: f.work, runtime: "embedded", model, initial_instruction: "Read pagination.txt" });
        await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
        await page.locator(`[data-agent-id="${name}"]`).click();
        await expect(page.getByText(answer, { exact: true })).toBeVisible({ timeout: 20_000 });
        expect(f.turns.slice(start)).toHaveLength(2);
        const [first, second] = f.turns.slice(start);
        expect(first.path).toContain(model.provider === "google" ? "/gemini/" : "/vertex/");
        const declarations = (first.body.tools as any[]).flatMap(tool => tool.functionDeclarations ?? []);
        const read = declarations.find((tool: any) => tool.name === "read");
        expect(read).toBeDefined();
        for (const field of ["offset", "limit"]) {
          expect(read.parameters.properties[field]).toMatchObject({ type: expect.arrayContaining(["integer", "null"]) });
        }
        const wire = JSON.stringify(second.body);
        expect(wire).toContain("functionResponse");
        expect(wire).toContain(variant === "integer" ? "second\\n" : "first\\nsecond\\nthird\\n");
      }
    }
    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
    expect(f.frames.length).toBeGreaterThan(0);
  } finally { await f.close(); }
});
