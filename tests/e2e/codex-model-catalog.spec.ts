import { test, expect, type Page } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, mkdir, writeFile, readFile, chmod, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { randomUUID } from "node:crypto";
import { allocatePort, serveFrontendDist, startRouterProxy } from "./harness";

const repo = resolve(import.meta.dirname, "../..");
let buildRoot: string;
let binary: string;

test.beforeAll(async () => {
  test.setTimeout(90_000);
  buildRoot = await mkdtemp(join(tmpdir(), "mecha-codex-build-"));
  binary = join(buildRoot, "router");
  await new Promise<void>((resolve, reject) => {
    const child = spawn("go", ["build", "-tags", "embedded_restart_test_only", "-o", binary, "./cmd/mailbox-router"], { cwd: join(repo, "router-proxy") });
    let diagnostics = "";
    child.stderr.on("data", data => { diagnostics += data; });
    child.on("error", reject);
    child.on("close", code => code === 0 ? resolve() : reject(new Error(diagnostics)));
  });
});
test.afterAll(async () => { if (buildRoot) await rm(buildRoot, { recursive: true, force: true }); });

async function stop(child: ChildProcess) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise<void>(resolve => child.once("exit", () => resolve()));
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 3000);
  await exited;
  clearTimeout(timer);
}

async function environment(signedIn: boolean) {
  const root = await mkdtemp(join(tmpdir(), "mecha-codex-catalog-"));
  await chmod(root, 0o700);
  const paths = {
    home: join(root, "managed"), agent: join(root, "pi-agent"), sessions: join(root, "managed/sessions"),
    project: join(root, "project"), credential: join(root, "private/codex.json"),
  };
  for (const path of [paths.home, paths.agent, paths.sessions, paths.project, join(root, "private")]) {
    await mkdir(path, { recursive: true, mode: 0o700 });
  }
  if (signedIn) await writeFile(paths.credential, JSON.stringify({ access_token: "offline-access", refresh_token: "offline-refresh", account_id: "offline-account", expires_at: new Date(Date.now() + 3600_000).toISOString() }), { mode: 0o600 });
  const pi = join(root, "pi");
  await writeFile(pi, `#!${process.execPath}\nprocess.exit(1);\n`, { mode: 0o700 });
  const requests: Array<{ model: string }> = [];
  const mock = createServer((req, res) => {
    let body = "";
    req.on("data", data => { body += data; });
    req.on("end", () => {
      requests.push(JSON.parse(body));
      if (req.url !== "/codex/responses") { res.writeHead(404).end(); return; }
      const events = [
        { type: "response.created", response: { id: "offline-response" } },
        { type: "response.output_item.added", output_index: 0, item: { id: "message", type: "message", role: "assistant", content: [] } },
        { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
        { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: "catalog launch verified" },
        { type: "response.output_text.done", output_index: 0, content_index: 0, text: "catalog launch verified" },
        { type: "response.completed", response: { id: "offline-response", status: "completed" } },
      ];
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
    });
  });
  await new Promise<void>(resolve => mock.listen(0, "127.0.0.1", resolve));
  const address = mock.address();
  if (!address || typeof address === "string") throw new Error("mock did not bind");
  const port = await allocatePort();
  const child = spawn(binary, [
    "--database", join(root, "router.sqlite"), "--host", "127.0.0.1", "--port", String(port),
    "--managed=true", "--managed-quiet-recipient", "supervisor", "--managed-pi", pi, "--managed-home", paths.home,
    "--managed-pi-agent-dir", paths.agent, "--managed-session-root", paths.sessions,
    "--managed-cwd-roots", paths.project, "--managed-runtime-root", repo,
    "--managed-manifest", join(repo, "router/contract/managed-tools-v1.json"),
    "--managed-mecha-rpc", join(repo, "pi-extensions/mecha-rpc/index.ts"),
    "--managed-pi-subagents", resolve(repo, "../pi-packages/packages/pi-subagents/src/index.ts"),
    "--managed-auth-helper", join(repo, "auth-helper/index.mjs"), "--codex-credential-path", paths.credential,
  ], { cwd: repo, env: {
    PATH: process.env.PATH, HOME: root, EMBEDDED_RESTART_FAKE_ENABLE: "1",
    EMBEDDED_RESTART_FAKE_ENDPOINT: `http://127.0.0.1:${address.port}`,
  }, stdio: ["ignore", "pipe", "pipe"] });
  let diagnostics = "";
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let frontend: Awaited<ReturnType<typeof serveFrontendDist>> | undefined;
  try {
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`router startup timed out: ${diagnostics}`)), 12000);
      child.stdout!.on("data", data => { diagnostics += data; if (diagnostics.includes("MECHA_V2_ROUTER_READY")) { clearTimeout(timer); resolve(); } });
      child.stderr!.on("data", data => { diagnostics += data; });
      child.once("error", error => { clearTimeout(timer); reject(error); });
      child.once("exit", () => { clearTimeout(timer); reject(new Error(diagnostics)); });
    });
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: port });
    frontend = await serveFrontendDist(join(repo, "web/router-ui-react/dist"));
    return {
      root, paths, requests, url: proxy.url, frontend: frontend.url,
      close: async () => {
        await stop(child);
        await proxy!.close();
        await frontend!.close();
        await new Promise<void>(resolve => mock.close(() => resolve()));
        await rm(root, { recursive: true, force: true });
      },
    };
  } catch (error) {
    await stop(child);
    await proxy?.close();
    await frontend?.close();
    await new Promise<void>(resolve => mock.close(() => resolve()));
    await rm(root, { recursive: true, force: true });
    throw error;
  }
}

