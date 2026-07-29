import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { isCliEntrypoint } from "../harness/cli_entrypoint.js";
import {
  claudeMailboxPrompt,
  createBodyFreeLogger,
  createClaudeSidecar,
  createTmuxInjector,
  createTmuxWindowMonitor,
  rotateBodyFreeLog,
  sanitizeTerminalText,
} from "../.claude/skills/supervisor/tools/claude_sidecar.mjs";

const SIDECAR_PATH = fileURLToPath(
  new URL("../.claude/skills/supervisor/tools/claude_sidecar.mjs", import.meta.url),
);
const TOOLS_DIR = dirname(SIDECAR_PATH);

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

test("Claude sidecar monitor exits when its target tmux window disappears", () => {
  const intervals = [];
  const cleared = [];
  let windowExists = true;
  const missing = [];
  const monitor = createTmuxWindowMonitor({
    window: "claude-worker",
    tmux: () => {
      if (!windowExists) throw new Error("no such window");
    },
    setIntervalImpl: (callback, delay) => {
      const timer = { callback, delay };
      intervals.push(timer);
      return timer;
    },
    clearIntervalImpl: (timer) => cleared.push(timer),
    onMissing: () => missing.push("missing"),
  });

  assert.equal(intervals[0].delay, 5_000);
  intervals[0].callback();
  assert.deepEqual(missing, []);
  windowExists = false;
  intervals[0].callback();
  assert.deepEqual(missing, ["missing"]);
  assert.deepEqual(cleared, [intervals[0]]);
  monitor.close();
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

// Starts the real CLI and reports whether it got as far as its first log write. The
// failure being pinned is a silent exit 0, so "started" must be observed from a side
// effect, not from the absence of a crash.
async function startSidecarThroughDirectory(t, directory, agentId) {
  const logDir = await mkdtemp(join(tmpdir(), "claude-sidecar-cli-log-"));
  t.after(() => rm(logDir, { recursive: true, force: true }));
  const child = spawn(
    process.execPath,
    [
      join(directory, "claude_sidecar.mjs"),
      "--agent-id", agentId,
      "--window", agentId,
      // Unroutable loopback port: connect() retries forever without a router, and runCli
      // passes no kickstart, so the client's no-op default runs and nothing is spawned.
      "--router-url", "http://127.0.0.1:1",
      "--log-dir", logDir,
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  const exited = new Promise((resolveExit) => child.on("exit", (code) => resolveExit(code)));
  const logPath = join(logDir, `${agentId}.log`);
  try {
    for (let attempt = 0; attempt < 100; attempt += 1) {
      let log;
      try {
        log = await readFile(logPath, "utf8");
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
      }
      if (log?.includes("event=sidecar_start")) return { started: true, log, exitCode: null };
      if (child.exitCode !== null) return { started: false, log: log ?? "", exitCode: child.exitCode };
      await new Promise((resolveSleep) => setTimeout(resolveSleep, 50));
    }
    return { started: false, log: "", exitCode: child.exitCode, timedOut: true };
  } finally {
    child.kill("SIGKILL");
    await exited;
  }
}

// The worktree this runs from has no symlink component, so a repo-relative invocation
// would start fine either way and prove nothing. The test builds the symlink itself.
test("sidecar CLI starts when invoked through a symlinked tools directory", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "claude-sidecar-symlink-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const symlinkedTools = join(parent, "tools");
  await symlink(TOOLS_DIR, symlinkedTools, "dir");

  const result = await startSidecarThroughDirectory(t, symlinkedTools, "claude-symlink-worker");

  assert.equal(
    result.started,
    true,
    `sidecar did not start through the symlinked path (exit=${result.exitCode}, timedOut=${result.timedOut ?? false})`,
  );
  assert.match(result.log, /event=sidecar_start agent_id="claude-symlink-worker"/);
});

test("sidecar CLI starts when invoked through its real tools directory", async (t) => {
  const result = await startSidecarThroughDirectory(t, TOOLS_DIR, "claude-realpath-worker");

  assert.equal(result.started, true, `sidecar did not start through the real path (exit=${result.exitCode})`);
  assert.match(result.log, /event=sidecar_start agent_id="claude-realpath-worker"/);
});

test("entrypoint detection accepts every spelling of a module's own path and nothing else", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "claude-sidecar-entrypoint-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const symlinkedFile = join(parent, "sidecar-alias.mjs");
  await symlink(SIDECAR_PATH, symlinkedFile, "file");
  const sidecarUrl = pathToFileURL(SIDECAR_PATH).href;

  assert.equal(isCliEntrypoint(sidecarUrl, SIDECAR_PATH), true);
  assert.equal(isCliEntrypoint(sidecarUrl, symlinkedFile), true);
  assert.equal(isCliEntrypoint(sidecarUrl, join(TOOLS_DIR, ".", "claude_sidecar.mjs")), true);
  assert.equal(isCliEntrypoint(sidecarUrl, relative(process.cwd(), SIDECAR_PATH)), true);

  assert.equal(isCliEntrypoint(sidecarUrl, join(TOOLS_DIR, "worker_capture.py")), false);
  // Exercises the realpathSync-throws fallback: an unstattable path resolves but never matches.
  assert.equal(isCliEntrypoint(sidecarUrl, join(TOOLS_DIR, "does-not-exist.mjs")), false);
  assert.equal(isCliEntrypoint(sidecarUrl, "./also-does-not-exist.mjs"), false);
  // A module with no entrypoint argv at all must not start its CLI.
  assert.equal(isCliEntrypoint(sidecarUrl, null), false);
  assert.equal(isCliEntrypoint(sidecarUrl, ""), false);
  // A shared helper cannot guess the caller's module, so it must refuse to.
  assert.throws(() => isCliEntrypoint(undefined, SIDECAR_PATH), /moduleUrl is required/);
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
