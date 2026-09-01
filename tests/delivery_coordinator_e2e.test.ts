import assert from "node:assert/strict";
import { appendFile, readFile, stat, writeFile } from "node:fs/promises";
import test from "node:test";
import WebSocket from "ws";

import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.ts";
import {
  createPiMailboxDeliveryCoordinator,
  mailboxSessionDeliveryState,
} from "../pi-extensions/mecha-messaging/delivery-coordinator.ts";
import {
  isLoopbackPortAvailable,
  startIsolatedGoRouter,
} from "../web/router-ui-react/tests/support/isolated-go-router.mjs";

const sleep = (milliseconds: number) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function waitFor(check: () => boolean | Promise<boolean>, description: string, attempts = 200): Promise<void> {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (await check()) return;
    await sleep(10);
  }
  assert.ok(await check(), description);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function messageIdFromFrame(frame: Record<string, unknown>): string | undefined {
  if (typeof frame.message_id === "string") return frame.message_id;
  return isRecord(frame.message) && typeof frame.message.id === "string" ? frame.message.id : undefined;
}

type FrameObservation = {
  direction: "client" | "router";
  raw: string;
  frame: Record<string, unknown>;
};

function entryFor(messageId: string, deliveryItems: unknown[], content: unknown): Record<string, unknown> {
  return {
    type: "custom_message",
    customType: "mecha-agent-message",
    content,
    display: true,
    details: { message_id: messageId, delivery_items: deliveryItems },
  };
}

