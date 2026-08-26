import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import test from "node:test";
import WebSocket, { WebSocketServer } from "ws";

import { startRouterWebUi } from "../tools/router_web_ui.mjs";

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function startFakeRouter() {
  const frames = [];
  const httpRequests = [];
  let registration = 0;
  const http = createServer(async (request, response) => {
    response.setHeader("content-type", "application/json");
    if (["POST", "PATCH", "DELETE"].includes(request.method) && request.url.startsWith("/v1/reminders")) {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = Buffer.concat(chunks).toString("utf8");
      httpRequests.push({ method: request.method, url: request.url, body: body ? JSON.parse(body) : undefined });
      if (request.method === "DELETE") { response.writeHead(204); return response.end(); }
      response.writeHead(request.method === "POST" ? 201 : 200);
      return response.end(JSON.stringify({ reminder: { id: request.url.split("/").pop() || "created", ...(body ? JSON.parse(body) : {}) } }));
    }
    if (request.url === "/v1/capabilities") return response.end(JSON.stringify({}));
    if (request.url === "/v1/agents" || request.url === "/v1/agents?models=omit") return response.end(JSON.stringify({ agents: [] }));
    if (request.url === "/v1/reminders") return response.end(JSON.stringify({ reminders: [] }));
    if (request.url === "/v1/reminders/reminder-1" && request.method === "PATCH") return response.end(JSON.stringify({ reminder: { id: "reminder-1" } }));
    if (request.url === "/v1/reminders/reminder-1" && request.method === "DELETE") return response.end(JSON.stringify({ deleted: true }));
    response.writeHead(404);
    response.end(JSON.stringify({ error: "not_found" }));
  });
  const wsServer = new WebSocketServer({ server: http, path: "/v1/agents" });
  wsServer.on("connection", (socket) => {
    const lease = `lease-${++registration}`;
    socket.on("message", (payload) => {
      const frame = JSON.parse(payload.toString());
      frames.push({ socket, frame, lease });
      if (frame.type === "register") {
        socket.send(JSON.stringify({ version: "v1", frame_id: `registered-${lease}`, type: "registered", agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: lease }));
      }
    });
  });
  await new Promise((resolve) => http.listen(0, "127.0.0.1", resolve));
  const address = http.address();
  return {
    httpUrl: `http://127.0.0.1:${address.port}`,
    frames,
    httpRequests,
    async close() {
      wsServer.close();
      await new Promise((resolve) => http.close(resolve));
    },
  };
}

function openBrowser(httpUrl) {
  return new Promise((resolve, reject) => {
    const browser = new WebSocket(`${httpUrl.replace("http://", "ws://")}/ui/events`, { origin: httpUrl });
    browser.once("open", () => resolve(browser));
    browser.once("error", reject);
  });
}

function nextEvent(socket, predicate, timeoutMs = 1_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { socket.off("message", onMessage); reject(new Error("timed out waiting for WebUI event")); }, timeoutMs);
    const onMessage = (payload) => {
      const event = JSON.parse(payload.toString());
      if (!predicate(event)) return;
      clearTimeout(timer);
      socket.off("message", onMessage);
      resolve(event);
    };
    socket.on("message", onMessage);
  });
}

async function connect(ui, router, agentId = "web-test") {
  const browser = await openBrowser(ui.httpUrl);
  browser.send(JSON.stringify({ type: "connect", routerUrl: router.httpUrl, agentId }));
  await nextEvent(browser, (event) => event.type === "status" && event.state === "connected");
  return browser;
}

