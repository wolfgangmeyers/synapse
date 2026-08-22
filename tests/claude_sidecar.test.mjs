import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, mkdtemp, readdir, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { assertAgentId, assertPresenceAgentId } from "../pi-extensions/mailbox-client.js";
import { isCliEntrypoint } from "../harness/cli_entrypoint.js";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import {
  createActivityReporter,
  createCoverageMonitor,
  createTmuxPaneMonitor,
  fetchConnectedAgentIds,
  managedAgentPresent,
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
    ["send-keys", "-t", "mecha:claude-worker", "\r"],
  ]);
});

test("Claude sidecar monitor exits when its target tmux window disappears", () => {
  // The fake returns what real tmux returns: a list, and exit 0 whether or not the target exists.
  // The previous version of this test had the fake THROW for a missing window, which tmux never
  // does. `display-message -p -t <dead window>` exits 0 and prints the active pane's id, so the
  // old check could not fail and sidecars outlived their windows until they were killed by hand.
  const intervals = [];
  const cleared = [];
  const missing = [];
  let windows = ["claude-worker", "supervisor"];
  const monitor = createTmuxWindowMonitor({
    window: "claude-worker",
    tmux: (args) => {
      assert.deepEqual(args, ["list-windows", "-t", "mecha", "-F", "#{window_name}"],
        "existence must be checked by enumerating, not by asking tmux about the target");
      return `${windows.join("\n")}\n`;
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
  assert.deepEqual(missing, [], "reported missing while the window was listed");

  windows = ["supervisor"]; // the window is gone, and tmux still exits 0
  intervals[0].callback();
  assert.deepEqual(missing, ["missing"], "did not notice a window that tmux no longer lists");
  assert.deepEqual(cleared, [intervals[0]]);
  monitor.close();
});

test("a tmux command that fails outright is not treated as a dead window", () => {
  // tmux being briefly unavailable is not evidence the window is gone. Exiting on it would kill a
  // healthy sidecar and stop mail delivery.
  const intervals = [];
  const missing = [];
  createTmuxWindowMonitor({
    window: "claude-worker",
    tmux: () => { throw new Error("tmux: no server running"); },
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; intervals.push(timer); return timer; },
    clearIntervalImpl: () => {},
    onMissing: () => missing.push("missing"),
  });
  intervals[0].callback();
  assert.deepEqual(missing, [], "a failing tmux call was treated as a dead window");
});

test("the pane monitor exits when its pane is no longer listed", () => {
  const intervals = [];
  const missing = [];
  let panes = ["%1", "%76"];
  createTmuxPaneMonitor({
    pane: "%76",
    tmux: (args) => {
      assert.deepEqual(args, ["list-panes", "-a", "-F", "#{pane_id}"]);
      return `${panes.join("\n")}\n`;
    },
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; intervals.push(timer); return timer; },
    clearIntervalImpl: () => {},
    onMissing: () => missing.push("missing"),
  });
  intervals[0].callback();
  assert.deepEqual(missing, []);
  panes = ["%1"];
  intervals[0].callback();
  assert.deepEqual(missing, ["missing"], "a presence sidecar would outlive its pane and keep a router lease");
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

// --- Coverage: a session already represented in the router must not register a second time -------
//
// Every Claude session self-registers a presence sidecar, so a managed worker appeared twice: once
// as its mailbox agent and once as its own session. Six of these existed at one time on 2026-08-10.
// The question is "is this window already in the router's list", and the router is what answers it.

// Fakes for `fetch` must behave the way `fetch` behaves, and the difference that matters here is
// that it does NOT reject on 404 or 500 — it resolves with `ok: false`. A fake that threw for a
// non-2xx would let the non-2xx test pass against code that never reads `response.ok`. So only the
// network-failure fake rejects. `fetchAgainstRealFetch` below pins that claim against real `fetch`.
function fakeFetch(body, { ok = true, status = 200 } = {}) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return { ok, status, json: async () => body };
  };
  return { fetchImpl, calls };
}

test("a window already listed as a connected agent counts as coverage", async () => {
  const { fetchImpl } = fakeFetch({ agents: [{ agent_id: "van-770-eval-gate" }, { agent_id: "supervisor" }] });
  assert.equal(await managedAgentPresent({ window: "van-770-eval-gate", fetchImpl }), true,
    "a managed worker's window was not recognised, so it would register a duplicate");
  assert.equal(await managedAgentPresent({ window: "pr-review", fetchImpl }), false,
    "a window nobody holds was treated as covered, so that session would never appear");
});

test("a presence id that embeds the window is not coverage for that window", async () => {
  // The router's list contains session.dup-probe.72121 while dup-probe itself is gone. A substring
  // or prefix test here would let two ad hoc sessions in identically named windows suppress each
  // other, which is the collision the dotted id space exists to keep apart.
  const { fetchImpl } = fakeFetch({ agents: [{ agent_id: "session.dup-probe.72121" }] });
  assert.equal(await managedAgentPresent({ window: "dup-probe", fetchImpl }), false,
    "a presence id counted as coverage, so a second ad hoc session in that window would not appear");
});

test("an unavailable answer is not coverage, so a hand-opened session still appears", async () => {
  // Unavailable is deliberately not the same as absent. A session that cannot appear is worse than
  // a duplicate, and a router that cannot answer this GET will not accept the registration either.
  const refused = async () => { const error = new Error("connect ECONNREFUSED"); error.code = "ECONNREFUSED"; throw error; };
  assert.equal(await managedAgentPresent({ window: "van-770-eval-gate", fetchImpl: refused }), false,
    "a dead router suppressed a session that has no other representation");

  // The body here is deliberately a well-formed agent list, so the assertion tests the status and
  // not the shape. Against a body the router would really send with an error status, a 404 answers
  // "unavailable" through the shape check whether or not the status is looked at, and the check
  // that the status is looked at would pass against code that never looks. No capture has produced
  // a non-2xx carrying a usable list, so this case is synthetic and says so.
  const { fetchImpl: errorWithBody } = fakeFetch({ agents: [{ agent_id: "van-770-eval-gate" }] }, { ok: false, status: 503 });
  assert.equal(await managedAgentPresent({ window: "van-770-eval-gate", fetchImpl: errorWithBody }), false,
    "a non-2xx response was read as an agent list");

  const unparseable = async () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError("Unexpected token"); } });
  assert.equal(await managedAgentPresent({ window: "van-770-eval-gate", fetchImpl: unparseable }), false,
    "an unparseable body was read as an agent list");

  const { fetchImpl: noAgents } = fakeFetch({ generated_at: "2026-08-10T18:15:22.465Z" });
  assert.equal(await fetchConnectedAgentIds({ fetchImpl: noAgents }), undefined,
    "a body with no agents array was reported as an empty list rather than as unavailable");
});

