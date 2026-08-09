import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { PassThrough, Writable } from "node:stream";
import test from "node:test";

import {
  colorBucketForRemaining,
  createCodexUsageHud,
  isCodexSubscriptionModel,
  normalizeRateLimitsPayload,
  readCodexRateLimits,
  renderUsageStatus,
} from "../pi-extensions/codex-usage-hud.js";

function createTheme() {
  return {
    fg(color, text) {
      return `<${color}>${text}</${color}>`;
    },
  };
}

function createContext({ provider = "openai-codex" } = {}) {
  const calls = [];
  return {
    model: provider ? { provider, id: "gpt-5.5" } : undefined,
    ui: {
      theme: createTheme(),
      setStatus(key, value) {
        calls.push({ key, value });
      },
    },
    calls,
  };
}

function createPiHarness() {
  const handlers = new Map();
  return {
    on(event, handler) {
      handlers.set(event, handler);
    },
    async emit(event, ctx) {
      await handlers.get(event)?.({}, ctx);
    },
  };
}

test("normalizes Codex rate-limit responses into remaining percent", () => {
  const snapshot = normalizeRateLimitsPayload({
    result: {
      rateLimits: {
        primary: { usedPercent: 12.5, resetsAt: 123, windowDurationMins: 300 },
        rateLimitReachedType: null,
      },
    },
  });

  assert.equal(snapshot.remainingPercent, 87.5);
  assert.equal(snapshot.usedPercent, 12.5);
  assert.equal(snapshot.resetsAt, 123);
  assert.equal(snapshot.windowDurationMins, 300);
});

test("bad or unavailable rate-limit payloads hide by returning undefined", () => {
  assert.equal(normalizeRateLimitsPayload(undefined), undefined);
  assert.equal(normalizeRateLimitsPayload({ result: { rateLimits: null } }), undefined);
  assert.equal(normalizeRateLimitsPayload({ result: { rateLimits: { primary: { usedPercent: null } } } }), undefined);
  assert.equal(normalizeRateLimitsPayload({ result: { rateLimits: { primary: { usedPercent: "30" } } } }), undefined);
  assert.equal(normalizeRateLimitsPayload({ result: { rateLimits: { primary: { usedPercent: "nope" } } } }), undefined);
  assert.equal(renderUsageStatus(undefined, createTheme()), undefined);
});

test("only OpenAI Codex subscription models are eligible for display", () => {
  assert.equal(isCodexSubscriptionModel({ provider: "openai-codex", id: "gpt-5.5" }), true);
  assert.equal(isCodexSubscriptionModel({ provider: "openai", id: "gpt-5.5" }), false);
  assert.equal(isCodexSubscriptionModel(undefined), false);
});

test("renders valid usage with compact label", () => {
  assert.match(renderUsageStatus({ remainingPercent: 87.2 }, createTheme()), /Codex 87%/);
});

test("maps remaining percentage to requested color thresholds", () => {
  assert.equal(colorBucketForRemaining(95), "blue");
  assert.equal(colorBucketForRemaining(94), "green");
  assert.equal(colorBucketForRemaining(75), "green");
  assert.equal(colorBucketForRemaining(74), "yellow");
  assert.equal(colorBucketForRemaining(50), "yellow");
  assert.equal(colorBucketForRemaining(49), "orange");
  assert.equal(colorBucketForRemaining(25), "orange");
  assert.equal(colorBucketForRemaining(24), "red");
});

test("HUD hides gracefully when source is unavailable or user is not logged in", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  createCodexUsageHud(pi, {
    readRateLimits: async () => undefined,
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });
});

test("HUD hides and does not call source for non-Codex models", async () => {
  const pi = createPiHarness();
  const ctx = createContext({ provider: "anthropic" });
  let sourceCalls = 0;
  createCodexUsageHud(pi, {
    readRateLimits: async () => { sourceCalls++; return { remainingPercent: 80 }; },
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(sourceCalls, 0);
  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });
});

