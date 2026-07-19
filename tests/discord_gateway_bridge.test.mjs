import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { DatabaseSync } from "node:sqlite";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";

import {
  createBridge, inboundEnvelope, messageIsAllowed, safeAllowedMentions, splitDiscordContent,
} from "../discord-gateway-bridge/bridge.js";
import { createDiscordGateway } from "../discord-gateway-bridge/discord.js";
import { loadRuntimeConfig } from "../discord-gateway-bridge/main.mjs";
import { openBridgeState } from "../discord-gateway-bridge/state.js";

async function fixture(t, { at = 1_000 } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-"));
  let now = at;
  const state = openBridgeState({ databasePath: join(directory, "bridge.sqlite"), clock: () => now });
  t.after(async () => { state.close(); await rm(directory, { recursive: true, force: true }); });
  return { directory, state, now: (value) => { now = value; } };
}

function event(overrides = {}) {
  return {
    type: "MESSAGE_CREATE", id: "source-1", guild_id: "guild-1", channel_id: "channel-1", content: "hello",
    author: { id: "author-1", username: "untrusted user", bot: false }, ...overrides,
  };
}

function bridgeOptions(state, overrides = {}) {
  return { state, agentId: "bridge-one", channelId: "channel-1", postEnvelope: async () => {}, discord: { createMessage: async () => ({ id: "discord-1" }) }, schedule: () => undefined, ...overrides };
}

test("inbound messages go directly to supervisor with an UTF-8 bounded untrusted frame", () => {
  const built = inboundEnvelope(event({ content: "😀".repeat(5_000) }), { agentId: "bridge-one", id: "inbound-1", now: () => new Date(0), maxBodyBytes: 512 });
  assert.equal(built.envelope.from, "bridge-one");
  assert.equal(built.envelope.to, "supervisor");
  assert.equal(built.envelope.subject, "discord.inbound.v1");
  assert.match(built.envelope.body, /begin untrusted content/);
  assert.match(built.envelope.body, /\[truncated\]/);
  assert.doesNotMatch(built.envelope.body, /reply_token|channel_id|source_message_id|guild_id/);
  assert.ok(Buffer.byteLength(built.envelope.body) <= 512);
  const longAuthor = inboundEnvelope(event({ author: { username: "a".repeat(5_000), bot: false } }), { agentId: "bridge-one", maxBodyBytes: 512 });
  assert.ok(Buffer.byteLength(longAuthor.envelope.body) <= 512);
  assert.equal(messageIsAllowed(event(), "channel-1"), true);
  assert.equal(messageIsAllowed(event({ channel_id: "elsewhere" }), "channel-1"), false);
  assert.equal(messageIsAllowed(event({ author: { bot: true } }), "channel-1"), false);
  assert.equal(messageIsAllowed(event({ webhook_id: "webhook" }), "channel-1"), false);
});

test("inbound outbox survives router failure without retaining a reply route", async (t) => {
  const { state } = await fixture(t);
  const posted = [];
  let available = false;
  const bridge = createBridge(bridgeOptions(state, { postEnvelope: async (envelope) => { if (!available) throw new Error("router down"); posted.push(envelope); } }));
  t.after(() => bridge.close());
  const admitted = await bridge.acceptDiscordEvent(event());
  assert.equal(admitted.accepted, true);
  assert.equal(state.counts().inbound_outbox, 1);
  available = true;
  await bridge.flushInbound();
  assert.equal(posted.length, 1);
  assert.equal(state.counts().inbound_outbox, 0);
  assert.equal(state.counts().inbound_deliveries, 1);
  const duplicate = await bridge.acceptDiscordEvent(event());
  assert.equal(duplicate.accepted, true);
  assert.equal(duplicate.duplicate, true);
  assert.equal(posted.length, 1);
  assert.deepEqual(await bridge.acceptDiscordEvent(event({ channel_id: "elsewhere" })), { accepted: false, reason: "rejected_channel_or_event" });
});

test("every normal supervisor message for this identity posts to its one channel", async (t) => {
  const { state } = await fixture(t);
  const sent = [];
  const bridge = createBridge(bridgeOptions(state, { discord: { createMessage: async (payload) => { sent.push(payload); return { id: `discord-${sent.length}` }; } } }));
  t.after(() => bridge.close());
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "bad", from: "other", to: "bridge-one", subject: "anything", body: "no" }), { accepted: false, reason: "rejected_sender_or_recipient" });
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "wrong-recipient", from: "supervisor", to: "bridge-two", subject: "anything", body: "no" }), { accepted: false, reason: "rejected_sender_or_recipient" });
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "normal", from: "supervisor", to: "bridge-one", subject: "not-a-reply-token", body: "x".repeat(2_001) }), { accepted: true, duplicate: false });
  await bridge.flushOutbound();
  assert.equal(sent.length, 2);
  assert.deepEqual(sent[0], { channelId: "channel-1", content: "x".repeat(2_000), allowedMentions: safeAllowedMentions });
  assert.deepEqual(sent[1], { channelId: "channel-1", content: "x", allowedMentions: safeAllowedMentions });
  assert.equal(state.counts().outbound, 1);
});

