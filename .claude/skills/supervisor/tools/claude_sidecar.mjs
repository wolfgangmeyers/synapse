#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { mkdirSync, appendFileSync, rmSync } from "node:fs";
import { join } from "node:path";

import { isCliEntrypoint } from "../../../../harness/cli_entrypoint.js";
import { rotateSizeCappedLog } from "../../../../harness/log_rotation.js";
import { createMailboxClient, assertAgentId, assertPresenceAgentId, routerHttpUrl } from "../../../../pi-extensions/mailbox-client.js";
import { createActivityTailer } from "./activity_tail.mjs";
import { createActivityState } from "./activity_state.mjs";
import { createTmuxRestClient } from "../../../../harness/tmux-rest-client.js";

export const DEFAULT_LOG_DIR = "~/.mecha-developer/claude-sidecars";
export const DEFAULT_MAX_LOG_BYTES = 256 * 1024;
export const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
// The router expires activity after 15s (activityTtlMs, harness/router.js:101), so the current
// state has to be re-sent well inside that. 5s matches the Pi extension's cadence.
export const ACTIVITY_REFRESH_MS = 5_000;
export const ACTIVITY_MIN_RESEND_MS = 10_000;
export const ACTIVITY_POLL_MS = 1_000;
// A self-registered session that cannot claim its id gives up rather than retrying forever. The
// router answers a losing lease claim with silence, not an error, so a client that retries on
// timeout looks alive and does nothing. A session that fails to register is still a working
// session; it just does not appear in the UI.
// `client.connect()` resolves only on a `registered` frame and rejects only when the client is
// closed, so with the router down it never settles. Retrying it is meaningless: the promise is
// memoized, so a second await returns the same pending one. A bounded wait is the only thing that
// actually gives up. Without this, every SessionStart in a pane spawned another process that hung
// forever holding nothing, which is how nine of them accumulated and had to be killed by hand.
export const PRESENCE_CONNECT_TIMEOUT_MS = 15_000;
// How often a registered presence session re-asks whether a managed sidecar has taken over its
// window. Nothing in the router retires a duplicate on its own: connectedAgentsSnapshot
// (harness/router.js:1140) lists every session with an open socket and a live lease, filtering at
// :1145, so a duplicate stays visible for as long as it is connected and this is its only bound.
export const PRESENCE_COVERAGE_POLL_MS = 10_000;
// Bounds the GET so a wedged router cannot stall the sidecar's own startup.
export const PRESENCE_COVERAGE_TIMEOUT_MS = 2_000;
// One shared, size-capped file rather than a log per suppressed session. A presence log is removed
// when its process exits, so without this a suppressed session would leave nothing at all behind:
// no router entry, no process, no marker, no log, and no way to answer "why did my session not
// appear". It is the only log here with more than one writer process. Appends are O_APPEND so the
// records stay intact; rotateSizeCappedLog is check-then-rename, so two processes crossing the cap
// together can drop a generation, which at one record per suppressed session against 256 KiB is a
// thing that has to be said rather than a thing that will happen.
export const PRESENCE_SUPPRESSED_LOG_ID = "presence-suppressed";

// Presence ids are dotted so they cannot collide with a managed worker id, which is a tmux window
// name and can never contain a dot. Two sessions in identically named windows differ by pid.
// Best-effort, for labelling only. A presence id stays valid even when this returns nothing,
// because the pid is what makes it unique.
export function tmuxWindowName(pane = process.env.TMUX_PANE) {
  if (typeof pane !== "string" || !/^%\d+$/.test(pane)) return undefined;
  try {
    return execFileSync("tmux", ["display-message", "-p", "-t", pane, "#{window_name}"],
      { encoding: "utf8" }).trim() || undefined;
  } catch {
    return undefined;
  }
}

