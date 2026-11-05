import { test, expect } from "@playwright/test";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { createServer } from "node:http";
import { mkdtemp, mkdir, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { once } from "node:events";
import { WebSocket } from "ws";
import { startIsolatedRouter, allocatePort, type IsolatedRouter } from "./harness";
import { createOperationalToolDefinitions } from "../../pi-extensions/mecha-messaging/operational-tools";

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, "../..");
const sender = join(repo, "tools/mailbox_send.py");
const runner = join(repo, "tools/managed_root_control.py");
const environment = { ...process.env, GOMAXPROCS: "2", GOFLAGS: "-p=2", PYTHONDONTWRITEBYTECODE: "1" };

async function python(args: string[], input?: unknown, env: NodeJS.ProcessEnv = environment) {
  return new Promise<{ code: number | null; stdout: string; stderr: string }>((done, reject) => {
    const child = spawn("python3", args, { env, stdio: "pipe" });
    let stdout = "", stderr = "";
    const timeout = setTimeout(() => child.kill("SIGKILL"), 10_000);
    child.stdout.on("data", chunk => { stdout += chunk; });
    child.stderr.on("data", chunk => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", code => { clearTimeout(timeout); done({ code, stdout, stderr }); });
    child.stdin.end(input === undefined ? "" : JSON.stringify(input));
  });
}

async function boundary(handler: Parameters<typeof createServer>[0]) {
  const server = createServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("missing address");
  return { url: `http://127.0.0.1:${address.port}`, close: async () => { server.closeAllConnections(); await new Promise<void>((done, reject) => server.close(error => error ? reject(error) : done())); } };
}

let router: IsolatedRouter;
let url: string;
let root: string;
let stopProxy: (() => Promise<void>) | undefined;

test.beforeAll(async () => {
  test.setTimeout(120_000);
  root = await mkdtemp(join(tmpdir(), "native-tooling-"));
  // Serial, CPU-bounded builds. No UI build, dependencies or running services touched.
  const oldMax = process.env.GOMAXPROCS, oldFlags = process.env.GOFLAGS;
  try {
    process.env.GOMAXPROCS = "2"; process.env.GOFLAGS = "-p=2";
    router = await startIsolatedRouter({ repoRoot: repo });
  } finally {
    if (oldMax === undefined) delete process.env.GOMAXPROCS; else process.env.GOMAXPROCS = oldMax;
    if (oldFlags === undefined) delete process.env.GOFLAGS; else process.env.GOFLAGS = oldFlags;
  }
  await exec("go", ["build", "-o", join(root, "proxy"), "."], { cwd: join(repo, "router-proxy"), env: environment, timeout: 60_000 });
  const port = await allocatePort();
  const child = spawn(join(root, "proxy"), ["--upstream-host", "127.0.0.1", "--upstream-port", String(router.port), "--listen-host", "127.0.0.1", "--listen-port", String(port)], { stdio: ["ignore", "pipe", "pipe"] });
  stopProxy = async () => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    const closed = once(child, "close"); child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
    try { await closed; } finally { clearTimeout(timer); }
  };
  await new Promise<void>((done, reject) => {
    const timer = setTimeout(() => reject(new Error("proxy readiness timeout")), 5000);
    child.stdout.on("data", chunk => { if (String(chunk).includes("MECHA_ROUTER_PROXY_READY")) { clearTimeout(timer); done(); } });
    child.once("error", reject);
    child.once("exit", code => { clearTimeout(timer); reject(new Error(`proxy exited ${code}`)); });
  });
  url = `http://127.0.0.1:${port}`;
});

test.afterAll(async () => {
  await stopProxy?.();
  await router?.close();
  if (root) await rm(root, { recursive: true, force: true });
});

const mailArgs = (base: string) => [sender, "--router-url", base, "--from", "native-sender", "--to", "native-recipient", "--body", "durable mail"];
const invocation = (request_id: string, operation_id: string, args: Record<string, unknown>) => ({ request_id, operation_id, arguments: args, fence: { agent_id: "native-sender" } });
async function host(home: string, request: ReturnType<typeof invocation>, base = url, mode = "execute") {
  // Per-child config, never change router or workstation services.
  const completed = await new Promise<string>((done, reject) => {
    const child = execFile("python3", [runner, mode, "--home", home], { env: { ...environment, MECHA_ROUTER_URL: base }, timeout: 10_000 }, (error, stdout) => error ? reject(error) : done(stdout));
    child.stdin?.end(JSON.stringify(request));
  });
  return JSON.parse(completed);
}