test("WebUI reminder lock relay binds user_interaction identity and correlates set and clear", async () => {
  const router = await startFakeRouter();
  const ui = await startRouterWebUi({ router: router.httpUrl, modelControlTimeoutMs: 100, output: () => {} });
  let browser;
  try {
    browser = await connect(ui, router);
    browser.send(JSON.stringify({
      type: "reminder_lock_control",
      controlId: "lock-set",
      targetAgentId: "target-agent",
      targetInstanceId: "target-instance",
      reminderId: "reminder-1",
      action: "set",
      agentId: "spoofed-source",
      sourceAgentId: "spoofed-source",
    }));
    const request = await new Promise((resolve) => {
      const timer = setInterval(() => {
        const frame = router.frames.find(({ frame }) => frame.type === "reminder_lock_control" && frame.control_id === "lock-set");
        if (!frame) return;
        clearInterval(timer);
        resolve(frame);
      }, 1);
    });
    assert.equal(request.frame.agent_id, "web-test");
    assert.equal(request.frame.instance_id.length > 0, true);
    assert.equal(request.frame.lease_id, "lease-1");
    assert.equal(Object.hasOwn(request.frame, "source_agent_id"), false);
    assert.deepEqual({ target: request.frame.target_agent_id, instance: request.frame.target_instance_id, reminder: request.frame.reminder_id, action: request.frame.action }, { target: "target-agent", instance: "target-instance", reminder: "reminder-1", action: "set" });

    request.socket.send(JSON.stringify({ version: "v1", frame_id: "result-set", type: "reminder_lock_control_result", agent_id: "target-agent", instance_id: "target-instance", lease_id: "target-lease", source_agent_id: "web-test", control_id: "lock-set", reminder_id: "reminder-1", accepted: true }));
    const accepted = await nextEvent(browser, (event) => event.type === "reminder_lock_control_result" && event.controlId === "lock-set");
    assert.deepEqual({ targetAgentId: accepted.targetAgentId, targetInstanceId: accepted.targetInstanceId, targetLeaseId: accepted.targetLeaseId, reminderId: accepted.reminderId, accepted: accepted.accepted }, { targetAgentId: "target-agent", targetInstanceId: "target-instance", targetLeaseId: "target-lease", reminderId: "reminder-1", accepted: true });

    const patch = await fetch(`${router.httpUrl}/v1/reminders/reminder-1`, { method: "PATCH", body: JSON.stringify({ enabled: false }) });
    assert.equal(patch.ok, true);
    const deletion = await fetch(`${router.httpUrl}/v1/reminders/reminder-1`, { method: "DELETE" });
    assert.equal(deletion.ok, true);

    browser.send(JSON.stringify({ type: "reminder_lock_control", controlId: "lock-clear", targetAgentId: "target-agent", targetInstanceId: "target-instance", reminderId: "reminder-1", action: "clear" }));
    await waitForFrame(router, (frame) => frame.control_id === "lock-clear");
    const clearRequest = router.frames.find(({ frame }) => frame.control_id === "lock-clear");
    clearRequest.socket.send(JSON.stringify({ version: "v1", frame_id: "result-clear", type: "reminder_lock_control_result", agent_id: "target-agent", instance_id: "target-instance", lease_id: "target-lease", source_agent_id: "web-test", control_id: "lock-clear", reminder_id: "reminder-1", accepted: true }));
    assert.equal((await nextEvent(browser, (event) => event.controlId === "lock-clear")).accepted, true);
  } finally {
    browser?.close();
    await ui.close();
    await router.close();
  }
});

