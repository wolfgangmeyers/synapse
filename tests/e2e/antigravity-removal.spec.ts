import { test, expect } from "@playwright/test";
import { join, resolve } from "node:path";
import { readFile, writeFile } from "node:fs/promises";
import { DatabaseSync } from "node:sqlite";
import { startIsolatedRouter, startRouterProxy, serveFrontendDist } from "./harness";

const repoRoot = resolve(import.meta.dirname, "../..");

// AGR-001/002/007. Real browser, cross-origin REST, Go proxy/router, helper
// process and SQLite. The helper is deterministic; no model requests occur.
test("refreshes a legacy snapshot to four providers and rejects obsolete selections", async ({ page }) => {
  test.setTimeout(90_000);
  const router = await startIsolatedRouter({ repoRoot, managed: true });
  const proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
  const frontend = await serveFrontendDist(join(repoRoot, "web/router-ui-react/dist"));
  try {
    const authPath = join(router.root, "managed-home/.pi/agent/auth.json");
    const credentials = JSON.stringify({ antigravity: { type: "oauth", access: "inert", refresh: "inert" } });
    await writeFile(authPath, credentials, { mode: 0o600 });
    const db = new DatabaseSync(router.databasePath);
    db.exec(`INSERT OR REPLACE INTO managed_provider_status(provider,label,authenticated,status,updated_at) VALUES('antigravity','Google Antigravity',1,'authenticated','2026-01-01T00:00:00Z');
      INSERT OR REPLACE INTO managed_provider_models(provider,model_id,name,available,updated_at) VALUES('antigravity','gemini-3.8-flash','Legacy',1,'2026-01-01T00:00:00Z');`);
    const revision = Number(db.prepare("SELECT auth_revision FROM managed_settings").get()!.auth_revision);
    db.close();
    // Block nonlocal browser requests, including incidental icon CDN requests.
    await page.route("**/*", route => {
      const url = new URL(route.request().url());
      return ["127.0.0.1", "localhost"].includes(url.hostname) ? route.continue() : route.abort();
    });
    await page.goto(frontend.url);
    const snapshot = await page.evaluate(async url => {
      const response = await fetch(url + "/v1/provider-auth", { credentials: "omit" });
      return { status: response.status, body: await response.json() };
    }, proxy.url);
    expect(snapshot.status).toBe(200);
    expect(snapshot.body.providers.map((p: { provider: string }) => p.provider).sort()).toEqual(["cursor", "google", "google-vertex", "openai-codex"]);
    const catalog = await page.evaluate(async url => (await fetch(url + "/v1/models", { credentials: "omit" })).json(), proxy.url);
    expect(catalog.models.some((m: { provider: string }) => m.provider === "antigravity")).toBe(false);
    for (const operation of ["login", "logout"]) {
      const status = await page.evaluate(async ({ url, operation }) => (await fetch(url + `/v1/provider-auth/antigravity/${operation}`, { method: "POST", credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() }, body: "{}" })).status, { url: proxy.url, operation });
      expect(status).toBeGreaterThanOrEqual(400);
    }
    const rejected = await page.evaluate(async url => (await fetch(url + "/v1/settings/default-model", { method: "PUT", credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() }, body: JSON.stringify({ model: { provider: "antigravity", id: "gemini-3.8-flash" } }) })).status, proxy.url);
    expect(rejected).toBeGreaterThanOrEqual(400);
    expect(await readFile(authPath, "utf8")).toBe(credentials);
    const refreshed = new DatabaseSync(router.databasePath);
    try {
      expect(Number(refreshed.prepare("SELECT auth_revision FROM managed_settings").get()!.auth_revision)).toBeGreaterThan(revision);
      for (const table of ["managed_provider_status", "managed_provider_models"]) {
        expect(Number(refreshed.prepare(`SELECT count(*) AS count FROM ${table} WHERE provider='antigravity'`).get()!.count)).toBe(0);
      }
      expect(Number(refreshed.prepare("SELECT count(*) AS count FROM managed_provider_status").get()!.count)).toBe(4);
    } finally { refreshed.close(); }
  } finally { await proxy.close(); await router.close(); await frontend.close(); }
});

// Standalone provider boundary fixture; deliberately independent of workflow E2E.
import { providerFixture, selections } from "./antigravity-provider-fixture";

