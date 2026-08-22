import assert from "node:assert/strict";
import http from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { createTmuxAdapter } from "../harness/tmux-adapter.js";

function call(url, method, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, { method, headers: { ...(body === undefined ? {} : { "content-type": "application/json" }), ...headers } }, (response) => {
      let text = ""; response.setEncoding("utf8"); response.on("data", (chunk) => { text += chunk; });
      response.on("end", () => resolve({ status: response.statusCode, headers: response.headers, body: text === "" ? undefined : JSON.parse(text) }));
    });
    request.on("error", reject); if (body !== undefined) request.end(JSON.stringify(body)); else request.end();
  });
}

async function fixture(t, adapter, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), "router-tmux-")); t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") }); t.after(() => repository.close());
  const router = createAgentRouter({ repository, tmuxAdapter: adapter, timeoutSweepIntervalMs: 60_000, ...options });
  const ws = await router.listen(); t.after(() => router.close());
  return `http://${ws.slice(5).replace(/\/v1\/agents$/, "")}`;
}

test("default tmux adapter treats an empty server as an empty inventory", async () => {
  const calls = [];
  const adapter = createTmuxAdapter({
    exec: async (_binary, args) => {
      calls.push(args);
      if (args[0] === "list-sessions") {
        const error = new Error("no server running on socket");
        error.code = 1;
        throw error;
      }
      return { stdout: "", stderr: "" };
    },
  });

  assert.deepEqual(await adapter.enumerate(), []);
  assert.deepEqual(await adapter.provisionSession("selected", "supervisor"), { session: "selected", name: "supervisor" });
  assert.deepEqual(await adapter.provision("selected", "first"), { name: "first" });
  assert.deepEqual(calls, [
    ["list-sessions", "-F", "#{session_name}"],
    ["new-session", "-d", "-s", "selected", "-n", "supervisor"],
    ["new-window", "-d", "-t", "selected", "-n", "first"],
  ]);
});

test("router bootstrap provisions an empty tmux inventory and serves the worker list", async (t) => {
  const calls = [];
  const windows = [];
  const adapter = {
    enumerate: async () => [],
    enumerateWindows: async () => windows,
    provisionSession: async (session, window) => { windows.push({ name: window, target: `${session}:${window}` }); calls.push(["provision-session", session, window]); },
    sendKeys: async (session, window, body, subject) => calls.push(["send-keys", session, window, body, subject]),
  };
  const base = await fixture(t, adapter, { startupSession: "mecha", startupWindow: "supervisor", startupProvision: true });
  assert.equal(base.startsWith("http://"), true);
  assert.equal(calls[0][0], "provision-session");
  assert.deepEqual(calls[0].slice(1), ["mecha", "supervisor"]);
  assert.deepEqual(calls[1], [
    "send-keys", "mecha", "supervisor",
    "Load the mailbox and reminder skills, assess the current situation, and set up the sweep reminder.",
    "router.startup.directive.v1",
  ]);
  const result = await call(`${base}/v1/tmux/windows`, "GET");
  assert.equal(result.status, 200);
  assert.deepEqual(result.body, { session: "mecha", windows: [{ name: "supervisor", target: "mecha:supervisor" }] });
});

test("router bootstrap selects the first listed mecha session and sends the startup directive", async (t) => {
  const calls = [];
  const adapter = {
    enumerate: async () => ["worker", "first-mecha-session", "second-mecha-session"],
    enumerateWindows: async () => [{ name: "supervisor", target: "first-mecha-session:supervisor" }],
    sendKeys: async (...args) => calls.push(args),
  };
  const base = await fixture(t, adapter, { startupSession: "mecha", startupWindow: "supervisor", startupProvision: true });
  assert.deepEqual(calls, [[
    "first-mecha-session", "supervisor",
    "Load the mailbox and reminder skills, assess the current situation, and set up the sweep reminder.",
    "router.startup.directive.v1",
  ]]);
  const result = await call(`${base}/v1/tmux/windows`, "GET");
  assert.equal(result.status, 200);
  assert.equal(result.body.session, "first-mecha-session");
});

test("re-instantiated router lazily selects the existing mecha session for windows", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-tmux-reinstantiate-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const calls = [];
  const windows = [{ name: "worker", target: "real-mecha:worker" }];
  const adapter = {
    enumerate: async () => ["other", "real-mecha"],
    enumerateWindows: async (session) => windows.map((window) => ({ ...window, target: `${session}:${window.name}` })),
    rename: async (session, name, nextName) => {
      calls.push(["rename", session, name, nextName]);
      windows[0] = { name: nextName, target: `${session}:${nextName}` };
    },
    sendKeys: async (...args) => calls.push(["send-keys", ...args]),
  };
  const firstRouter = createAgentRouter({ repository, tmuxAdapter: adapter, timeoutSweepIntervalMs: 60_000, startupSession: "real-mecha", supervisorDirective: null });
  await firstRouter.listen();
  await firstRouter.close();
  const router = createAgentRouter({ repository, tmuxAdapter: adapter, timeoutSweepIntervalMs: 60_000 });
  const ws = await router.listen();
  t.after(() => router.close());
  const base = `http://${ws.slice(5).replace(/\/v1\/agents$/, "")}`;

  let result = await call(`${base}/v1/tmux/windows`, "GET");
  assert.equal(result.status, 200);
  assert.deepEqual(result.body, { session: "real-mecha", windows: [{ name: "worker", target: "real-mecha:worker" }] });
  result = await call(`${base}/v1/tmux/windows/worker`, "PATCH", { name: "renamed" });
  assert.equal(result.status, 200);
  assert.deepEqual(calls, [
    ["rename", "real-mecha", "worker", "renamed"],
    ["send-keys", "real-mecha", "renamed", JSON.stringify({ type: "tmux_window_renamed", old_name: "worker", new_name: "renamed" }), "router.tmux.window-renamed.v1"],
  ]);
});

