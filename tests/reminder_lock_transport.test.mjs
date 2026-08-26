import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { validateClientFrame, validateServerFrame } from "../harness/protocol.js";

function nextFrame(socket, predicate = () => true, timeoutMs = 1_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { socket.off("message", onMessage); reject(new Error("frame timeout")); }, timeoutMs);
    function onMessage(payload) {
      const frame = JSON.parse(payload.toString("utf8"));
      if (!predicate(frame)) return;
      clearTimeout(timer);
      socket.off("message", onMessage);
      resolve(frame);
    }
    socket.on("message", onMessage);
  });
}

async function register(url, agentId, instanceId, agentRole) {
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => { socket.once("open", resolve); socket.once("error", reject); });
  socket.send(JSON.stringify({
    version: "v1", frame_id: `${agentId}-${instanceId}-register`, type: "register",
    agent_id: agentId, instance_id: instanceId, ...(agentRole === undefined ? {} : { agent_role: agentRole }),
  }));
  const registered = await nextFrame(socket, (frame) => frame.type === "registered");
  return { socket, registered };
}

function control(overrides = {}) {
  return {
    version: "v1", frame_id: "lock-request", type: "reminder_lock_control",
    agent_id: "web", instance_id: "web-instance", lease_id: "web-lease",
    target_agent_id: "worker", target_instance_id: "worker-instance",
    control_id: "lock-control", reminder_id: "reminder-1", action: "set", ...overrides,
  };
}

test("reminder lock frames are bounded and strict", () => {
  const request = control();
  assert.deepEqual(validateClientFrame(request), request);
  assert.throws(() => validateClientFrame({ ...request, action: "off" }));
  assert.throws(() => validateClientFrame({ ...request, extra: true }));
  assert.throws(() => validateClientFrame({ ...request, reminder_id: "!" }));
  assert.throws(() => validateClientFrame({ ...request, reminder_id: "x".repeat(100) }));

  const forwarded = {
    version: "v1", frame_id: "lock-forward", type: "reminder_lock_control",
    agent_id: "worker", instance_id: "worker-instance", lease_id: "worker-lease",
    source_agent_id: "web", control_id: "lock-control", reminder_id: "reminder-1", action: "clear",
    expires_at: "2030-01-01T00:00:30.000Z",
  };
  assert.deepEqual(validateServerFrame(forwarded), forwarded);
  assert.throws(() => validateServerFrame({ ...forwarded, expires_at: "2030-01-01T00:00:30Z" }));
  assert.throws(() => validateServerFrame({ ...forwarded, extra: true }));

  const result = {
    version: "v1", frame_id: "lock-result", type: "reminder_lock_control_result",
    agent_id: "worker", instance_id: "worker-instance", lease_id: "worker-lease",
    source_agent_id: "web", control_id: "lock-control", reminder_id: "reminder-1",
    accepted: false, error_code: "reminder_locked",
  };
  assert.deepEqual(validateClientFrame(result), result);
  assert.deepEqual(validateServerFrame(result), result);
  assert.throws(() => validateClientFrame({ ...result, accepted: false, error_code: undefined }));
});

test("router relays owned reminder locks with exact identity and correlation fencing", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "reminder-lock-transport-"));
  let now = new Date("2030-01-01T00:00:00.000Z");
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => now });
  const router = createAgentRouter({ repository, clock: () => now, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  t.after(async () => { for (const socket of sockets) socket.close(); await router.close(); repository.close(); await rm(directory, { recursive: true, force: true }); });

  const url = await router.listen();
  const web = await register(url, "web", "web-instance", "user_interaction");
  const worker = await register(url, "worker", "worker-instance");
  const other = await register(url, "other", "other-instance");
  sockets.push(web.socket, worker.socket, other.socket);
  repository.createReminder({ id: "reminder-1", target: "worker", creator: "web", body: "opaque", created_at: now.toISOString(), next_due_at: "2030-01-02T00:00:00.000Z", interval_ms: null });

  const roleSource = await register(url, "agent-source", "agent-instance");
  sockets.push(roleSource.socket);
  const roleError = nextFrame(roleSource.socket, (frame) => frame.type === "error");
  roleSource.socket.send(JSON.stringify(control({ agent_id: "agent-source", instance_id: "agent-instance", lease_id: roleSource.registered.lease_id, control_id: "role-control" })));
  assert.equal((await roleError).code, "reminder_lock_control_not_allowed");

  const ownerError = nextFrame(web.socket, (frame) => frame.type === "error");
  web.socket.send(JSON.stringify(control({ frame_id: "wrong-owner", control_id: "wrong-owner", target_agent_id: "other", target_instance_id: "other-instance" , lease_id: web.registered.lease_id })));
  assert.equal((await ownerError).code, "reminder_target_mismatch");

  const forwardedPromise = nextFrame(worker.socket, (frame) => frame.type === "reminder_lock_control");
  web.socket.send(JSON.stringify(control({ lease_id: web.registered.lease_id })));
  const forwarded = await forwardedPromise;
  assert.equal(forwarded.agent_id, "worker");
  assert.equal(forwarded.lease_id, worker.registered.lease_id);
  assert.equal(forwarded.expires_at, "2030-01-01T00:00:30.000Z");

  const resultPromise = nextFrame(web.socket, (frame) => frame.type === "reminder_lock_control_result");
  worker.socket.send(JSON.stringify({ version: "v1", frame_id: "worker-result", type: "reminder_lock_control_result", agent_id: forwarded.agent_id, instance_id: forwarded.instance_id, lease_id: forwarded.lease_id, source_agent_id: forwarded.source_agent_id, control_id: forwarded.control_id, reminder_id: forwarded.reminder_id, accepted: false, error_code: "reminder_locked" }));
  const result = await resultPromise;
  assert.equal(result.control_id, "lock-control");
  assert.equal(result.error_code, "reminder_locked");
  const duplicateError = nextFrame(worker.socket, (frame) => frame.type === "error");
  worker.socket.send(JSON.stringify({ version: "v1", frame_id: "duplicate-result", type: "reminder_lock_control_result", agent_id: forwarded.agent_id, instance_id: forwarded.instance_id, lease_id: forwarded.lease_id, source_agent_id: forwarded.source_agent_id, control_id: forwarded.control_id, reminder_id: forwarded.reminder_id, accepted: true }));
  assert.equal((await duplicateError).code, "reminder_lock_control_result_not_pending");
  assert.equal(Object.keys(repository.reminderFor("reminder-1")).some((key) => key.includes("lock")), false);
  assert.equal(Object.keys(repository.reminders()[0]).some((key) => key.includes("lock")), false);
});