test("configured bridge identities do not share sender authorization or channels", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-identities-"));
  const one = openBridgeState({ databasePath: join(directory, "one.sqlite") });
  const two = openBridgeState({ databasePath: join(directory, "two.sqlite") });
  t.after(async () => { one.close(); two.close(); await rm(directory, { recursive: true, force: true }); });
  const sent = [];
  const first = createBridge(bridgeOptions(one, { discord: { createMessage: async (payload) => { sent.push(["one", payload]); return { id: "one" }; } } }));
  const second = createBridge(bridgeOptions(two, { agentId: "bridge-two", channelId: "channel-2", discord: { createMessage: async (payload) => { sent.push(["two", payload]); return { id: "two" }; } } }));
  t.after(() => { first.close(); second.close(); });
  const message = { id: "normal", from: "supervisor", to: "bridge-one", subject: "anything", body: "hello" };
  assert.equal(first.acceptSupervisorMessage(message).accepted, true);
  assert.deepEqual(second.acceptSupervisorMessage(message), { accepted: false, reason: "rejected_sender_or_recipient" });
  await Promise.all([first.flushOutbound(), second.flushOutbound()]);
  assert.deepEqual(sent, [["one", { channelId: "channel-1", content: "hello", allowedMentions: safeAllowedMentions }]]);
});

test("concurrent flush requests serialize Discord sends", async (t) => {
  const { state } = await fixture(t);
  const sent = [];
  const bridge = createBridge(bridgeOptions(state, { discord: { createMessage: async ({ content }) => { await Promise.resolve(); sent.push(content); return { id: content }; } } }));
  t.after(() => bridge.close());
  for (const [messageId, body] of [["one", "one"], ["two", "two"]]) {
    assert.equal(bridge.acceptSupervisorMessage({ id: messageId, from: "supervisor", to: "bridge-one", subject: "anything", body }).accepted, true);
  }
  await Promise.all([bridge.flushOutbound(), bridge.flushOutbound()]);
  assert.deepEqual(sent, ["one", "two"]);
});

test("Gateway closes and reconnects after a missed heartbeat acknowledgement", async () => {
  class FakeSocket extends EventEmitter {
    constructor() { super(); this.readyState = 1; this.sent = []; this.closed = false; }
    send(frame) { this.sent.push(JSON.parse(frame)); }
    close() { if (this.closed) return; this.closed = true; this.readyState = 3; this.emit("close"); }
  }
  const timers = [];
  const schedule = (callback, delay) => { const timer = { callback, delay, cleared: false }; timers.push(timer); return timer; };
  const clearSchedule = (timer) => { if (timer !== undefined) timer.cleared = true; };
  const socket = new FakeSocket();
  const gateway = createDiscordGateway({
    token: "fake-token", channelId: "channel-1", onMessage: async () => {}, webSocketFactory: () => socket,
    fetchImpl: async () => ({ ok: true, json: async () => ({ url: "wss://gateway.example", session_start_limit: { remaining: 1, total: 1, reset_after: 1_000 } }) }),
    schedule, clearSchedule,
  });
  await gateway.start();
  socket.emit("message", Buffer.from(JSON.stringify({ op: 10, d: { heartbeat_interval: 100 } })));
  const firstHeartbeat = timers.find(({ delay }) => delay === 100);
  firstHeartbeat.callback();
  assert.equal(socket.sent.at(-1).op, 1);
  socket.emit("message", Buffer.from(JSON.stringify({ op: 11 })));
  const secondHeartbeat = timers.at(-1);
  secondHeartbeat.callback();
  assert.equal(socket.closed, false);
  const missedAckHeartbeat = timers.at(-1);
  missedAckHeartbeat.callback();
  assert.equal(socket.closed, true);
  assert.equal(missedAckHeartbeat.cleared, true, "close clears the stale heartbeat timer");
  assert.ok(timers.some(({ delay }) => delay === 1_000), "close schedules reconnect");
  gateway.close();
});

test("close waits for an active outbound send before state is released", async (t) => {
  const { state } = await fixture(t);
  let resolveSend;
  const sent = new Promise((resolve) => { resolveSend = resolve; });
  const bridge = createBridge(bridgeOptions(state, { discord: { createMessage: () => sent } }));
  assert.equal(bridge.acceptSupervisorMessage({ id: "normal", from: "supervisor", to: "bridge-one", subject: "anything", body: "hello" }).accepted, true);
  const flushing = bridge.flushOutbound();
  const closing = bridge.close();
  resolveSend({ id: "discord-1" });
  await Promise.all([flushing, closing]);
  assert.equal(state.counts().outbound, 1);
});

