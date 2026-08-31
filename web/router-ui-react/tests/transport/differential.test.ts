import { afterEach, describe, expect, it } from "vitest";
import {
  EXPECTED_CAPABILITIES,
  EXPECTED_GOAL_MIGRATIONS,
  FIXTURE_TIME,
  fixtureGoalSettings,
  fixtureMessage,
  startIsolatedGoRouter,
} from "../support/isolated-go-router.mjs";
import {
  connectFixturePeer,
  sendFixtureReceipt,
  startIsolatedNodeRouter,
  waitForWebSocketFrame,
} from "../support/node-router.mjs";

type IsolatedRouter = Awaited<ReturnType<typeof startIsolatedGoRouter>>;
type JsonRecord = Record<string, unknown>;

const runningRouters: Array<IsolatedRouter | Awaited<ReturnType<typeof startIsolatedNodeRouter>>> = [];

afterEach(async () => {
  while (runningRouters.length > 0) await runningRouters.pop()?.close();
});

function record(value: unknown): JsonRecord {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("expected an object");
  const result: JsonRecord = {};
  for (const [key, entry] of Object.entries(value)) result[key] = entry;
  return result;
}

function stringValue(value: unknown, field: string): string {
  if (typeof value !== "string") throw new Error(`expected ${field} to be a string`);
  return value;
}

function numberValue(value: unknown, field: string): number {
  if (typeof value !== "number") throw new Error(`expected ${field} to be a number`);
  return value;
}

function stringArray(value: unknown, field: string): string[] {
  if (!Array.isArray(value)) throw new Error(`expected ${field} to be a string array`);
  const strings: string[] = [];
  for (const entry of value) {
    if (typeof entry !== "string") throw new Error(`expected ${field} to be a string array`);
    strings.push(entry);
  }
  return strings;
}

async function jsonRecord(response: Response): Promise<JsonRecord> {
  const value: unknown = await response.json();
  return record(value);
}

async function requestSummary(router: IsolatedRouter) {
  const agents = await fetch(new URL("/v1/agents", router.httpUrl), { headers: { Origin: "https://diff.example" } });
  const agentsBody = await jsonRecord(agents);
  const agentList = agentsBody.agents;
  if (!Array.isArray(agentList)) throw new Error("agents response has no agent list");
  const preflight = await fetch(new URL("/v1/agents", router.httpUrl), { method: "OPTIONS", headers: { Origin: "https://diff.example" } });
  const noOrigin = await fetch(new URL("/v1/agents", router.httpUrl), { method: "OPTIONS" });
  const noOriginBody = await jsonRecord(noOrigin);
  const health = await fetch(new URL("/v1/health", router.httpUrl));
  const healthBody = await jsonRecord(health);
  const capabilities = await fetch(new URL("/v1/capabilities", router.httpUrl));
  const capabilitiesBody = await jsonRecord(capabilities);
  const peer = await connectFixturePeer(router, { agent_id: "diff-agent", instance_id: "diff-instance", frame_id: "register-diff" });
  try {
    const registered = record(peer.registered);
    peer.send({ version: "v1", frame_id: "ping-diff", type: "ping" });
    const pong = record(await waitForWebSocketFrame(peer.socket, (frame) => {
      const candidate = record(frame);
      return candidate.type === "pong" && candidate.request_id === "ping-diff";
    }));
    const message = fixtureMessage("diff-message", "diff-agent", { from: "diff-sender", body: "same deterministic body", created_at: FIXTURE_TIME });
    const enqueue = await fetch(new URL("/v1/messages", router.httpUrl), {
      method: "POST",
      headers: { "content-type": "application/json", "x-correlation-id": "corr-diff" },
      body: JSON.stringify(message),
    });
    const enqueueBody = await jsonRecord(enqueue);
    const offered = record(await waitForWebSocketFrame(peer.socket, (frame) => {
      const candidate = record(frame);
      const offeredMessage = record(candidate.message);
      return candidate.type === "message" && offeredMessage.id === "diff-message";
    }));
    const offeredMessage = record(offered.message);
    const offeredAttempt = numberValue(offered.attempt_number, "attempt_number");
    sendFixtureReceipt(peer, "injection_requested", "diff-message", offeredAttempt, { frame_id: "injection-diff" });
    const acknowledgement = waitForWebSocketFrame(peer.socket, (frame) => {
      const candidate = record(frame);
      return candidate.type === "delivery_acknowledged" && candidate.request_id === "ack-diff";
    });
    sendFixtureReceipt(peer, "ack", "diff-message", offeredAttempt, { frame_id: "ack-diff" });
    const acknowledged = record(await acknowledgement);
    return {
      agents: { status: agents.status, cors: agents.headers.get("access-control-allow-origin"), count: agentList.length },
      preflight: { status: preflight.status, cors: preflight.headers.get("access-control-allow-origin") },
      noOrigin: { status: noOrigin.status, code: record(noOriginBody.error).code },
      health: { status: health.status, migrations: EXPECTED_GOAL_MIGRATIONS.filter((id) => stringArray(healthBody.schema_migrations, "schema_migrations").includes(id)) },
      capabilities: Object.fromEntries(EXPECTED_CAPABILITIES.map((key) => [key, capabilitiesBody[key]])),
      registration: { type: stringValue(registered.type, "registered.type"), request_id: stringValue(registered.request_id, "registered.request_id"), agent_id: stringValue(registered.agent_id, "registered.agent_id"), instance_id: stringValue(registered.instance_id, "registered.instance_id") },
      pong: { type: stringValue(pong.type, "pong.type"), request_id: stringValue(pong.request_id, "pong.request_id") },
      enqueue: { status: enqueue.status, correlation: enqueue.headers.get("x-correlation-id"), id: stringValue(record(enqueueBody.message ?? enqueueBody.conversation_message).id, "message.id") },
      offer: { type: stringValue(offered.type, "offer.type"), id: stringValue(offeredMessage.id, "offer.message.id"), attempt: offeredAttempt, hasIncarnation: typeof offered.message_incarnation_id === "string" },
      acknowledged: { type: stringValue(acknowledged.type, "acknowledged.type"), request_id: stringValue(acknowledged.request_id, "acknowledged.request_id"), id: stringValue(acknowledged.message_id, "acknowledged.message_id"), attempt: numberValue(acknowledged.attempt_number, "acknowledged.attempt_number"), hasIncarnation: typeof acknowledged.message_incarnation_id === "string" },
    };
  } finally {
    await peer.close();
  }
}