test("reminder lock results arriving during forwarding are correlated", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "reminder-lock-immediate-result-"));
  let now = new Date("2030-01-01T00:00:00.000Z");
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => now });
  const router = createAgentRouter({ repository, clock: () => now, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  const originalSend = WebSocket.prototype.send;
  t.after(async () => {
    WebSocket.prototype.send = originalSend;
    for (const socket of sockets) socket.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  });

  const url = await router.listen();
  const web = await register(url, "web", "web-instance", "user_interaction");
  const worker = await register(url, "worker", "worker-instance");
  sockets.push(web.socket, worker.socket);
  repository.createReminder({ id: "reminder-1", target: "worker", creator: "web", body: "opaque", created_at: now.toISOString(), next_due_at: "2030-01-02T00:00:00.000Z", interval_ms: null });

  WebSocket.prototype.send = function sendWithImmediateResult(data, ...args) {
    const sent = originalSend.call(this, data, ...args);
    if (this._isServer) {
      const forwarded = JSON.parse(data.toString("utf8"));
      if (forwarded.type === "reminder_lock_control") {
        this.emit("message", Buffer.from(JSON.stringify({
          version: "v1",
          frame_id: "immediate-result",
          type: "reminder_lock_control_result",
          agent_id: forwarded.agent_id,
          instance_id: forwarded.instance_id,
          lease_id: forwarded.lease_id,
          source_agent_id: forwarded.source_agent_id,
          control_id: forwarded.control_id,
          reminder_id: forwarded.reminder_id,
          accepted: false,
          error_code: "reminder_locked",
        })), false);
      }
    }
    return sent;
  };

  const resultPromise = nextFrame(web.socket, (frame) => frame.type === "reminder_lock_control_result");
  web.socket.send(JSON.stringify(control({ lease_id: web.registered.lease_id })));
  const result = await resultPromise;
  assert.equal(result.control_id, "lock-control");
  assert.equal(result.error_code, "reminder_locked");
});

test("reminder lock pending controls expire and disconnect without replay", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "reminder-lock-expiry-"));
  let now = new Date("2030-01-01T00:00:00.000Z");
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock: () => now });
  const router = createAgentRouter({ repository, clock: () => now, pendingModelControlTtlMs: 100, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  t.after(async () => { for (const socket of sockets) socket.close(); await router.close(); repository.close(); await rm(directory, { recursive: true, force: true }); });
  const url = await router.listen();
  const web = await register(url, "web", "web-instance", "user_interaction");
  const worker = await register(url, "worker", "worker-instance");
  sockets.push(web.socket, worker.socket);
  repository.createReminder({ id: "reminder-1", target: "worker", creator: "web", body: "opaque", created_at: now.toISOString(), next_due_at: "2030-01-02T00:00:00.000Z", interval_ms: null });

  const forwardedPromise = nextFrame(worker.socket, (frame) => frame.type === "reminder_lock_control");
  web.socket.send(JSON.stringify(control({ lease_id: web.registered.lease_id })));
  const forwarded = await forwardedPromise;
  now = new Date("2030-01-01T00:00:00.101Z");
  const expiry = nextFrame(web.socket, (frame) => frame.type === "error");
  router.sweepMessageLifecycles();
  assert.equal((await expiry).code, "reminder_lock_control_expired");
  const stale = nextFrame(worker.socket, (frame) => frame.type === "error");
  worker.socket.send(JSON.stringify({ version: "v1", frame_id: "stale-result", type: "reminder_lock_control_result", agent_id: forwarded.agent_id, instance_id: forwarded.instance_id, lease_id: forwarded.lease_id, source_agent_id: forwarded.source_agent_id, control_id: forwarded.control_id, reminder_id: forwarded.reminder_id, accepted: true }));
  assert.equal((await stale).code, "reminder_lock_control_result_not_pending");

  const secondForward = nextFrame(worker.socket, (frame) => frame.type === "reminder_lock_control");
  web.socket.send(JSON.stringify(control({ frame_id: "disconnect-request", control_id: "disconnect-control", lease_id: web.registered.lease_id })));
  const pending = await secondForward;
  const targetFailure = nextFrame(web.socket, (frame) => frame.type === "error");
  worker.socket.close();
  assert.equal((await targetFailure).code, "target_not_connected");
  const replacement = await register(url, "worker", "worker-instance");
  sockets.push(replacement.socket);
  await assert.rejects(nextFrame(replacement.socket, (frame) => frame.type === "reminder_lock_control", 100), /frame timeout/);
  assert.equal(pending.reminder_id, "reminder-1");
});