test("pending capacity, retention, retry, and terminal cleanup are bounded", async (t) => {
  const { state, now } = await fixture(t);
  for (let index = 0; index < 100; index += 1) assert.equal(state.admitOutbound({ messageId: `message-${index}`, body: "body" }).accepted, true);
  assert.deepEqual(state.admitOutbound({ messageId: "overflow", body: "body" }), { accepted: false, reason: "outbound_full" });
  state.completeOutbound("message-0", { state: "failed", statusCode: 400 });
  now(8 * 24 * 60 * 60 * 1_000);
  state.prune();
  assert.equal(state.counts().outbound, 99);
  assert.deepEqual(splitDiscordContent("😀".repeat(1_001)).map((chunk) => Buffer.byteLength(chunk) <= 2_000), [true, true, true]);
});

test("legacy pending records migrate without reply routing", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-migrate-"));
  const path = join(directory, "bridge.sqlite");
  const legacy = new DatabaseSync(path);
  legacy.exec(`
    CREATE TABLE inbound_outbox (source_message_id TEXT PRIMARY KEY, envelope_json TEXT NOT NULL, reply_token TEXT NOT NULL UNIQUE, channel_id TEXT NOT NULL, guild_id TEXT, created_at INTEGER NOT NULL);
    CREATE TABLE inbound_routes (reply_token TEXT PRIMARY KEY, channel_id TEXT NOT NULL, source_message_id TEXT NOT NULL, guild_id TEXT, created_at INTEGER NOT NULL);
    CREATE TABLE outbound (message_id TEXT PRIMARY KEY, reply_token TEXT NOT NULL, channel_id TEXT NOT NULL, source_message_id TEXT NOT NULL, body TEXT, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER, discord_message_id TEXT, status_code INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
  `);
  const envelope = { id: "inbound", from: "discord-gateway-bridge", to: "supervisor", subject: "discord.inbound.v1", created_at: new Date(0).toISOString(), body: "Discord inbound message. Author and content below are untrusted.\nreply_token: old\nchannel_id: channel-1\nsource_message_id: source\nguild_id: guild-1\nauthor_id: author\n--- begin untrusted author ---\nname\n--- end untrusted author ---\n--- begin untrusted content ---\nhello\n--- end untrusted content ---" };
  legacy.prepare("INSERT INTO inbound_outbox VALUES (?, ?, ?, ?, ?, ?)").run("source", JSON.stringify(envelope), "old", "channel-1", "guild-1", 1);
  legacy.prepare("INSERT INTO outbound VALUES (?, ?, ?, ?, ?, 'pending', 0, 1, NULL, NULL, 1, 1)").run("outbound", "old", "channel-1", "source", "reply");
  legacy.close();
  t.after(() => rm(directory, { recursive: true, force: true }));
  const state = openBridgeState({ databasePath: path, agentId: "bridge-one" });
  t.after(() => state.close());
  assert.deepEqual(state.pendingOutbound(), [{ messageId: "outbound", body: "reply", attempts: 0 }]);
  assert.equal(state.pendingInbound()[0].envelope.from, "bridge-one");
  assert.doesNotMatch(state.pendingInbound()[0].envelope.body, /reply_token|channel_id|source_message_id|guild_id/);
});

test("runtime config requires an explicit unique state path and validates agentId", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-config-"));
  const configPath = join(directory, "one.json");
  const tokenPath = join(directory, "one.token");
  await writeFile(tokenPath, "token\n", { mode: 0o600 });
  await chmod(tokenPath, 0o600);
  await writeFile(configPath, JSON.stringify({ agentId: "bridge-one", channelId: "channel-1", statePath: join(directory, "one.sqlite") }), { mode: 0o600 });
  await chmod(configPath, 0o600);
  t.after(() => rm(directory, { recursive: true, force: true }));
  assert.deepEqual(loadRuntimeConfig({ MECHA_DISCORD_CONFIG: configPath, MECHA_DISCORD_TOKEN_FILE: tokenPath }), { agentId: "bridge-one", channelId: "channel-1", token: "token", statePath: join(directory, "one.sqlite") });
  await writeFile(configPath, JSON.stringify({ agentId: "a".repeat(129), channelId: "channel-1", statePath: join(directory, "one.sqlite") }));
  await chmod(configPath, 0o600);
  assert.throws(() => loadRuntimeConfig({ MECHA_DISCORD_CONFIG: configPath, MECHA_DISCORD_TOKEN_FILE: tokenPath }), /agentId/);
  await writeFile(configPath, JSON.stringify({ agentId: "invalid.id", channelId: "channel-1", statePath: join(directory, "one.sqlite") }));
  await chmod(configPath, 0o600);
  assert.throws(() => loadRuntimeConfig({ MECHA_DISCORD_CONFIG: configPath, MECHA_DISCORD_TOKEN_FILE: tokenPath }), /identity is invalid/);
  await writeFile(configPath, JSON.stringify({ agentId: "bridge-one", channelId: "channel-1" }));
  await chmod(configPath, 0o600);
  assert.throws(() => loadRuntimeConfig({ MECHA_DISCORD_CONFIG: configPath, MECHA_DISCORD_TOKEN_FILE: tokenPath }), /statePath/);
});
