#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { mkdirSync, appendFileSync } from "node:fs";
import { join } from "node:path";

import { isCliEntrypoint } from "../../../../harness/cli_entrypoint.js";
import { rotateSizeCappedLog } from "../../../../harness/log_rotation.js";
import { createMailboxClient, assertAgentId, assertPresenceAgentId } from "../../../../pi-extensions/mailbox-client.js";
import { createActivityTailer } from "./activity_tail.mjs";
import { createActivityState } from "./activity_state.mjs";

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
export const PRESENCE_MAX_ATTEMPTS = 3;

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

function assertTmuxWindowExists({ tmux, session, window }) {
  tmux(["display-message", "-p", "-t", `${session}:${window}`, "#{pane_id}"]);
}

export function createTmuxInjector({ tmux = (args) => execFileSync("tmux", args, { stdio: "ignore" }), session = "mecha", enterDelayMs = 5_000, sleep = sleepSync } = {}) {
  if (!Number.isSafeInteger(enterDelayMs) || enterDelayMs < 0) throw new TypeError("enterDelayMs must be a non-negative safe integer");
  if (typeof sleep !== "function") throw new TypeError("sleep must be a function");
  return function inject({ window, text }) {
    assertAgentId(window);
    try {
      assertTmuxWindowExists({ tmux, session, window });
      tmux(["send-keys", "-t", `${session}:${window}`, "\x1b[200~"]);
      tmux(["send-keys", "-l", "-t", `${session}:${window}`, text]);
      tmux(["send-keys", "-t", `${session}:${window}`, "\x1b[201~"]);
      sleep(enterDelayMs);
      tmux(["send-keys", "-t", `${session}:${window}`, "Enter"]);
    } catch {
      throw new Error("tmux_injection_failed");
    }
  };
}

export function createTmuxWindowMonitor({
  window,
  session = "mecha",
  intervalMs = 5_000,
  tmux = (args) => execFileSync("tmux", args, { stdio: "ignore" }),
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  onMissing,
} = {}) {
  assertAgentId(window);
  if (!Number.isSafeInteger(intervalMs) || intervalMs <= 0) throw new TypeError("intervalMs must be a positive safe integer");
  if (typeof setIntervalImpl !== "function" || typeof clearIntervalImpl !== "function") throw new TypeError("timer functions are required");
  if (typeof onMissing !== "function") throw new TypeError("onMissing must be a function");
  const timer = setIntervalImpl(() => {
    try {
      assertTmuxWindowExists({ tmux, session, window });
    } catch {
      clearIntervalImpl(timer);
      onMissing();
    }
  }, intervalMs);
  return { close: () => clearIntervalImpl(timer) };
}

export function createTmuxPaneMonitor({
  pane,
  intervalMs = 5_000,
  tmux = (args) => execFileSync("tmux", args, { stdio: "ignore" }),
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  onMissing,
} = {}) {
  if (typeof pane !== "string" || !/^%\d+$/.test(pane)) throw new TypeError("pane must be a tmux pane id");
  if (typeof onMissing !== "function") throw new TypeError("onMissing must be a function");
  const timer = setIntervalImpl(() => {
    try {
      tmux(["display-message", "-p", "-t", pane, "#{pane_id}"]);
    } catch {
      clearIntervalImpl(timer);
      onMissing();
    }
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
    inject({ window, text });
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
    else throw new Error(`unknown argument: ${arg}`);
  }
  // A self-registered session derives its own id: it is the only thing that knows its pid, and the
  // pid is what keeps two sessions in identically named windows apart.
  if (!result.agentId && result.presenceOnly) {
    result.agentId = presenceAgentId({ window: result.window ?? tmuxWindowName(), pid: process.pid });
  }
  if (!result.agentId) throw new Error("--agent-id is required");
  if (!result.window) result.window = result.agentId;
  if (!Number.isSafeInteger(result.maxLogBytes) || result.maxLogBytes <= 0) {
    throw new Error("--max-log-bytes must be a positive integer");
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
    inject: createTmuxInjector(),
    log: (event, fields) => logger.write(event, fields),
  });
  const state = createActivityState();
  const tailer = createActivityTailer({
    agentId: args.agentId,
    dir: args.activityDir,
    onRecord: (record) => reporter.onRecord(record),
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
    // Removing the marker stops the hook writing files nobody will read.
    try { tailer.stop(); } catch { /* the pid check covers a marker we cannot remove */ }
    client.close();
    process.exit(0);
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
    // The router answers a losing lease claim with silence rather than an error, so a retry loop
    // here would look alive and do nothing forever. Give up quietly and leave the session working.
    let connected = false;
    for (let attempt = 1; attempt <= PRESENCE_MAX_ATTEMPTS && !connected; attempt += 1) {
      try {
        await client.connect();
        connected = true;
      } catch {
        logger.write("presence_register_failed", { agent_id: args.agentId, attempt: String(attempt) });
      }
    }
    if (!connected) {
      logger.write("presence_gave_up", { agent_id: args.agentId });
      process.exit(0);
    }
  } else {
    await client.connect();
  }
  logger.write("sidecar_connected", { agent_id: args.agentId });

  // Started only after the lease is held: the marker is a promise to the hook that something is
  // reading, and until the socket is up that promise is not true.
  tailer.start();
  const activityTimer = setInterval(() => reporter.tick(), ACTIVITY_POLL_MS);
  activityTimer.unref?.();

  await new Promise(() => {});
}

// The documented launch path is ~/.claude/skills/supervisor/tools/claude_sidecar.mjs,
// where the skills directory is a symlink into the repo, so the entrypoint check must
// canonicalize both sides. See harness/cli_entrypoint.js.
if (isCliEntrypoint(import.meta.url)) {
  runCli().catch((error) => {
    try {
      const agentIdIndex = process.argv.indexOf("--agent-id");
      const agentId = agentIdIndex >= 0 ? process.argv[agentIdIndex + 1] : "claude-sidecar";
      createBodyFreeLogger({ agentId }).write("sidecar_fatal", { state: "failed" });
    } catch {
      // Last-resort stderr still omits message bodies.
      console.error(`claude sidecar failed: ${error.message}`);
    }
    process.exit(1);
  });
}