// Whether a session is already represented is a question about the router's agent list, so the
// router is what answers it. The alternative, scanning for a managed sidecar process locally, closes
// the startup race better but answers a different question: a sidecar that spawned and never
// registered, or gave up registering, would suppress the presence session and leave it invisible,
// which is the failure this feature exists to prevent.
//
// Returns a Set of connected agent ids, or undefined when the answer is unavailable. Unavailable is
// deliberately not an empty set: the caller registers on it.
export async function fetchConnectedAgentIds({
  routerUrl = DEFAULT_ROUTER_URL,
  fetchImpl = fetch,
  timeoutMs = PRESENCE_COVERAGE_TIMEOUT_MS,
} = {}) {
  try {
    // routerHttpUrl validates a loopback base and strips a trailing slash. Concatenating instead
    // would turn a MECHA_ROUTER_URL ending in "/" into //v1/agents, which the router does not
    // route, so every answer would be a 404 read as unavailable and the check silently off.
    const response = await fetchImpl(`${routerHttpUrl(routerUrl)}/v1/agents`, {
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response?.ok) return undefined;
    const body = await response.json();
    if (!Array.isArray(body?.agents)) return undefined;
    return new Set(body.agents.map((agent) => agent?.agent_id).filter((id) => typeof id === "string"));
  } catch {
    return undefined;
  }
}

// Exact equality only. The router's list holds presence ids too, and a prefix or substring test
// would let two ad hoc sessions in identically named windows suppress each other, which is the
// collision the dotted id space exists to keep apart.
export async function managedAgentPresent({ window, onUnavailable = () => {}, ...options } = {}) {
  if (typeof window !== "string" || window.length === 0) return false;
  const connected = await fetchConnectedAgentIds(options);
  if (connected === undefined) {
    // Unavailable is not absent, and the caller has to be able to tell them apart: both register,
    // so without this an unreachable router looks exactly like a free window and the check is off
    // with the duplicates it was meant to stop as the only evidence.
    onUnavailable();
    return false;
  }
  return connected.has(window);
}

export function presenceAgentId({ window, pid }) {
  const safeWindow = String(window ?? "session").replace(/[^A-Za-z0-9_-]/g, "-").slice(0, 64)
    || "session";
  return assertPresenceAgentId(`session.${safeWindow}.${pid}`);
}

const ANSI_PATTERN = /(?:\x1B\[[0-?]*[ -/]*[@-~]|\x1B\][^\x07\x1B]*(?:\x07|\x1B\\)|\x1B[P^_][\s\S]*?(?:\x1B\\)|\x1B[@-Z\\-_])/g;
const CONTROL_PATTERN = /[\x00-\x08\x0B\x0C\x0E-\x1F\x7F\x80-\x9F]/g;
const BODY_FREE_LOG_FIELDS = new Set(["agent_id", "attempt", "from", "message_id", "signal", "state", "to", "window"]);

function expandHome(path, home = process.env.HOME) {
  if (path === "~") return home;
  if (path?.startsWith("~/")) return join(home, path.slice(2));
  return path;
}

export function sanitizeTerminalText(text) {
  if (typeof text !== "string") throw new TypeError("terminal text must be a string");
  return text.replace(ANSI_PATTERN, "").replace(CONTROL_PATTERN, "").replace(/\r\n?/g, "\n");
}

export function claudeMailboxPrompt(message) {
  const subject = message.subject === undefined ? "" : `\nSubject: ${message.subject}`;
  return sanitizeTerminalText(`Mailbox message from ${message.from}${subject}\n\n${message.body}`);
}

export function rotateBodyFreeLog(logPath, maxBytes = DEFAULT_MAX_LOG_BYTES) {
  rotateSizeCappedLog(logPath, maxBytes);
}

export function createBodyFreeLogger({ agentId, logDir = DEFAULT_LOG_DIR, maxBytes = DEFAULT_MAX_LOG_BYTES, now = () => new Date(), assertIdentity = assertAgentId } = {}) {
  // Validated, not merely sanitized: the id becomes a filename, so it must not be able to escape
  // the log directory. Both validators forbid a slash.
  assertIdentity(agentId);
  const resolvedLogDir = expandHome(logDir);
  mkdirSync(resolvedLogDir, { recursive: true });
  const logPath = join(resolvedLogDir, `${agentId}.log`);
  return {
    logPath,
    write(event, fields = {}) {
      rotateBodyFreeLog(logPath, maxBytes);
      const safeFields = Object.entries(fields)
        .filter(([key, value]) => BODY_FREE_LOG_FIELDS.has(key) && value !== undefined)
        .map(([key, value]) => `${key}=${JSON.stringify(String(value))}`)
        .join(" ");
      appendFileSync(logPath, `${now().toISOString()} event=${event}${safeFields ? ` ${safeFields}` : ""}\n`, "utf8");
    },
  };
}

function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// Existence is checked by enumerating and looking for the name, NOT by asking tmux about the
// target. `display-message -p -t <gone>` exits 0: for a dead pane it prints nothing, and for a
// dead window it prints the ACTIVE pane's id, so the old check could not fail and both monitors
// never fired. That is why sidecars outlived their windows and had to be killed by hand.
function tmuxLists(tmux, args) {
  const out = tmux(args);
  return String(out ?? "").split("\n").map((line) => line.trim()).filter(Boolean);
}

export function tmuxWindowExists({ tmux, session, window }) {
  return tmuxLists(tmux, ["list-windows", "-t", session, "-F", "#{window_name}"]).includes(window);
}

export function tmuxPaneExists({ tmux, pane }) {
  return tmuxLists(tmux, ["list-panes", "-a", "-F", "#{pane_id}"]).includes(pane);
}

function assertTmuxWindowExists({ tmux, session, window }) {
  tmux(["display-message", "-p", "-t", `${session}:${window}`, "#{pane_id}"]);
}

export function createTmuxInjector({ tmux, tmuxRest = createTmuxRestClient(), session = "mecha", enterDelayMs = 5_000, sleep = sleepSync } = {}) {
  if (!Number.isSafeInteger(enterDelayMs) || enterDelayMs < 0) throw new TypeError("enterDelayMs must be a non-negative safe integer");
  if (typeof sleep !== "function") throw new TypeError("sleep must be a function");
  if (typeof tmux === "function") return function inject({ window, text }) {
    assertAgentId(window);
    try {
      assertTmuxWindowExists({ tmux, session, window });
      tmux(["send-keys", "-t", `${session}:${window}`, "\x1b[200~"]);
      tmux(["send-keys", "-l", "-t", `${session}:${window}`, text]);
      tmux(["send-keys", "-t", `${session}:${window}`, "\x1b[201~"]);
      sleep(enterDelayMs);
      tmux(["send-keys", "-t", `${session}:${window}`, "\r"]);
    } catch { throw new Error("tmux_injection_failed"); }
  };
  return async function inject({ window, text }) {
    assertAgentId(window);
    try {
      const result = await tmuxRest.list();
      if (!Array.isArray(result?.windows) || !result.windows.some((item) => item?.name === window)) throw new Error("tmux_unavailable");
      await tmuxRest.keys(window, "\x1b[200~");
      await tmuxRest.keys(window, text);
      await tmuxRest.keys(window, "\x1b[201~");
      await new Promise((resolve) => setTimeout(resolve, enterDelayMs));
      // REST keys are literal terminal input; CR preserves Enter/submit semantics.
      await tmuxRest.keys(window, "\r");
    } catch { throw new Error("tmux_injection_failed"); }
  };
}

export function createTmuxWindowMonitor({
  window,
  session = "mecha",
  intervalMs = 5_000,
  tmux = (args) => execFileSync("tmux", args, { encoding: "utf8" }),
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  onMissing,
} = {}) {
  assertAgentId(window);
  if (!Number.isSafeInteger(intervalMs) || intervalMs <= 0) throw new TypeError("intervalMs must be a positive safe integer");
  if (typeof setIntervalImpl !== "function" || typeof clearIntervalImpl !== "function") throw new TypeError("timer functions are required");
  if (typeof onMissing !== "function") throw new TypeError("onMissing must be a function");
  const timer = setIntervalImpl(() => {
    let present;
    try {
      present = tmuxWindowExists({ tmux, session, window });
    } catch {
      return; // tmux itself failed; try again next tick rather than treating it as a dead window
    }
    if (present) return;
    clearIntervalImpl(timer);
    onMissing();
  }, intervalMs);
  return { close: () => clearIntervalImpl(timer) };
}

export function createTmuxPaneMonitor({
  pane,
  intervalMs = 5_000,
  tmux = (args) => execFileSync("tmux", args, { encoding: "utf8" }),
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  onMissing,
} = {}) {
  if (typeof pane !== "string" || !/^%\d+$/.test(pane)) throw new TypeError("pane must be a tmux pane id");
  if (typeof onMissing !== "function") throw new TypeError("onMissing must be a function");
  const timer = setIntervalImpl(() => {
    let present;
    try {
      present = tmuxPaneExists({ tmux, pane });
    } catch {
      return;
    }
    if (present) return;
    clearIntervalImpl(timer);
    onMissing();
  }, intervalMs);
  return { close: () => clearIntervalImpl(timer) };
}

// Watches for a managed sidecar taking over this session's window after it has already registered.
// Built like the tmux monitors above, with injectable timers, because the thing worth testing is
// when it fires and when it stays quiet, not that it can wait ten seconds.
export function createCoverageMonitor({
  intervalMs = PRESENCE_COVERAGE_POLL_MS,
  present,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  onCovered,
} = {}) {
  if (!Number.isSafeInteger(intervalMs) || intervalMs <= 0) throw new TypeError("intervalMs must be a positive safe integer");
  if (typeof present !== "function") throw new TypeError("present must be a function");
  if (typeof onCovered !== "function") throw new TypeError("onCovered must be a function");
  // One poll at a time. The fetch has its own 2 s bound and --coverage-poll-ms can be set below
  // that, so without this a slow router would let polls overlap and stand the session down twice.
  let polling = false;
  const timer = setIntervalImpl(() => {
    if (polling) return;
    polling = true;
    // runCli parks on a promise that never resolves, so an unhandled rejection here would end the
    // process. For a presence session that costs its own entry and its indicator; it carries no
    // mail, because a presence client builds no injector. An answer that never arrives is not
    // coverage either way.
    Promise.resolve()
      .then(() => present())
      .then((covered) => {
        polling = false;
        if (!covered) return;
        clearIntervalImpl(timer);
        onCovered();
      })
      .catch(() => { polling = false; });
  }, intervalMs);
  return { close: () => clearIntervalImpl(timer) };
}

export function createClaudeSidecar({ agentId, window = agentId, client, inject, log = () => {} } = {}) {
  assertAgentId(agentId);
  assertAgentId(window);
  if (client === undefined || typeof client.sendReceipt !== "function") throw new TypeError("a mailbox client is required");
  if (typeof inject !== "function") throw new TypeError("a tmux injector is required");

  async function onEnvelope(message, delivery) {
    log("delivery_received", { message_id: message.id, attempt: delivery.attemptNumber, from: message.from, to: message.to });
    client.sendReceipt("injection_requested", delivery);
    const text = claudeMailboxPrompt(message);
    await inject({ window, text });
    client.sendReceipt("ack", delivery);
    log("delivery_acked", { message_id: message.id, attempt: delivery.attemptNumber });
  }

  return { onEnvelope };
}

// Drives the router's activity frames from the hook's records.
//
// Every callback here is isolated. runCli parks on a promise that never resolves, so an uncaught
// throw in a timer ends the process and takes mail delivery with it, and sendActivity does throw
// when the socket is between reconnects (pi-extensions/mailbox-client.js:157). Activity is the
// less important of the sidecar's two jobs and must never be able to stop the other one.
export function createActivityReporter({
  client,
  tailer,
  state,
  log = () => {},
  now = () => Date.now(),
  refreshIntervalMs = ACTIVITY_REFRESH_MS,
  minResendMs = ACTIVITY_MIN_RESEND_MS,
} = {}) {
  let lastSentAtMs = 0;
  let lastSignature;

  function publish(force = false) {
    const activityState = state.state === "idle" ? "settled" : state.state;
    const subagents = state.subagents();
    const signature = `${activityState}|${state.subagentCount}|${JSON.stringify(subagents)}`;
    // Same suppression the Pi extension uses (mecha-messaging.js:1039-1046): skip an identical
    // frame unless it is old enough that the router's 15s expiry would otherwise lapse.
    if (!force && signature === lastSignature && now() - lastSentAtMs < minResendMs) return;
    client.sendActivity(activityState, { subagentCount: state.subagentCount, subagents });
    lastSentAtMs = now();
    lastSignature = signature;
  }

  return {
    // Called for every record the tailer produces.
    onRecord(record) {
      try {
        state.apply(record, record.at ?? now());
      } catch {
        log("activity_apply_failed", { state: "error" });
        return;
      }
      try {
        publish();
      } catch {
        log("activity_send_failed", { state: "error" });
      }
    },
    // Called on a fixed tick, independent of whether anything arrived.
    tick() {
      try {
        tailer.tick();
      } catch {
        log("activity_read_failed", { state: "error" });
      }
      try {
        state.tick(now());
        if (state.exhausted(now())) return; // stop refreshing; the router expires the agent
        publish(now() - lastSentAtMs >= refreshIntervalMs);
      } catch {
        log("activity_send_failed", { state: "error" });
      }
    },
    publish,
  };
}

function parseArgs(argv) {
  const result = {
    routerUrl: process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL,
    logDir: DEFAULT_LOG_DIR,
    maxLogBytes: DEFAULT_MAX_LOG_BYTES,
    // One setting, read by the hook and the sidecar, so they cannot disagree about where the
    // records live. The hook reads the same variable.
    activityDir: process.env.MECHA_ACTIVITY_DIR,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    const next = () => {
      const value = argv[index + 1];
      if (value === undefined) throw new Error(`${arg} requires a value`);
      index += 1;
      return value;
    };
    if (arg === "--agent-id") result.agentId = next();
    else if (arg === "--window") result.window = next();
    else if (arg === "--router-url") result.routerUrl = next();
    else if (arg === "--log-dir") result.logDir = next();
    else if (arg === "--max-log-bytes") result.maxLogBytes = Number(next());
    else if (arg === "--activity-dir") result.activityDir = next();
    else if (arg === "--presence-only") result.presenceOnly = true;
    else if (arg === "--coverage-poll-ms") result.coveragePollMs = Number(next());
    else throw new Error(`unknown argument: ${arg}`);
  }
  // The tmux window name, resolved once. It is both what the presence id is built from and the name
  // a managed sidecar for this window would be registered under, which is what makes the coverage
  // check possible at all. A rename after this point is not followed: the id is fixed too, and the
  // two would otherwise disagree about which window this session is in.
  if (result.presenceOnly) result.coverageWindow = result.window ?? tmuxWindowName();
  // A self-registered session derives its own id: it is the only thing that knows its pid, and the
  // pid is what keeps two sessions in identically named windows apart.
  if (!result.agentId && result.presenceOnly) {
    result.agentId = presenceAgentId({ window: result.coverageWindow, pid: process.pid });
  }
  if (!result.agentId) throw new Error("--agent-id is required");
  if (!result.window) result.window = result.agentId;
  if (!Number.isSafeInteger(result.maxLogBytes) || result.maxLogBytes <= 0) {
    throw new Error("--max-log-bytes must be a positive integer");
  }
  if (result.coveragePollMs === undefined) result.coveragePollMs = PRESENCE_COVERAGE_POLL_MS;
  if (!Number.isSafeInteger(result.coveragePollMs) || result.coveragePollMs <= 0) {
    throw new Error("--coverage-poll-ms must be a positive integer");
  }
  return result;
}

export async function runCli(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  const logger = createBodyFreeLogger({
    agentId: args.agentId,
    logDir: args.logDir,
    maxBytes: args.maxLogBytes,
    assertIdentity: args.presenceOnly ? assertPresenceAgentId : assertAgentId,
  });
  let sidecar;
  const client = createMailboxClient({
    agentId: args.agentId,
    routerUrl: args.routerUrl,
    assertIdentity: args.presenceOnly ? assertPresenceAgentId : assertAgentId,
    // A presence client builds no injector, so there is no code path that can paste into the
    // terminal. The split is structural rather than a flag checked at delivery time.
    onEnvelope: args.presenceOnly
      ? (message, delivery) => { logger.write("presence_ignored_envelope", { message_id: message.id }); client.sendReceipt("ack", delivery); }
      : (message, delivery) => sidecar.onEnvelope(message, delivery),
    log: () => logger.write("mailbox_client", { state: "event" }),
  });
  sidecar = args.presenceOnly ? undefined : createClaudeSidecar({
    agentId: args.agentId,
    window: args.window,
    client,
    inject: createTmuxInjector({ tmuxRest: createTmuxRestClient({ routerUrl: args.routerUrl }) }),
    log: (event, fields) => logger.write(event, fields),
  });
  const state = createActivityState();
  const tailer = createActivityTailer({
    agentId: args.agentId,
    dir: args.activityDir,
    onRecord: (record) => reporter.onRecord(record),
    onDropped: () => logger.write("activity_record_dropped", { agent_id: args.agentId }),
  });
  const reporter = createActivityReporter({
    client,
    tailer,
    state,
    log: (event, fields) => logger.write(event, fields),
  });

  logger.write("sidecar_start", { agent_id: args.agentId, window: args.window });
  const stop = (signal) => {
    logger.write("sidecar_stop", { signal });
    // A presence session's log is per-process and disposable, and one is created for every Claude
    // session on the machine. A worker's log is kept: it is the record of that worker's delivery.
    if (args.presenceOnly) {
      try { rmSync(logger.logPath, { force: true }); } catch { /* the next sweep is size-capped anyway */ }
    }
    // Removing the marker stops the hook writing files nobody will read.
    try { tailer.stop(); } catch { /* the pid check covers a marker we cannot remove */ }
    client.close();
    process.exit(0);
  };
  // One question, asked from two places, with the "router could not answer" case recorded once.
  // Without that record an unreachable router, a router with no /v1/agents route, or a body this
  // client cannot parse all read exactly like "nobody holds this window", and the check is off with
  // the duplicates it was meant to stop as the only evidence. Once per process, because the monitor
  // asks again every poll for as long as the session lives.
  let reportedCoverageUnavailable = false;
  const coverageCheck = () => managedAgentPresent({
    window: args.coverageWindow,
    routerUrl: args.routerUrl,
    onUnavailable: () => {
      // Once per process: the monitor asks again every poll for the life of the session, and this
      // goes to the shared log because stop() removes this process's own. Same argument as the
      // suppression record, and the same failure without it: a check that is silently off.
      if (reportedCoverageUnavailable) return;
      reportedCoverageUnavailable = true;
      writeSharedPresenceRecord("presence_coverage_unavailable");
    },
  });
  // Records that outlive this process. A presence sidecar removes its own log on the way out, so
  // anything worth reading afterwards has to go somewhere else.
  const writeSharedPresenceRecord = (event, signal) => {
    try {
      createBodyFreeLogger({ agentId: PRESENCE_SUPPRESSED_LOG_ID, logDir: args.logDir, maxBytes: args.maxLogBytes })
        .write(event, { window: args.coverageWindow, agent_id: args.agentId, signal });
    } catch {
      // Standing down, or noting that it could not be decided, must not depend on saying so.
    }
  };
  // This session is already represented, so it stands down. The record goes to the shared log
  // because stop() removes this process's own, and this is the one event someone will look for.
  const suppressed = (reason) => {
    writeSharedPresenceRecord("presence_suppressed", reason);
    stop(reason);
  };
  process.on("SIGTERM", () => stop("SIGTERM"));
  process.on("SIGINT", () => stop("SIGINT"));
  // The monitor lives for the whole process; stop() is the only exit path.
  if (!args.presenceOnly) {
    createTmuxWindowMonitor({
      window: args.window,
      onMissing: () => stop("tmux_window_missing"),
    });
  } else {
    // A self-registered session's window can be named anything, including names that are not valid
    // agent ids, so the pane is what gets watched.
    createTmuxPaneMonitor({
      pane: process.env.TMUX_PANE,
      onMissing: () => stop("tmux_pane_missing"),
    });
  }
  if (args.presenceOnly) {
    // A managed worker already holds a slot under its window name, and a second one for the same
    // window is the duplicate this check exists to stop: two entries in the list and two processes
    // per worker. Asked before registering, so the usual case never claims a lease at all.
    if (await coverageCheck()) suppressed("managed_sidecar_present");
    // The router answers a losing lease claim with silence rather than an error, so waiting on it
    // is waiting on something that may never arrive. Give up and exit: a session that cannot
    // register is still a working session, and a process that lingers is not.
    let timer;
    const timeout = new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error("presence_connect_timeout")), PRESENCE_CONNECT_TIMEOUT_MS);
      timer.unref?.();
    });
    try {
      await Promise.race([client.connect(), timeout]);
      clearTimeout(timer);
    } catch {
      logger.write("presence_gave_up", { agent_id: args.agentId });
      try { client.close(); } catch { /* nothing to release if it never opened */ }
      process.exit(0);
    }
  } else {
    await client.connect();
  }
  logger.write("sidecar_connected", { agent_id: args.agentId });

  // Started only after the lease is held: the marker is a promise to the hook that something is
  // reading, and until the socket is up that promise is not true.
  // Activity must never be able to stop mail delivery. A full disk or an unwritable
  // MECHA_ACTIVITY_DIR throws here, and without this the process would exit and the worker would
  // silently stop receiving mail because its indicator failed to start.
  let activityTimer;
  try {
    tailer.start();
    activityTimer = setInterval(() => reporter.tick(), ACTIVITY_POLL_MS);
    activityTimer.unref?.();
  } catch {
    logger.write("activity_start_failed", { agent_id: args.agentId });
    if (args.presenceOnly) process.exit(0); // presence with no activity has no purpose
  }

  // A managed worker starts its sidecar and its agent from one shell command, so a SessionStart can
  // fire before the managed sidecar has registered and the check above then reads "not covered".
  // This is what corrects that: the duplicate lasts at most one poll and needs no intervention. It
  // also covers a managed sidecar restarted under a session that has already registered. Neither
  // case is what produced the six duplicates of 2026-08-10 -- the startup check alone would have
  // caught all six -- and no instance of either has been observed in the retained router log. Both
  // follow from how worker_launch.py and the documented sidecar restart work. See
  // ACTIVITY_DECISIONS.md, "The ordering question".
  if (args.presenceOnly) {
    createCoverageMonitor({
      present: coverageCheck,
      intervalMs: args.coveragePollMs,
      onCovered: () => suppressed("managed_sidecar_appeared"),
    });
  }

  await new Promise(() => {});
}

// The documented launch path is ~/.claude/skills/supervisor/tools/claude_sidecar.mjs,
// where the skills directory is a symlink into the repo, so the entrypoint check must
// canonicalize both sides. See harness/cli_entrypoint.js.
if (isCliEntrypoint(import.meta.url)) {
  runCli().catch((error) => {
    try {
      // --log-dir is honoured here too. Without it the one record that explains a sidecar dying
      // goes somewhere the operator did not choose, and a test that expects a fatal exit writes
      // into the live log directory no matter what it passes.
      const argvValue = (flag) => {
        const index = process.argv.indexOf(flag);
        return index >= 0 ? process.argv[index + 1] : undefined;
      };
      const agentId = argvValue("--agent-id") ?? "claude-sidecar";
      createBodyFreeLogger({ agentId, logDir: argvValue("--log-dir") ?? DEFAULT_LOG_DIR })
        .write("sidecar_fatal", { state: "failed" });
    } catch {
      // Last-resort stderr still omits message bodies.
      console.error(`claude sidecar failed: ${error.message}`);
    }
    process.exit(1);
  });
}
