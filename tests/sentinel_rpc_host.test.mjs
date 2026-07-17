import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import test from "node:test";

import { runSentinelRpcHost } from "../pi-extensions/sentinel-child.js";

const nextTurn = () => new Promise((resolve) => setImmediate(resolve));

function fakeChild() {
  const child = new EventEmitter();
  child.stdin = {
    writes: [],
    closed: false,
    write(value) { this.writes.push(value); },
    end() { this.closed = true; },
  };
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.killCalls = [];
  child.kill = (signal) => child.killCalls.push(signal);
  return child;
}

function promptId(child) {
  return JSON.parse(child.stdin.writes[0]).id;
}

function frame(child, value) {
  child.stdout.emit("data", Buffer.from(`${JSON.stringify(value)}\n`));
}

function assistant(text, stopReason = "stop") {
  return { role: "assistant", content: [{ type: "text", text }], stopReason };
}

function host(child, options = {}) {
  const attempts = [];
  const results = [];
  const diagnostics = [];
  const runner = runSentinelRpcHost({
    child,
    prompt: "opaque private prompt",
    markResultSendAttempt: async () => attempts.push("attempt"),
    sendResult: async (body) => results.push(body),
    onDiagnostic: (diagnostic) => diagnostics.push(diagnostic),
    ...options,
  });
  return { ...runner, attempts, results, diagnostics };
}

test("isolated runner RPC host accepts concurrent response/event ordering and closes stdin only after one relay", async () => {
  const child = fakeChild();
  const steps = [];
  const runner = host(child, {
    markResultSendAttempt: async () => steps.push("marker"),
    sendResult: async (body) => { steps.push("post"); return runner.results.push(body); },
  });
  frame(child, { type: "turn_end", message: assistant("done") });
  frame(child, { type: "agent_settled" });
  await nextTurn();
  assert.deepEqual(runner.results, []);
  frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
  assert.deepEqual(await runner.completion, { state: "succeeded", code: "result_sent" });
  assert.deepEqual(steps, ["marker", "post"]);
  assert.deepEqual(runner.results, ["done"]);
  assert.equal(child.stdin.closed, true);
});

test("RPC host uses the final assistant result across multiple turns", async () => {
  const child = fakeChild();
  const runner = host(child);
  frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
  frame(child, { type: "turn_end", message: assistant("intermediate", "toolUse") });
  frame(child, { type: "turn_end", message: assistant("final") });
  frame(child, { type: "agent_end", messages: [assistant("intermediate", "toolUse"), assistant("final")] });
  frame(child, { type: "agent_settled" });
  await runner.completion;
  assert.deepEqual(runner.results, ["final"]);
});

test("expected stdout EOF after finalized relay is normal", async () => {
  const child = fakeChild();
  const runner = host(child);
  frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
  frame(child, { type: "turn_end", message: assistant("done") });
  frame(child, { type: "agent_settled" });
  await runner.completion;
  child.stdout.emit("end");
  assert.equal(runner.diagnostics.length, 1);
  assert.equal(runner.diagnostics[0].state, "succeeded");
  assert.deepEqual(child.killCalls, []);
});

test("stdout EOF, no result, and duplicate terminal events fail closed or relay once", async (t) => {
  await t.test("EOF before finalization", async () => {
    const child = fakeChild();
    const runner = host(child);
    child.stdout.emit("end");
    assert.deepEqual(await runner.completion, { state: "failed", code: "stdout_eof" });
    assert.deepEqual(runner.results, []);
    assert.deepEqual(child.killCalls, ["SIGTERM"]);
  });
  await t.test("settled with no assistant result", async () => {
    const child = fakeChild();
    const runner = host(child);
    frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
    frame(child, { type: "agent_settled" });
    assert.deepEqual(await runner.completion, { state: "failed", code: "no_terminal_result" });
    assert.deepEqual(runner.results, []);
  });
  await t.test("duplicate terminal events", async () => {
    const child = fakeChild();
    const runner = host(child);
    frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
    frame(child, { type: "turn_end", message: assistant("first") });
    frame(child, { type: "turn_end", message: assistant("second") });
    frame(child, { type: "agent_settled" });
    await runner.completion;
    assert.deepEqual(runner.attempts, ["attempt"]);
    assert.deepEqual(runner.results, ["second"]);
  });
});

test("failed and unknown result POSTs terminalize without a replay", async (t) => {
  await t.test("known rejection", async () => {
    const child = fakeChild();
    const runner = host(child, { sendResult: async () => { const error = new Error("rejected"); error.code = "result_send_rejected"; throw error; } });
    frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
    frame(child, { type: "turn_end", message: assistant("done") });
    frame(child, { type: "agent_settled" });
    assert.deepEqual(await runner.completion, { state: "failed", code: "result_send_rejected" });
    assert.deepEqual(runner.attempts, ["attempt"]);
  });
  await t.test("unknown outcome", async () => {
    const child = fakeChild();
    const runner = host(child, { sendResult: async () => { throw new Error("connection lost"); } });
    frame(child, { type: "response", id: promptId(child), command: "prompt", success: true });
    frame(child, { type: "turn_end", message: assistant("done") });
    frame(child, { type: "agent_settled" });
    assert.deepEqual(await runner.completion, { state: "failed", code: "result_send_unknown" });
    assert.deepEqual(runner.attempts, ["attempt"]);
  });
});

test("timeout and malformed/private output record body-free diagnostics", async () => {
  const child = fakeChild();
  let timeout;
  const runner = host(child, { setTimeoutImpl: (callback) => { timeout = callback; return "timer"; }, clearTimeoutImpl() {} });
  timeout();
  assert.deepEqual(await runner.completion, { state: "failed", code: "timeout" });
  assert.equal(JSON.stringify(runner.diagnostics).includes("opaque private prompt"), false);

  const malformedChild = fakeChild();
  const malformed = host(malformedChild);
  malformedChild.stdout.emit("data", Buffer.from("not json\n"));
  assert.deepEqual(await malformed.completion, { state: "failed", code: "malformed_frame" });
  assert.equal(JSON.stringify(malformed.diagnostics).includes("not json"), false);
  malformedChild.stderr.emit("data", Buffer.from("private stderr text"));
  assert.equal(JSON.stringify(malformed.diagnostics).includes("private stderr text"), false);
});
