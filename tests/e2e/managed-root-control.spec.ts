import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { startIsolatedRouter, startRouterProxy } from "./harness";

const repo = resolve(import.meta.dirname, "../..");
const exec = promisify(execFile);

test("managed root operations dispatch, reconcile and deduplicate across real boundaries", async ({ page }) => {
  test.setTimeout(120_000);
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true, rootControlFixture: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    const url = proxy.url;
    const request = async (path: string, body?: object) => page.evaluate(async ({ url, path, body, requestId }) => {
      const res = await fetch(url + path, { method: body ? "POST" : "GET", credentials: "omit", headers: body ? { "content-type": "application/json", "x-request-id": requestId } : {}, body: body ? JSON.stringify(body) : undefined });
      return { status: res.status, body: await res.json() };
    }, { url, path, body, requestId: randomUUID() });
    const launch = await request("/v1/managed-agents", { name: "root-boundary", cwd: join(router.root, "work"), runtime: "rpc", model: { provider: "openai-codex", id: "gpt-5.6-terra" } });
    expect(launch.status, JSON.stringify(launch.body)).toBe(202);
    await expect.poll(async () => { const result = await request("/v1/managed-agents/root-boundary"); return JSON.stringify(result.body); }, { timeout: 20_000 }).toContain('"accepting_prompts":true');
    const db = async (query: string, args: string[] = []) => {
      const { stdout } = await exec(process.env.PYTHON || "python3", ["-c", "import sqlite3,sys,json; c=sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True); c.row_factory=sqlite3.Row; print(json.dumps([dict(x) for x in c.execute(sys.argv[2],sys.argv[3:])]))", router.databasePath, query, ...args]);
      return JSON.parse(stdout);
    };
    const log = async (name: string) => (await readFile(join(router.root, name), "utf8").catch(() => "")).trim().split("\n").filter(Boolean);
    const send = async (id: string, operation: string, args: object) => {
      const result = await request("/v1/messages", { id: randomUUID(), from: "web-ui", to: "root-boundary", body: `ROOT_CONTROL ${JSON.stringify({ id, operation, arguments: args })}`, created_at: new Date().toISOString(), source: { kind: "web_ui" } });
      expect(result.status, JSON.stringify(result.body)).toBe(201);
    };
    const rows = (id: string) => db("select request_id, state, result_status, result_content from managed_control_requests where request_id=?", [id]);
    const retired = randomUUID();
    await send(retired, "ops.state", { action: "query", task: "task" });
    await expect.poll(async () => (await rows(retired))[0]?.state, { timeout: 25_000, message: router.stderr() }).toBe("delivered");
    expect((await rows(retired))[0].result_status).toBe("error");
    const retiredResults = (await log("root-fixture.jsonl")).map(line => JSON.parse(line))
      .filter(event => event.kind === "result" && event.payload.request_id === retired);
    expect(retiredResults).toHaveLength(1);
    expect(retiredResults[0].payload).toMatchObject({ request_id: retired, status: "error", error: { code: "ROOT_OPERATION_UNAVAILABLE" } });
    expect(await log("python-spawns.log")).toHaveLength(0);

    const id = randomUUID(), args = { recipient: "root-test-recipient", body: "root dispatch evidence" };
    await send(id, "mail.send", args);
    await expect.poll(async () => (await rows(id))[0]?.state, { timeout: 25_000 }).toBe("delivered");
    expect((await rows(id))[0].result_status).toBe("ok");
    const spawns = await log("python-spawns.log");
    expect(spawns).toHaveLength(1);
    expect(spawns[0]).toContain(`managed_root_control.py execute --home ${join(router.root, "managed-home")}`);
    const ledger = JSON.parse(await readFile(join(router.root, "managed-home/root-control/requests.json"), "utf8"));
    expect(ledger.requests[id].result.status).toBe("ok");
    expect(await db("select sender, recipient, body from conversation_messages where body=?", [args.body])).toEqual([{ sender: "root-boundary", recipient: args.recipient, body: args.body }]);
    const events = (await log("root-fixture.jsonl")).map(line => JSON.parse(line));
    for (const requestId of [retired, id]) {
      expect(events.filter(event => event.kind === "result" && event.payload.request_id === requestId)).toHaveLength(1);
      expect(events.filter(event => event.kind === "reconcile" && event.envelope.request_id === requestId)).toHaveLength(1);
    }
    const barrier = randomUUID();
    await writeFile(join(router.root, "replay-request"), JSON.stringify({ id, barrier: { id: barrier, operation: "ops.state", arguments: { action: "query" } } }));
    await expect.poll(async () => (await log("root-fixture.jsonl")).map(line => JSON.parse(line)).filter(event => event.kind === "request" && event.details.request_id === id).length).toBe(2);
    await expect.poll(async () => (await rows(barrier))[0]?.state, { timeout: 20_000 }).toBe("delivered");
    expect(await rows(id)).toHaveLength(1);
    expect(await log("python-spawns.log")).toEqual(spawns);
    expect(await db("select body from conversation_messages where body=?", [args.body])).toHaveLength(1);
    expect(Object.keys(JSON.parse(await readFile(join(router.root, "managed-home/root-control/requests.json"), "utf8")).requests)).toEqual([id]);
  } catch (error) {
    console.error("router stderr", router.stderr(), "router log", await readFile(router.logPath, "utf8").catch(() => ""));
    throw error;
  } finally {
    try { await proxy?.close(); } finally { await router.close(); }
  }
});