test("AGR-003 routes Gemini Vertex and Codex full tool turns to browser WS results", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await providerFixture();
  try {
    await f.open(page);
    for (const [i, model] of selections.entries()) {
      const name = `agr-turn-${i}`;
      const answer = `AGR ${model.provider} real tool complete`;
      const start = f.turns.length;
      f.replies.push({ tool: "read", args: { path: "note.txt" } }, { answer });
      await f.api("/v1/managed-agents", { name, cwd: f.work, runtime: "embedded", model, initial_instruction: "Read note.txt then answer" });
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      await expect(page.getByText(answer, { exact: true })).toBeVisible({ timeout: 15000 });
      await expect.poll(() => f.frames.some(text => { const frame = JSON.parse(text); return frame.type === "history_entry" && frame.entry?.agent_id === name && frame.entry.message_content === answer; })).toBe(true);
      expect(f.turns.slice(start)).toHaveLength(2);
      const [first, second] = f.turns.slice(start);
      const wire = JSON.stringify(second.body);
      expect(wire).toContain("AGR real tool evidence");
      if (model.provider === "openai-codex") {
        expect(first.path).toBe("/codex/responses");
        expect(first.body).toMatchObject({ model: model.id, stream: true });
        expect(wire).toContain("function_call_output");
      } else {
        expect(first.path).toContain(model.provider === "google" ? "/gemini/v1beta/models/" : "/vertex/v1/projects/agr-project/locations/global/");
        expect(first.path).toContain(model.id + ":streamGenerateContent");
        expect(JSON.stringify(first.body.tools)).toContain("functionDeclarations");
        expect(wire).toContain("functionResponse");
      }
      expect(f.rows("SELECT selected_model_provider AS provider, selected_model_id AS id FROM managed_agents WHERE agent_id=?", name)).toEqual([model]);
    }
    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("AGR-004 inherits Google Vertex and Codex parent selections through actual child execution", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await providerFixture();
  try {
    await f.open(page);
    for (const [i, model] of selections.entries()) {
      const name = `agr-parent-${i}`;
      const start = f.turns.length;
      // Parent and child can race after spawn. Completion mailbox wakes get
      // a separate deterministic reply in the fixture. Both queued terminal responses
      // are identical; selection and tool-result assertions distinguish them.
      f.replies.push({ tool: "subagent", args: { prompt: "Answer AGR child complete", description: "AGR child", max_turns: 3 } }, { answer: "AGR child complete" }, { answer: "AGR child complete" });
      await f.api("/v1/managed-agents", { name, cwd: f.work, runtime: "embedded", model, initial_instruction: "Launch a child" });
      await expect.poll(() => f.rows("SELECT a.selected_model_provider AS provider, a.selected_model_id AS id, s.status FROM managed_agents a JOIN managed_subagents s ON a.agent_id=s.agent_id WHERE a.parent_agent_id=?", name), { timeout: 15000 }).toEqual([{ ...model, status: "completed" }]);
      await expect.poll(() => f.turns.length).toBe(start + 3);
      // Completion notification persistence is independent of whether the parent
      // has consumed its queued mailbox notice in another model turn.
      await expect.poll(() => f.rows("SELECT body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", name)).toEqual([{ body: expect.stringContaining("finished with status completed: AGR child complete") }]);
      const calls = f.turns.slice(start);
      for (const call of calls) {
        if (model.provider === "openai-codex") expect(call.body.model).toBe(model.id);
        else expect(call.path).toContain(`${model.id}:streamGenerateContent`);
        expect(call.path).toContain(model.provider === "google" ? "/gemini/" : model.provider === "google-vertex" ? "/vertex/" : "/codex/");
      }
    }
    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test("AGR-001 offers only supported provider methods and models in actual settings and chooser", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await providerFixture(true);
  try {
    await f.open(page);
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
    const settings = page.getByRole("dialog", { name: "Provider settings", exact: true });
    await expect(settings.getByText("ChatGPT", { exact: true })).toBeVisible();
    await expect(settings.getByText("Google", { exact: true })).toBeVisible();
    await expect(settings).not.toContainText(/antigravity/i);
    // The hidden native select and visible custom list must agree.
    const method = settings.getByLabel("Google authentication method", { exact: true });
    await expect(method.locator("option")).toHaveText(["API key", "Vertex AI ADC"]);
    await settings.getByRole("button", { name: "API key", exact: true }).click();
    const methods = settings.getByRole("listbox", { name: "Google authentication method" });
    await expect(methods.getByRole("option")).toHaveText(["API key", "Vertex AI ADC"]);
    await methods.getByRole("option", { name: "Vertex AI ADC", exact: true }).click();
    await expect(method).toHaveValue("vertex_adc");
    await settings.getByRole("button", { name: "Vertex AI ADC", exact: true }).click();
    await methods.getByRole("option", { name: "API key", exact: true }).click();
  } finally { await f.close(); }
  // Signed-out Google exposes authentication methods but no usable models.
  // Use a separate authenticated catalog for settings and launch selections.
  const catalog = await providerFixture();
  try {
    await catalog.open(page);
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Provider settings", exact: true }).click();
    const settings = page.getByRole("dialog", { name: "Provider settings", exact: true });
    await settings.getByRole("button", { name: "Use ChatGPT default", exact: true }).click();
    const models = settings.getByRole("listbox", { name: "Default model options" });
    const expectedCatalog = (await catalog.api("/v1/models")).models;
    for (const provider of ["google", "google-vertex", "openai-codex"]) {
      await expect(models.getByRole("option").filter({ hasText: `${provider}:` })).toHaveCount(expectedCatalog.filter((m: { provider: string }) => m.provider === provider).length);
    }
    for (const model of selections) await expect(models.getByRole("option").filter({ hasText: `${model.provider}:${model.id}` })).toHaveCount(1);
    await expect(models).not.toContainText(/antigravity/i);
    await page.getByRole("button", { name: "Close provider settings", exact: true }).click();
    await page.getByRole("button", { name: "Managed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Launch Agent", exact: true }).click();
    const launch = page.getByRole("dialog", { name: "Launch managed agent", exact: true });
    await launch.getByRole("button", { name: "Use router default", exact: true }).click();
    const picker = page.getByRole("dialog", { name: "Choose agent model", exact: true });
    const chooser = picker.getByRole("listbox");
    // The launch picker caps unfiltered results at ten; search each exact
    // selection rather than assuming the entire Codex catalog is rendered.
    for (const model of selections) {
      await picker.getByRole("searchbox", { name: "Search available models" }).fill(`${model.provider}/${model.id}`);
      await expect(chooser.getByRole("option").filter({ hasText: `${model.provider}/${model.id}` })).toHaveCount(1);
    }
    await picker.getByRole("searchbox", { name: "Search available models" }).fill("antigravity");
    await expect(chooser.getByRole("option").filter({ hasText: /antigravity/i })).toHaveCount(0);
    await expect(chooser.getByText("No matching models", { exact: true })).toBeVisible();
  } finally { await catalog.close(); }
});

test("AGR-004 uses Vertex Flash only for a genuinely missing inherited selection and rejects a stale removed parent", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await providerFixture();
  try {
    await f.open(page);
    for (const removed of [false, true]) {
      const name = removed ? "agr-stale-parent" : "agr-missing-parent";
      const start = f.turns.length;
      // Alter only isolated persisted state at the provider boundary, after
      // ordinary parent admission but before the real subagent tool executes.
      // This reaches the otherwise inaccessible legacy/missing inheritance case.
      f.beforeReply(() => f.exec(`UPDATE managed_agents SET selected_model_provider='${removed ? "antigravity" : ""}',selected_model_id='${removed ? "gemini-3.8-flash" : ""}' WHERE agent_id='${name}'`));
      f.replies.push({ tool: "subagent", args: { prompt: "AGR fallback child", description: "Fallback", max_turns: 3 } }, { answer: "AGR settled" });
      if (!removed) f.replies.push({ answer: "AGR settled" });
      await f.api("/v1/managed-agents", { name, cwd: f.work, runtime: "embedded", model: selections[2], initial_instruction: "Spawn child" });
      await expect.poll(() => f.turns.length, { timeout: 15000 }).toBe(start + (removed ? 2 : 3));
      if (removed) {
        expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", name)).toEqual([]);
        expect(JSON.stringify(f.turns[start + 1].body)).toContain("provider is unavailable");
        expect(f.turns.slice(start).every(r => r.path === "/codex/responses")).toBe(true);
      } else {
        await expect.poll(() => f.rows("SELECT a.selected_model_provider AS provider,a.selected_model_id AS id,s.status FROM managed_agents a JOIN managed_subagents s ON a.agent_id=s.agent_id WHERE s.parent_agent_id=?", name)).toEqual([{ provider: "google-vertex", id: "gemini-3.8-flash", status: "completed" }]);
        expect(f.turns.slice(start).filter(r => r.path.startsWith("/vertex/")).map(r => r.path)).toEqual([expect.stringContaining("gemini-3.8-flash:streamGenerateContent")]);
      }
    }
    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

// Existing public child API has no explicit model selector: even supported
// selections are unsupported arguments. Keep that contract visible rather than
// inventing a test-only production API.
test("AGR-004 rejects explicit child model arguments including removed provider before execution", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await providerFixture();
  try {
    await f.open(page);
    for (const [i, model] of [...selections, { provider: "antigravity", id: "gemini-3.8-flash" }].entries()) {
      const name = `agr-explicit-${i}`;
      const start = f.turns.length;
      f.replies.push({ tool: "subagent", args: { prompt: "Child must not run", model } }, { answer: "AGR rejected selection" });
      await f.api("/v1/managed-agents", { name, cwd: f.work, runtime: "embedded", model: selections[2], initial_instruction: "Attempt explicit child" });
      await expect.poll(() => f.turns.length, { timeout: 15000 }).toBe(start + 2);
      expect(f.rows("SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?", name)).toEqual([]);
      expect(JSON.stringify(f.turns[start + 1].body)).toMatch(/unsupported argument|[Vv]alidation|[Aa]dditional propert/);
      expect(f.turns.slice(start).every(r => r.path === "/codex/responses")).toBe(true);
    }
    expect(f.replies).toHaveLength(0);
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