test("no window name asks the router nothing and is not coverage", async () => {
  // A session outside a named tmux window has id session.session.<pid>, which no managed agent can
  // equal, so there is nothing to ask.
  const { fetchImpl, calls } = fakeFetch({ agents: [{ agent_id: "supervisor" }] });
  assert.equal(await managedAgentPresent({ window: undefined, fetchImpl }), false);
  assert.equal(await managedAgentPresent({ window: "", fetchImpl }), false);
  assert.equal(calls.length, 0, "asked the router about a window that does not exist");
});

test("the coverage request is a GET to /v1/agents and survives a trailing slash", async () => {
  // MECHA_ROUTER_URL is allowed to end in a slash. Concatenating would request //v1/agents, which
  // the router does not route, so every answer would be a 404 read as "unavailable" and the whole
  // check would be silently off on that machine.
  const { fetchImpl, calls } = fakeFetch({ agents: [] });
  await managedAgentPresent({ window: "supervisor", routerUrl: "http://127.0.0.1:8765", fetchImpl });
  await managedAgentPresent({ window: "supervisor", routerUrl: "http://127.0.0.1:8765/", fetchImpl });
  assert.deepEqual(calls.map(({ url }) => url),
    ["http://127.0.0.1:8765/v1/agents", "http://127.0.0.1:8765/v1/agents"]);
  assert.equal(calls[0].options?.method ?? "GET", "GET");
});