test("persists Python and native worker mail through proxy", async ({ page }) => {
  const socket = new WebSocket(`${url.replace("http", "ws")}/v1/agents`);
  const frames: Record<string, unknown>[] = [];
  socket.on("message", raw => frames.push(JSON.parse(String(raw))));
  try {
    await once(socket, "open");
    socket.send(JSON.stringify({ version: "v1", frame_id: "native-register", type: "register", agent_id: "native-recipient", instance_id: "native-instance", agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", delivery_receipts: "delivery_items_v1" }));
    await expect.poll(() => frames.some(frame => frame.type === "registered")).toBe(true);
    const sent = await python(mailArgs(url), undefined, { ...environment, TMUX: "", TMUX_PANE: "", PATH: "/usr/bin:/bin" });
    expect(sent.code, sent.stderr).toBe(0);
    expect(sent.stdout).toMatch(/Message [\w.-]+ accepted: (queued|leased|injection_requested|acknowledged)/);
    const id = sent.stdout.split(" ")[1];
    // Browser crosses the real proxy/router CORS boundary, without a UI build.
    const persisted = await page.evaluate(async ({ url, id }) => (await fetch(`${url}/v1/messages/${id}`, { credentials: "omit" })).json(), { url, id });
    expect(JSON.stringify(persisted)).toContain("durable mail");
    await expect.poll(() => JSON.stringify(frames)).toContain(id);
    expect(createOperationalToolDefinitions().some(tool => tool.name === "mecha_worker")).toBe(false);
    // Native mailbox persistence is exercised through the embedded SDK in worker-tool-actions.
    // Read the actual SQLite store with Python's stdlib; no internal JS mocks.
    const db = await python(["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute('select count(*) from messages').fetchone()[0])", router.databasePath]);
    expect(db.code, db.stderr).toBe(0);
    expect(Number(db.stdout)).toBeGreaterThanOrEqual(1);
  } finally { socket.terminate(); }
});

test("rejects invalid mail boundaries without recovery", async () => {
  const identityEnv = { ...environment, TMUX: "", TMUX_PANE: "", MECHA_AGENT_ID: "not-a-verified-current-pane" };
  const inferredArgs = [sender, "--router-url", url, "--to", "identity-recipient", "--body", "inferred sender"];
  expect((await python(inferredArgs, undefined, identityEnv)).code).not.toBe(0);
  // A private tmux server is a real identity boundary, never the workstation server.
  const socket = join(root, "identity.sock");
  await exec("tmux", ["-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", "identity-test", "-n", "inferred-sender", "sleep 60"], { env: identityEnv, timeout: 5000 });
  try {
    const { stdout: pane } = await exec("tmux", ["-S", socket, "display-message", "-p", "#{pane_id}"], { timeout: 5000 });
    const currentEnv = { ...identityEnv, TMUX: `${socket},0,0`, TMUX_PANE: pane.trim() };
    const sent = await python(inferredArgs, undefined, currentEnv);
    expect(sent.code, sent.stderr).toBe(0);
    const id = sent.stdout.split(" ")[1];
    const persisted = await (await fetch(`${url}/v1/messages/${id}`)).json();
    expect(persisted.message.from).toBe("inferred-sender");
    await exec("tmux", ["-S", socket, "rename-window", "not a mailbox identity"], { timeout: 5000 });
    expect((await python(inferredArgs, undefined, currentEnv)).code).not.toBe(0);
    expect((await python(inferredArgs, undefined, { ...currentEnv, TMUX_PANE: "invalid" })).code).not.toBe(0);
  } finally { await exec("tmux", ["-S", socket, "kill-server"], { timeout: 5000 }); }
  let requests = 0;
  let status = 201, body = "{}", redirect = false;
  const fake = await boundary((_req, res) => { requests++; res.writeHead(status, redirect ? { location: `${url}/v1/messages` } : {}); res.end(body); });
  try {
    for (const bad of ["file:///tmp/router", `${fake.url}/path`, `${fake.url}?query=1`, `${fake.url}#fragment`, fake.url.replace("//", "//user:password@"), "http://127.0.0.1:99999"]) {
      expect((await python(mailArgs(bad))).code).not.toBe(0);
    }
    expect((await python([sender, "--router-url", fake.url, "--to", "worker", "--body", "hello"], undefined, identityEnv)).code).not.toBe(0);
    expect(requests).toBe(0);
    for (const response of ["{}", "not json", JSON.stringify({ message: { id: "bad", state: "invented" } }), "x".repeat(512 * 1024 + 1)]) {
      body = response;
      const failed = await python(mailArgs(fake.url));
      expect(failed.code).not.toBe(0); expect(failed.stdout).not.toContain("accepted:");
    }
    status = 429; body = JSON.stringify({ error: { code: "queue_full", message: "full" } });
    expect((await python(mailArgs(fake.url))).stderr).toContain("queue_full");
    status = 302; redirect = true;
    expect((await python(mailArgs(fake.url))).code).not.toBe(0);
    status = 200; redirect = false; body = JSON.stringify({ message: { id: "existing-delivery", state: "acknowledged" } });
    expect((await python(mailArgs(fake.url))).stdout).toBe("Message existing-delivery accepted: acknowledged\n");
  } finally { await fake.close(); }
  const deadPort = await allocatePort();
  const unavailable = await python(mailArgs(`http://127.0.0.1:${deadPort}`));
  expect(unavailable.code).not.toBe(0); expect(unavailable.stderr).toContain("Router unavailable");
  expect(unavailable.stderr).not.toMatch(/kickstart|Traceback/);
});

test("reports retired operations unavailable before subprocess", async () => {
  const home = join(root, "unavailable");
  const retired: [string, Record<string, unknown>][] = [
    ["ops.state", { action: "query", task: "task" }], ["ops.priority", { action: "status", priorityId: "priority", status: "active" }],
    ["ops.verification_handoff", { action: "record", task: "task", reminderId: "reminder", successor: "worker" }],
    ["ops.pr_status", { action: "status", pullRequest: 1 }], ["ops.git_status", { action: "status", task: "task" }],
    ...["launch", "list", "send", "cleanup"].map(action => ["ops.worker", { action }] as [string, Record<string, unknown>]), ["mail.parent", { message: "hello" }],
  ];
  for (const [index, [operation, args]] of retired.entries()) {
    const result = await host(home, invocation(`unavailable-${index}`, operation, args));
    expect(result.status).toBe("error"); expect(result.error_code, `${operation}: ${JSON.stringify(result)}`).toBe("ROOT_OPERATION_UNAVAILABLE");
    expect(result.error_message).not.toMatch(/No such file|can't open/);
  }
  let calls = 0;
  const tools = createOperationalToolDefinitions({ runFile: async () => { calls++; throw new Error("must not spawn"); } });
  for (const [name, args] of [["mecha_state", { action: "query", task: "task" }]] as const) {
    expect(JSON.stringify(await tools.find(tool => tool.name === name)!.execute("call", args))).toContain("ROOT_OPERATION_UNAVAILABLE");
  }
  expect(tools.some(tool => tool.name === "mecha_worker")).toBe(false);
  expect(calls).toBe(0);
});

test("forwards correlation and replays without resending", async () => {
  // Actual host dispatch -> proxy -> router -> SQLite, including recovery and conflicts.
  for (const operation of ["mail.send"]) {
    const body = `real-host-${operation}`;
    const args = { recipient: "host-recipient", body };
    const request = invocation(`real-${operation}`, operation, args);
    const home = join(root, `real-${operation}`);
    const first = await host(home, request);
    expect(first.status).toBe("ok");
    expect(await host(home, request)).toEqual(first);
    expect(await host(home, request, url, "reconcile")).toEqual(first);
    const reordered = { ...request, arguments: Object.fromEntries(Object.entries(args).reverse()) };
    expect(await host(home, reordered)).toEqual(first);
    await expect(host(home, { ...request, arguments: { ...args, subject: "conflict" } })).rejects.toThrow();
    await expect(host(home, { ...request, fence: { agent_id: "another-sender" } })).rejects.toThrow();
    const count = await python(["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute('select count(*) from messages where body=?', (sys.argv[2],)).fetchone()[0])", router.databasePath, body]);
    expect(count.code, count.stderr).toBe(0);
    expect(Number(count.stdout)).toBe(1);
  }
  // Seed only the quiet-recipient setting; no authorization is seeded. A forged
  // control ID must not classify otherwise ordinary accepted mail as quiet.
  const configured = await python(["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute(\"update managed_settings set canonical_quiet_recipient_id='quiet-recipient' where singleton_id=1\"); c.commit()", router.databasePath]);
  expect(configured.code, configured.stderr).toBe(0);
  const quietHome = join(root, "forged-quiet");
  const forged = await host(quietHome, invocation("forged-control", "mail.send", { recipient: "quiet-recipient", body: "ordinary forged-header candidate" }));
  expect(forged.status).toBe("ok");
  const quiet = await python(["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute('select quiet_eligible from conversation_messages where body=?', (sys.argv[2],)).fetchone()[0])", router.databasePath, "ordinary forged-header candidate"]);
  expect(quiet.code, quiet.stderr).toBe(0); expect(Number(quiet.stdout)).toBe(0);
  const persisted = await python(["-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); print(c.execute('select count(*) from messages where body=?', (sys.argv[2],)).fetchone()[0])", router.databasePath, "ordinary forged-header candidate"]);
  expect(persisted.code, persisted.stderr).toBe(0); expect(Number(persisted.stdout)).toBe(1);
  const received: { header: string | string[] | undefined; body: string }[] = [];
  const fake = await boundary((req, res) => {
    let body = ""; req.on("data", chunk => { body += chunk; });
    req.on("end", () => { received.push({ header: req.headers["x-mecha-managed-control-request-id"], body }); res.end(JSON.stringify({ message: { id: "accepted-id", state: "queued" } })); });
  });
  try {
    for (const operation of ["mail.send"]) {
      const args = { recipient: "worker", body: "correlated" };
      const request = invocation(`correlation-${operation}`, operation, args);
      const home = join(root, `replay-${operation}`);
      const first = await host(home, request, fake.url);
      expect(first.status).toBe("ok");
      expect(await host(home, request, fake.url)).toEqual(first);
      expect(await host(home, request, fake.url, "reconcile")).toEqual(first);
      await expect(host(home, { ...request, arguments: { ...args, subject: "conflict" } }, fake.url)).rejects.toThrow();
      expect(received.at(-1)?.header).toBe(request.request_id);
      expect(JSON.parse(received.at(-1)!.body).from).toBe("native-sender");
    }
    expect(received).toHaveLength(1);
  } finally { await fake.close(); }
});

test("preserves router controls and prunes host history", async () => {
  const home = join(root, "retention"), directory = join(home, "root-control");
  await mkdir(directory, { recursive: true });
  const now = Date.now() / 1000;
  const requests = Object.fromEntries(Array.from({ length: 205 }, (_, index) => [`old-${index}`, { recorded_at: now - index, fingerprint: "old", result: {} }]));
  requests.expired = { recorded_at: now - 31 * 86400, fingerprint: "expired", result: {} };
  await writeFile(join(directory, "requests.json"), JSON.stringify({ version: 1, requests, settings: {} }));
  expect((await host(home, invocation("health", "router.control", { action: "health" }))).status).toBe("ok");
  const state = JSON.parse(await readFile(join(directory, "requests.json"), "utf8"));
  expect(Object.keys(state.requests)).toHaveLength(200); expect(state.requests.expired).toBeUndefined();
  const created = await host(home, invocation("reminder-create", "reminder.control", { action: "create", message: "check work", delayMinutes: 60 }));
  expect(created.status).toBe("ok");
  const reminderId = created.details.reminder.id;
  const listed = await host(home, invocation("reminder-list", "reminder.control", { action: "list" }));
  expect(listed.details.reminders.map((row: { id: string }) => row.id)).toContain(reminderId);
  const updated = await host(home, invocation("reminder-update", "reminder.control", { action: "update", reminderId, message: "updated work" }));
  expect(updated.details.reminder.body).toBe("updated work");
  expect((await host(home, invocation("reminder-delete", "reminder.control", { action: "delete", reminderId }))).status).toBe("ok");
});
