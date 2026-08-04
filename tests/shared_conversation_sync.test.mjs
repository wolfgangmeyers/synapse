import assert from "node:assert/strict";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { DatabaseSync } from "node:sqlite";

import WebSocket from "ws";

import {
  DEFAULT_CONVERSATION_HISTORY_LIMIT,
  DEFAULT_CONVERSATION_RETENTION_MS,
  openMessageRepository,
} from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const at = (day, second = 0) => `2026-07-${String(day).padStart(2, "0")}T00:00:${String(second).padStart(2, "0")}.000Z`;

async function fixture(options = {}, repositoryOptions = {}) {
  const directory = await mkdtemp(join(tmpdir(), "shared-conversation-"));
  let now = Date.parse(at(1));
  const clock = () => new Date(now);
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock,
    maxBatchBodyChars: repositoryOptions.maxBatchBodyChars ?? 0,
  });
  const attachmentDirectory = join(directory, "attachments");
  const router = createAgentRouter({
    repository,
    clock,
    imageAttachmentDirectory: attachmentDirectory,
    timeoutSweepIntervalMs: 60_000,
    ...options,
  });
  const wsUrl = await router.listen();
  return {
    directory,
    repository,
    router,
    wsUrl,
    httpUrl: wsUrl.replace("ws://", "http://").replace("/v1/agents", ""),
    attachmentDirectory,
    setNow(value) { now = Date.parse(value); },
    async close() {
      await router.close();
      repository.close();
      await rm(directory, { recursive: true, force: true });
    },
  };
}

function nextFrame(socket, predicate = () => true, timeoutMs = 1_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(reject, new Error("timed out waiting for frame")), timeoutMs);
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (predicate(frame)) cleanup(resolve, frame);
    };
    const onError = (error) => cleanup(reject, error);
    const cleanup = (done, value) => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
      done(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

async function connect(wsUrl, { agentId, instanceId, sync = true, role } = {}) {
  const socket = new WebSocket(wsUrl);
  await new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });
  const response = nextFrame(socket);
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: `register-${instanceId}`,
    type: "register",
    agent_id: agentId,
    instance_id: instanceId,
    ...(role === undefined ? {} : { agent_role: role }),
    ...(sync ? { conversation_sync: "v1" } : {}),
  }));
  return { socket, registered: await response };
}

async function renew(client, instanceId, requestId) {
  const response = nextFrame(client.socket, (frame) => frame.type === "registered" && frame.request_id === requestId);
  client.socket.send(JSON.stringify({
    version: "v1",
    frame_id: requestId,
    type: "lease_renew",
    agent_id: "shared-user",
    instance_id: instanceId,
    lease_id: client.registered.lease_id,
  }));
  return response;
}

function onceClose(socket, timeoutMs = 1_000) {
  if (socket.readyState === WebSocket.CLOSED) return Promise.resolve();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(reject, new Error("timed out waiting for socket close")), timeoutMs);
    const onClose = () => cleanup(resolve);
    const cleanup = (done, value) => {
      clearTimeout(timer);
      socket.off("close", onClose);
      done(value);
    };
    socket.once("close", onClose);
  });
}

function tinyWebpDataUrl() {
  return "data:image/webp;base64,UklGRioCAABXRUJQVlA4WAoAAAAgAAAAHwAAHwAASUNDUMgBAAAAAAHIAAAAAAQwAABtbnRyUkdCIFhZWiAH4AABAAEAAAAAAABhY3NwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAA9tYAAQAAAADTLQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAlkZXNjAAAA8AAAACRyWFlaAAABFAAAABRnWFlaAAABKAAAABRiWFlaAAABPAAAABR3dHB0AAABUAAAABRyVFJDAAABZAAAAChnVFJDAAABZAAAAChiVFJDAAABZAAAAChjcHJ0AAABjAAAADxtbHVjAAAAAAAAAAEAAAAMZW5VUwAAAAgAAAAcAHMAUgBHAEJYWVogAAAAAAAAb6IAADj1AAADkFhZWiAAAAAAAABimQAAt4UAABjaWFlaIAAAAAAAACSgAAAPhAAAts9YWVogAAAAAAAA9tYAAQAAAADTLXBhcmEAAAAAAAQAAAACZmYAAPKnAAANWQAAE9AAAApbAAAAAAAAAABtbHVjAAAAAAAAAAEAAAAMZW5VUwAAACAAAAAcAEcAbwBvAGcAbABlACAASQBuAGMALgAgADIAMAAxADZWUDggPAAAAFADAJ0BKiAAIAA+YS6TRyQioaEoCACADAllAHYA/AAAgyggAP7lhz//6En4JPwSfun///hVuGIZOgAAAA==";
}