test("WebUI CRUD relay forwards normal POST and PATCH payloads", async () => {
  const router = await startFakeRouter();
  const ui = await startRouterWebUi({ router: router.httpUrl, modelControlTimeoutMs: 100, output: () => {} });
  let browser;
  try {
    browser = await connect(ui, router);
    browser.send(JSON.stringify({ type: "reminder_crud", requestId: "crud-create", operation: "create", payload: {
      id: "new-reminder", target: "target-agent", creator: "web-test", body: "hello", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:01:00.000Z", interval_ms: null,
    } }));
    assert.equal((await nextEvent(browser, (event) => event.type === "reminder_crud_result" && event.requestId === "crud-create")).accepted, true);
    browser.send(JSON.stringify({ type: "reminder_crud", requestId: "crud-update", operation: "update", reminderId: "new-reminder", payload: { body: "updated", subject: "new subject" } }));
    assert.equal((await nextEvent(browser, (event) => event.type === "reminder_crud_result" && event.requestId === "crud-update")).accepted, true);
    assert.deepEqual(router.httpRequests.map(({ method, url, body }) => ({ method, url, body })), [
      { method: "POST", url: "/v1/reminders", body: { id: "new-reminder", target: "target-agent", creator: "web-test", body: "hello", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:01:00.000Z", interval_ms: null } },
      { method: "PATCH", url: "/v1/reminders/new-reminder", body: { body: "updated", subject: "new subject" } },
    ]);
  } finally {
    browser?.close();
    await ui.close();
    await router.close();
  }
});

test("WebUI rejects mismatched results and fences pending controls on reconnect", async () => {
  const router = await startFakeRouter();
  const ui = await startRouterWebUi({ router: router.httpUrl, modelControlTimeoutMs: 500, output: () => {} });
  let browser;
  try {
    browser = await connect(ui, router);
    browser.send(JSON.stringify({ type: "reminder_lock_control", controlId: "stale-result", targetAgentId: "target-agent", targetInstanceId: "target-instance", reminderId: "reminder-1", action: "set" }));
    const request = await waitForFrame(router, (frame) => frame.control_id === "stale-result");
    request.socket.send(JSON.stringify({ version: "v1", frame_id: "wrong-result", type: "reminder_lock_control_result", agent_id: "other-agent", instance_id: "target-instance", lease_id: "target-lease", source_agent_id: "web-test", control_id: "stale-result", reminder_id: "reminder-1", accepted: true }));
    await assert.rejects(nextEvent(browser, (event) => event.controlId === "stale-result", 50));

    const rejected = nextEvent(browser, (event) => event.type === "reminder_lock_control_result" && event.controlId === "stale-result");
    browser.send(JSON.stringify({ type: "connect", routerUrl: router.httpUrl, agentId: "web-test" }));
    assert.equal((await rejected).errorCode, "router_reconnecting");
    request.socket.send(JSON.stringify({ version: "v1", frame_id: "late-result", type: "reminder_lock_control_result", agent_id: "target-agent", instance_id: "target-instance", lease_id: "target-lease", source_agent_id: "web-test", control_id: "stale-result", reminder_id: "reminder-1", accepted: true }));
    await assert.rejects(nextEvent(browser, (event) => event.controlId === "stale-result" && event.accepted === true, 50));
  } finally {
    browser?.close();
    await ui.close();
    await router.close();
  }
});

test("reminder controls remain separate from normal CRUD and creation", async () => {
  const [app, server, html, styles] = await Promise.all([
    readFile(new URL("../web/router-ui/app.js", import.meta.url), "utf8"),
    readFile(new URL("../tools/router_web_ui.mjs", import.meta.url), "utf8"),
    readFile(new URL("../web/router-ui/index.html", import.meta.url), "utf8"),
    readFile(new URL("../web/router-ui/styles.css", import.meta.url), "utf8"),
  ]);
  assert.match(app, /textContent = action === "set" \? "Set lock" : "Clear lock"/);
  assert.match(app, /type: "reminder_lock_control", controlId, targetAgentId: peer, targetInstanceId, reminderId: reminder\.id, action/);
  assert.match(app, /state\.reminders = Array\.isArray\(event\.reminders\)/);
  assert.match(app, /formatReminderCountdown\(reminder\.next_due_at\)/);
  assert.match(app, /reminderRecurrenceLabel\(reminder\.interval_ms\)/);
  assert.match(app, /button\.disabled = pending \|\| \(action === "clear" && lock\?\.locked !== true\)/);
  assert.match(app, /current\?\.instance_id !== undefined[\s\S]*clearReminderLockState\(agent\.agent_id\)/);
  assert.match(app, /const incomingAgentIds = new Set\(incomingAgents\.map\(\(agent\) => agent\.agent_id\)\);[\s\S]*current\.agent_id !== state\.agentId && !incomingAgentIds\.has\(current\.agent_id\)[\s\S]*clearReminderLockState\(current\.agent_id\)/);
  assert.match(app, /: \{ locked: false, error: reminderLockErrorMessage\(event\.errorCode\) \}/);
  assert.doesNotMatch(app, /locked.*reminder\.id|reminder\.locked/);
  assert.doesNotMatch(app, /reminder_lock_control[\s\S]{0,300}action:\s*["']create["']/);
  assert.match(server, /load\("\/v1\/reminders"\)/);
  assert.match(html, /id="reminders-dialog-body"/);
  assert.match(styles, /\.reminder-list-item/);
});

async function waitForFrame(router, predicate, timeoutMs = 1_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const found = router.frames.find(({ frame }) => predicate(frame));
    if (found) return found;
    await wait(2);
  }
  throw new Error("timed out waiting for router frame");
}