async function launch(page: Page, env: Awaited<ReturnType<typeof environment>>, id: string, provider = "openai-codex") {
  return page.evaluate(async ({ url, cwd, id, provider, requestID }) => {
    const response = await fetch(`${url}/v1/managed-agents`, { method: "POST", credentials: "omit",
      headers: { "Content-Type": "application/json", "X-Request-ID": requestID },
      body: JSON.stringify({ name: `test-${requestID.slice(0, 8)}`, cwd, runtime: "embedded", model: { provider, id }, initial_instruction: "Say ready." }),
    });
    return { status: response.status, body: await response.json() };
  }, { url: env.url, cwd: env.paths.project, id, provider, requestID: randomUUID() });
}

for (const [id, title] of [
  ["gpt-6.1-sol", "launches advertised gpt-6.1-sol on a fresh install using local mock inference"],
  ["gpt-6-sol", "retains built-in Codex model catalog and embedded launch"],
]) {
  test(title!, async ({ page }) => {
    const env = await environment(true);
    try {
      await page.goto(env.frontend);
      const catalog = await page.evaluate(url => fetch(`${url}/v1/models`, { credentials: "omit" }).then(r => r.json()), env.url);
      expect(catalog.models).toContainEqual(expect.objectContaining({ provider: "openai-codex", id, available: true }));
      const result = await launch(page, env, id!);
      expect(result.status, JSON.stringify(result.body)).toBe(202);
      expect(result.body.agent.model).toMatchObject({ provider: "openai-codex", id });
      await expect.poll(() => env.requests.map(r => r.model)).toContain(id);
      await expect.poll(async () => {
        const response = await fetch(`${env.url}/v1/conversation?agent_id=${result.body.agent.agent_id}&limit=30`);
        const data = await response.json();
        return data.events.some((event: { role?: string; content?: string }) => event.role === "assistant" && event.content?.includes("catalog launch verified"));
      }).toBe(true);
      await expect(readFile(join(env.paths.agent, "models-store.json"))).rejects.toThrow();
      expect((await readFile(join(env.root, "router.sqlite"))).subarray(0, 16).toString()).toBe("SQLite format 3\0");
    } finally { await env.close(); }
  });
}

test("does not advertise or launch Codex model when signed out", async ({ page }) => {
  const env = await environment(false);
  try {
    await page.goto(env.frontend);
    const catalog = await page.evaluate(url => fetch(`${url}/v1/models`, { credentials: "omit" }).then(r => r.json()), env.url);
    expect(catalog.models.some((model: { provider: string; available: boolean }) => model.provider === "openai-codex" && model.available)).toBe(false);
    const result = await launch(page, env, "gpt-6.1-sol");
    expect(result.body.error.code).toBe("MODEL_UNAVAILABLE");
    expect(env.requests).toEqual([]);
  } finally { await env.close(); }
});

test("rejects unknown and cross-provider explicit selections without transport calls", async ({ page }) => {
  const env = await environment(true);
  try {
    await page.goto(env.frontend);
    for (const [provider, id] of [["openai-codex", "unknown-fixture-model"], ["openai", "gpt-6.1-sol"]]) {
      const result = await launch(page, env, id!, provider!);
      expect(result.body.error.code).toBe("MODEL_UNAVAILABLE");
    }
    expect(env.requests).toEqual([]);
  } finally { await env.close(); }
});
