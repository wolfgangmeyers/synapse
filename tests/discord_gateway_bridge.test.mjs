import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import test from "node:test";

import {
  BRIDGE_AGENT_ID, createBridge, inboundEnvelope, messageIsAllowed, safeAllowedMentions, splitDiscordContent,
} from "../discord-gateway-bridge/bridge.js";
import { openBridgeState } from "../discord-gateway-bridge/state.js";
import { createDiscordGateway } from "../discord-gateway-bridge/discord.js";

async function fixture(t, { at = 1_000 } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "discord-bridge-"));
  let now = at;
  const state = openBridgeState({ databasePath: join(directory, "bridge.sqlite"), clock: () => now });
  t.after(async () => { state.close(); await rm(directory, { recursive: true, force: true }); });
  return { state, now: (value) => { now = value; } };
}

function event(overrides = {}) {
  return {
    type: "MESSAGE_CREATE", id: "source-1", guild_id: "guild-1", channel_id: "channel-1", content: "hello",
    author: { id: "author-1", username: "untrusted user", bot: false }, ...overrides,
  };
}

test("inbound messages have fixed routing and an UTF-8 bounded untrusted frame", () => {
  const built = inboundEnvelope(event({ content: "😀".repeat(5_000) }), { id: "reply-1", now: () => new Date(0), maxBodyBytes: 512 });
  assert.equal(built.envelope.from, BRIDGE_AGENT_ID);
  assert.equal(built.envelope.to, "supervisor");
  assert.equal(built.envelope.subject, "discord.inbound.v1");
  assert.equal(built.replyToken, built.envelope.id);
  assert.match(built.envelope.body, /begin untrusted content/);
  assert.match(built.envelope.body, /\[truncated\]/);
  assert.ok(Buffer.byteLength(built.envelope.body) <= 512);
  assert.equal(messageIsAllowed(event(), [{ guildId: "guild-1", channelId: "channel-1" }]), true);
  assert.equal(messageIsAllowed(event({ author: { bot: true } }), [{ guildId: "guild-1", channelId: "channel-1" }]), false);
  assert.equal(messageIsAllowed(event({ webhook_id: "webhook" }), [{ guildId: "guild-1", channelId: "channel-1" }]), false);
});

test("inbound outbox survives router failure and becomes a reply route only after acceptance", async (t) => {
  const { state } = await fixture(t);
  const posted = [];
  let available = false;
  const bridge = createBridge({ state, postEnvelope: async (envelope) => { if (!available) throw new Error("router down"); posted.push(envelope); }, discord: { createMessage() {} } });
  t.after(() => bridge.close());
  const admitted = await bridge.acceptDiscordEvent(event());
  assert.equal(admitted.accepted, true);
  assert.equal(state.counts().inbound_outbox, 1);
  assert.equal(state.routeFor(admitted.envelope.id), undefined);
  available = true;
  await bridge.flushInbound();
  assert.equal(posted.length, 1);
  assert.equal(state.counts().inbound_outbox, 0);
  assert.equal(state.routeFor(admitted.envelope.id).channelId, "channel-1");
  const duplicate = await bridge.acceptDiscordEvent(event());
  assert.equal(duplicate.accepted, true);
  assert.equal(duplicate.duplicate, true);
  assert.equal(posted.length, 1);
  bridge.close();
});

test("only a durable explicit supervisor reply reaches the original channel with safe mentions", async (t) => {
  const { state } = await fixture(t);
  const posted = [];
  const sent = [];
  const bridge = createBridge({ state, postEnvelope: async (envelope) => posted.push(envelope), discord: { createMessage: async (payload) => { sent.push(payload); return { id: `discord-${sent.length}` }; } } });
  t.after(() => bridge.close());
  const inbound = await bridge.acceptDiscordEvent(event());
  assert.equal(state.routeFor(inbound.envelope.id).sourceMessageId, "source-1");
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "bad", from: "other", to: BRIDGE_AGENT_ID, subject: `discord.reply.v1/${inbound.envelope.id}`, body: "no" }), { accepted: false, reason: "rejected_sender_or_subject" });
  assert.deepEqual(bridge.acceptSupervisorMessage({ id: "reply", from: "supervisor", to: BRIDGE_AGENT_ID, subject: `discord.reply.v1/${inbound.envelope.id}`, body: "x".repeat(2_001) }), { accepted: true, duplicate: false });
  await bridge.flushOutbound();
  assert.equal(sent.length, 2);
  assert.deepEqual(sent[0].allowedMentions, safeAllowedMentions);
  assert.equal(sent[0].messageReference, "source-1");
  assert.equal(sent[1].messageReference, undefined);
  assert.equal(state.counts().outbound, 1);
  bridge.close();
});

test("concurrent flush requests serialize Discord sends", async (t) => {
  const { state } = await fixture(t);
  const built = inboundEnvelope(event(), { id: "token", now: () => new Date(0) });
  state.admitInbound(built); state.markInboundAccepted("source-1");
  const sent = [];
  const bridge = createBridge({ state, postEnvelope: async () => {}, discord: { createMessage: async ({ content }) => { await Promise.resolve(); sent.push(content); return { id: content }; } } });
  t.after(() => bridge.close());
  for (const [messageId, body] of [["one", "one"], ["two", "two"]]) {
    assert.equal(bridge.acceptSupervisorMessage({ id: messageId, from: "supervisor", to: BRIDGE_AGENT_ID, subject: "discord.reply.v1/token", body }).accepted, true);
  }
  await Promise.all([bridge.flushOutbound(), bridge.flushOutbound()]);
  assert.deepEqual(sent, ["one", "two"]);
  bridge.close();
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
    token: "fake-token", allowlist: [], onMessage: async () => {}, webSocketFactory: () => socket,
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

test("pending capacity, retention, retry, and terminal cleanup are bounded", async (t) => {
  const { state, now } = await fixture(t);
  const built = inboundEnvelope(event(), { id: "token", now: () => new Date(0) });
  state.admitInbound(built); state.markInboundAccepted("source-1");
  for (let index = 0; index < 100; index += 1) assert.equal(state.admitOutbound({ messageId: `reply-${index}`, replyToken: "token", body: "body" }).accepted, true);
  assert.deepEqual(state.admitOutbound({ messageId: "overflow", replyToken: "token", body: "body" }), { accepted: false, reason: "outbound_full" });
  state.completeOutbound("reply-0", { state: "failed", statusCode: 400 });
  now(8 * 24 * 60 * 60 * 1_000);
  state.prune();
  assert.equal(state.routeFor("token"), undefined);
  assert.equal(state.counts().outbound, 99);
  assert.deepEqual(splitDiscordContent("😀".repeat(1_001)).map((chunk) => Buffer.byteLength(chunk) <= 2_000), [true, true, true]);
});
