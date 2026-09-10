import { afterEach, describe, expect, it } from "vitest";
import { createRouterTransport, type RouterTransport, type TransportEvent } from "../../src/transport/router-transport";
import { isRecord } from "../../src/protocol/guards";
import type { SocketEvent, WebSocketFactory, WebSocketLike } from "../../src/transport/websocket-client";
import { fixtureGoalSettings, fixtureMessage, startIsolatedGoRouter } from "../support/isolated-go-router.mjs";
import { connectFixturePeer, waitForWebSocketFrame } from "../support/node-router.mjs";

type JsonRecord = Record<string, unknown>;
type IsolatedRouter = Awaited<ReturnType<typeof startIsolatedGoRouter>>;

const routers: IsolatedRouter[] = [];
const transports: RouterTransport[] = [];
const peers: Array<{ close: () => Promise<void> }> = [];

afterEach(async () => {
  while (peers.length > 0) await peers.pop()?.close();
  while (transports.length > 0) transports.pop()?.close();
  while (routers.length > 0) await routers.pop()?.close();
});

function record(value: unknown): JsonRecord {
  if (!isRecord(value)) throw new Error("expected an object");
  return value;
}

function stringValue(value: unknown, field: string): string {
  if (typeof value !== "string") throw new Error(`expected ${field} to be a string`);
  return value;
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));
}

function waitForEvent(transport: RouterTransport, predicate: (event: TransportEvent) => boolean, timeoutMs = 10_000): Promise<TransportEvent> {
  return new Promise<TransportEvent>((resolveEvent, rejectEvent) => {
    const unsubscribe = transport.subscribe((event) => {
      if (!predicate(event)) return;
      clearTimeout(timer);
      unsubscribe();
      resolveEvent(event);
    });
    const timer = setTimeout(() => {
      unsubscribe();
      rejectEvent(new Error("timed out waiting for transport event"));
    }, timeoutMs);
  });
}

async function waitForOutgoing(outgoing: JsonRecord[], predicate: (frame: JsonRecord) => boolean, timeoutMs = 10_000): Promise<JsonRecord> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const frame = outgoing.find(predicate);
    if (frame !== undefined) return frame;
    await delay(10);
  }
  throw new Error("timed out waiting for transport websocket frame");
}

function eventFromDomSocket(event: Event): SocketEvent {
  if (event instanceof MessageEvent) return { data: event.data };
  if (event instanceof CloseEvent) return { code: event.code, reason: event.reason };
  return {};
}

function instrumentedWebSocketFactory(
  outgoing: JsonRecord[],
  sockets: globalThis.WebSocket[],
  mutateIncoming?: (frame: unknown) => unknown,
): WebSocketFactory {
  return (url: string): WebSocketLike => {
    const raw = new globalThis.WebSocket(url);
    sockets.push(raw);
    return {
      get readyState() { return raw.readyState; },
      addEventListener(type, listener) {
        raw.addEventListener(type, (event) => {
          const converted = eventFromDomSocket(event);
          if (typeof converted.data === "string") {
            const parsed: unknown = JSON.parse(converted.data);
            const frame = mutateIncoming?.(parsed) ?? parsed;
            listener({ data: JSON.stringify(frame) });
            return;
          }
          listener(converted);
        });
      },
      send(value) {
        const frame: unknown = JSON.parse(value);
        outgoing.push(record(frame));
        raw.send(value);
      },
      close(code, reason) { raw.close(code, reason); },
    };
  };
}

