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
    await proxy?.close();
    await router.close();
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