test("tmux REST provisioning registers the Pi session ID for lifecycle recovery", async (t) => {
  const calls = [];
  const adapter = {
    enumerate: async () => ["pi"],
    enumerateWindows: async () => [{ name: "worker", target: "pi:worker" }],
    provision: async (session, name) => { calls.push(["provision", session, name]); },
    sendKeys: async () => {},
    stop: async (session, name, sessionId) => { calls.push(["stop", session, name, sessionId]); return { name, state: "stopped" }; },
    start: async (session, name, sessionId) => { calls.push(["start", session, name, sessionId]); return { name, state: "started" }; },
    resume: async (session, name, sessionId) => { calls.push(["resume", session, name, sessionId]); return { name, state: "resumed" }; },
  };
  const base = await fixture(t, adapter, { startupSession: "pi", supervisorDirective: null });
  let result = await call(`${base}/v1/tmux/windows`, "POST", { name: "worker", session_id: "pi-session-1", directive: null });
  assert.equal(result.status, 201);
  assert.equal(result.body.session_id, "pi-session-1");
  for (const action of ["stop", "start", "resume"]) {
    result = await call(`${base}/v1/tmux/windows/worker/${action}`, "POST", {});
    assert.equal(result.status, 200);
    assert.equal(result.body.session_id, "pi-session-1");
  }
  assert.deepEqual(calls, [
    ["provision", "pi", "worker"],
    ["stop", "pi", "worker", "pi-session-1"],
    ["start", "pi", "worker", "pi-session-1"],
    ["resume", "pi", "worker", "pi-session-1"],
  ]);
});

test("tmux REST rejects caller-supplied resume IDs without router state", async (t) => {
  const calls = [];
  const base = await fixture(t, {
    enumerate: async () => ["pi"],
    enumerateWindows: async () => [{ name: "worker", target: "pi:worker" }],
    resume: async (...args) => { calls.push(args); return { name: "worker", state: "resumed", session_id: args[2] }; },
  }, { startupSession: "pi", supervisorDirective: null });
  const result = await call(`${base}/v1/tmux/windows/worker/resume`, "POST", { session_id: "caller-supplied" });
  assert.equal(result.status, 400);
  assert.equal(result.body.error.code, "invalid_request");
  assert.deepEqual(calls, []);
});

test("tmux REST lifecycle carries session IDs from start into resume", async (t) => {
  const calls = [];
  const adapter = {
    enumerate: async () => ["pi"],
    enumerateWindows: async () => [{ name: "pi", target: "pi:pi" }],
    provision: async () => {},
    sendKeys: async () => {},
    start: async (session, name, sessionId) => { calls.push(["start", session, name, sessionId]); return { name, state: "started", session_id: sessionId }; },
    resume: async (session, name, sessionId) => { calls.push(["resume", session, name, sessionId]); return { name, state: "resumed", session_id: sessionId }; },
  };
  const base = await fixture(t, adapter, { startupSession: "pi" });

  let result = await call(`${base}/v1/tmux/windows/pi/start`, "POST", { session_id: "pi-session-1" });
  assert.equal(result.status, 200);
  assert.equal(result.body.session_id, "pi-session-1");
  result = await call(`${base}/v1/tmux/windows/pi/resume`, "POST", {});
  assert.equal(result.status, 200);
  assert.equal(result.body.session_id, "pi-session-1");
  assert.deepEqual(calls, [
    ["start", "pi", "pi", "pi-session-1"],
    ["resume", "pi", "pi", "pi-session-1"],
  ]);
});

