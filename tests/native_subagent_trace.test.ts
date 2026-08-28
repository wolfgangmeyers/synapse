import assert from "node:assert/strict";
import { once } from "node:events";
import { existsSync, readFileSync, statSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createNativeSubagentTrace as createRouterTrace } from "../harness/native_subagent_trace.js";
import { createAgentRouter } from "../harness/router.js";
import {
  createMechaMessagingExtension,
  createNativeSubagentTrace as createExtensionTrace,
} from "../pi-extensions/mecha-messaging/index.ts";

async function temporaryDirectory(t) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-native-subagent-trace-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}

function readRecords(path) {
  return readFileSync(path, "utf8").trim().split("\n").map(JSON.parse);
}

for (const [name, createTrace] of [["extension", createExtensionTrace], ["router", createRouterTrace]]) {
  test(`${name} lifecycle trace is opt-in, redacted, and size-capped`, async (t) => {
    const directory = await temporaryDirectory(t);
    const path = join(directory, `${name}.jsonl`);
    const disabled = createTrace({ environment: {} });
    assert.equal(disabled.enabled, false);
    assert.equal(disabled.record("test_disabled", { body: "private body" }), false);
    assert.equal(existsSync(path), false);

    const trace = createTrace({ path, maxBytes: 400, clock: () => new Date("2030-01-01T00:00:00.000Z"), now: () => new Date("2030-01-01T00:00:00.000Z") });
    assert.equal(trace.enabled, true);
    assert.equal(trace.record("test_record", {
      agent_id: "parent", parent_session_id: "session-1", body: "private body", token: "credential", raw_payload: "raw",
      subagents: [{ id: "child", type: "general-purpose", status: "running", description: "private child instructions" }],
    }), true);
    for (let index = 0; index < 50; index += 1) {
      assert.equal(trace.record("test_record", { agent_id: `parent-${index}`, subagents: [{ id: `child-${index}`, description: "private child instructions" }] }), true);
    }

    const text = [path, `${path}.1`].filter(existsSync).map((candidate) => readFileSync(candidate, "utf8")).join("");
    assert.doesNotMatch(text, /private body|credential|raw|private child instructions/);
    assert.ok(existsSync(`${path}.1`), "the trace keeps one rotated generation");
    assert.ok(statSync(path).size < 700);
    assert.ok(statSync(`${path}.1`).size < 700);
    const newest = readRecords(path).at(-1);
    assert.deepEqual(newest.subagents, [{ id: "child-49", description_length: 26 }]);
  });
}

test("extension traces native service lookup, snapshot summaries, and publish attempts", () => {
  const handlers = new Map();
  const childEvents = new Map();
  const timers = [];
  const records = [];
  const service = {
    getLifecycleSnapshots: () => [{
      id: "native-child", type: "general-purpose", status: "running", description: "private scheduled prompt",
    }],
  };
  const pi = {
    events: { on(type, handler) { childEvents.set(type, handler); } },
    on(type, handler) { handlers.set(type, handler); },
    registerCommand() {}, registerTool() {}, sendMessage() {}, exec: async () => ({ code: 0, stderr: "" }),
  };
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "parent",
    createClient: () => ({ connect: async () => {}, close() {}, sendReceipt() {}, sendLifecycleSnapshot: () => "lifecycle-event" }),
    getSubagentsService: () => service,
    nativeSubagentTrace: { record: (event, fields) => { records.push({ event, ...fields }); return true; } },
    setIntervalImpl: (callback) => { timers.push(callback); return { unref() {} }; },
    clearIntervalImpl() {},
  });
  const context = {
    sessionManager: { getSessionId: () => "parent-session" },
    ui: { notify() {}, setStatus() {} },
  };
  handlers.get("session_start")({}, context);
  assert.equal(timers.length, 1);
  timers[0]();

  assert.ok(records.some((record) => record.event === "extension_service_lookup" && record.parent_session_id === "parent-session" && record.available));
  assert.ok(records.some((record) => record.event === "extension_snapshot_read" && record.snapshot_count === 1));
  assert.ok(records.some((record) => record.event === "extension_snapshot_publish" && record.published));
  assert.doesNotMatch(JSON.stringify(records), /private scheduled prompt/);
});

test("router traces lifecycle validation, persistence, acknowledgement, and projection without payload text", async (t) => {
  const directory = await temporaryDirectory(t);
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const records = [];
  const router = createAgentRouter({
    repository,
    timeoutSweepIntervalMs: 60_000,
    nativeSubagentTrace: { record: (event, fields) => { records.push({ event, ...fields }); return true; } },
  });
  t.after(async () => { await router.close(); repository.close(); });
  const url = await router.listen();
  const socket = new WebSocket(url);
  t.after(() => socket.close());
  await once(socket, "open");
  socket.send(JSON.stringify({ version: "v1", frame_id: "register", type: "register", agent_id: "parent", instance_id: "instance" }));
  const registered = await nextFrame(socket, "registered");
  socket.send(JSON.stringify({
    version: "v1", frame_id: "snapshot", type: "subagent_lifecycle_snapshot", agent_id: "parent", instance_id: "instance",
    parent_session_id: "session-1", lease_id: registered.lease_id, producer_epoch: "11111111-1111-4111-8111-111111111111",
    client_sequence: 1, event_id: "event-1", subagents: [{ id: "child", type: "general-purpose", status: "running", description: "private scheduled prompt" }],
  }));
  await nextFrame(socket, "subagent_lifecycle_acknowledged");

  for (const event of ["router_lifecycle_validated", "router_lifecycle_received", "router_lifecycle_persist_attempt", "router_lifecycle_persisted", "router_lifecycle_acknowledged", "router_lifecycle_projected"]) {
    assert.ok(records.some((record) => record.event === event), `missing ${event}`);
  }
  assert.doesNotMatch(JSON.stringify(records), /private scheduled prompt|11111111-1111-4111-8111-111111111111|lease_id/);
});

async function nextFrame(socket, type) {
  while (true) {
    const [payload] = await once(socket, "message");
    const frame = JSON.parse(payload.toString("utf8"));
    if (frame.type === type) return frame;
  }
}