describe("React transport against an isolated Go router", () => {
  it("uses the real clients for registration, lifecycle, correlation, receipts, and reconnect", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const outgoing: JsonRecord[] = [];
    const sockets: globalThis.WebSocket[] = [];
    const responseCorrelations: Array<string | null> = [];
    const transport = createRouterTransport({
      routerUrl: router.httpUrl,
      agentId: "integration-ui",
      fetchImpl: async (input, init) => {
        const response = await fetch(input, init);
        responseCorrelations.push(response.headers.get("x-correlation-id"));
        return response;
      },
      webSocketFactory: instrumentedWebSocketFactory(outgoing, sockets),
    });
    transports.push(transport);

    const connected = waitForEvent(transport, (event) => event.type === "status" && event.status === "connected");
    const lifecycle = waitForEvent(transport, (event) => event.type === "frame" && event.frame.type === "subagent_state_snapshot");
    transport.connect();
    const connectedEvent = await connected;
    const lifecycleEvent = await lifecycle;
    if (connectedEvent.type !== "status" || lifecycleEvent.type !== "frame") throw new Error("transport event narrowing failed");
    expect(transport.socket.registered).toBe(true);
    expect(connectedEvent.identity.agentId).toBe("integration-ui");
    expect(lifecycleEvent.frame.type).toBe("subagent_state_snapshot");
    expect(transport.lifecycle.version).toBe(0);

    const health = await transport.http.health();
    const correlatedHealth = await transport.http.request("/v1/health", { headers: { "x-correlation-id": "integration-health" } });
    expect(health.status).toBe("ok");
    expect(record(correlatedHealth).status).toBe("ok");
    expect(responseCorrelations).toContain("integration-health");

    const deliveryEvent = waitForEvent(transport, (event) => event.type === "delivery" && event.message.id === "integration-message");
    const posted = await transport.sendMessage(fixtureMessage("integration-message", "integration-ui", { from: "integration-sender", body: "real Go delivery" }));
    const delivery = await deliveryEvent;
    const injection = await waitForOutgoing(outgoing, (frame) => frame.type === "injection_requested" && frame.message_id === "integration-message");
    const acknowledgement = await waitForOutgoing(outgoing, (frame) => frame.type === "ack" && frame.message_id === "integration-message");
    if (delivery.type !== "delivery") throw new Error("delivery event narrowing failed");
    expect(record(posted).message).toMatchObject({ id: "integration-message" });
    expect(delivery.messages.map((message) => message.id)).toEqual(["integration-message"]);
    expect(injection).toMatchObject({ agent_id: "integration-ui", message_id: "integration-message", attempt_number: 1 });
    expect(acknowledgement).toMatchObject({ agent_id: "integration-ui", message_id: "integration-message", attempt_number: 1 });

    const firstIdentity = transport.socket.identity;
    const reconnected = waitForEvent(transport, (event) => event.type === "status" && event.status === "connected");
    sockets[0]?.close(1000, "integration reconnect");
    const reconnectedEvent = await reconnected;
    if (reconnectedEvent.type !== "status") throw new Error("reconnect event narrowing failed");
    expect(reconnectedEvent.identity.generation).toBe(firstIdentity.generation + 1);
    expect(reconnectedEvent.identity.instanceId).not.toBe(firstIdentity.instanceId);
    expect(transport.socket.registered).toBe(true);
    // Registration starts background HTTP refreshes. Drain them while the
    // task-owned router is still running so teardown has no rejected fetches.
    await transport.refreshAgents();
    await delay(250);
  }, 60_000);

  it("uses a memory-only Web UI lease to receive and acknowledge three Wolfgang deliveries", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const outgoing: JsonRecord[] = [];
    const sockets: globalThis.WebSocket[] = [];
    const transport = createRouterTransport({
      routerUrl: router.httpUrl,
      agentId: "wolfgang",
      webSocketFactory: instrumentedWebSocketFactory(outgoing, sockets),
    });
    transports.push(transport);

    const connected = waitForEvent(transport, (event) => event.type === "status" && event.status === "connected");
    const received: string[] = [];
    let unsubscribe: () => void = () => undefined;
    const delivered = new Promise<void>((resolveDelivered, rejectDelivered) => {
      const timeout = setTimeout(() => {
        unsubscribe();
        rejectDelivered(new Error("timed out waiting for three Web UI deliveries"));
      }, 10_000);
      unsubscribe = transport.subscribe((event) => {
        if (event.type !== "delivery") return;
        received.push(...event.messages.map((message) => message.id));
        if (received.length < 3) return;
        clearTimeout(timeout);
        unsubscribe();
        resolveDelivered();
      });
    });
    transport.connect();
    await connected;
    await Promise.all(["browser-delivery-1", "browser-delivery-2", "browser-delivery-3"].map((id) => transport.sendMessage(fixtureMessage(id, "wolfgang", { from: `sender-${id}` }))));
    await delivered;
    const acknowledgement = await waitForOutgoing(outgoing, (frame) => frame.type === "ack" && Array.isArray(frame.delivery_items) && frame.delivery_items.length === 3);
    expect(received).toHaveLength(3);
    expect(received).toEqual(expect.arrayContaining(["browser-delivery-1", "browser-delivery-2", "browser-delivery-3"]));
    expect(acknowledgement).toMatchObject({ agent_id: "wolfgang", type: "ack" });
    // sendMessage starts a background projection refresh. Finish it before the
    // isolated router is torn down so this proof has no detached request.
    await transport.refreshAgents();
    await delay(250);
  }, 60_000);

  it("refetches reminders through React transport after a Go reminder broadcast", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const outgoing: JsonRecord[] = [];
    const sockets: globalThis.WebSocket[] = [];
    const transport = createRouterTransport({
      routerUrl: router.httpUrl,
      agentId: "integration-ui",
      webSocketFactory: instrumentedWebSocketFactory(outgoing, sockets),
    });
    transports.push(transport);

    const connected = waitForEvent(transport, (event) => event.type === "status" && event.status === "connected");
    transport.connect();
    await connected;
    const remindersChanged = waitForEvent(transport, (event) => event.type === "reminders");
    const response = await fetch(new URL("/v1/reminders", router.httpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: "react-live-reminder", target: "reminder-target", creator: "creator", body: "React reminder", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:01:00.000Z" }),
    });
    expect(response.status).toBe(201);
    const event = await remindersChanged;
    if (event.type !== "reminders") throw new Error("reminders event narrowing failed");
    expect(event.reminders.map((reminder) => reminder.id)).toContain("react-live-reminder");
  }, 60_000);

  it("correlates a real Go control response and reports a malformed server frame through the guard", async () => {
    const router = await startIsolatedGoRouter();
    routers.push(router);
    const outgoing: JsonRecord[] = [];
    const sockets: globalThis.WebSocket[] = [];
    const transport = createRouterTransport({
      routerUrl: router.httpUrl,
      agentId: "integration-ui",
      webSocketFactory: instrumentedWebSocketFactory(outgoing, sockets, (frame) => {
        const candidate = record(frame);
        return candidate.type === "delivery_acknowledged" ? { ...candidate, type: "invalid-server-frame" } : frame;
      }),
    });
    transports.push(transport);
    const connected = waitForEvent(transport, (event) => event.type === "status" && event.status === "connected");
    const capabilities = waitForEvent(transport, (event) => event.type === "capabilities" && event.goalControlAvailable);
    transport.connect();
    await connected;
    await capabilities;

    const target = await connectFixturePeer(router, { agent_id: "integration-target", instance_id: "integration-target-instance", role: "agent" });
    peers.push(target);
    const forwardedFrame = waitForWebSocketFrame(target.socket, (frame) => isRecord(frame) && frame.type === "goal_control");
    const controlEvent = waitForEvent(transport, (event) => event.type === "control" && event.kind === "goal");
    const accepted = transport.sendGoalControl("integration-target", "integration-target-instance", {
      controlId: "integration-goal-control",
      action: "set",
      goal: "Control from integration",
      settings: fixtureGoalSettings(),
      revision: 1,
    });
    expect(accepted).toBe(true);
    const forwarded = record(await forwardedFrame);
    target.send({
      version: "v1",
      frame_id: "integration-goal-result",
      type: "goal_control_result",
      agent_id: "integration-target",
      instance_id: "integration-target-instance",
      lease_id: stringValue(record(target.registered).lease_id, "target.lease_id"),
      source_agent_id: "integration-ui",
      control_id: "integration-goal-control",
      revision: forwarded.revision,
      accepted: true,
      goal_state: { enabled: true, goal: "Control from integration", ...fixtureGoalSettings(), outcome_counts: { COMPLETED: 1, NOT_DONE: 0 } },
    });
    const control = await controlEvent;
    if (control.type !== "control") throw new Error("control event narrowing failed");
    expect(control.kind).toBe("goal");
    expect(control.frame.control_id).toBe("integration-goal-control");
    expect(control.pending.revision).toBe(1);

    const protocolError = waitForEvent(transport, (event) => event.type === "error" && event.error.message.includes("Mailbox V2 server frame"));
    const deliveryEvent = waitForEvent(transport, (event) => event.type === "delivery" && event.message.id === "integration-guard-message");
    await transport.sendMessage(fixtureMessage("integration-guard-message", "integration-ui", { from: "integration-sender" }));
    await deliveryEvent;
    await waitForOutgoing(outgoing, (frame) => frame.type === "ack" && frame.message_id === "integration-guard-message");
    const error = await protocolError;
    expect(error.type).toBe("error");
    await target.close();
    peers.splice(peers.indexOf(target), 1);
  }, 60_000);
});