test("the coverage query has working defaults for fetch and its timeout", async () => {
  // The timeout constant existing is not the same as it reaching AbortSignal.timeout. Called with
  // neither, AbortSignal.timeout(undefined) throws ERR_INVALID_ARG_TYPE, which this function's own
  // catch would turn into "unavailable" and the feature would never suppress anything, silently.
  // Nothing is listening on port 1, so this exercises the real global fetch and the real signal.
  assert.equal(
    await managedAgentPresent({ window: "supervisor", routerUrl: "http://127.0.0.1:1" }),
    false,
  );
  const { fetchImpl, calls } = fakeFetch({ agents: [{ agent_id: "supervisor" }] });
  assert.equal(await managedAgentPresent({ window: "supervisor", fetchImpl }), true);
  assert.ok(calls[0].options?.signal, "no abort signal was attached, so a wedged router hangs startup");
});

// Serves one canned /v1/agents response and records websocket upgrade attempts, which is how the
// CLI tests below tell "exited before registering" from "went on to register".
async function throwawayRouter(t, agentIds) {
  const upgrades = [];
  const server = createServer((request, response) => {
    if (request.url === "/v1/agents") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ generated_at: new Date().toISOString(), agents: agentIds.map((id) => ({ agent_id: id })) }));
      return;
    }
    response.writeHead(404);
    response.end();
  });
  server.on("upgrade", (request, socket) => { upgrades.push(request.url); socket.destroy(); });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  return { url: `http://127.0.0.1:${server.address().port}`, upgrades };
}

// Real `fetch` against a real 404, to pin the claim the fakes above are built on: fetch resolves
// with ok:false rather than rejecting. If that were untrue, the non-2xx fake would be testing a
// path the production code never takes.
test("real fetch resolves rather than rejects on a non-2xx, which is what the fakes copy", async (t) => {
  const router = await throwawayRouter(t, ["supervisor"]);
  let rejected = false;
  const response = await fetch(`${router.url}/nothing-here`).catch(() => { rejected = true; });
  assert.equal(rejected, false, "fetch rejected on a 404, so the non-2xx fake is wrong");
  assert.equal(response.ok, false);
  assert.equal(response.status, 404);
  // And the same function reaches the same answers against the real thing as against the fakes.
  assert.equal(await managedAgentPresent({ window: "supervisor", routerUrl: router.url }), true);
  assert.equal(await managedAgentPresent({ window: "van-756-rfc", routerUrl: router.url }), false);
});

// Spawns the real CLI on the presence path. TMUX_PANE is set because createTmuxPaneMonitor runs
// before the coverage check and throws on anything that is not %<digits>, and a synthetic pane id
// plus temp directories keep a run inside tmux away from a live pane's marker.
async function startPresenceSidecar(t, { routerUrl, window }) {
  const logDir = await mkdtemp(join(tmpdir(), "presence-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-activity-"));
  t.after(() => Promise.all([rm(logDir, { recursive: true, force: true }), rm(activityDir, { recursive: true, force: true })]));
  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", window,
    "--router-url", routerUrl, "--log-dir", logDir, "--activity-dir", activityDir,
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999999" } });
  const exited = new Promise((resolve) => child.on("exit", (code) => resolve(code)));
  return { child, exited, logDir, activityDir };
}

