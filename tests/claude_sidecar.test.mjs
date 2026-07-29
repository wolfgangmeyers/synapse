import assert from "node:assert/strict";
import { mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  claudeMailboxPrompt,
  createBodyFreeLogger,
  createClaudeSidecar,
  createTmuxInjector,
  rotateBodyFreeLog,
  sanitizeTerminalText,
} from "../.claude/skills/supervisor/tools/claude_sidecar.mjs";

const message = {
  id: "message-1",
  from: "supervisor",
  to: "claude-worker",
  subject: "Task",
  body: "do the thing",
};
const delivery = { messageId: "message-1", attemptNumber: 1 };

test("Claude sidecar sanitizes terminal controls while preserving useful message text", () => {
  assert.equal(sanitizeTerminalText("hello\x1b[31m red\x1b[0m\x07\r\nnext\tline"), "hello red\nnext\tline");
  assert.equal(
    claudeMailboxPrompt({ ...message, body: "body\x1b]0;title\x07\nmore" }),
    "Mailbox message from supervisor\nSubject: Task\n\nbody\nmore",
  );
});

test("Claude sidecar acks only after tmux bracketed paste submission succeeds", async () => {
  const receipts = [];
  const injected = [];
  const sidecar = createClaudeSidecar({
    agentId: "claude-worker",
    client: { sendReceipt: (type, seenDelivery, reason) => receipts.push({ type, seenDelivery, reason }) },
    inject: (payload) => injected.push(payload),
  });

  await sidecar.onEnvelope(message, delivery);

  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested", "ack"]);
  assert.equal(injected[0].window, "claude-worker");
  assert.equal(injected[0].text, "Mailbox message from supervisor\nSubject: Task\n\ndo the thing");
});

test("Claude sidecar leaves delivery unacked when tmux injection fails so the client can nack", async () => {
  const receipts = [];
  const sidecar = createClaudeSidecar({
    agentId: "claude-worker",
    client: { sendReceipt: (type, seenDelivery, reason) => receipts.push({ type, seenDelivery, reason }) },
    inject: () => { throw new Error("no pane"); },
  });

  await assert.rejects(() => sidecar.onEnvelope(message, delivery), /no pane/);
  assert.deepEqual(receipts.map(({ type }) => type), ["injection_requested"]);
});

test("tmux injector uses bracketed paste, waits for Claude paste handling, then sends Enter", () => {
  const calls = [];
  const inject = createTmuxInjector({
    tmux: (args) => calls.push(args),
    session: "mecha",
    enterDelayMs: 5_000,
    sleep: (ms) => calls.push(["sleep", ms]),
  });

  inject({ window: "claude-worker", text: "safe text" });

  assert.deepEqual(calls, [
    ["display-message", "-p", "-t", "mecha:claude-worker", "#{pane_id}"],
    ["send-keys", "-t", "mecha:claude-worker", "\x1b[200~"],
    ["send-keys", "-l", "-t", "mecha:claude-worker", "safe text"],
    ["send-keys", "-t", "mecha:claude-worker", "\x1b[201~"],
    ["sleep", 5_000],
    ["send-keys", "-t", "mecha:claude-worker", "Enter"],
  ]);
});

test("tmux injector failure errors do not include pasted mailbox text", () => {
  const inject = createTmuxInjector({
    tmux: (args) => {
      if (args.includes("secret text")) throw new Error(`failed command ${args.join(" ")}`);
    },
    session: "mecha",
    sleep: () => {},
  });

  assert.throws(() => inject({ window: "claude-worker", text: "secret text" }), (error) => {
    assert.equal(error.message, "tmux_injection_failed");
    assert.doesNotMatch(error.message, /secret/);
    return true;
  });
});

test("body-free sidecar logs rotate at configured bound and omit message bodies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "claude-sidecar-log-"));
  const logger = createBodyFreeLogger({
    agentId: "claude-worker",
    logDir: directory,
    maxBytes: 80,
    now: () => new Date("2026-07-28T00:00:00.000Z"),
  });

  logger.write("delivery_received", { message_id: "message-1", body: "secret body", prompt: "secret prompt" });
  const first = await readFile(logger.logPath, "utf8");
  assert.match(first, /message_id="message-1"/);
  assert.doesNotMatch(first, /secret/);
  await writeFile(logger.logPath, "x".repeat(81));
  rotateBodyFreeLog(logger.logPath, 80);
  assert.equal((await stat(`${logger.logPath}.1`)).size, 81);
});
