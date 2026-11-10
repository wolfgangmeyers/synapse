import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
import { execFileSync } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { startIsolatedRouter, startRouterProxy } from "./harness";

// MR-001: real browser, proxy, router and SQLite; only the provider is mocked.
test("default embedded launch persists an empty retired RPC extension and answers", async ({ page }) => {
  test.setTimeout(120_000);
  const repo = resolve(import.meta.dirname, "../..");
  const dir = await mkdtemp(join(tmpdir(), "empty-rpc-e2e-"));
  const server = createServer((req, res) => {
    req.resume();
    req.on("end", () => {
      const item = { type: "message", id: "msg_1", role: "assistant", status: "completed", content: [{ type: "output_text", text: "empty RPC launch works" }] };
      const events = [
        { type: "response.created", response: { id: "resp_1" } },
        { type: "response.output_item.added", output_index: 0, item: { ...item, content: [] } },
        { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
        { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: "empty RPC launch works" },
        { type: "response.output_text.done", output_index: 0, content_index: 0, text: "empty RPC launch works" },
        { type: "response.output_item.done", output_index: 0, item },
        { type: "response.completed", response: { id: "resp_1", status: "completed" } },
      ];
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
    });
  });
  let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
    const address = server.address();
    if (!address || typeof address === "string") throw Error("missing mock address");
    const binary = join(dir, "router"), wrapper = join(dir, "wrapper.py");
    execFileSync("go", ["build", "-tags=embedded_restart_test_only", "-o", binary, "./cmd/mailbox-router"], { cwd: join(repo, "router-proxy"), timeout: 90_000 });
    // The shared harness supplies the historical extension path. Remove that
    // argument pair so the real router receives its actual empty default.
    await writeFile(wrapper, `#!/usr/bin/python3\nimport os,sys\na=sys.argv[1:]\nif '--managed-mecha-rpc' in a:\n i=a.index('--managed-mecha-rpc')\n del a[i:i+2]\nos.execv(${JSON.stringify(binary)}, [${JSON.stringify(binary)}]+a)\n`, { mode: 0o700 });
    router = await startIsolatedRouter({ repoRoot: repo, managed: true, embedded: { endpoint: `http://127.0.0.1:${address.port}` }, routerExecutableFixture: wrapper });
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await page.goto(`${router.httpUrl}/v1/health`);
    const result = await page.evaluate(async ({ url, cwd }) => {
      const response = await fetch(`${url}/v1/managed-agents`, { method: "POST", credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() }, body: JSON.stringify({ name: "empty-rpc", cwd, runtime: "embedded", model: { provider: "openai-codex", id: "gpt-6.1-terra" }, initial_instruction: "Say hello" }) });
      return { status: response.status, body: await response.text() };
    }, { url: proxy.url, cwd: join(router.root, "work") });
    expect(result.status, result.body).toBe(202);
    const db = new DatabaseSync(router.databasePath, { readOnly: true });
    try {
      await expect.poll(() => db.prepare("SELECT mecha_rpc_extension FROM managed_launch_recipes WHERE agent_id='empty-rpc'").get()?.mecha_rpc_extension).toBe("");
      await expect.poll(() => JSON.stringify(db.prepare("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id='empty-rpc' AND kind='assistant'").all()), { timeout: 30_000 }).toContain("empty RPC launch works");
    } finally { db.close(); }
  } finally {
    await proxy?.close();
    await router?.close();
    server.closeAllConnections();
    await new Promise<void>(r => server.close(() => r()));
    await rm(dir, { recursive: true, force: true });
  }
});