test("tmux REST lifecycle accepts arbitrary origins and preserves the directive", async (t) => {
  const calls = []; const windows = [{ name: "old", target: "old:old" }]; const adapter = {
    enumerate: async () => ["old"],
    enumerateWindows: async () => windows,
    provision: async (session, name) => { windows.push({ name, target: `${session}:${name}` }); calls.push(["provision", session, name]); },
    remove: async (session, name) => { windows.splice(windows.findIndex((window) => window.name === name), 1); calls.push(["remove", session, name]); },
    rename: async (session, name, nextName) => { const current = windows.find((window) => window.name === name); current.name = nextName; current.target = `${session}:${nextName}`; calls.push(["rename", session, name, nextName]); },
    sendKeys: async (session, name, text, subject) => calls.push(["send-keys", session, name, text, subject]),
    start: async (session, name) => calls.push(["start", session, name]), stop: async (session, name) => calls.push(["stop", session, name]), restart: async (session, name) => calls.push(["restart", session, name]),
  };
  const base = await fixture(t, adapter, { startupSession: "old" });
  assert.equal(calls[0][0], "send-keys");
  assert.equal(calls[0][1], "old");
  assert.equal(calls[0][2], "old");
  assert.equal(calls[0][3], "Load the mailbox and reminder skills, assess the current situation, and set up the sweep reminder.");
  assert.equal(calls[0][4], "router.startup.directive.v1");
  let result = await call(`${base}/v1/tmux/windows`, "GET", undefined, { origin: "https://random-origin.example", "x-correlation-id": "corr-1" });
  assert.equal(result.status, 200); assert.equal(result.headers["x-correlation-id"], "corr-1");
  result = await call(`${base}/v1/tmux/windows`, "GET");
  assert.equal(result.status, 200);
  assert.deepEqual(result.body, { session: "old", windows: [{ name: "old", target: "old:old" }] });
  result = await call(`${base}/v1/tmux/windows`, "POST", { name: "new" }); assert.equal(result.status, 201);
  result = await call(`${base}/v1/tmux/windows`, "GET");
  assert.deepEqual(result.body.windows, [{ name: "old", target: "old:old" }, { name: "new", target: "old:new" }]);
  await call(`${base}/v1/tmux/windows/new/keys`, "POST", { keys: "hello" });
  await call(`${base}/v1/tmux/windows/new/start`, "POST", {}); await call(`${base}/v1/tmux/windows/new/stop`, "POST", {}); await call(`${base}/v1/tmux/windows/new/restart`, "POST", {});
  assert.deepEqual(calls.slice(-4).map((entry) => entry[0]), ["send-keys", "start", "stop", "restart"]);
  assert.deepEqual(calls.slice(1, 5).map((entry) => entry[1]), ["old", "old", "old", "old"]);
  result = await call(`${base}/v1/tmux/windows/new`, "PATCH", { name: "renamed" }); assert.equal(result.status, 200);
  result = await call(`${base}/v1/tmux/windows/renamed`, "DELETE"); assert.equal(result.status, 204);
  result = await call(`${base}/v1/tmux/windows`, "OPTIONS", undefined, { origin: "https://random-origin.example" }); assert.equal(result.status, 204);
});

test("tmux REST delegates duplicate provisioning and lifecycle state to tmux", async (t) => {
  const calls = [];
  const windows = [];
  const adapter = {
    enumerate: async () => ["selected"],
    enumerateWindows: async () => windows,
    provision: async (session, name) => { windows.push({ name, target: `${session}:${name}` }); calls.push(["provision", session, name]); },
    sendKeys: async (session, name, keys) => { calls.push(["keys", session, name, keys]); },
    start: async (session, name) => { calls.push(["start", session, name]); return { name, state: "started" }; },
    stop: async (session, name) => { calls.push(["stop", session, name]); return { name, state: "stopped" }; },
    restart: async (session, name) => { calls.push(["restart", session, name]); return { name, state: "started" }; },
  };
  const base = await fixture(t, adapter, { startupSession: "selected", supervisorDirective: null });
  assert.equal((await call(`${base}/v1/tmux/windows`, "POST", { name: "pi", directive: null })).status, 201);
  assert.equal((await call(`${base}/v1/tmux/windows`, "POST", { name: "pi", directive: null })).status, 201);
  assert.equal((await call(`${base}/v1/tmux/windows/pi/start`, "POST", {})).status, 200);
  assert.equal((await call(`${base}/v1/tmux/windows/pi/start`, "POST", {})).status, 200);
  assert.equal((await call(`${base}/v1/tmux/windows/pi/stop`, "POST", {})).status, 200);
  assert.equal((await call(`${base}/v1/tmux/windows/pi/stop`, "POST", {})).status, 200);
  assert.equal((await call(`${base}/v1/tmux/windows/pi/keys`, "POST", { keys: "x" })).status, 200);
  assert.deepEqual(calls, [["provision", "selected", "pi"], ["provision", "selected", "pi"], ["start", "selected", "pi"], ["start", "selected", "pi"], ["stop", "selected", "pi"], ["stop", "selected", "pi"], ["keys", "selected", "pi", "x"]]);
});

test("tmux REST rejects invalid path names before adapter invocation", async (t) => {
  let invoked = false;
  const base = await fixture(t, {
    enumerate: async () => [],
    remove: async () => { invoked = true; },
  });
  const result = await call(`${base}/v1/tmux/windows/%2F`, "DELETE");
  assert.equal(result.status, 400);
  assert.equal(result.body.error.code, "tmux_invalid_name");
  assert.equal(invoked, false);
});