type RawSocket = globalThis.WebSocket;

function decodeRawMessage(data: unknown): unknown {
  if (typeof data === "string") return JSON.parse(data);
  if (data instanceof ArrayBuffer) return JSON.parse(new TextDecoder().decode(new Uint8Array(data)));
  if (ArrayBuffer.isView(data)) return JSON.parse(new TextDecoder().decode(new Uint8Array(data.buffer, data.byteOffset, data.byteLength)));
  throw new Error("unexpected WebSocket message data");
}

function openRawSocket(url: string): Promise<RawSocket> {
  return new Promise<RawSocket>((resolveSocket, rejectSocket) => {
    const socket = new globalThis.WebSocket(url);
    socket.addEventListener("open", () => resolveSocket(socket), { once: true });
    socket.addEventListener("error", () => rejectSocket(new Error("WebSocket connection failed")), { once: true });
  });
}

function nextRawFrame(socket: RawSocket, predicate: (frame: unknown) => boolean = () => true, timeoutMs = 10_000): Promise<unknown> {
  return new Promise<unknown>((resolveFrame, rejectFrame) => {
    const timer = setTimeout(() => {
      socket.removeEventListener("message", onMessage);
      rejectFrame(new Error("timed out waiting for router WebSocket frame"));
    }, timeoutMs);
    const onMessage = (event: MessageEvent<unknown>) => {
      let frame: unknown;
      try { frame = decodeRawMessage(event.data); } catch (error) {
        clearTimeout(timer);
        socket.removeEventListener("message", onMessage);
        rejectFrame(error instanceof Error ? error : new Error(String(error)));
        return;
      }
      if (!predicate(frame)) return;
      clearTimeout(timer);
      socket.removeEventListener("message", onMessage);
      resolveFrame(frame);
    };
    socket.addEventListener("message", onMessage);
  });
}

async function connectRawPeer(router: IsolatedRouter, registration: JsonRecord): Promise<{ socket: RawSocket; registered: JsonRecord; next: (predicate?: (frame: unknown) => boolean) => Promise<unknown> }> {
  const socket = await openRawSocket(router.websocketUrl);
  const buffered: unknown[] = [];
  socket.addEventListener("message", (event: MessageEvent<unknown>) => {
    try { buffered.push(decodeRawMessage(event.data)); } catch { /* nextRawFrame reports malformed payloads */ }
  });
  const registeredFrame = nextRawFrame(socket, (frame) => {
    const candidate = record(frame);
    return candidate.type === "registered" && candidate.request_id === registration.frame_id;
  });
  socket.send(JSON.stringify(registration));
  const registered = record(await registeredFrame);
  const next = (predicate: (frame: unknown) => boolean = () => true): Promise<unknown> => {
    const index = buffered.findIndex(predicate);
    if (index >= 0) return Promise.resolve(buffered.splice(index, 1)[0]);
    return nextRawFrame(socket, predicate);
  };
  return { socket, registered, next };
}

async function closeSocket(socket: RawSocket): Promise<void> {
  if (socket.readyState === 3) return;
  await new Promise<void>((resolveClose) => {
    socket.addEventListener("close", () => resolveClose(), { once: true });
    socket.close(1000, "fixture complete");
  });
}

async function waitForCloseCode(socket: RawSocket): Promise<number> {
  return new Promise<number>((resolveClose) => socket.addEventListener("close", (event) => resolveClose(event.code), { once: true }));
}

async function expectNoFrame(socket: RawSocket, predicate: (frame: unknown) => boolean): Promise<void> {
  await expect(nextRawFrame(socket, predicate, 200)).rejects.toThrow("timed out waiting for router WebSocket frame");
}