test("a presence session whose window a managed sidecar already holds exits without registering", async (t) => {
  const router = await throwawayRouter(t, ["dup-probe"]);
  const { child, exited, logDir } = await startPresenceSidecar(t, { routerUrl: router.url, window: "dup-probe" });

  const code = await exited;

  assert.equal(code, 0, "a covered session did not exit cleanly");
  assert.deepEqual(router.upgrades, [],
    "a covered session opened a websocket, so it registered a duplicate anyway");
  const suppressed = await readFile(join(logDir, "presence-suppressed.log"), "utf8");
  assert.match(suppressed, /event=presence_suppressed .*window="dup-probe"/,
    "a suppressed session left no record of why it did not appear");
  assert.match(suppressed, /signal="managed_sidecar_present"/);
  child.kill("SIGKILL");
});

test("a presence session with no managed sidecar for its window goes on to register", async (t) => {
  // The case the feature exists for. Only the upgrade attempt proves it got past the check; an exit
  // code cannot, because this process is meant to keep running.
  const router = await throwawayRouter(t, ["some-other-worker"]);
  const { child, exited } = await startPresenceSidecar(t, { routerUrl: router.url, window: "ad-hoc-window" });

  try {
    for (let attempt = 0; attempt < 100 && router.upgrades.length === 0; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.deepEqual(router.upgrades, ["/v1/agents"],
      "an uncovered session never tried to register, so a hand-opened session would not appear");
  } finally {
    child.kill("SIGKILL");
    await exited;
  }
});

test("the coverage monitor fires only when the window is actually taken over", async () => {
  const intervals = [];
  const cleared = [];
  const covered = [];
  let listed = ["some-other-worker"];
  const monitor = createCoverageMonitor({
    present: async () => listed.includes("dup-probe"),
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; intervals.push(timer); return timer; },
    clearIntervalImpl: (timer) => cleared.push(timer),
    onCovered: () => covered.push("covered"),
  });
  const tick = async () => { intervals[0].callback(); await new Promise((resolve) => setImmediate(resolve)); };

  assert.equal(intervals[0].delay, 10_000, "the default poll interval changed without the constant changing");
  await tick();
  assert.deepEqual(covered, [], "stood a session down while nothing else held its window");

  // A managed sidecar takes this window over.
  listed = ["some-other-worker", "dup-probe"];
  await tick();
  assert.deepEqual(covered, ["covered"], "a managed sidecar took the window over and the session stayed registered");
  assert.deepEqual(cleared, [intervals[0]], "the poll kept running after the session stood down");
  monitor.close();
});

test("an unavailable answer never stands a session down, and never throws out of the timer", async () => {
  // sendActivity-style: this callback runs inside a timer in a process that parks on a promise that
  // never resolves, so a rejection escaping here ends the process and takes mail delivery with it.
  const intervals = [];
  const covered = [];
  createCoverageMonitor({
    present: async () => { throw new Error("connect ECONNREFUSED"); },
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; intervals.push(timer); return timer; },
    clearIntervalImpl: () => {},
    onCovered: () => covered.push("covered"),
  });
  assert.doesNotThrow(() => intervals[0].callback());
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(covered, [], "an unreachable router was read as coverage");

  // And a present() that throws synchronously is the same story.
  const syncIntervals = [];
  createCoverageMonitor({
    present: () => { throw new TypeError("boom"); },
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; syncIntervals.push(timer); return timer; },
    clearIntervalImpl: () => {},
    onCovered: () => covered.push("covered"),
  });
  assert.doesNotThrow(() => syncIntervals[0].callback());
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(covered, []);
});

