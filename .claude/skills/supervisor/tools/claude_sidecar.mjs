#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { mkdirSync, renameSync, statSync, appendFileSync, rmSync } from "node:fs";
import { join } from "node:path";

import { isCliEntrypoint } from "../../../../harness/cli_entrypoint.js";
import { createMailboxClient, assertAgentId } from "../../../../pi-extensions/mailbox-client.js";

export const DEFAULT_LOG_DIR = "~/.mecha-developer/claude-sidecars";
export const DEFAULT_MAX_LOG_BYTES = 256 * 1024;
export const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";

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
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) throw new TypeError("maxBytes must be a positive safe integer");
  try {
    if (statSync(logPath).size < maxBytes) return;
  } catch (error) {
    if (error?.code === "ENOENT") return;
    throw error;
  }
  rmSync(`${logPath}.1`, { force: true });
  renameSync(logPath, `${logPath}.1`);
}

export function createBodyFreeLogger({ agentId, logDir = DEFAULT_LOG_DIR, maxBytes = DEFAULT_MAX_LOG_BYTES, now = () => new Date() } = {}) {
  assertAgentId(agentId);
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

function parseArgs(argv) {
  const result = {
    routerUrl: process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL,
    logDir: DEFAULT_LOG_DIR,
    maxLogBytes: DEFAULT_MAX_LOG_BYTES,
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
    else throw new Error(`unknown argument: ${arg}`);
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
  const logger = createBodyFreeLogger({ agentId: args.agentId, logDir: args.logDir, maxBytes: args.maxLogBytes });
  let sidecar;
  const client = createMailboxClient({
    agentId: args.agentId,
    routerUrl: args.routerUrl,
    onEnvelope: (message, delivery) => sidecar.onEnvelope(message, delivery),
    log: () => logger.write("mailbox_client", { state: "event" }),
  });
  sidecar = createClaudeSidecar({
    agentId: args.agentId,
    window: args.window,
    client,
    inject: createTmuxInjector(),
    log: (event, fields) => logger.write(event, fields),
  });
  logger.write("sidecar_start", { agent_id: args.agentId, window: args.window });
  const stop = (signal) => { logger.write("sidecar_stop", { signal }); client.close(); process.exit(0); };
  process.on("SIGTERM", () => stop("SIGTERM"));
  process.on("SIGINT", () => stop("SIGINT"));
  // The monitor lives for the whole process; stop() is the only exit path.
  createTmuxWindowMonitor({
    window: args.window,
    onMissing: () => stop("tmux_window_missing"),
  });
  await client.connect();
  logger.write("sidecar_connected", { agent_id: args.agentId });
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