async function postMessage(httpUrl, message) {
  const response = await fetch(new URL("/v1/messages", httpUrl), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(message),
  });
  return { response, body: await response.json() };
}

test("migration 013 upgrades schema 012 without disturbing queued data", async () => {
  const directory = await mkdtemp(join(tmpdir(), "conversation-migration-"));
  const databasePath = join(directory, "router.sqlite");
  let repository = openMessageRepository({ databasePath });
  try {
    repository.enqueue({ id: "before-conversations", from: "sender", to: "peer", body: "queued", created_at: at(1) });
    repository.close();
    const database = new DatabaseSync(databasePath);
    try {
      database.exec("DROP TABLE conversation_messages");
      database.prepare("DELETE FROM schema_migrations WHERE id = ?").run("013_conversation_history");
    } finally {
      database.close();
    }
    repository = openMessageRepository({ databasePath });
    assert.equal(repository.messageFor("before-conversations").body, "queued");
    assert.equal(repository.migrationIds().at(-1), "014_reminder_batch_links");
    repository.enqueue({ id: "after-conversations", from: "sender", to: "peer", body: "recorded", created_at: at(1, 1) });
    assert.equal(repository.conversationHistory({ owner: "peer", peer: "sender" })[0].id, "after-conversations");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("conversation storage is idempotent and prunes by trusted age and per-peer count", async () => {
  const directory = await mkdtemp(join(tmpdir(), "conversation-storage-"));
  let now = Date.parse(at(1));
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock: () => new Date(now),
    maxBatchBodyChars: 0,
    maxQueuedMessages: 500,
  });
  try {
    assert.equal(DEFAULT_CONVERSATION_HISTORY_LIMIT, 200);
    assert.equal(DEFAULT_CONVERSATION_RETENTION_MS, 30 * 24 * 60 * 60 * 1_000);
    const first = {
      id: "conversation-1",
      from: "shared-user",
      to: "peer",
      body: "first",
      created_at: "2099-01-01T00:00:00.000Z",
    };
    repository.enqueue(first, {
      conversation: {
        body: "first",
        attachments: [{ id: "image.webp", url: "/v1/attachments/image.webp", mime_type: "image/webp", size_bytes: 16 }],
      },
    });
    repository.enqueue(first, {
      conversation: {
        body: "first",
        attachments: [{ id: "image.webp", url: "/v1/attachments/image.webp", mime_type: "image/webp", size_bytes: 16 }],
      },
    });
    assert.equal(repository.conversationHistory({ owner: "shared-user", peer: "peer" }).length, 1);
    assert.equal(repository.conversationAttachmentReferenced("image.webp"), true);

    for (let index = 2; index <= 202; index += 1) {
      now += 1_000;
      repository.enqueue({
        id: `conversation-${index}`,
        from: index % 2 === 0 ? "peer" : "shared-user",
        to: index % 2 === 0 ? "shared-user" : "peer",
        body: `message ${index}`,
        created_at: at(1, index % 60),
      });
    }
    const countPrune = repository.pruneConversationHistory();
    assert.equal(countPrune.deleted_count, 2);
    const retained = repository.conversationHistory({ owner: "shared-user", peer: "peer" });
    assert.equal(retained.length, 200);
    assert.equal(retained[0].id, "conversation-3");
    assert.equal(repository.conversationAttachmentReferenced("image.webp"), false);
    assert.deepEqual(countPrune.unreferenced_attachment_ids, ["image.webp"]);

    now += DEFAULT_CONVERSATION_RETENTION_MS + 1;
    const agePrune = repository.pruneConversationHistory();
    assert.equal(agePrune.deleted_count, 200);
    assert.deepEqual(repository.conversationHistory({ owner: "peer", peer: "shared-user" }), []);
    assert.equal(repository.conversationHighWaterSequence(), 202);

    repository.enqueue({
      id: "legacy-email-sender",
      from: "external@example.com",
      to: "shared-user",
      body: "legacy sender",
      created_at: at(1),
    });
    assert.equal(repository.conversationHistory({ owner: "shared-user", peer: "external@example.com" })[0].id, "legacy-email-sender");
  } finally {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("two opted-in instances share live outbound and inbound events while legacy duplicate identity still rejects", async () => {
  const app = await fixture();
  const sockets = [];
  try {
    const first = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "web-one", role: "user_interaction" });
    const second = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "android-one" });
    sockets.push(first.socket, second.socket);
    assert.equal(first.registered.type, "registered");
    assert.equal(second.registered.type, "registered");
    assert.equal(first.registered.conversation_sync, "v1");
    assert.equal(second.registered.conversation_sync, "v1");

    const firstOutbound = nextFrame(first.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "outbound-1");
    const secondOutbound = nextFrame(second.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "outbound-1");
    const outbound = await postMessage(app.httpUrl, {
      id: "outbound-1",
      from: "shared-user",
      to: "peer",
      body: "from web",
      created_at: at(1),
    });
    assert.equal(outbound.response.status, 201);
    assert.equal(outbound.body.conversation_message.id, "outbound-1");
    assert.equal((await firstOutbound).message.body, "from web");
    assert.equal((await secondOutbound).message.body, "from web");

    const firstInbound = nextFrame(first.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "inbound-1");
    const secondInbound = nextFrame(second.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "inbound-1");
    const inbound = await postMessage(app.httpUrl, {
      id: "inbound-1",
      from: "peer",
      to: "shared-user",
      body: "to both clients",
      created_at: at(1, 1),
    });
    assert.equal(inbound.response.status, 201);
    assert.equal((await firstInbound).message.body, "to both clients");
    assert.equal((await secondInbound).message.body, "to both clients");

    const historyResponse = await fetch(new URL("/v1/conversations?owner=shared-user&peer=peer", app.httpUrl));
    assert.equal(historyResponse.status, 200);
    assert.equal(historyResponse.headers.get("cache-control"), "no-store");
    const snapshot = await historyResponse.json();
    assert.deepEqual(snapshot.messages.map(({ id }) => id), ["outbound-1", "inbound-1"]);
    assert.deepEqual(snapshot.messages.map(({ sequence }) => sequence), [1, 2]);
    assert.equal(snapshot.high_water_sequence, 2);
    for (const query of ["peer=peer", "owner=bad%40owner&peer=peer", "owner=shared-user&peer="]) {
      const invalid = await fetch(new URL(`/v1/conversations?${query}`, app.httpUrl));
      assert.equal(invalid.status, 400);
      assert.equal((await invalid.json()).error.code, "invalid_request");
    }

    const legacyOne = await connect(app.wsUrl, { agentId: "legacy-user", instanceId: "legacy-one", sync: false });
    sockets.push(legacyOne.socket);
    assert.equal(legacyOne.registered.type, "registered");
    const legacyTwo = await connect(app.wsUrl, { agentId: "legacy-user", instanceId: "legacy-two", sync: false });
    sockets.push(legacyTwo.socket);
    assert.equal(legacyTwo.registered.type, "lease_rejected");
  } finally {
    for (const socket of sockets) socket.terminate();
    await app.close();
  }
});