// The wiring, end to end against a real router, because the tailer rule and runCli passing it are
// two different things and the second is the one that has historically been missed. A presence
// session that registers still must not take the pane marker away from the managed sidecar sharing
// its pane: doing so leaves the pane with no marker, the hook then writes nothing, and that worker's
// indicator is dead for the rest of its session.
test("a registered presence session leaves a live managed sidecar's pane marker alone", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "presence-marker-router-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const websocketUrl = await router.listen();
  t.after(() => router.close?.());
  const routerUrl = websocketUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");

  const activityDir = await mkdtemp(join(tmpdir(), "presence-marker-activity-"));
  const logDir = await mkdtemp(join(tmpdir(), "presence-marker-log-"));
  t.after(() => Promise.all([rm(activityDir, { recursive: true, force: true }), rm(logDir, { recursive: true, force: true })]));
  // The managed sidecar for this pane got there first. process.pid is a real live pid.
  const marker = join(activityDir, "pane%999998.watch");
  await writeFile(marker, `managed-worker ${process.pid}\n`, "utf8");

  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", "ad-hoc-window",
    "--router-url", routerUrl, "--log-dir", logDir, "--activity-dir", activityDir,
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999998" } });
  const exited = new Promise((resolve) => child.on("exit", (code) => resolve(code)));

  try {
    let listed = [];
    for (let attempt = 0; attempt < 100; attempt += 1) {
      const response = await fetch(`${routerUrl}/v1/agents`);
      listed = (await response.json()).agents.map(({ agent_id: id }) => id);
      if (listed.some((id) => id.startsWith("session.ad-hoc-window."))) break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.ok(listed.some((id) => id.startsWith("session.ad-hoc-window.")),
      `an uncovered session never registered, so the case the feature exists for is broken: ${listed.join(",")}`);
    assert.equal((await readFile(marker, "utf8")).trim(), `managed-worker ${process.pid}`,
      "a presence session took over the pane marker of a live managed sidecar");
  } finally {
    child.kill("SIGTERM");
    await exited;
  }
  // And it did not remove it on the way out either.
  assert.equal((await readFile(marker, "utf8")).trim(), `managed-worker ${process.pid}`,
    "a presence session removed a pane marker it never wrote");
});

test("a suppression that cannot be written down still stands the session down", async (t) => {
  // Standing down must not depend on being able to say so. A directory where the shared log file
  // belongs makes the append fail the way a full or unwritable disk would, and the session must
  // still refuse to register rather than dying through the fatal handler.
  const router = await throwawayRouter(t, ["dup-probe"]);
  const logDir = await mkdtemp(join(tmpdir(), "presence-blocked-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-blocked-activity-"));
  t.after(() => Promise.all([rm(logDir, { recursive: true, force: true }), rm(activityDir, { recursive: true, force: true })]));
  await mkdir(join(logDir, "presence-suppressed.log"));

  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", "dup-probe",
    "--router-url", router.url, "--log-dir", logDir, "--activity-dir", activityDir,
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999997" } });

  const code = await new Promise((resolve) => child.on("exit", resolve));

  assert.equal(code, 0, "a failed log write took the suppression down with it");
  assert.deepEqual(router.upgrades, [], "a covered session registered because it could not log");
});

// A duplicate created in the startup window, before the managed sidecar has registered, is corrected
// rather than prevented, and this poll is the correction. Tested through the real CLI against a real
// router, because the monitor working in isolation and runCli actually building one are two
// different things. Note this is not what produced the six duplicates of 2026-08-10: the startup
// check alone would have caught all six. See ACTIVITY_DECISIONS.md.
test("a registered presence session stands down when a managed sidecar takes its window", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "presence-late-router-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const websocketUrl = await router.listen();
  t.after(() => router.close?.());
  const routerUrl = websocketUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");

  const logDir = await mkdtemp(join(tmpdir(), "presence-late-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-late-activity-"));
  t.after(() => Promise.all([rm(logDir, { recursive: true, force: true }), rm(activityDir, { recursive: true, force: true })]));

  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", "late-window",
    "--router-url", routerUrl, "--log-dir", logDir, "--activity-dir", activityDir,
    "--coverage-poll-ms", "100",
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999996" } });
  const exited = new Promise((resolve) => child.on("exit", (code) => resolve(code)));

  const agentIds = async () => (await (await fetch(`${routerUrl}/v1/agents`)).json()).agents.map(({ agent_id: id }) => id);
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if ((await agentIds()).some((id) => id.startsWith("session.late-window."))) break;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  assert.ok((await agentIds()).some((id) => id.startsWith("session.late-window.")),
    "the presence session never registered, so there is nothing to stand down");

  // The managed sidecar for that window arrives, which is what happened at 18:04 and 18:10.
  const managed = new WebSocket(websocketUrl);
  t.after(() => managed.close());
  await new Promise((resolve, reject) => { managed.once("open", resolve); managed.once("error", reject); });
  managed.send(JSON.stringify({ version: "v1", frame_id: "managed-1", type: "register", agent_id: "late-window", instance_id: "managed-instance" }));

  const code = await exited;

  assert.equal(code, 0, "the presence session did not stand down when a managed sidecar took its window");
  assert.deepEqual((await agentIds()).filter((id) => id.includes("late-window")), ["late-window"],
    "the window is still represented twice");
  const suppressed = await readFile(join(logDir, "presence-suppressed.log"), "utf8");
  assert.match(suppressed, /event=presence_suppressed .*window="late-window".*signal="managed_sidecar_appeared"/,
    "stood down without recording that a managed sidecar had appeared");
});

