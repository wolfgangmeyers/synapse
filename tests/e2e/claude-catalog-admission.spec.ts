import { randomUUID } from "node:crypto";
import { writeFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test, expect } from "@playwright/test";
import { startIsolatedRouter, startRouterProxy } from "./harness";

// CC-001/002/010: backend slice only. No Claude turn/runtime parity claim.
test("Claude aliases stay selectable and save as default while signed-out launch is refused", async ({ page }) => {
  test.setTimeout(180_000);
  const repo = resolve(import.meta.dirname, "../..");
  const dir = await mkdtemp(join(tmpdir(), "claude-catalog-"));
  const script = join(dir, "signed-out.json");
  await writeFile(script, JSON.stringify({ auth: { loggedIn: false } }));
  const saved = { binary: process.env.MECHA_CLAUDE_BIN, script: process.env.FAKE_CLAUDE_SCRIPT };
  let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    process.env.MECHA_CLAUDE_BIN = join(repo, "tests/fixtures/fake-claude/claude.mjs");
    process.env.FAKE_CLAUDE_SCRIPT = script;
    router = await startIsolatedRouter({ repoRoot: repo, managed: true });
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await page.goto(`${proxy.url}/v1/health`);
    const catalog = await page.evaluate(async () => (await fetch("/v1/models", { credentials: "omit" })).json());
    expect(catalog.models.filter((m: { provider: string }) => m.provider === "claude-code").map((m: { id: string }) => m.id))
      .toEqual(["fable", "opus", "sonnet", "haiku"]);
    expect(catalog.default_model?.provider).not.toBe("claude-code");
    const outcomes = await page.evaluate(async ({ cwd, ids }) => {
      const call = async (path: string, method: string, body: object, id: string) => {
        const response = await fetch(path, { method, credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": id }, body: JSON.stringify(body) });
        return { status: response.status, body: await response.json() };
      };
      return [
        await call("/v1/managed-agents", "POST", { name: "claude-refused", cwd, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" }, worktree: true, worktree_branch: "must-not-create" }, ids[0]),
        await call("/v1/settings/default-model", "PUT", { model: { provider: "claude-code", id: "sonnet" } }, ids[1]),
      ];
    }, { cwd: join(router.root, "work"), ids: [randomUUID(), randomUUID()] });
    expect(outcomes[0].status).toBe(422);
    expect(outcomes[0].body.error.code).toBe("CLAUDE_UNAVAILABLE");
    expect(outcomes[0].body.error.message).toContain("host");
    expect(outcomes[1].status).toBe(200);
    const db = new DatabaseSync(router.databasePath, { readOnly: true });
    try {
      expect(db.prepare("SELECT agent_id FROM managed_agents WHERE agent_id='claude-refused'").all()).toEqual([]);
      expect(db.prepare("SELECT default_model_provider FROM managed_settings WHERE singleton_id=1").get()?.default_model_provider).toBe("claude-code");
    } finally { db.close(); }
  } finally {
    if (saved.binary === undefined) delete process.env.MECHA_CLAUDE_BIN; else process.env.MECHA_CLAUDE_BIN = saved.binary;
    if (saved.script === undefined) delete process.env.FAKE_CLAUDE_SCRIPT; else process.env.FAKE_CLAUDE_SCRIPT = saved.script;
    await proxy?.close();
    await router?.close();
    await rm(dir, { recursive: true, force: true });
  }
});