test("a synced sender preserves batching for a legacy recipient while receiving its canonical response", async () => {
  const app = await fixture({}, { maxBatchBodyChars: 4_096 });
  let sender;
  try {
    sender = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "web-sender" });
    app.router.enqueueMessage({ id: "legacy-first", from: "other", to: "legacy-peer", body: "first", created_at: at(1) });
    const live = nextFrame(sender.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "legacy-second");
    const second = await postMessage(app.httpUrl, {
      id: "legacy-second",
      from: "shared-user",
      to: "legacy-peer",
      body: "second",
      created_at: at(1, 1),
    });
    assert.equal(second.response.status, 200);
    assert.equal(second.body.message.batched, true);
    assert.equal(second.body.message.id, "legacy-first");
    assert.equal(second.body.conversation_message.id, "legacy-second");
    assert.equal((await live).message.body, "second");
    const retry = await postMessage(app.httpUrl, {
      id: "legacy-second",
      from: "shared-user",
      to: "legacy-peer",
      body: "second",
      created_at: at(1, 1),
    });
    assert.equal(retry.response.status, 200);
    assert.equal(retry.body.conversation_message.id, "legacy-second");
    assert.equal(app.repository.queuedFor("legacy-peer").length, 1);
    assert.equal(app.repository.queuedFor("legacy-peer")[0].body.match(/second/g)?.length, 1);
    assert.equal(app.repository.conversationHistory({ owner: "shared-user", peer: "legacy-peer" }).filter(({ id }) => id === "legacy-second").length, 1);
  } finally {
    sender?.socket.terminate();
    await app.close();
  }
});