// The production launch is `node claude_sidecar.mjs --presence-only` with no --window
// (hooks/claude-activity.sh), so the window name comes from tmuxWindowName(). Every other CLI test
// here passes --window, which means the input the whole check depends on would otherwise be
// exercised by nothing. The shim behaves the way real tmux does: it exits 0 and prints a name.
test("the production argv resolves its window from tmux and uses it for the coverage check", async (t) => {
  const router = await throwawayRouter(t, ["shimmed-window"]);
  const binDir = await mkdtemp(join(tmpdir(), "presence-tmux-shim-"));
  const logDir = await mkdtemp(join(tmpdir(), "presence-shim-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-shim-activity-"));
  t.after(() => Promise.all([binDir, logDir, activityDir].map((d) => rm(d, { recursive: true, force: true }))));
  // Dispatches on the subcommand, because real tmux does. A shim that answers everything with a
  // window name would also be answering the pane monitor's list-panes, which really returns pane
  // ids, and a later test could then exit through a path it did not mean to take. Both branches
  // exit 0 and print, which is what real tmux does for a target that exists.
  await writeFile(join(binDir, "tmux"),
    '#!/bin/sh\ncase "$1" in\n  display-message) echo shimmed-window ;;\n  list-panes) echo "$TMUX_PANE" ;;\n  *) ;;\nesac\nexit 0\n',
    { mode: 0o755 });

  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only",
    "--router-url", router.url, "--log-dir", logDir, "--activity-dir", activityDir,
  ], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, TMUX_PANE: "%999995", PATH: `${binDir}:${process.env.PATH}` },
  });

  const code = await new Promise((resolve) => child.on("exit", resolve));

  assert.equal(code, 0, "a session whose tmux window a managed sidecar holds did not stand down");
  assert.deepEqual(router.upgrades, [], "it registered anyway, so the tmux-derived window never reached the check");
  const suppressed = await readFile(join(logDir, "presence-suppressed.log"), "utf8");
  assert.match(suppressed, /window="shimmed-window"/,
    "the window name did not come from tmux, so the production launch path checks nothing");
  assert.match(suppressed, /agent_id="session\.shimmed-window\.\d+"/);
});

