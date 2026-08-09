import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { assertAgentId, assertPresenceAgentId } from "../pi-extensions/mailbox-client.js";
import { isCliEntrypoint } from "../harness/cli_entrypoint.js";
import {
  createActivityReporter,
  presenceAgentId,
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

// Self-registered Claude sessions use dotted agent ids so they cannot collide with a managed
// worker's id, which is its tmux window name and can never contain a dot. assertAgentId is what
// keeps a dotted id from ever reaching a mail-injecting sidecar. If someone widens the pattern to
// allow dots as a harmless-looking relaxation, the two id spaces merge and an ad hoc session can
// register under a live worker's name. This test is the tripwire for that.
test("assertAgentId rejects the dotted ids used by self-registered sessions", () => {
  assert.throws(() => assertAgentId("session.claude-activity-indicators.4821"), /agent identity is invalid/);
  assert.throws(() => assertAgentId("session.supervisor.1"), /agent identity is invalid/);
  assert.throws(() => assertAgentId("a.b"), /agent identity is invalid/);
  // The protocol itself allows dots, so this is a deliberate narrowing and not a shared rule.
  assert.equal(assertAgentId("claude-activity-indicators"), "claude-activity-indicators");
  assert.equal(assertAgentId("supervisor"), "supervisor");
});

// The sidecar has two jobs and they are not equally important. runCli parks on a promise that
// never resolves, so an uncaught throw in the activity timer ends the process and takes mail
// delivery with it. sendActivity genuinely throws when the socket is between reconnects
// (pi-extensions/mailbox-client.js:157), so this is a reachable path and not defensive coding.
test("a failing activity send never stops mail delivery", async () => {
  const receipts = [];
  const injected = [];
  const logged = [];
  const client = {
    sendReceipt: (type) => receipts.push(type),
    sendActivity: () => { throw new Error("Mailbox v2 socket is not connected"); },
  };
  const state = {
    state: "active",
    subagentCount: 0,
    subagents: () => [],
    apply: () => {},
    tick: () => "active",
    exhausted: () => false,
  };
  const tailer = { tick: () => { throw new Error("directory vanished"); } };
  const reporter = createActivityReporter({
    client, tailer, state, log: (event) => logged.push(event),
  });

  // Neither path may throw out of the timer.
  assert.doesNotThrow(() => reporter.onRecord({ kind: "turn_start", at: 1 }));
  assert.doesNotThrow(() => reporter.tick());
  assert.ok(logged.includes("activity_send_failed"), "a failed send was not logged");
  assert.ok(logged.includes("activity_read_failed"), "a failed read was not logged");

  // And mail still flows through the same client.
  const sidecar = createClaudeSidecar({
    agentId: "claude-worker",
    client,
    inject: (payload) => injected.push(payload),
  });
  await sidecar.onEnvelope({ id: "m1", from: "supervisor", body: "still delivered" },
    { attemptNumber: 1 });
  assert.deepEqual(receipts, ["injection_requested", "ack"]);
  assert.equal(injected.length, 1);
});

test("an identical activity frame is not re-sent inside the resend window", () => {
  const sent = [];
  let clock = 0;
  const state = {
    state: "active", subagentCount: 0, subagents: () => [],
    apply: () => {}, tick: () => "active", exhausted: () => false,
  };
  const reporter = createActivityReporter({
    client: { sendActivity: (s, extra) => sent.push({ s, ...extra }) },
    tailer: { tick: () => {} },
    state,
    now: () => clock,
  });
  reporter.publish();
  reporter.publish();
  assert.equal(sent.length, 1, "an unchanged frame was re-sent immediately");
  clock += 11_000; // past the resend window, so the router's 15s expiry cannot lapse
  reporter.tick();
  assert.equal(sent.length, 2, "the frame was not refreshed before the router would expire it");
});

// The two id spaces are complements, not conventions. No string satisfies both validators, so a
// presence id can never reach a mail-injecting sidecar and a worker id can never be claimed by a
// self-registered session. Widening either pattern breaks that, and these are the tripwires.
test("presence ids and worker ids are mutually exclusive", () => {
  const workerIds = ["supervisor", "claude-activity-indicators", "van-770-eval-gate"];
  const presenceIds = ["session.supervisor.4821", "session.ad-hoc.1", "s.x.2"];

  for (const id of workerIds) {
    assert.equal(assertAgentId(id), id, `${id} should be a valid worker id`);
    assert.throws(() => assertPresenceAgentId(id), /presence identity/,
      `${id} was accepted as a presence id, so the spaces overlap`);
  }
  for (const id of presenceIds) {
    assert.equal(assertPresenceAgentId(id), id, `${id} should be a valid presence id`);
    assert.throws(() => assertAgentId(id), /agent identity is invalid/,
      `${id} was accepted as a worker id, so an ad hoc session could take a worker's mail`);
  }
});

// The collision the id space exists to prevent. Two Claude sessions opened in windows that happen
// to share a name must both register and must not become the same agent, and neither may land on
// a managed worker's id even when the window is named exactly after one.
test("two sessions in identically named windows register distinctly", () => {
  const first = presenceAgentId({ window: "supervisor", pid: 4821 });
  const second = presenceAgentId({ window: "supervisor", pid: 5109 });

  assert.notEqual(first, second, "two sessions in the same window name collapsed to one agent");
  assert.equal(first, "session.supervisor.4821");
  assert.equal(second, "session.supervisor.5109");

  // Neither can be a worker id, so neither can take a live worker's mail.
  for (const id of [first, second]) {
    assert.throws(() => assertAgentId(id), /agent identity is invalid/,
      `${id} was accepted as a worker id`);
    assert.equal(assertPresenceAgentId(id), id);
  }
});

test("a window named to look like a presence id cannot become one", () => {
  // A window called "session.supervisor.1" is not a valid tmux-derived worker id anyway, but the
  // sanitiser must not let a crafted window name forge a different session's id either.
  const forged = presenceAgentId({ window: "supervisor.4821", pid: 9 });
  assert.equal(forged, "session.supervisor-4821.9",
    "a dot in the window name survived into the id and could forge another session");
  assert.notEqual(forged, presenceAgentId({ window: "supervisor", pid: 4821 }));
});

test("a window with no usable name still produces a valid presence id", () => {
  assert.equal(presenceAgentId({ window: "!!!", pid: 7 }), "session.---.7");
  assert.equal(presenceAgentId({ window: "", pid: 7 }), "session.session.7");
  assert.equal(presenceAgentId({ window: undefined, pid: 7 }), "session.session.7");
});
