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
import {
  parsePioneerSquareRoute, parsePioneerSquareRouteSubject, pioneerSquareRouteSubject, pioneerSquareRouteTarget,
} from "../pi-extensions/mecha-messaging/mailbox-client.js";
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
  assert.deepEqual(built.envelope.source, { kind: "discord" });
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
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "normal", from: "supervisor", to: "bridge-one", subject: pioneerSquareRouteSubject("pioneer-square:#channel-1:123456789012345678"), body: "x".repeat(2_001) }), { accepted: true, duplicate: false });
  await bridge.flushOutbound();
  assert.equal(sent.length, 2);
  assert.deepEqual(sent[0], { channelId: "123456789012345678", content: "x".repeat(2_000), allowedMentions: safeAllowedMentions });
  assert.deepEqual(sent[1], { channelId: "123456789012345678", content: "x", allowedMentions: safeAllowedMentions });
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
  const message = { id: "normal", from: "supervisor", to: "bridge-one", subject: pioneerSquareRouteSubject("pioneer-square:#channel-1:123456789012345678"), body: "hello" };
  assert.equal(first.acceptSupervisorMessage(message).accepted, true);
  assert.deepEqual(second.acceptSupervisorMessage(message), { accepted: false, reason: "rejected_sender_or_recipient" });
  await Promise.all([first.flushOutbound(), second.flushOutbound()]);
  assert.deepEqual(sent, [["one", { channelId: "123456789012345678", content: "hello", allowedMentions: safeAllowedMentions }]]);
});