async function httpOutcome(router: IsolatedRouter, path: string, init: RequestInit = {}): Promise<{ status: number; code?: string; correlation: string | null; cors: string | null; allowMethods: string | null; allowHeaders: string | null }> {
  const response = await fetch(new URL(path, router.httpUrl), init);
  let code: string | undefined;
  if (response.status !== 204) {
    const body = await jsonRecord(response);
    const error = body.error;
    if (error !== undefined) code = stringValue(record(error).code, "error.code");
  }
  return {
    status: response.status,
    ...(code === undefined ? {} : { code }),
    correlation: response.headers.get("x-correlation-id"),
    cors: response.headers.get("access-control-allow-origin"),
    allowMethods: response.headers.get("access-control-allow-methods"),
    allowHeaders: response.headers.get("access-control-allow-headers"),
  };
}

describe("isolated Node and Go router differential", () => {
  it("matches CORS, HTTP errors, and correlation headers", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const outcomes = async (router: IsolatedRouter) => ({
      preflight: await httpOutcome(router, "/v1/agents", { method: "OPTIONS", headers: { Origin: "https://diff.example", "X-Correlation-Id": "corr-preflight", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type" } }),
      noOrigin: await httpOutcome(router, "/v1/agents", { method: "OPTIONS", headers: { "X-Correlation-Id": "corr-no-origin" } }),
      missing: await httpOutcome(router, "/v1/missing", { headers: { Origin: "https://diff.example", "X-Request-Id": "req-http-missing" } }),
      wrongContentType: await httpOutcome(router, "/v1/messages", { method: "POST", headers: { Origin: "https://diff.example", "Content-Type": "text/plain", "X-Correlation-Id": "corr-http-invalid" }, body: "{}" }),
      missingConversation: await httpOutcome(router, "/v1/conversations?owner=one", { headers: { Origin: "https://diff.example", "X-Correlation-Id": "corr-conversation-invalid" } }),
    });
    const nodeResult = await outcomes(node);
    const goResult = await outcomes(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.preflight).toEqual({ status: 204, correlation: "corr-preflight", cors: "*", allowMethods: "GET, POST, PATCH, DELETE, OPTIONS", allowHeaders: "content-type" });
    expect(nodeResult.noOrigin).toEqual({ status: 404, code: "not_found", correlation: "corr-no-origin", cors: null, allowMethods: null, allowHeaders: null });
    expect(nodeResult.missing).toEqual({ status: 404, code: "not_found", correlation: "req-http-missing", cors: "*", allowMethods: "GET, POST, PATCH, DELETE, OPTIONS", allowHeaders: "content-type" });
    expect(nodeResult.wrongContentType).toEqual({ status: 400, code: "invalid_request", correlation: "corr-http-invalid", cors: "*", allowMethods: "GET, POST, PATCH, DELETE, OPTIONS", allowHeaders: "content-type" });
    expect(nodeResult.missingConversation).toEqual({ status: 400, code: "invalid_request", correlation: "corr-conversation-invalid", cors: "*", allowMethods: "GET, POST, PATCH, DELETE, OPTIONS", allowHeaders: "content-type" });
  }, 120_000);

  it("matches registration correlation, lease rejection, replacement, and malformed frames", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const exercise = async (router: IsolatedRouter) => {
      const first = await connectRawPeer(router, { version: "v1", frame_id: "register-duplicate", type: "register", agent_id: "duplicate-agent", instance_id: "duplicate-one" });
      const duplicateSocket = await openRawSocket(router.websocketUrl);
      const duplicateClosed = waitForCloseCode(duplicateSocket);
      const rejectedFrame = nextRawFrame(duplicateSocket, (frame) => record(frame).type === "lease_rejected");
      duplicateSocket.send(JSON.stringify({ version: "v1", frame_id: "register-duplicate-two", type: "register", agent_id: "duplicate-agent", instance_id: "duplicate-two" }));
      const rejected = record(await rejectedFrame);
      const duplicateCloseCode = await duplicateClosed;
      await closeSocket(first.socket);

      const replacementOld = await connectRawPeer(router, { version: "v1", frame_id: "register-replacement-old", type: "register", agent_id: "replacement-agent", instance_id: "replacement-old", conversation_sync: "v1" });
      const replacementNew = await connectRawPeer(router, { version: "v1", frame_id: "register-replacement-new", type: "register", agent_id: "replacement-agent", instance_id: "replacement-old", conversation_sync: "v1" });
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 1_000));
      const replacementCloseCode = replacementOld.socket.readyState === 3 ? 4001 : undefined;
      const alreadyRegisteredFrame = nextRawFrame(replacementNew.socket, (frame) => record(frame).type === "error" && record(frame).request_id === "register-again");
      replacementNew.socket.send(JSON.stringify({ version: "v1", frame_id: "register-again", type: "register", agent_id: "replacement-agent", instance_id: "replacement-old", conversation_sync: "v1" }));
      const alreadyRegistered = record(await alreadyRegisteredFrame);
      await closeSocket(replacementNew.socket);

      const malformed = await connectRawPeer(router, { version: "v1", frame_id: "register-malformed", type: "register", agent_id: "malformed-agent", instance_id: "malformed-instance" });
      const malformedTextFrame = malformed.next((frame) => record(frame).type === "error");
      malformed.socket.send("not-json");
      const malformedText = record(await malformedTextFrame);
      const malformedBinaryFrame = malformed.next((frame) => record(frame).type === "error");
      malformed.socket.send(new Uint8Array([0xff, 0xfe]));
      const malformedBinary = record(await malformedBinaryFrame);
      await closeSocket(malformed.socket);
      return {
        duplicate: { type: stringValue(rejected.type, "lease_rejected.type"), request_id: stringValue(rejected.request_id, "lease_rejected.request_id"), code: stringValue(rejected.code, "lease_rejected.code"), agent_id: stringValue(rejected.agent_id, "lease_rejected.agent_id"), instance_id: stringValue(rejected.instance_id, "lease_rejected.instance_id"), active_instance_id: stringValue(rejected.active_instance_id, "lease_rejected.active_instance_id"), close_code: duplicateCloseCode },
        replacement: { registered_instance_id: stringValue(replacementNew.registered.instance_id, "replacement.instance_id"), old_close_code: replacementCloseCode, already_registered: { type: stringValue(alreadyRegistered.type, "already_registered.type"), code: stringValue(alreadyRegistered.code, "already_registered.code"), request_id: stringValue(alreadyRegistered.request_id, "already_registered.request_id") } },
        malformed: { text: { type: stringValue(malformedText.type, "malformed.text.type"), code: stringValue(malformedText.code, "malformed.text.code") }, binary: { type: stringValue(malformedBinary.type, "malformed.binary.type"), code: stringValue(malformedBinary.code, "malformed.binary.code") } },
      };
    };
    const nodeResult = await exercise(node);
    const goResult = await exercise(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.duplicate).toMatchObject({ type: "lease_rejected", request_id: "register-duplicate-two", code: "duplicate_identity", agent_id: "duplicate-agent", instance_id: "duplicate-two", active_instance_id: "duplicate-one", close_code: 4009 });
    expect(nodeResult.replacement).toMatchObject({ registered_instance_id: "replacement-old", old_close_code: 4001, already_registered: { type: "error", code: "already_registered", request_id: "register-again" } });
    expect(nodeResult.malformed).toEqual({ text: { type: "error", code: "invalid_frame" }, binary: { type: "error", code: "invalid_frame" } });
  }, 120_000);

  it("matches lifecycle acknowledgements, dispositions, broadcasts, and replay", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const exercise = async (router: IsolatedRouter) => {
      const producer = await connectRawPeer(router, { version: "v1", frame_id: "register-lifecycle-producer", type: "register", agent_id: "lifecycle-parent", instance_id: "lifecycle-instance", conversation_sync: "v1" });
      const enableSnapshot = producer.next((frame) => record(frame).type === "subagent_state_snapshot");
      producer.socket.send(JSON.stringify({ version: "v1", frame_id: "enable-lifecycle", type: "subagent_sync_enable", agent_id: "lifecycle-parent", instance_id: "lifecycle-instance", lease_id: producer.registered.lease_id }));
      const initial = record(await enableSnapshot);
      const base = { version: "v1", type: "subagent_lifecycle_snapshot", agent_id: "lifecycle-parent", instance_id: "lifecycle-instance", parent_session_id: "lifecycle-session", lease_id: producer.registered.lease_id, producer_epoch: "123e4567-e89b-42d3-a456-426614174000", client_sequence: 1, event_id: "lifecycle-event-1", subagents: [{ id: "lifecycle-child", type: "worker", description: "Lifecycle child", status: "running" }] };
      const appliedUpdate = producer.next((frame) => record(frame).type === "subagent_state_update");
      const appliedAck = producer.next((frame) => record(frame).type === "subagent_lifecycle_acknowledged" && record(frame).request_id === "lifecycle-frame-1");
      producer.socket.send(JSON.stringify({ ...base, frame_id: "lifecycle-frame-1" }));
      const applied = record(await appliedAck);
      const update = record(await appliedUpdate);

      const duplicateAck = producer.next((frame) => record(frame).type === "subagent_lifecycle_acknowledged" && record(frame).request_id === "lifecycle-frame-duplicate");
      producer.socket.send(JSON.stringify({ ...base, frame_id: "lifecycle-frame-duplicate" }));
      const duplicate = record(await duplicateAck);
      await expectNoFrame(producer.socket, (frame) => record(frame).type === "subagent_state_update" && record(record(frame).agent).subagent_version === 1);

      const conflictAck = producer.next((frame) => record(frame).type === "subagent_lifecycle_acknowledged" && record(frame).request_id === "lifecycle-frame-conflict");
      producer.socket.send(JSON.stringify({ ...base, frame_id: "lifecycle-frame-conflict", subagents: [{ id: "lifecycle-child", type: "worker", description: "Changed payload", status: "running" }] }));
      const conflict = record(await conflictAck);

      const terminalAck = producer.next((frame) => record(frame).type === "subagent_lifecycle_acknowledged" && record(frame).request_id === "lifecycle-frame-terminal");
      const terminalUpdate = producer.next((frame) => record(frame).type === "subagent_state_update" && record(record(frame).agent).subagent_version === 2);
      producer.socket.send(JSON.stringify({ version: "v1", frame_id: "lifecycle-frame-terminal", type: "subagent_lifecycle_event", agent_id: "lifecycle-parent", instance_id: "lifecycle-instance", parent_session_id: "lifecycle-session", lease_id: producer.registered.lease_id, producer_epoch: base.producer_epoch, client_sequence: 2, event_id: "lifecycle-event-2", lifecycle_event: "terminal", terminal_reason: "completed", subagent: { id: "lifecycle-child", type: "worker", description: "Lifecycle child", status: "completed" } }));
      const terminal = record(await terminalAck);
      const terminalBroadcast = record(await terminalUpdate);

      const staleAck = producer.next((frame) => record(frame).type === "subagent_lifecycle_acknowledged" && record(frame).request_id === "lifecycle-frame-stale");
      producer.socket.send(JSON.stringify({ ...base, frame_id: "lifecycle-frame-stale", event_id: "lifecycle-event-stale", subagents: [] }));
      const stale = record(await staleAck);

      const observer = await connectRawPeer(router, { version: "v1", frame_id: "register-lifecycle-observer", type: "register", agent_id: "lifecycle-observer", instance_id: "observer-instance", conversation_sync: "v1" });
      const replayFrame = observer.next((frame) => record(frame).type === "subagent_state_replay");
      observer.socket.send(JSON.stringify({ version: "v1", frame_id: "resync-lifecycle", type: "subagent_state_resync", agent_id: "lifecycle-observer", instance_id: "observer-instance", lease_id: observer.registered.lease_id, after_version: 1 }));
      const replay = record(await replayFrame);
      await closeSocket(observer.socket);
      await closeSocket(producer.socket);
      const agents = replay.updates;
      if (!Array.isArray(agents)) throw new Error("lifecycle replay omitted updates");
      return {
        initial: { type: stringValue(initial.type, "initial.type"), router_version: numberValue(initial.router_version, "initial.router_version"), agents: Array.isArray(initial.agents) ? initial.agents.length : -1 },
        applied: { disposition: stringValue(applied.disposition, "applied.disposition"), router_version: numberValue(applied.router_version, "applied.router_version"), event_id: stringValue(applied.event_id, "applied.event_id"), broadcast_type: stringValue(update.type, "update.type") },
        duplicate: { disposition: stringValue(duplicate.disposition, "duplicate.disposition"), router_version: numberValue(duplicate.router_version, "duplicate.router_version") },
        conflict: { disposition: stringValue(conflict.disposition, "conflict.disposition"), router_version: numberValue(conflict.router_version, "conflict.router_version") },
        terminal: { disposition: stringValue(terminal.disposition, "terminal.disposition"), router_version: numberValue(terminal.router_version, "terminal.router_version"), broadcast_version: numberValue(record(terminalBroadcast.agent).subagent_version, "terminal.broadcast_version") },
        stale: { disposition: stringValue(stale.disposition, "stale.disposition"), router_version: numberValue(stale.router_version, "stale.router_version") },
        replay: { type: stringValue(replay.type, "replay.type"), router_version: numberValue(replay.router_version, "replay.router_version"), update_count: agents.length, parent_ids: agents.map((agent) => stringValue(record(agent).agent_id, "replay.agent_id")), versions: agents.map((agent) => numberValue(record(agent).subagent_version, "replay.subagent_version")) },
      };
    };
    const nodeResult = await exercise(node);
    const goResult = await exercise(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.initial).toEqual({ type: "subagent_state_snapshot", router_version: 0, agents: 0 });
    expect(nodeResult.applied).toEqual({ disposition: "applied", router_version: 1, event_id: "lifecycle-event-1", broadcast_type: "subagent_state_update" });
    expect(nodeResult.duplicate).toEqual({ disposition: "duplicate", router_version: 1 });
    expect(nodeResult.conflict).toEqual({ disposition: "conflict", router_version: 1 });
    expect(nodeResult.terminal).toEqual({ disposition: "applied", router_version: 2, broadcast_version: 2 });
    expect(nodeResult.stale).toEqual({ disposition: "stale", router_version: 2 });
    expect(nodeResult.replay).toEqual({ type: "subagent_state_replay", router_version: 2, update_count: 1, parent_ids: ["lifecycle-parent"], versions: [2] });
  }, 120_000);

  it("matches goal and reminder-lock forwarding and correlated results", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const exercise = async (router: IsolatedRouter) => {
      const source = await connectRawPeer(router, { version: "v1", frame_id: "register-control-source", type: "register", agent_id: "control-source", instance_id: "control-source-instance", agent_role: "user_interaction", conversation_sync: "v1" });
      const target = await connectRawPeer(router, { version: "v1", frame_id: "register-control-target", type: "register", agent_id: "control-target", instance_id: "control-target-instance", agent_role: "agent" });
      const settings = fixtureGoalSettings();
      const goalState = { enabled: true, goal: "Goal from fixture", ...settings, outcome_counts: { COMPLETED: 1, NOT_DONE: 0 } };
      const forwardedGoalFrame = target.next((frame) => record(frame).type === "goal_control");
      const sourceGoalResultFrame = source.next((frame) => record(frame).type === "goal_control_result" && record(frame).control_id === "goal-control");
      source.socket.send(JSON.stringify({ version: "v1", frame_id: "goal-request", type: "goal_control", agent_id: "control-source", instance_id: "control-source-instance", lease_id: source.registered.lease_id, target_agent_id: "control-target", target_instance_id: "control-target-instance", control_id: "goal-control", action: "set", goal: "Goal from fixture", settings }));
      const forwardedGoal = record(await forwardedGoalFrame);
      target.socket.send(JSON.stringify({ version: "v1", frame_id: "goal-result-frame", type: "goal_control_result", agent_id: "control-target", instance_id: "control-target-instance", lease_id: target.registered.lease_id, source_agent_id: "control-source", control_id: "goal-control", revision: 1, accepted: true, goal_state: goalState }));
      const goalResult = record(await sourceGoalResultFrame);

      const reminderResponse = await fetch(new URL("/v1/reminders", router.httpUrl), { method: "POST", headers: { "content-type": "application/json", "x-correlation-id": "corr-control-reminder" }, body: JSON.stringify({ id: "control-reminder", target: "control-target", creator: "control-source", subject: "Fixture reminder", body: "Reminder body", created_at: FIXTURE_TIME, next_due_at: "2030-01-01T01:00:00.000Z", interval_ms: null }) });
      const reminderBody = await jsonRecord(reminderResponse);
      const forwardedReminderFrame = target.next((frame) => record(frame).type === "reminder_lock_control");
      const sourceReminderResultFrame = source.next((frame) => record(frame).type === "reminder_lock_control_result" && record(frame).control_id === "reminder-control");
      source.socket.send(JSON.stringify({ version: "v1", frame_id: "reminder-request", type: "reminder_lock_control", agent_id: "control-source", instance_id: "control-source-instance", lease_id: source.registered.lease_id, target_agent_id: "control-target", target_instance_id: "control-target-instance", control_id: "reminder-control", reminder_id: "control-reminder", action: "set" }));
      const forwardedReminder = record(await forwardedReminderFrame);
      target.socket.send(JSON.stringify({ version: "v1", frame_id: "reminder-result-frame", type: "reminder_lock_control_result", agent_id: "control-target", instance_id: "control-target-instance", lease_id: target.registered.lease_id, source_agent_id: "control-source", control_id: "reminder-control", reminder_id: "control-reminder", accepted: true }));
      const reminderResult = record(await sourceReminderResultFrame);
      await closeSocket(target.socket);
      await closeSocket(source.socket);
      return {
        goal: { forwarded_type: stringValue(forwardedGoal.type, "forwarded_goal.type"), target_agent_id: stringValue(forwardedGoal.agent_id, "forwarded_goal.agent_id"), source_agent_id: stringValue(forwardedGoal.source_agent_id, "forwarded_goal.source_agent_id"), control_id: stringValue(forwardedGoal.control_id, "forwarded_goal.control_id"), revision: numberValue(forwardedGoal.revision, "forwarded_goal.revision"), result_type: stringValue(goalResult.type, "goal_result.type"), accepted: goalResult.accepted, result_control_id: stringValue(goalResult.control_id, "goal_result.control_id") },
        reminder: { status: reminderResponse.status, correlation: reminderResponse.headers.get("x-correlation-id"), id: stringValue(record(reminderBody.reminder).id, "reminder.id"), forwarded_type: stringValue(forwardedReminder.type, "forwarded_reminder.type"), control_id: stringValue(forwardedReminder.control_id, "forwarded_reminder.control_id"), result_type: stringValue(reminderResult.type, "reminder_result.type"), accepted: reminderResult.accepted, result_control_id: stringValue(reminderResult.control_id, "reminder_result.control_id") },
      };
    };
    const nodeResult = await exercise(node);
    const goResult = await exercise(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.goal).toEqual({ forwarded_type: "goal_control", target_agent_id: "control-target", source_agent_id: "control-source", control_id: "goal-control", revision: 1, result_type: "goal_control_result", accepted: true, result_control_id: "goal-control" });
    expect(nodeResult.reminder).toEqual({ status: 201, correlation: "corr-control-reminder", id: "control-reminder", forwarded_type: "reminder_lock_control", control_id: "reminder-control", result_type: "reminder_lock_control_result", accepted: true, result_control_id: "reminder-control" });
  }, 120_000);

  it("matches conversation broadcasts, reconnect delivery, and history order", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const exercise = async (router: IsolatedRouter) => {
      const registration = { version: "v1", frame_id: "register-history", type: "register", agent_id: "history-target", instance_id: "history-instance", conversation_sync: "v1", ack_confirmation: "correlated_v1", delivery_receipts: "delivery_items_v1" };
      const receiver = await connectRawPeer(router, registration);
      const firstConversationFrame = receiver.next((frame) => record(frame).type === "conversation_message");
      const firstOfferFrame = receiver.next((frame) => record(frame).type === "message");
      const post = async (id: string) => {
        const response = await fetch(new URL("/v1/messages", router.httpUrl), { method: "POST", headers: { "content-type": "application/json", "x-correlation-id": `corr-${id}` }, body: JSON.stringify(fixtureMessage(id, "history-target", { from: "history-sender", body: `body-${id}` })) });
        return { status: response.status, correlation: response.headers.get("x-correlation-id") };
      };
      const firstPost = await post("history-one");
      const firstConversation = record(await firstConversationFrame);
      const firstOffer = record(await firstOfferFrame);
      const firstMessage = record(firstOffer.message);
      const firstReceipt = { agent_id: "history-target", instance_id: "history-instance", lease_id: stringValue(receiver.registered.lease_id, "history.lease_id"), message_id: "history-one", attempt_number: numberValue(firstOffer.attempt_number, "history.attempt_number") };
      receiver.socket.send(JSON.stringify({ version: "v1", frame_id: "history-injection", type: "injection_requested", ...firstReceipt }));
      const firstAcknowledgedFrame = receiver.next((frame) => record(frame).type === "delivery_acknowledged");
      receiver.socket.send(JSON.stringify({ version: "v1", frame_id: "history-ack", type: "ack", ...firstReceipt }));
      const firstAcknowledged = record(await firstAcknowledgedFrame);
      await closeSocket(receiver.socket);

      const secondPost = await post("history-two");
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
      const reconnected = await connectRawPeer(router, registration);
      const secondOffer = record(await reconnected.next((frame) => record(frame).type === "message"));
      const historyResponse = await fetch(new URL("/v1/conversations?owner=history-target&peer=history-sender", router.httpUrl));
      const history = await jsonRecord(historyResponse);
      const historyMessages = history.messages;
      if (!Array.isArray(historyMessages)) throw new Error("history response omitted messages");
      const secondReceipt = { agent_id: "history-target", instance_id: "history-instance", lease_id: stringValue(reconnected.registered.lease_id, "reconnected.lease_id"), message_id: "history-two", attempt_number: numberValue(secondOffer.attempt_number, "second.attempt_number") };
      reconnected.socket.send(JSON.stringify({ version: "v1", frame_id: "history-two-injection", type: "injection_requested", ...secondReceipt }));
      const secondAcknowledgedFrame = reconnected.next((frame) => record(frame).type === "delivery_acknowledged");
      reconnected.socket.send(JSON.stringify({ version: "v1", frame_id: "history-two-ack", type: "ack", ...secondReceipt }));
      const secondAcknowledged = record(await secondAcknowledgedFrame);
      await closeSocket(reconnected.socket);
      return {
        first_post: firstPost,
        broadcast: { type: stringValue(firstConversation.type, "broadcast.type"), id: stringValue(record(firstConversation.message).id, "broadcast.message.id") },
        first_offer: { id: stringValue(firstMessage.id, "first_offer.id"), attempt: numberValue(firstOffer.attempt_number, "first_offer.attempt") },
        first_ack: { type: stringValue(firstAcknowledged.type, "first_ack.type"), request_id: stringValue(firstAcknowledged.request_id, "first_ack.request_id") },
        second_post: secondPost,
        history: { status: historyResponse.status, high_water_sequence: numberValue(history.high_water_sequence, "history.high_water_sequence"), ids: historyMessages.map((message) => stringValue(record(message).id, "history.message.id")) },
        second_ack: { type: stringValue(secondAcknowledged.type, "second_ack.type"), request_id: stringValue(secondAcknowledged.request_id, "second_ack.request_id"), id: stringValue(secondAcknowledged.message_id, "second_ack.message_id") },
      };
    };
    const nodeResult = await exercise(node);
    const goResult = await exercise(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.first_post).toEqual({ status: 201, correlation: "corr-history-one" });
    expect(nodeResult.broadcast).toEqual({ type: "conversation_message", id: "history-one" });
    expect(nodeResult.first_offer).toEqual({ id: "history-one", attempt: 1 });
    expect(nodeResult.first_ack).toEqual({ type: "delivery_acknowledged", request_id: "history-ack" });
    expect(nodeResult.second_post).toEqual({ status: 201, correlation: "corr-history-two" });
    expect(nodeResult.history).toEqual({ status: 200, high_water_sequence: 2, ids: ["history-one", "history-two"] });
    expect(nodeResult.second_ack).toEqual({ type: "delivery_acknowledged", request_id: "history-two-ack", id: "history-two" });
  }, 120_000);

  it("matches batched delivery items, ordered messages, retries, and receipt idempotency", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const exercise = async (router: IsolatedRouter) => {
      const target = "batch-target";
      const post = async (id: string) => {
        const response = await fetch(new URL("/v1/messages", router.httpUrl), {
          method: "POST",
          headers: { "content-type": "application/json", "x-correlation-id": `corr-${id}` },
          body: JSON.stringify(fixtureMessage(id, target, { from: "batch-sender", body: `body-${id}` })),
        });
        return { status: response.status, correlation: response.headers.get("x-correlation-id"), body: record(await response.json()) };
      };
      const posted = [await post("batch-one"), await post("batch-two"), await post("batch-three")];
      const peer = await connectRawPeer(router, {
        version: "v1", frame_id: "register-batch-target", type: "register", agent_id: target, instance_id: "batch-instance",
        ack_confirmation: "correlated_v1", delivery_receipts: "delivery_items_v1",
      });
      const offered = record(await peer.next((frame) => record(frame).type === "message"));
      const offeredMessage = record(offered.message);
      const items = offered.delivery_items;
      const messages = offered.delivery_messages;
      if (!Array.isArray(items) || !Array.isArray(messages)) throw new Error("batched offer omitted delivery arrays");
      const itemSummary = items.map((item) => {
        const entry = record(item);
        return { message_id: stringValue(entry.message_id, "delivery_items.message_id"), attempt_number: numberValue(entry.attempt_number, "delivery_items.attempt_number"), hasIncarnation: typeof entry.message_incarnation_id === "string" };
      });
      const messageSummary = messages.map((message) => stringValue(record(message).id, "delivery_messages.id"));
      const receiptFields = { agent_id: target, instance_id: "batch-instance", lease_id: stringValue(peer.registered.lease_id, "batch.lease_id"), message_id: stringValue(itemSummary[0]?.message_id, "first.delivery_item.message_id"), attempt_number: numberValue(itemSummary[0]?.attempt_number, "first.delivery_item.attempt_number"), delivery_items: items };
      peer.socket.send(JSON.stringify({ version: "v1", frame_id: "batch-injection", type: "injection_requested", ...receiptFields }));
      const acknowledgedFrame = peer.next((frame) => record(frame).type === "delivery_acknowledged");
      peer.socket.send(JSON.stringify({ version: "v1", frame_id: "batch-ack", type: "ack", ...receiptFields }));
      const acknowledged = record(await acknowledgedFrame);
      const duplicateFrame = peer.next((frame) => record(frame).type === "error" && record(frame).request_id === "batch-ack-duplicate");
      peer.socket.send(JSON.stringify({ version: "v1", frame_id: "batch-ack-duplicate", type: "ack", ...receiptFields }));
      const duplicate = record(await duplicateFrame);
      await closeSocket(peer.socket);
      return {
        posted: posted.map(({ status, correlation, body }) => ({ status, correlation, id: stringValue(record(body.message).id, "posted.message.id") })),
        offer: { type: stringValue(offered.type, "offer.type"), id: stringValue(offeredMessage.id, "offer.message.id"), itemIds: itemSummary.map(({ message_id }) => message_id), messageIds: messageSummary, attempts: itemSummary.map(({ attempt_number }) => attempt_number), allIncarnations: itemSummary.every(({ hasIncarnation }) => hasIncarnation) },
        acknowledged: { type: stringValue(acknowledged.type, "acknowledged.type"), request_id: stringValue(acknowledged.request_id, "acknowledged.request_id"), message_id: stringValue(acknowledged.message_id, "acknowledged.message_id"), attempt_number: numberValue(acknowledged.attempt_number, "acknowledged.attempt_number") },
        duplicate: { type: stringValue(duplicate.type, "duplicate.type"), code: stringValue(duplicate.code, "duplicate.code"), request_id: stringValue(duplicate.request_id, "duplicate.request_id") },
      };
    };
    const nodeResult = await exercise(node);
    const goResult = await exercise(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.posted).toEqual([
      { status: 201, correlation: "corr-batch-one", id: "batch-one" },
      { status: 201, correlation: "corr-batch-two", id: "batch-two" },
      { status: 201, correlation: "corr-batch-three", id: "batch-three" },
    ]);
    expect(nodeResult.offer).toEqual({ type: "message", id: "batch-one", itemIds: ["batch-one", "batch-two", "batch-three"], messageIds: ["batch-one", "batch-two", "batch-three"], attempts: [1, 1, 1], allIncarnations: true });
    expect(nodeResult.acknowledged).toMatchObject({ type: "delivery_acknowledged", request_id: "batch-ack", message_id: "batch-one", attempt_number: 1 });
    expect(nodeResult.duplicate).toEqual({ type: "error", code: "delivery_not_active", request_id: "batch-ack-duplicate" });
  }, 120_000);

  it("matches CORS, readiness, registration, and correlated delivery behavior", async () => {
    const node = await startIsolatedNodeRouter();
    runningRouters.push(node);
    const go = await startIsolatedGoRouter();
    runningRouters.push(go);
    const nodeResult = await requestSummary(node);
    const goResult = await requestSummary(go);
    expect(nodeResult).toEqual(goResult);
    expect(nodeResult.health.migrations).toEqual(EXPECTED_GOAL_MIGRATIONS);
    expect(nodeResult.capabilities).toEqual({ goal_control_v1: true, subagent_lifecycle_v2: true });
    expect(nodeResult.preflight).toEqual({ status: 204, cors: "*" });
    expect(nodeResult.noOrigin).toEqual({ status: 404, code: "not_found" });
    expect(nodeResult.registration).toEqual({ type: "registered", request_id: "register-diff", agent_id: "diff-agent", instance_id: "diff-instance" });
    expect(nodeResult.pong).toEqual({ type: "pong", request_id: "ping-diff" });
    expect(nodeResult.enqueue).toEqual({ status: 201, correlation: "corr-diff", id: "diff-message" });
    expect(nodeResult.offer).toMatchObject({ type: "message", id: "diff-message", attempt: 1, hasIncarnation: true });
    expect(nodeResult.acknowledged).toMatchObject({ type: "delivery_acknowledged", request_id: "ack-diff", id: "diff-message", attempt: 1, hasIncarnation: true });
  }, 120_000);
});
