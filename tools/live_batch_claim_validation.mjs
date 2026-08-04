#!/usr/bin/env node
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const recipient = `batch-recipient-${Date.now()}`;
const directory = await mkdtemp(join(tmpdir(), "mecha-live-claim-batch-"));
const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
let socket;
let frameId = 0;

function envelope(index) {
  return {
    id: `live-batch-${index}`,
    from: `sender-${index}`,
    to: recipient,
    subject: "Claim-time batch validation",
    body: `live validation body ${index}`,
    created_at: "2026-07-13T00:00:00.000Z",
  };
}

function frameQueue(ws) {
  const frames = [];
  const waiters = [];
  ws.on("message", (payload) => {
    const frame = JSON.parse(payload.toString("utf8"));
    const waiter = waiters.shift();
    if (waiter === undefined) frames.push(frame);
    else waiter.resolve(frame);
  });
  ws.on("error", (error) => {
    const waiter = waiters.shift();
    if (waiter !== undefined) waiter.reject(error);
  });
  return {
    next() {
      const frame = frames.shift();
      if (frame !== undefined) return Promise.resolve(frame);
      return new Promise((resolve, reject) => waiters.push({ resolve, reject }));
    },
  };
}

try {
  for (let index = 1; index <= 10; index += 1) {
    repository.enqueue(envelope(index), { disableBatching: true });
  }
  assert.equal(repository.queuedFor(recipient).length, 10);

  const websocketUrl = await router.listen();
  socket = new WebSocket(websocketUrl);
  await new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });

  const frames = frameQueue(socket);
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: `validation-${++frameId}`,
    type: "register",
    agent_id: recipient,
    instance_id: "claim-batch-validation",
  }));
  assert.equal((await frames.next()).type, "registered");

  const delivery = await frames.next();
  assert.equal(delivery.type, "message");
  assert.equal(delivery.message.id, "live-batch-1");
  assert.match(delivery.message.body, /^Mailbox message batch\n\n/);
  assert.equal((delivery.message.body.match(/\n\n---\n\n/g) ?? []).length, 9);
  for (let index = 1; index <= 10; index += 1) {
    assert.match(delivery.message.body, new RegExp(`live validation body ${index}`));
  }
  console.log(JSON.stringify({
    ok: true,
    recipient,
    delivery_id: delivery.message.id,
    batched_items: 10,
    queued_after_claim: repository.queuedFor(recipient).length,
  }));
} finally {
  socket?.terminate();
  await router.close();
  repository.close();
  await rm(directory, { recursive: true, force: true });
}