async function postMessage(baseUrl: string, message: Record<string, unknown>): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/messages`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(message),
  });
  assert.equal(response.status, 201);
  await response.arrayBuffer();
}

async function messageRecord(baseUrl: string, messageId: string): Promise<Record<string, unknown> | undefined> {
  const response = await fetch(`${baseUrl}/v1/messages/${messageId}`);
  if (response.status === 404) return undefined;
  assert.equal(response.status, 200);
  const payload: unknown = await response.json();
  return isRecord(payload) && isRecord(payload.message) ? payload.message : undefined;
}

function assertRedacted(values: unknown[], secrets: string[]): void {
  const text = JSON.stringify(values);
  for (const secret of secrets) assert.equal(text.includes(secret), false, `secret was exposed: ${secret}`);
}

function censor(value: unknown, secrets: string[]): unknown {
  if (typeof value === "string") return secrets.reduce((result, secret) => result.split(secret).join("[REDACTED]"), value);
  if (Array.isArray(value)) return value.map((item) => censor(item, secrets));
  if (isRecord(value)) return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, censor(item, secrets)]));
  return value;
}

function requiredField(record: Record<string, unknown>, key: string, label: string): unknown {
  assert.equal(Object.hasOwn(record, key), true, `${label} must include ${key}`);
  return record[key];
}

function requiredRecord(value: unknown, label: string): Record<string, unknown> {
  assert.ok(isRecord(value), `${label} must be an object`);
  return value;
}

function assertEnvelopeFields(value: unknown, label: string): Record<string, unknown> {
  const envelope = requiredRecord(value, label);
  for (const field of ["id", "from", "to", "body", "created_at"]) {
    requiredField(envelope, field, label);
  }
  return envelope;
}

function assertSentinelMetadata(value: unknown, expectedModel: string, label: string): void {
  const envelope = assertEnvelopeFields(value, label);
  const sentinel = requiredRecord(requiredField(envelope, "sentinel", label), `${label}.sentinel`);
  assert.equal(requiredField(sentinel, "model", `${label}.sentinel`), expectedModel);
}

function replayContract(frame: Record<string, unknown>): Record<string, unknown> {
  const message = assertEnvelopeFields(requiredField(frame, "message", "message frame"), "message frame.message");
  return {
    message: {
      id: requiredField(message, "id", "message frame.message"),
      from: requiredField(message, "from", "message frame.message"),
      to: requiredField(message, "to", "message frame.message"),
      subject: message.subject,
      body: requiredField(message, "body", "message frame.message"),
      created_at: requiredField(message, "created_at", "message frame.message"),
      source: message.source,
      sentinel: message.sentinel,
    },
    message_incarnation_id: requiredField(frame, "message_incarnation_id", "message frame"),
    attempt_number: requiredField(frame, "attempt_number", "message frame"),
    delivery_expires_at: requiredField(frame, "delivery_expires_at", "message frame"),
    ...(Object.hasOwn(frame, "delivery_items") ? { delivery_items: frame.delivery_items } : {}),
    ...(Object.hasOwn(frame, "delivery_messages") ? { delivery_messages: frame.delivery_messages } : {}),
  };
}

test("isolated router proves durable coordinator delivery, replay, Sentinel redaction, and teardown", async (t) => {
  const router = await startIsolatedGoRouter();
  const sessionFile = `${router.rootPath}/session.jsonl`;
  await writeFile(sessionFile, "", { mode: 0o600 });

  const branch: Record<string, unknown>[] = [];
  const injections: Record<string, unknown>[] = [];
  const statuses: unknown[] = [];
  const uiStatuses: unknown[] = [];
  const traces: unknown[] = [];
  const connectionStates: unknown[] = [];
  const logs: unknown[] = [];
  const sentinelCalls: Record<string, unknown>[] = [];
  const frames: FrameObservation[] = [];
  const sockets: WebSocket[] = [];
  let client: ReturnType<typeof createMailboxClient> | undefined;
  let routerClosed = false;
  let failurePaused = false;
  let failureNackCount = 0;
  let releaseSentinelReplay: (() => void) | undefined;
  const sentinelReplayGate = new Promise<void>((resolve) => { releaseSentinelReplay = resolve; });

  const context = {
    sessionManager: {
      getBranch: () => branch,
      getSessionId: () => "isolated-delivery-session",
      isPersisted: () => true,
      getSessionFile: () => sessionFile,
    },
    ui: {
      setStatus: (key: string, value: string | undefined) => uiStatuses.push({ key, value }),
      notify: (message: string, level?: string) => uiStatuses.push({ message, level }),
    },
  };
  const pi = {
    sendMessage(message: Record<string, unknown>) {
      if (message.customType === "mecha-agent-message") {
        injections.push(message);
        branch.push({ type: "custom_message", ...message });
      } else {
        statuses.push(message);
      }
    },
  };

  const socketFactory = (url: string) => {
    const socket = new WebSocket(url);
    const originalSend = socket.send.bind(socket);
    socket.send = (data: WebSocket.Data) => {
      const raw = data.toString();
      let parsed: unknown;
      try { parsed = JSON.parse(raw); } catch { parsed = undefined; }
      if (isRecord(parsed)) {
        frames.push({ direction: "client", raw, frame: parsed });
        const messageId = messageIdFromFrame(parsed);
        if (parsed.type === "register") {
          // Exercise the correlated delivery contract so every required frame field is present.
          originalSend(JSON.stringify({ ...parsed, ack_confirmation: "correlated_v1" }));
          return;
        }
        if (parsed.type === "nack" && messageId === "sentinel-failure") {
          failurePaused = true;
          failureNackCount += 1;
          originalSend(data);
          // The isolated router retries immediately. Let it process all three
          // NACKs so the durable dead-letter state proves receipt handling.
          if (failureNackCount === 3) queueMicrotask(() => client?.pause());
          return;
        }
      }
      originalSend(data);
    };
    socket.on("message", (data) => {
      const raw = data.toString();
      let parsed: unknown;
      try { parsed = JSON.parse(raw); } catch { return; }
      if (!isRecord(parsed)) return;
      frames.push({ direction: "router", raw, frame: parsed });
    });
    sockets.push(socket);
    return socket;
  };

  const coordinator = createPiMailboxDeliveryCoordinator({
    pi,
    sessionContext: context,
    client: {
      sendReceipt: (type, delivery, reason) => client?.sendReceipt(type, delivery, reason),
    },
    checkpointRetryDelayMs: 10,
    spawnSubagent: async (message) => {
      sentinelCalls.push(message);
      if (message.id === "sentinel-failure") throw new Error("PRIVATE native failure error");
      if (message.id === "sentinel-replay") await sentinelReplayGate;
      return "native-child";
    },
  });
  client = createMailboxClient({
    agentId: "isolated-delivery",
    routerUrl: router.httpUrl,
    onEnvelope: (message, delivery) => coordinator.onEnvelope(message, delivery),
    webSocketFactory: socketFactory,
    reconnectInitialDelayMs: 1,
    reconnectMaxDelayMs: 1,
    renewIntervalMs: 60_000,
    log: (...values) => logs.push(values),
    onConnectionState: (state) => connectionStates.push(state),
    onLifecycleTrace: (trace) => traces.push({ kind: "lifecycle", trace }),
    onLifecycleDiagnostic: (diagnostic) => traces.push({ kind: "lifecycle-diagnostic", diagnostic }),
    onModelControlTrace: (trace) => traces.push({ kind: "model", trace }),
    onGoalControlTrace: (trace) => traces.push({ kind: "goal", trace }),
  });

  t.after(async () => {
    coordinator.close();
    client?.close();
    for (const socket of sockets) {
      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) socket.close();
    }
    if (!routerClosed) await router.close();
  });

  const ordinary = {
    id: "ordinary-replay",
    from: "sender",
    to: "isolated-delivery",
    subject: "ordinary subject",
    body: "private first body",
    created_at: "2030-01-01T00:00:00.000Z",
    source: { kind: "agent" },
  };
  const ordinaryFollowup = {
    id: "ordinary-replay-followup",
    from: "sender",
    to: "isolated-delivery",
    subject: "followup subject",
    body: "private second body",
    created_at: "2030-01-01T00:00:00.000Z",
    source: { kind: "reminder" },
  };
  // Queue both messages before registration so the router creates one durable
  // batch and exposes every delivery item on both offers.
  await postMessage(router.httpUrl, ordinary);
  await postMessage(router.httpUrl, ordinaryFollowup);
  await client.connect();
  await waitFor(() => injections.length === 1, "Pi should accept the ordinary batch");
  await waitFor(() => frames.some(({ direction, frame }) => direction === "client" && frame.type === "injection_requested" && messageIdFromFrame(frame) === ordinary.id), "injection receipt should be sent");

  const firstInjection = injections[0];
  assert.ok(isRecord(firstInjection.details));
  const originalItems = firstInjection.details.delivery_items;
  assert.ok(Array.isArray(originalItems));
  assert.equal(originalItems.length, 2);
  assert.deepEqual(originalItems.map((item) => isRecord(item) ? item.messageId : undefined), [ordinary.id, ordinaryFollowup.id]);

  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: ordinary.id } } }, context);
  await sleep(30);
  assert.equal(frames.some(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === ordinary.id), false);
  assert.equal(mailboxSessionDeliveryState(context.sessionManager, ordinary.id, { deliveryItems: [] }), "history_unverified");

  const firstBatchFrame = frames.find(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === ordinary.id);
  assert.ok(firstBatchFrame);
  for (const field of ["message", "message_incarnation_id", "attempt_number", "delivery_expires_at", "delivery_items", "delivery_messages"]) {
    requiredField(firstBatchFrame.frame, field, "ordinary batch frame");
  }
  const firstBatchMessage = assertEnvelopeFields(firstBatchFrame.frame.message, "ordinary batch frame.message");
  assert.equal(firstBatchMessage.body, `Mailbox message batch\n\nFrom: sender\nSubject: ${ordinary.subject}\n\n${ordinary.body}\n\n[Reply via mailbox to sender.]\n\n---\n\nFrom: sender\nSubject: ${ordinaryFollowup.subject}\n\n${ordinaryFollowup.body}\n\n[Reply via mailbox to sender.]`);
  const firstBatchItems = firstBatchFrame.frame.delivery_items;
  assert.ok(Array.isArray(firstBatchItems));
  assert.equal(firstBatchItems.length, 2);
  const firstBatchMessages = firstBatchFrame.frame.delivery_messages;
  assert.ok(Array.isArray(firstBatchMessages));
  assert.equal(firstBatchMessages.length, 2);
  firstBatchMessages.forEach((message, index) => assertEnvelopeFields(message, `ordinary batch frame.delivery_messages[${index}]`));
  assert.deepEqual(firstBatchMessages, [ordinary, ordinaryFollowup]);

  client.pause();
  await waitFor(() => sockets[0]?.readyState === WebSocket.CLOSED, "client should disconnect before router restart");
  await router.restart();
  await client.resume();
  await waitFor(() => frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === ordinary.id).length >= 2, "restart should replay the active delivery");
  await sleep(30);
  assert.equal(injections.length, 1, "history-unverified replay must not fabricate a second entry");
  const replayFrames = frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === ordinary.id);
  assert.equal(replayFrames.length, 2);
  for (const [index, replayFrame] of replayFrames.entries()) {
    for (const field of ["message", "message_incarnation_id", "attempt_number", "delivery_expires_at", "delivery_items", "delivery_messages"]) {
      requiredField(replayFrame.frame, field, `ordinary replay frame ${index}`);
    }
    const deliveryMessages = replayFrame.frame.delivery_messages;
    assert.ok(Array.isArray(deliveryMessages));
    assert.equal(deliveryMessages.length, 2);
    deliveryMessages.forEach((message, messageIndex) => assertEnvelopeFields(message, `ordinary replay frame ${index}.delivery_messages[${messageIndex}]`));
    assert.deepEqual(deliveryMessages, [ordinary, ordinaryFollowup]);
  }
  assert.deepEqual(replayContract(replayFrames[1].frame), replayContract(replayFrames[0].frame), "replay must preserve every message and delivery contract field");
  assert.deepEqual((replayFrames[0].frame.delivery_items as unknown[]).map((item) => isRecord(item) ? item.message_id : undefined), [ordinary.id, ordinaryFollowup.id]);
  assert.deepEqual((replayFrames[1].frame.delivery_items as unknown[]).map((item) => isRecord(item) ? item.message_id : undefined), [ordinary.id, ordinaryFollowup.id]);
  const replayReceipts = frames.filter(({ direction, frame }) => direction === "client" && frame.type === "injection_requested" && messageIdFromFrame(frame) === ordinary.id);
  assert.equal(replayReceipts.length, 2);
  assert.equal(replayReceipts.every(({ frame }) => frame.attempt_number === 1), true, "reconnect keeps the original attempt identity");

  await appendFile(sessionFile, `${JSON.stringify(entryFor(ordinary.id, originalItems, firstInjection.content))}\n`);
  assert.equal(mailboxSessionDeliveryState(context.sessionManager, ordinary.id, { deliveryItems: originalItems }), "durable");
  coordinator.onMessageEnd({ message: { role: "custom", customType: "mecha-agent-message", details: { message_id: ordinary.id } } }, context);
  await waitFor(() => frames.some(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === ordinary.id), "durability should release the final ACK");
  const ordinaryAck = frames.find(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === ordinary.id);
  assert.equal(ordinaryAck?.frame.attempt_number, 1);
  assert.equal((await messageRecord(router.httpUrl, ordinary.id)), undefined);
  assert.equal((await messageRecord(router.httpUrl, ordinaryFollowup.id)), undefined);

  const sentinelReplaySecret = "PRIVATE scheduled replay prompt";
  const sentinelReplayModelSecret = "PRIVATE-replay-model";
  await postMessage(router.httpUrl, {
    id: "sentinel-replay", from: "scheduler", to: "isolated-delivery", body: sentinelReplaySecret,
    sentinel: { model: sentinelReplayModelSecret }, created_at: "2030-01-01T00:00:00.000Z",
  });
  await waitFor(() => frames.some(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-replay"), "Sentinel replay delivery should arrive");
  const sentinelReplayFirst = frames.find(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-replay");
  assert.ok(sentinelReplayFirst);
  assertSentinelMetadata(sentinelReplayFirst.frame.message, sentinelReplayModelSecret, "Sentinel replay frame.message");
  assert.equal(requiredField(sentinelReplayFirst.frame, "message_incarnation_id", "Sentinel replay frame") !== undefined, true);
  assert.equal(requiredField(sentinelReplayFirst.frame, "attempt_number", "Sentinel replay frame"), 1);
  assert.equal(requiredField(sentinelReplayFirst.frame, "delivery_expires_at", "Sentinel replay frame") !== undefined, true);

  client.pause();
  await waitFor(() => sockets[0]?.readyState === WebSocket.CLOSED, "client should disconnect before Sentinel replay");
  await router.restart();
  await client.resume();
  await waitFor(() => frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-replay").length >= 2, "restart should replay the Sentinel delivery");
  const sentinelReplayFrames = frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-replay");
  assert.equal(sentinelReplayFrames.length, 2);
  for (const [index, replayFrame] of sentinelReplayFrames.entries()) {
    assertSentinelMetadata(replayFrame.frame.message, sentinelReplayModelSecret, `Sentinel replay frame ${index}.message`);
    for (const field of ["message", "message_incarnation_id", "attempt_number", "delivery_expires_at"]) {
      requiredField(replayFrame.frame, field, `Sentinel replay frame ${index}`);
    }
  }
  assert.deepEqual(replayContract(sentinelReplayFrames[1].frame), replayContract(sentinelReplayFrames[0].frame), "Sentinel replay must preserve metadata and delivery contract fields");
  releaseSentinelReplay?.();
  await waitFor(() => frames.some(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === "sentinel-replay"), "replayed Sentinel should ACK");

  const lostAckSecret = "PRIVATE scheduled lost-ack prompt";
  const lostAckModelSecret = "PRIVATE-lost-ack-model";
  let lostAck = true;
  await postMessage(router.httpUrl, {
    id: "sentinel-lost-ack", from: "scheduler", to: "isolated-delivery", body: lostAckSecret,
    sentinel: { model: lostAckModelSecret }, created_at: "2030-01-01T00:00:00.000Z",
  });
  // The child is already owned when this first ACK disappears. Reconnect the
  // same unsettled delivery and prove the retry does not spawn a second child.
  const originalSendReceipt = client?.sendReceipt.bind(client);
  if (client !== undefined) {
    client.sendReceipt = (type, delivery, reason) => {
      if (type === "ack" && delivery.messageId === "sentinel-lost-ack" && lostAck) {
        lostAck = false;
        throw new Error("lost ACK");
      }
      originalSendReceipt?.(type, delivery, reason);
    };
  }
  await waitFor(() => statuses.some((message) => isRecord(message) && isRecord(message.details) && message.details.message_id === "sentinel-lost-ack"), "lost-ACK Sentinel should record hidden status");
  await waitFor(() => lostAck === false, "lost-ACK Sentinel should exercise the dropped acknowledgement");
  const lostAckCalls = sentinelCalls.filter(({ id }) => id === "sentinel-lost-ack");
  assert.equal(lostAckCalls.length, 1);
  client?.pause();
  await waitFor(() => sockets.at(-1)?.readyState === WebSocket.CLOSED, "client should disconnect after the lost ACK");
  await router.restart();
  await client?.resume();
  await waitFor(() => frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-lost-ack").length >= 2, "restart should replay the lost-ACK Sentinel delivery");
  await waitFor(() => frames.some(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === "sentinel-lost-ack"), "retried Sentinel should ACK");
  assert.equal(sentinelCalls.filter(({ id }) => id === "sentinel-lost-ack").length, 1);

  const successSecret = "PRIVATE scheduled success prompt";
  const successModelSecret = "PRIVATE-success-model";
  await postMessage(router.httpUrl, {
    id: "sentinel-success", from: "scheduler", to: "isolated-delivery", body: successSecret,
    sentinel: { model: successModelSecret }, created_at: "2030-01-01T00:00:00.000Z",
  });
  await waitFor(() => statuses.some((message) => isRecord(message) && isRecord(message.details) && message.details.message_id === "sentinel-success"), "successful Sentinel should record hidden status");
  const successStatus = statuses.find((message) => isRecord(message) && isRecord(message.details) && message.details.message_id === "sentinel-success");
  assert.equal(isRecord(successStatus) ? successStatus.display : undefined, false);
  const successStatusDetails = isRecord(successStatus) ? requiredRecord(successStatus.details, "Sentinel success status details") : {};
  assert.equal(typeof requiredField(successStatusDetails, "message_incarnation_id", "Sentinel success status"), "string");
  assert.deepEqual(successStatusDetails, {
    message_id: "sentinel-success",
    message_incarnation_id: successStatusDetails.message_incarnation_id,
    subagent_id: "native-child",
    state: "handed_off",
  });
  await waitFor(() => frames.some(({ direction, frame }) => direction === "client" && frame.type === "ack" && messageIdFromFrame(frame) === "sentinel-success"), "successful Sentinel should ACK");

  const failureSecret = "PRIVATE scheduled failure prompt";
  const failureModelSecret = "PRIVATE-failure-model";
  const failureErrorSecret = "PRIVATE native failure error";
  const outputSecret = "PRIVATE native output";
  const resultSecret = "PRIVATE native result";
  await postMessage(router.httpUrl, {
    id: "sentinel-failure", from: "scheduler", to: "isolated-delivery", body: failureSecret,
    sentinel: { model: failureModelSecret }, created_at: "2030-01-01T00:00:00.000Z",
  });
  await waitFor(() => failurePaused && failureNackCount === 3, "failed Sentinel should send three NACKs");
  let failureAfterNack: Record<string, unknown> | undefined;
  await waitFor(async () => {
    failureAfterNack = await messageRecord(router.httpUrl, "sentinel-failure");
    return failureAfterNack?.state === "dead_letter";
  }, "isolated router should settle the NACKs as a dead letter");
  assert.equal(failureAfterNack?.state, "dead_letter");
  assert.equal(sentinelCalls.filter(({ id }) => id === "sentinel-failure").length, 3);
  const failureFrames = frames.filter(({ direction, frame }) => direction === "router" && frame.type === "message" && messageIdFromFrame(frame) === "sentinel-failure");
  assert.deepEqual(failureFrames.map(({ frame }) => frame.attempt_number), [1, 2, 3]);
  for (const { frame } of failureFrames) {
    assert.equal(isRecord(frame.message) ? frame.message.body : undefined, failureSecret);
    assert.equal(isRecord(frame.message) && isRecord(frame.message.sentinel) ? frame.message.sentinel.model : undefined, failureModelSecret);
  }

  const successfulCall = sentinelCalls.find(({ id }) => id === "sentinel-success");
  const failedCall = sentinelCalls.find(({ id }) => id === "sentinel-failure");
  assert.equal(successfulCall?.body, successSecret);
  assert.equal(isRecord(successfulCall?.sentinel) ? successfulCall.sentinel.model : undefined, successModelSecret);
  assert.equal(failedCall?.body, failureSecret);
  assert.equal(isRecord(failedCall?.sentinel) ? failedCall.sentinel.model : undefined, failureModelSecret);

  const observableFrames = frames.filter(({ frame }) => frame.type !== "message").map(({ raw, frame }) => ({ raw, frame }));
  const secrets = [successSecret, successModelSecret, failureSecret, failureModelSecret, failureErrorSecret, outputSecret, resultSecret, sentinelReplaySecret, sentinelReplayModelSecret, lostAckSecret, lostAckModelSecret];
  const evidenceSecrets = [...secrets, ordinary.body, ordinaryFollowup.body];
  assertRedacted([...observableFrames, statuses, uiStatuses, traces, connectionStates, logs], secrets);
  const routerLog = await readFile(router.logPath, "utf8");
  assertRedacted([routerLog], secrets);
  const censoredEvidence = censor({ frames, sentinelCalls }, evidenceSecrets);
  assertRedacted([censoredEvidence], evidenceSecrets);

  assert.ok(await stat(router.databasePath));
  const closeResult = await router.close();
  routerClosed = true;
  assert.equal(closeResult.rootRemoved, true);
  await assert.rejects(() => stat(router.rootPath), { code: "ENOENT" });
  assert.equal(await isLoopbackPortAvailable(router.port), true, "temporary router port should have no listener after teardown");
  await assert.rejects(
    fetch(`${router.httpUrl}/v1/health`),
    (error: unknown) => error instanceof TypeError || String(error).includes("ECONNREFUSED"),
    "temporary router endpoint should reject connections after teardown",
  );
  assert.equal(sockets.every((socket) => socket.readyState === WebSocket.CLOSED), true);
  console.log(`delivery-coordinator-e2e-proof=${JSON.stringify(censor({ attempt: 1, replay: replayContract(replayFrames[1].frame), replayOrder: [ordinary.id, ordinaryFollowup.id], sentinelFailureNack: failureAfterNack?.state, teardown: closeResult.rootRemoved === true, retained: { frameCount: frames.length, sentinelCallCount: sentinelCalls.length } }, evidenceSecrets))}`);
});