// "Unavailable" and "nobody holds this window" both register, so without a record they are
// indistinguishable and a router with no /v1/agents route leaves the check silently off, with the
// duplicates it was meant to stop as the only evidence.
test("a router that cannot answer the coverage question is recorded once, durably", async (t) => {
  // "Unavailable" and "nobody holds this window" both register, so without a record they are
  // indistinguishable and a router with no /v1/agents route leaves the check silently off, with the
  // duplicates it was meant to stop as the only evidence. It goes to the shared log because a
  // presence sidecar deletes its own on the way out, which is when someone would come looking. Once
  // per process, because the monitor asks again on every poll for the life of the session.
  const server = createServer((request, response) => { response.writeHead(500); response.end(); });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const logDir = await mkdtemp(join(tmpdir(), "presence-unavail-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-unavail-activity-"));
  t.after(() => Promise.all([rm(logDir, { recursive: true, force: true }), rm(activityDir, { recursive: true, force: true })]));

  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", "unanswered-window",
    "--router-url", `http://127.0.0.1:${server.address().port}`,
    "--log-dir", logDir, "--activity-dir", activityDir, "--coverage-poll-ms", "50",
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999994" } });
  const exited = new Promise((resolve) => child.on("exit", (code) => resolve(code)));
  const sharedLog = join(logDir, "presence-suppressed.log");

  try {
    let log = "";
    for (let attempt = 0; attempt < 60 && !log.includes("presence_coverage_unavailable"); attempt += 1) {
      log = await readFile(sharedLog, "utf8").catch(() => "");
      if (!log.includes("presence_coverage_unavailable")) await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.match(log, /event=presence_coverage_unavailable .*window="unanswered-window"/,
      "a router that could not answer left no durable trace, so a silently disabled check looks like a healthy one");
    // Long enough for many more polls at 50 ms. A record per poll would fill and rotate the one log
    // that has to survive, taking the history with it.
    await new Promise((resolve) => setTimeout(resolve, 600));
    const after = await readFile(sharedLog, "utf8");
    assert.equal(after.split("presence_coverage_unavailable").length - 1, 1,
      "recorded once per poll rather than once per process");
    assert.doesNotMatch(after, /event=presence_suppressed/,
      "an unavailable answer suppressed the session, which makes it invisible instead of duplicated");
  } finally {
    child.kill("SIGKILL");
    await exited;
  }
});

test("an unusable coverage poll interval stops the sidecar rather than silently disabling the check", async (t) => {
  // Rejected while parsing arguments, so this never reaches a router. The unroutable port is there
  // so that a build which accepted the value would sit in its connect timeout instead of talking to
  // whatever router happens to be running on this machine.
  const logDir = await mkdtemp(join(tmpdir(), "presence-badpoll-log-"));
  const activityDir = await mkdtemp(join(tmpdir(), "presence-badpoll-activity-"));
  t.after(() => Promise.all([rm(logDir, { recursive: true, force: true }), rm(activityDir, { recursive: true, force: true })]));
  const child = spawn(process.execPath, [
    SIDECAR_PATH, "--presence-only", "--window", "w", "--log-dir", logDir,
    "--activity-dir", activityDir, "--router-url", "http://127.0.0.1:1", "--coverage-poll-ms", "0",
  ], { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMUX_PANE: "%999993" } });

  const exited = new Promise((resolve) => child.on("exit", resolve));
  let stillRunning;
  const code = await Promise.race([
    exited,
    new Promise((resolve) => { stillRunning = setTimeout(() => resolve("still running"), 3_000); }),
  ]);
  clearTimeout(stillRunning);
  child.kill("SIGKILL");

  assert.equal(code, 1,
    "a zero poll interval was accepted, so the monitor that stands a duplicate down would never fire");
});

test("the coverage monitor runs one poll at a time", async () => {
  // --coverage-poll-ms can be set below the 2 s fetch bound, so a slow router would otherwise let
  // polls overlap and stand the same session down twice.
  const intervals = [];
  const covered = [];
  let inFlight = 0;
  let peak = 0;
  let release;
  createCoverageMonitor({
    intervalMs: 10,
    present: () => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      return new Promise((resolve) => { release = () => { inFlight -= 1; resolve(true); }; });
    },
    setIntervalImpl: (callback, delay) => { const timer = { callback, delay }; intervals.push(timer); return timer; },
    clearIntervalImpl: () => {},
    onCovered: () => covered.push("covered"),
  });

  intervals[0].callback();
  intervals[0].callback();
  intervals[0].callback();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(peak, 1, "polls overlapped, so a slow router can stand one session down more than once");

  release();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(covered, ["covered"]);
});
