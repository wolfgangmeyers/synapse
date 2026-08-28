import assert from "node:assert/strict";
import test from "node:test";

import { readFile } from "node:fs/promises";

import { createMailboxClient } from "../pi-extensions/mecha-messaging/mailbox-client.js";
import { routeMailboxEnvelope } from "../pi-extensions/mecha-messaging/index.ts";

const routerUrl = "http://127.0.0.1:8765";
const wakeup = { to: "pr-watch", subject: "PR watch wakeup" };

test("exact PR watch wakeups inject into Pi without an extension child process", async () => {
  const injected = [];

  routeMailboxEnvelope(wakeup, (message) => injected.push(message));

  assert.deepEqual(injected, [wakeup]);
  const source = await readFile(new URL("../pi-extensions/mecha-messaging/index.ts", import.meta.url), "utf8");
  assert.doesNotMatch(source, /runPrWatchWakeup|MECHA_PR_WATCH_SCRIPT|\bexecFile\s*\(/);
});

test("ordinary mailbox messages use the same injection path", () => {
  const injected = [];
  const message = { to: "supervisor", subject: "PR watch wakeup" };

  routeMailboxEnvelope(message, (received) => injected.push(received));

  assert.deepEqual(injected, [message]);
});

test("client defaults use NanoIDs for instance, frames, and replies", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  let replyPayload;
  const client = createMailboxClient({
    agentId: "pr-watch",
    onEnvelope: () => {},
    webSocketFactory: () => socket,
    fetchImpl: async (_url, options) => {
      replyPayload = JSON.parse(options.body);
      return { ok: true, json: async () => ({ message: { ...replyPayload, state: "queued" } }) };
    },
  });

  assert.match(client.instanceId, /^[A-Za-z0-9]{21}$/);
  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "pr-watch", instance_id: client.instanceId, lease_id: "lease",
    }),
  }));
  await connected;
  assert.match(socket.sent[0].frame_id, /^[A-Za-z0-9]{21}$/);

  await client.sendReply({ to: "supervisor", body: "Done." });
  assert.match(replyPayload.id, /^[A-Za-z0-9]{21}$/);
  assert.equal(new Set([client.instanceId, socket.sent[0].frame_id, replyPayload.id]).size, 3);
  client.close();
});

test("client delivery does not acknowledge merely because injection was requested", async () => {
  const socket = new EventTarget();
  socket.readyState = 0;
  socket.sent = [];
  socket.send = (frame) => socket.sent.push(JSON.parse(frame));
  socket.close = () => { socket.readyState = 3; };
  const injected = [];
  const client = createMailboxClient({
    agentId: "pr-watch",
    onEnvelope: (message) => injected.push(message),
    webSocketFactory: () => socket,
    generateId: (() => { let number = 0; return () => `id-${++number}`; })(),
  });

  const connected = client.connect();
  socket.readyState = 1;
  socket.dispatchEvent(new Event("open"));
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({
      type: "registered", agent_id: "pr-watch", instance_id: client.instanceId, lease_id: "lease",
    }),
  }));
  await connected;
  const message = { id: "message-1", from: "reminders", to: "pr-watch", subject: "PR watch wakeup", body: "Run one PR watch scan now." };
  socket.dispatchEvent(new MessageEvent("message", {
    data: JSON.stringify({ type: "message", message, attempt_number: 1 }),
  }));
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(injected, [message]);
  assert.deepEqual(socket.sent.map(({ type, message_id }) => ({ type, message_id })), [
    { type: "register", message_id: undefined },
  ]);
  client.close();
});
