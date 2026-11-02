import { test, expect } from "@playwright/test";
import { mkdir, writeFile, readFile, readdir, lstat } from "node:fs/promises";
import { join } from "node:path";
import { kbHarness, userTurn, model } from "./router-kb-harness";

const canonical = ".mecha-developer/kb";
const fallback = ".mecha/kb";
const sibling = ".unknown-kb-source/kb";
const roots = [canonical, fallback, sibling, "chosen-kb"];

type Selection = {
  name: string;
  dirs?: string[];
  files?: string[];
  env?: (home: string) => string;
  selected: string;
};

// Snapshot only KB fixture paths, not router/session files legitimately changed
// by a real turn. Include bytes, mode and mtime to detect migration or rewrites.
async function snapshot(home: string) {
  const entries: Record<string, unknown> = {};
  async function visit(relative: string): Promise<void> {
    const path = join(home, relative);
    let stat;
    try { stat = await lstat(path); }
    catch (error) {
      if (error.code === "ENOENT") { entries[relative] = null; return; }
      throw error;
    }
    entries[relative] = { mode: stat.mode, mtime: stat.mtimeMs,
      content: stat.isDirectory() ? null : await readFile(path, "utf8") };
    if (stat.isDirectory()) {
      for (const child of (await readdir(path)).sort()) await visit(join(relative, child));
    }
  }
  for (const root of roots) await visit(root);
  return entries;
}

const selections: Selection[] = [
  { name: "default absent roots", selected: canonical },
  { name: "blank environment", env: () => " \t ", selected: canonical },
  { name: "canonical precedes fallback and unknown sibling", dirs: [canonical, fallback, sibling], selected: canonical },
  { name: "supported fallback precedes unknown sibling", dirs: [fallback, sibling], selected: fallback },
  { name: "canonical file is not a directory", dirs: [fallback], files: [canonical], selected: fallback },
  { name: "fallback file is not a directory", files: [fallback], selected: canonical },
  { name: "explicit environment is trimmed and cleaned", dirs: [canonical, fallback, sibling, "chosen-kb"],
    env: home => `  ${home}/unused/../chosen-kb  `, selected: "chosen-kb" },
  { name: "explicit missing directory is selected", dirs: [canonical, fallback],
    env: home => join(home, "chosen-kb"), selected: "chosen-kb" },
  { name: "explicit arbitrary sibling is allowed", dirs: [canonical, fallback, sibling],
    env: home => join(home, sibling), selected: sibling },
];

async function exercise(selection: Selection, page: Parameters<typeof userTurn>[0]) {
  const f = await kbHarness(selection.env);
  try {
    const home = join(f.router.root, "managed-home");
    for (const dir of selection.dirs ?? []) {
      await mkdir(join(home, dir), { recursive: true });
      await writeFile(join(home, dir, "INDEX.md"), `# Index for ${dir}\n`);
      await writeFile(join(home, dir, "probe.md"), `KB evidence from ${dir}\n`);
    }
    for (const file of selection.files ?? []) {
      await mkdir(join(home, file, ".."), { recursive: true });
      await writeFile(join(home, file), "not a directory\n");
    }
    const before = await snapshot(home);
    await f.open(page);
    const agent = "kb-selection-worker";
    await f.api("/v1/managed-agents", { name: agent, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect(page.locator(`[data-agent-id="${agent}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${agent}"]`).click();
    f.replies.push({ tool: "kb_list", args: {} }, { tool: "kb_read", args: { topic: "probe" } }, { answer: "KB selection verified" });
    await userTurn(page, "Inspect the configured knowledge base");
    await expect.poll(() => f.requests.length).toBe(3);
    await expect(page.getByText("Inspect the configured knowledge base", { exact: true })).toBeVisible();
    const input = f.requests.at(-1)?.input;
    expect(Array.isArray(input)).toBe(true);
    if (!Array.isArray(input)) throw Error("missing provider tool outputs");
    const outputs = input.filter(item => item.type === "function_call_output").map(item => String(item.output));
    expect(outputs).toHaveLength(2);
    if (selection.dirs?.includes(selection.selected)) {
      expect(outputs[0]).toContain(`# Index for ${selection.selected}`);
      expect(outputs[1]).toContain(`KB evidence from ${selection.selected}`);
    } else {
      expect(outputs[0]).toContain(`Knowledge base directory ${join(home, selection.selected)} is empty or not found.`);
      expect(outputs[1]).toContain("Knowledge base file not found: probe");
    }
    for (const other of selection.dirs ?? []) {
      if (other !== selection.selected) expect(outputs.join("\n")).not.toContain(`KB evidence from ${other}`);
    }
    // Public input/answer traverses the browser and WS; SQLite is inspected
    // read-only, never seeded with tool receipts or fabricated conversation data.
    await expect.poll(() => f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("KB selection verified"))).toBe(true);
    expect(f.wsFrames.some(frame => frame.includes(agent))).toBe(true);
    expect(f.violations).toEqual([]);
    expect(f.replies).toHaveLength(0);
    expect(await snapshot(home)).toEqual(before);
  } finally { await f.close(); }
}

test.describe("Embedded KB root selection", () => {
  test.setTimeout(90_000);
  for (const selection of selections) {
    test(`SCN-CFG-009: ${selection.name} (real embedded KB tools)`, async ({ page }) => {
      await exercise(selection, page);
    });
  }
  test("SCN-CFG-010: unknown sibling ignored without filesystem changes", async ({ page }) => {
    await exercise({ name: "unknown sibling only", dirs: [sibling], selected: canonical }, page);
  });
});