test("rapid posts to a connected idle recipient coalesce before the first delivery", async () => {
  const app = await fixture({ deliveryOfferCoalesceMs: 25 }, { maxBatchBodyChars: 4_096 });
  let recipient;
  const delivered = [];
  try {
    recipient = await connect(app.wsUrl, { agentId: "burst-recipient", instanceId: "burst-owner", sync: false });
    recipient.socket.on("message", (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (frame.type !== "message") return;
      delivered.push(frame);
      for (const type of ["injection_requested", "ack"]) {
        recipient.socket.send(JSON.stringify({
          version: "v1",
          frame_id: `${type}-${frame.message.id}-${frame.attempt_number}`,
          type,
          agent_id: "burst-recipient",
          instance_id: "burst-owner",
          lease_id: recipient.registered.lease_id,
          message_id: frame.message.id,
          attempt_number: frame.attempt_number,
        }));
      }
    });

    const bodies = ["1", "2", "3", "4", "5", "6", "7", "8", "98", "0"];
    await Promise.all(bodies.map((body, index) => postMessage(app.httpUrl, {
      id: `burst-${index}`,
      from: "web-ui-sender",
      to: "burst-recipient",
      body,
      created_at: at(1, index),
    }).then(({ response }) => assert.ok([200, 201].includes(response.status), `unexpected status ${response.status}`))));
    await new Promise((resolve) => setTimeout(resolve, 150));

    assert.equal(delivered.length, 1);
    assert.equal(delivered[0].message.id, "burst-0");
    assert.match(delivered[0].message.body, /^Mailbox message batch\n\n/);
    assert.equal((delivered[0].message.body.match(/\n\n---\n\n/g) ?? []).length, 9);
    for (const body of bodies) assert.match(delivered[0].message.body, new RegExp(`\\n${body}\\n`));
    assert.equal(app.repository.conversationHistory({ owner: "web-ui-sender", peer: "burst-recipient" }).length, 10);
    const database = new DatabaseSync(join(app.directory, "router.sqlite"), { readOnly: true });
    try {
      const deliveryGroups = database.prepare(`
        SELECT delivery_message_id, COUNT(*) AS item_count
        FROM conversation_messages
        WHERE sender = 'web-ui-sender' AND recipient = 'burst-recipient'
        GROUP BY delivery_message_id
      `).all().map((row) => ({ ...row }));
      assert.deepEqual(deliveryGroups, [{ delivery_message_id: "burst-0", item_count: 10 }]);
    } finally {
      database.close();
    }
  } finally {
    recipient?.socket.terminate();
    await app.close();
  }
});