test("HUD displays valid live usage for Codex sessions", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  createCodexUsageHud(pi, {
    readRateLimits: async () => ({ remainingPercent: 42 }),
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(ctx.calls.at(-1).key, "codex-usage");
  assert.match(ctx.calls.at(-1).value, /Codex 42%/);
});

test("HUD does not restore an old Codex badge after switching away while a poll is in flight", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  let resolvePoll;
  const poll = new Promise((resolve) => { resolvePoll = resolve; });
  createCodexUsageHud(pi, {
    readRateLimits: () => poll,
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  ctx.model = { provider: "anthropic", id: "claude" };
  await pi.emit("model_select", ctx);
  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });

  resolvePoll({ remainingPercent: 91 });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });
});

test("HUD refreshes promptly when switching back to Codex while an old poll is in flight", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  let resolveFirstPoll;
  let sourceCalls = 0;
  const firstPoll = new Promise((resolve) => { resolveFirstPoll = resolve; });
  createCodexUsageHud(pi, {
    readRateLimits: () => {
      sourceCalls++;
      return sourceCalls === 1 ? firstPoll : Promise.resolve({ remainingPercent: 83 });
    },
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  ctx.model = { provider: "anthropic", id: "claude" };
  await pi.emit("model_select", ctx);
  ctx.model = { provider: "openai-codex", id: "gpt-5.5" };
  await pi.emit("model_select", ctx);

  resolveFirstPoll({ remainingPercent: 91 });
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(sourceCalls, 2);
  assert.match(ctx.calls.at(-1).value, /Codex 83%/);
});

test("HUD clears a previously valid badge when a later payload has bad shape", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  let next = { remainingPercent: 90 };
  createCodexUsageHud(pi, {
    readRateLimits: async () => next,
    setIntervalImpl: () => 1,
    clearIntervalImpl: () => {},
  });

  await pi.emit("session_start", ctx);
  await new Promise((resolve) => setImmediate(resolve));
  assert.match(ctx.calls.at(-1).value, /Codex 90%/);

  next = undefined;
  await pi.emit("model_select", ctx);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });
});

test("shutdown clears status and cancels polling timer", async () => {
  const pi = createPiHarness();
  const ctx = createContext();
  const cleared = [];
  createCodexUsageHud(pi, {
    readRateLimits: async () => ({ remainingPercent: 80 }),
    setIntervalImpl: () => 99,
    clearIntervalImpl: (timer) => cleared.push(timer),
  });

  await pi.emit("session_start", ctx);
  await pi.emit("session_shutdown", ctx);

  assert.deepEqual(cleared, [99]);
  assert.deepEqual(ctx.calls.at(-1), { key: "codex-usage", value: undefined });
});

test("readCodexRateLimits returns undefined when local Codex app-server is unavailable", async () => {
  const snapshot = await readCodexRateLimits({
    codexCli: "/definitely/missing/codex",
    requestTimeoutMs: 100,
  });
  assert.equal(snapshot, undefined);
});

test("readCodexRateLimits reads account/rateLimits/read from Codex app-server proxy", async () => {
  const spawnCalls = [];
  const spawnImpl = (command, args) => {
    spawnCalls.push({ command, args });
    const child = new EventEmitter();
    child.stdout = new PassThrough();
    child.stderr = new PassThrough();
    child.stdin = new Writable({ write(_chunk, _encoding, callback) { callback(); } });
    child.kill = () => {};
    queueMicrotask(() => {
      child.stdout.end(JSON.stringify({
        id: 2,
        result: { rateLimits: { primary: { usedPercent: 30 } } },
      }) + "\n");
      child.emit("close", 0);
    });
    return child;
  };

  const snapshot = await readCodexRateLimits({
    codexCli: "/usr/local/bin/codex",
    socketPath: "/tmp/codex.sock",
    spawnImpl,
  });

  assert.equal(snapshot.remainingPercent, 70);
  assert.deepEqual(spawnCalls, [{
    command: "/usr/local/bin/codex",
    args: ["app-server", "proxy", "--sock", "/tmp/codex.sock"],
  }]);
});