test("proxy harness reaps its own process on close and startup failure", async () => {
  test.setTimeout(120_000);
  const alive = (pid: number) => { try { process.kill(pid, 0); return true; } catch { return false; } };
  const unrelated = await startRouterProxy({ repoRoot: repo, upstreamPort: 1 });
  try {
    const proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: 1 });
    const pid = proxy.process.pid!;
    expect(alive(pid)).toBe(true);
    await proxy.close();
    expect(alive(pid)).toBe(false);
    expect(alive(unrelated.process.pid!)).toBe(true);
    let failedPid = 0;
    await expect(startRouterProxy({ repoRoot: repo, upstreamPort: 0, onSpawn: child => { failedPid = child.pid!; } })).rejects.toThrow();
    expect(failedPid).toBeGreaterThan(0);
    expect(alive(failedPid)).toBe(false);
    expect(alive(unrelated.process.pid!)).toBe(true);
  } finally {
    await unrelated.close();
  }
});

test("saved Claude default bootstraps an omitted-model root without Pi providers", async ({ page }) => {
  test.setTimeout(120_000);
  const { mkdtemp, chmod, rm, copyFile } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { spawn } = await import("node:child_process");
  const dir = await mkdtemp(join(tmpdir(), "root-claude-default-"));
  const script = join(dir, "script.json"), record = join(dir, "record.jsonl"), helper = join(dir, "empty-providers.mjs");
  await writeFile(script, JSON.stringify({ turns_by_input: [{ contains: "Root bootstrap", steps: [{ text: "Root booted from saved default" }] }] }));
  await writeFile(helper, `#!${process.execPath}\nimport {createInterface} from 'node:readline';\ncreateInterface({input:process.stdin}).on('line', line => { if (JSON.parse(line).operation === 'models') console.log(JSON.stringify({providers:[],models:[]})); });\n`);
  await chmod(helper, 0o700);
  const previous = { bin: process.env.MECHA_CLAUDE_BIN, script: process.env.FAKE_CLAUDE_SCRIPT, record: process.env.FAKE_CLAUDE_RECORD };
  process.env.MECHA_CLAUDE_BIN = join(repo, "tests/fixtures/fake-claude/claude.mjs");
  process.env.FAKE_CLAUDE_SCRIPT = script;
  process.env.FAKE_CLAUDE_RECORD = record;
  let router: Awaited<ReturnType<typeof startIsolatedRouter>> | undefined;
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    router = await startIsolatedRouter({ repoRoot: repo, managed: true });
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await page.goto(`${proxy.url}/v1/health`);
    const call = (path: string, method = "GET", body?: object, id = randomUUID()) => page.evaluate(async ({ path, method, body, id }) => {
      const response = await fetch(path, { method, credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": id }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
      return { status: response.status, body: await response.json() };
    }, { path, method, body, id });
    const model = { provider: "claude-code", id: "sonnet" };
    const id = randomUUID();
    const saved = await call("/v1/settings/default-model", "PUT", { model }, id);
    expect(saved.status, JSON.stringify(saved.body)).toBe(200);
    expect(await call("/v1/settings/default-model", "PUT", { model }, id)).toEqual(saved);
    expect((await call("/v1/settings/default-model", "PUT", { model: { ...model, id: "opus" } }, id)).status).toBe(409);
    expect((await call("/v1/settings/default-model", "PUT", { model: { ...model, id: "invalid" } })).status).toBe(422);
    const args = [...router.process.spawnargs];
    await copyFile(join(repo, "router/contract/managed-tools-v1.json"), join(dir, "manifest.json"));
    await copyFile(join(repo, "pi-extensions/mecha-rpc/index.ts"), join(dir, "rpc.ts"));
    args[args.indexOf("--managed-runtime-root") + 1] = dir;
    args[args.indexOf("--managed-manifest") + 1] = join(dir, "manifest.json");
    args[args.indexOf("--managed-mecha-rpc") + 1] = join(dir, "rpc.ts");
    args[args.indexOf("--managed-auth-helper") + 1] = helper;
    args.push("--embedded-root-bootstrap", "--embedded-root-request-id", randomUUID(), "--embedded-root-name", "saved-root", "--embedded-root-cwd", join(router.root, "work"), "--embedded-root-initial-instruction", "Root bootstrap");
    await router.restart(async () => {
      // Remove the initial harness's synthetic provider snapshot before startup.
      await exec(process.env.PYTHON || "python3", ["-c", `import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute("delete from managed_provider_models"); c.execute("update managed_provider_status set authenticated=0,status='signed_out'"); c.commit()`, router!.databasePath]);
      const child = spawn(args[0], args.slice(1), { cwd: repo, env: process.env, stdio: ["ignore", "pipe", "pipe"] });
      let output = "";
      child.stdout.on("data", chunk => { output += chunk; });
      child.stderr.on("data", chunk => { output += chunk; });
      const closed = new Promise<void>(resolve => child.once("close", () => resolve()));
      try {
        await expect.poll(() => output.includes("MECHA_V2_ROUTER_READY") || child.exitCode !== null, { timeout: 30_000 }).toBe(true);
        expect(child.exitCode, output).toBeNull();
        const catalog = await call("/v1/models");
        expect(catalog.body.default_model).toMatchObject(model);
        expect(catalog.body.models.filter((m: { provider: string; available: boolean }) => m.provider !== "claude-code" && m.available)).toEqual([]);
        await expect.poll(async () => JSON.stringify((await call("/v1/managed-agents/saved-root")).body), { timeout: 30_000 }).toContain('"accepting_prompts":true');
        await expect.poll(async () => (await readFile(record, "utf8").catch(() => "")).includes('Root bootstrap'), { timeout: 30_000 }).toBe(true);
        const records = (await readFile(record, "utf8")).trim().split("\n").map(line => JSON.parse(line));
        const start = records.find(row => row.kind === "start" && row.argv.includes("-p"));
        expect(start.argv).toEqual(expect.arrayContaining(["--model", "sonnet", "--tools", "--strict-mcp-config"]));
        const db = async (sql: string) => JSON.parse((await exec(process.env.PYTHON || "python3", ["-c", "import sqlite3,sys,json; c=sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True); print(json.dumps(c.execute(sys.argv[2]).fetchall()))", router!.databasePath, sql])).stdout);
        expect(await db("select selected_model_provider, selected_model_id from managed_agents where agent_id='saved-root'")).toEqual([["claude-code", "sonnet"]]);
        await expect.poll(async () => JSON.stringify(await db("select cast(body as text) from embedded_entries where agent_id='saved-root' and kind='assistant'")), { timeout: 30_000 }).toContain("Root booted from saved default");
      } finally {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
        const escalation = setTimeout(() => child.kill("SIGKILL"), 2000);
        try { await closed; } finally { clearTimeout(escalation); }
      }
    });
    expect((await call("/v1/models")).body.default_model).toMatchObject(model);
    expect((await call("/v1/settings/default-model", "PUT", { model: null })).status).toBe(200);
    await router.restart();
    expect((await call("/v1/models")).body.default_model?.provider).not.toBe("claude-code");
  } finally {
    for (const [key, value] of Object.entries({ MECHA_CLAUDE_BIN: previous.bin, FAKE_CLAUDE_SCRIPT: previous.script, FAKE_CLAUDE_RECORD: previous.record })) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    try { await proxy?.close(); } finally {
      try { await router?.close(); } finally { await rm(dir, { recursive: true, force: true }); }
    }
  }
});

test("unavailable saved Claude default rejects omitted-model root bootstrap", async ({ page }) => {
  test.setTimeout(120_000);
  const router = await startIsolatedRouter({ repoRoot: repo, managed: true });
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  try {
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: router.port });
    await page.goto(`${proxy.url}/v1/health`);
    const saved = await page.evaluate(async () => {
      const r = await fetch('/v1/settings/default-model', { method: 'PUT', headers: { 'content-type': 'application/json', 'x-request-id': crypto.randomUUID() }, body: JSON.stringify({ model: { provider: 'claude-code', id: 'sonnet' } }) });
      return { status: r.status, body: await r.text() };
    });
    expect(saved.status, saved.body).toBe(200);
    const args = [...router.process.spawnargs];
    args.push('--embedded-root-bootstrap', '--embedded-root-request-id', randomUUID(), '--embedded-root-name', 'unavailable-root', '--embedded-root-cwd', join(router.root, 'work'), '--embedded-root-initial-instruction', 'Root bootstrap');
    await router.restart(async () => {
      let output = "";
      let failed = false;
      try {
        const result = await exec(args[0], args.slice(1), {
          cwd: repo,
          env: { ...process.env, MECHA_CLAUDE_BIN: join(router.root, "missing-claude") },
          timeout: 30_000, killSignal: "SIGKILL", maxBuffer: 1024 * 1024,
        });
        output = result.stdout + result.stderr;
      } catch (error) {
        const failure = error as NodeJS.ErrnoException & { killed?: boolean; stdout?: string; stderr?: string };
        output = (failure.stdout ?? "") + (failure.stderr ?? "");
        if (failure.killed || typeof failure.code !== "number") throw error;
        failed = true;
      }
      expect(failed, output).toBe(true);
      expect(output).toContain("Claude CLI unavailable");
      expect(output).toContain("install Claude");
      expect(output).toContain("sign in");
      expect(output).not.toContain("MECHA_V2_ROUTER_READY");
      const rows = await exec(process.env.PYTHON || "python3", ["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute(\"select count(*) from managed_agents where agent_id='unavailable-root'\").fetchone()[0])", router.databasePath], { timeout: 2000, killSignal: "SIGKILL" });
      expect(rows.stdout.trim()).toBe("0");
      // This observes the failed bootstrap and missing row, not fallback invocation.
    });
  } finally {
    try { await proxy?.close(); } finally { await router.close(); }
  }
});