test("synced lease survives renewal, subscriber replacement, and owner handoff without duplicate delivery", async () => {
  const routerEvents = [];
  const app = await fixture({ eventLogger: (event, fields) => routerEvents.push({ event, ...fields }) });
  let owner;
  let subscriber;
  let replacement;
  try {
    owner = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "owner-one" });
    subscriber = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "owner-two" });
    assert.equal(subscriber.registered.lease_id, owner.registered.lease_id);

    app.setNow(at(1, 1));
    const ownerRenewed = await renew(owner, "owner-one", "renew-owner-one");
    app.setNow(at(1, 2));
    const subscriberRenewed = await renew(subscriber, "owner-two", "renew-owner-two");
    assert.equal(ownerRenewed.lease_id, owner.registered.lease_id);
    assert.equal(subscriberRenewed.lease_id, owner.registered.lease_id);
    assert.ok(Date.parse(subscriberRenewed.expires_at) > Date.parse(ownerRenewed.expires_at));

    const replaced = nextFrame(subscriber.socket, (frame) => frame.type === "error" && frame.code === "lease_expired");
    const subscriberClosed = onceClose(subscriber.socket);
    replacement = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "owner-two" });
    assert.equal(replacement.registered.type, "registered");
    assert.equal(replacement.registered.lease_id, subscriberRenewed.lease_id);
    await replaced;
    await subscriberClosed;
    const activeLease = app.repository.leaseFor("shared-user");
    assert.equal(activeLease.instance_id, "owner-one");
    assert.equal(activeLease.lease_id, subscriberRenewed.lease_id);
    assert.equal(activeLease.expires_at, subscriberRenewed.expires_at);

    const replacementLive = nextFrame(replacement.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "after-replacement");
    await postMessage(app.httpUrl, {
      id: "after-replacement",
      from: "shared-user",
      to: "peer",
      body: "replacement remains live",
      created_at: at(1, 3),
    });
    assert.equal((await replacementLive).message.body, "replacement remains live");

    const ownerClosed = onceClose(owner.socket);
    owner.socket.terminate();
    await ownerClosed;

    let deliveryCount = 0;
    replacement.socket.on("message", (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (frame.type === "message" && frame.message.id === "handoff-1") deliveryCount += 1;
    });
    const live = nextFrame(replacement.socket, (frame) => frame.type === "conversation_message" && frame.message.id === "handoff-1");
    const delivery = nextFrame(replacement.socket, (frame) => frame.type === "message" && frame.message.id === "handoff-1");
    const sent = await postMessage(app.httpUrl, {
      id: "handoff-1",
      from: "peer",
      to: "shared-user",
      body: "after owner close",
      created_at: at(1, 3),
    });
    assert.equal(sent.response.status, 201);
    assert.equal((await live).message.body, "after owner close");
    const offered = await delivery;
    for (const type of ["injection_requested", "ack"]) {
      replacement.socket.send(JSON.stringify({
        version: "v1",
        frame_id: `${type}-handoff`,
        type,
        agent_id: "shared-user",
        instance_id: "owner-two",
        lease_id: replacement.registered.lease_id,
        message_id: offered.message.id,
        attempt_number: offered.attempt_number,
      }));
    }
    for (let attempt = 0; attempt < 50 && app.repository.messageFor("handoff-1") !== undefined; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    assert.equal(app.repository.messageFor("handoff-1"), undefined);
    assert.equal(deliveryCount, 1);
    assert.equal(routerEvents.filter(({ event, message_id: id }) => event === "delivery_offered" && id === "handoff-1").length, 1);
    assert.equal(routerEvents.filter(({ event, message_id: id }) => event === "delivery_acknowledged" && id === "handoff-1").length, 1);
  } finally {
    owner?.socket.terminate();
    subscriber?.socket.terminate();
    replacement?.socket.terminate();
    await app.close();
  }
});

test("a new synced instance recovers an expired in-memory subscriber lease", async () => {
  const app = await fixture();
  let stale;
  let recovered;
  try {
    stale = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "stale-instance" });
    app.setNow(at(1, 31));
    recovered = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "fresh-instance" });
    assert.equal(recovered.registered.type, "registered");
    assert.notEqual(recovered.registered.lease_id, stale.registered.lease_id);
    assert.equal(recovered.registered.recovered, true);
  } finally {
    stale?.socket.terminate();
    recovered?.socket.terminate();
    await app.close();
  }
});