test("concurrent flush requests serialize Discord sends", async (t) => {
  const { state } = await fixture(t);
  const sent = [];
  const bridge = createBridge(bridgeOptions(state, { discord: { createMessage: async ({ content }) => { await Promise.resolve(); sent.push(content); return { id: content }; } } }));
  t.after(() => bridge.close());
  for (const [messageId, body] of [["one", "one"], ["two", "two"]]) {
    assert.equal(bridge.acceptSupervisorMessage({ id: messageId, from: "supervisor", to: "bridge-one", subject: pioneerSquareRouteSubject("pioneer-square:#channel-1:123456789012345678"), body }).accepted, true);
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
  assert.equal(bridge.acceptSupervisorMessage({ id: "normal", from: "supervisor", to: "bridge-one", subject: pioneerSquareRouteSubject("pioneer-square:#channel-1:123456789012345678"), body: "hello" }).accepted, true);
  const flushing = bridge.flushOutbound();
  const closing = bridge.close();
  resolveSend({ id: "discord-1" });
  await Promise.all([flushing, closing]);
  assert.equal(state.counts().outbound, 1);
});

test("pending capacity, retention, retry, and terminal cleanup are bounded", async (t) => {
  const { state, now } = await fixture(t);
  for (let index = 0; index < 100; index += 1) assert.equal(state.admitOutbound({ messageId: `message-${index}`, body: "body", channelId: "123456789012345678" }).accepted, true);
  assert.deepEqual(state.admitOutbound({ messageId: "overflow", body: "body", channelId: "123456789012345678" }), { accepted: false, reason: "outbound_full" });
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
  assert.deepEqual(state.pendingOutbound(), [{ messageId: "outbound", body: "reply", channelId: "channel-1", attempts: 0 }]);
  assert.equal(state.pendingInbound()[0].envelope.from, "bridge-one");
  assert.doesNotMatch(state.pendingInbound()[0].envelope.body, /reply_token|channel_id|source_message_id|guild_id/);
});

test("Pioneer Square route helpers round-trip canonical routes and targets", () => {
  const route = "pioneer-square:#dnsid:123456789012345678";
  const parsed = { channelName: "dnsid", channelId: "123456789012345678", route };
  assert.deepEqual(parsePioneerSquareRoute(route), parsed);
  assert.deepEqual(parsePioneerSquareRouteSubject(pioneerSquareRouteSubject(route)), parsed);
  assert.equal(pioneerSquareRouteSubject(route), `pioneer-square.route.v1:${route}`);
  assert.deepEqual(pioneerSquareRouteTarget(route), { to: "pioneer-square", subject: `pioneer-square.route.v1:${route}` });
});

test("Pioneer Square route helpers reject missing hash, whitespace, uppercase, delimiters, and wrong prefixes", () => {
  const invalidRoutes = [
    "pioneer-square:dnsid:123456789012345678",
    "pioneer-square:#dns_id:123456789012345678",
    `pioneer-square:#${"a".repeat(100)}:123456789012345678`,
    "pioneer-square:#DNSID:123456789012345678",
    "pioneer-square:#dns id:123456789012345678",
    "pioneer-square:#dnsid:1234567890123456",
    "pioneer-square:#dnsid:123456789012345678901",
    "pioneer-square:#dnsid:123456789012345678:extra",
    "other:#dnsid:123456789012345678",
    " pioneer-square:#dnsid:123456789012345678",
  ];
  for (const route of invalidRoutes) {
    assert.equal(parsePioneerSquareRoute(route), undefined, route);
    assert.throws(() => pioneerSquareRouteSubject(route), /invalid Pioneer Square route/);
  }
  assert.deepEqual(parsePioneerSquareRouteSubject("pioneer-square.route.v1:pioneer-square:#dnsid:123456789012345678"), {
    channelName: "dnsid", channelId: "123456789012345678", route: "pioneer-square:#dnsid:123456789012345678",
  });
  assert.equal(parsePioneerSquareRouteSubject("pioneer-square.route.v1:pioneer-square:#dnsid:123456789012345678:extra"), undefined);
  const maxLengthRoute = `pioneer-square:#${"a".repeat(99)}:123456789012345678`;
  assert.equal(parsePioneerSquareRoute(maxLengthRoute)?.channelName.length, 99);
});

test("outbound supervisor messages require a route and never admit invalid messages", async (t) => {
  const calls = [];
  const state = {
    admitInbound: () => ({ accepted: true }),
    admitOutbound: () => { calls.push("admit"); throw new Error("invalid route was admitted"); },
    pendingIncidents: () => [],
    raiseIncident: () => false,
  };
  const bridge = createBridge(bridgeOptions(state, { postEnvelope: async () => calls.push("post") }));
  t.after(() => bridge.close());
  const exactMissing = "Discord route metadata is required; provide subject 'pioneer-square.route.v1:pioneer-square:#<channel-name>:<channel-id>'.";
  const exactMalformed = "Discord route metadata is malformed; provide subject 'pioneer-square.route.v1:pioneer-square:#<channel-name>:<channel-id>'.";
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "missing", from: "supervisor", to: "bridge-one", body: "hello" }), { accepted: false, reason: "missing_discord_route", error: exactMissing });
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "unrelated", from: "supervisor", to: "bridge-one", subject: "anything", body: "hello" }), { accepted: false, reason: "missing_discord_route", error: exactMissing });
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "malformed", from: "supervisor", to: "bridge-one", subject: "pioneer-square.route.v1:pioneer-square:dnsid:123456789012345678", body: "hello" }), { accepted: false, reason: "malformed_discord_route", error: exactMalformed });
  assert.deepEqual(calls, []);
});

test("valid supervisor route is the outbound Discord destination", async (t) => {
  const { state } = await fixture(t);
  const sent = [];
  const bridge = createBridge(bridgeOptions(state, { channelId: "configured-inbound-only", discord: { createMessage: async (payload) => { sent.push(payload); return { id: "discord-1" }; } } }));
  t.after(() => bridge.close());
  const route = "pioneer-square:#dnsid:123456789012345678";
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "routed", from: "supervisor", to: "bridge-one", subject: pioneerSquareRouteSubject(route), body: "hello" }), { accepted: true, duplicate: false });
  await bridge.flushOutbound();
  assert.deepEqual(sent, [{ channelId: "123456789012345678", content: "hello", allowedMentions: safeAllowedMentions }]);
});