test("conversation history and image references survive a router repository restart", async () => {
  const directory = await mkdtemp(join(tmpdir(), "conversation-restart-"));
  const databasePath = join(directory, "router.sqlite");
  const attachmentDirectory = join(directory, "router.sqlite.attachments");
  let repository = openMessageRepository({ databasePath, maxBatchBodyChars: 0 });
  let router = createAgentRouter({ repository, imageAttachmentDirectory: attachmentDirectory, timeoutSweepIntervalMs: 60_000 });
  let wsUrl = await router.listen();
  try {
    const first = await postMessage(wsUrl.replace("ws://", "http://").replace("/v1/agents", ""), {
      id: "restart-image",
      from: "shared-user",
      to: "peer",
      body: "survives restart",
      created_at: at(1),
      attachments: [{ type: "image", data_url: tinyWebpDataUrl(), name: "restart.webp" }],
    });
    assert.equal(first.response.status, 201);
    const attachmentUrl = first.body.message.attachments[0].url;
    await router.close();
    repository.close();

    repository = openMessageRepository({ databasePath, maxBatchBodyChars: 0 });
    router = createAgentRouter({ repository, imageAttachmentDirectory: attachmentDirectory, timeoutSweepIntervalMs: 60_000 });
    wsUrl = await router.listen();
    const httpUrl = wsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const history = await (await fetch(new URL("/v1/conversations?owner=shared-user&peer=peer", httpUrl))).json();
    assert.equal(history.messages[0].body, "survives restart");
    assert.equal(history.messages[0].attachments[0].id, first.body.message.attachments[0].id);
    assert.equal((await fetch(new URL(attachmentUrl, httpUrl))).status, 200);
  } finally {
    await router.close().catch(() => {});
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("history survives reconnect and retained attachment references outlive the temporary image TTL", async () => {
  const app = await fixture({ imageAttachmentTtlMs: 1_000 });
  let socket;
  try {
    const connected = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "web-before" });
    socket = connected.socket;
    const event = nextFrame(socket, (frame) => frame.type === "conversation_message" && frame.message.id === "image-message");
    const sent = await postMessage(app.httpUrl, {
      id: "image-message",
      from: "shared-user",
      to: "peer",
      body: "durable image",
      created_at: at(1),
      attachments: [{ type: "image", data_url: tinyWebpDataUrl(), name: "proof.webp" }],
    });
    assert.equal(sent.response.status, 201);
    const accepted = sent.body.message;
    assert.equal(accepted.attachments.length, 1);
    const live = await event;
    assert.deepEqual(live.message.attachments, accepted.attachments);
    const conflictingMetadata = await postMessage(app.httpUrl, {
      id: "image-message",
      from: "shared-user",
      to: "peer",
      body: "durable image",
      created_at: at(1),
      attachments: [{ type: "image", data_url: tinyWebpDataUrl(), name: "different-name.webp" }],
    });
    assert.equal(conflictingMetadata.response.status, 409);
    socket.terminate();

    app.setNow(at(2));
    app.router.sweepMessageLifecycles();
    const imageResponse = await fetch(new URL(accepted.attachments[0].url, app.httpUrl));
    assert.equal(imageResponse.status, 200);
    assert.equal(imageResponse.headers.get("content-type"), "image/webp");

    const reconnected = await connect(app.wsUrl, { agentId: "shared-user", instanceId: "android-after" });
    socket = reconnected.socket;
    assert.equal(reconnected.registered.type, "registered");
    const historyResponse = await fetch(new URL("/v1/conversations?owner=shared-user&peer=peer", app.httpUrl));
    const history = await historyResponse.json();
    assert.equal(history.messages.length, 1);
    assert.deepEqual(history.messages[0].attachments, accepted.attachments);

    app.setNow("2026-08-02T00:00:00.001Z");
    app.router.sweepMessageLifecycles();
    await new Promise((resolve) => setTimeout(resolve, 25));
    assert.equal((await fetch(new URL(accepted.attachments[0].url, app.httpUrl))).status, 404);
    assert.deepEqual(await readdir(app.attachmentDirectory), []);
  } finally {
    socket?.terminate();
    await app.close();
  }
});