test("a pending route incident retries on a later rejected route", async (t) => {
  const { state } = await fixture(t);
  const attempts = [];
  const timers = [];
  let available = false;
  const bridge = createBridge(bridgeOptions(state, {
    postEnvelope: async (envelope) => { attempts.push(envelope); if (!available) throw new Error("router down"); },
    schedule: (callback, delay) => { const timer = { callback, delay }; timers.push(timer); return timer; },
  }));
  t.after(() => bridge.close());
  const exactMissing = "Discord route metadata is required; provide subject 'pioneer-square.route.v1:pioneer-square:#<channel-name>:<channel-id>'.";
  const exactMalformed = "Discord route metadata is malformed; provide subject 'pioneer-square.route.v1:pioneer-square:#<channel-name>:<channel-id>'.";
  const first = bridge.acceptSupervisorMessage({ id: "missing", from: "supervisor", to: "bridge-one", body: "hello" });
  assert.deepEqual(first, { accepted: false, reason: "missing_discord_route", error: exactMissing });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(attempts.length, 1);
  assert.equal(attempts[0].body, exactMissing);
  assert.deepEqual(state.pendingIncidents(), ["discord_route_missing"]);
  available = true;
  const second = bridge.acceptSupervisorMessage({ id: "malformed", from: "supervisor", to: "bridge-one", subject: "pioneer-square.route.v1:pioneer-square:dnsid:123456789012345678", body: "hello" });
  assert.deepEqual(second, { accepted: false, reason: "malformed_discord_route", error: exactMalformed });
  assert.equal(timers.length, 1);
  assert.equal(timers[0].delay, 1_000);
  await timers[0].callback();
  assert.equal(attempts.length, 2);
  assert.equal(attempts[1].body, "Discord bridge received a supervisor message without a valid routed destination; provide subject 'pioneer-square.route.v1:pioneer-square:#<channel-name>:<channel-id>'.");
  assert.deepEqual(state.pendingIncidents(), []);
});

test("schema v2 migration preserves destinations and terminal-fails legacy pending rows", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-v2-migrate-"));
  const path = join(directory, "bridge.sqlite");
  const legacy = new DatabaseSync(path);
  legacy.exec(`
    CREATE TABLE outbound (message_id TEXT PRIMARY KEY, body TEXT, channel_id TEXT, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER, discord_message_id TEXT, status_code INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
    PRAGMA user_version = 2;
  `);
  const insert = legacy.prepare("INSERT INTO outbound VALUES (?, ?, ?, ?, 0, 1, NULL, NULL, 1, 1)");
  insert.run("pending-valid", "keep", "123456789012345678", "pending");
  insert.run("pending-legacy", "drop", null, "pending");
  insert.run("succeeded", null, null, "succeeded");
  insert.run("failed", null, null, "failed");
  insert.run("ambiguous", null, null, "ambiguous");
  legacy.close();
  t.after(async () => { await rm(directory, { recursive: true, force: true }); });
  const state = openBridgeState({ databasePath: path, agentId: "bridge-one", clock: () => 1 });
  assert.deepEqual(state.pendingOutbound(), [{ messageId: "pending-valid", body: "keep", channelId: "123456789012345678", attempts: 0 }]);
  assert.deepEqual(state.pendingIncidents(), ["discord_route_missing"]);
  state.close();
  const checked = new DatabaseSync(path);
  assert.deepEqual(checked.prepare("SELECT message_id, body, state, channel_id FROM outbound ORDER BY message_id").all().map((row) => ({ ...row })), [
    { message_id: "ambiguous", body: null, state: "ambiguous", channel_id: null },
    { message_id: "failed", body: null, state: "failed", channel_id: null },
    { message_id: "pending-legacy", body: null, state: "failed", channel_id: null },
    { message_id: "pending-valid", body: "keep", state: "pending", channel_id: "123456789012345678" },
    { message_id: "succeeded", body: null, state: "succeeded", channel_id: null },
  ]);
  checked.close();
});

test("pending outbound destination survives close, reopen, and retry", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-reopen-"));
  const path = join(directory, "bridge.sqlite");
  const first = openBridgeState({ databasePath: path, agentId: "bridge-one" });
  first.admitOutbound({ messageId: "reopen", body: "hello", channelId: "123456789012345678" });
  first.close();
  const second = openBridgeState({ databasePath: path, agentId: "bridge-one" });
  const sent = [];
  const bridge = createBridge(bridgeOptions(second, { channelId: "configured-inbound-only", discord: { createMessage: async (payload) => { sent.push(payload); return { id: "discord-1" }; } } }));
  t.after(async () => { await bridge.close(); second.close(); await rm(directory, { recursive: true, force: true }); });
  await bridge.flushOutbound();
  assert.equal(sent[0].channelId, "123456789012345678");
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
