import { spawn } from "node:child_process";
import { homedir } from "node:os";
import { join } from "node:path";

const STATUS_KEY = "codex-usage";
const DEFAULT_POLL_INTERVAL_MS = 60_000;
const DEFAULT_REQUEST_TIMEOUT_MS = 2_500;
const DEFAULT_CODEX_CLI = process.env.MECHA_CODEX_CLI || "/opt/homebrew/bin/codex";
const DEFAULT_CODEX_SOCKET = process.env.MECHA_CODEX_APP_SERVER_SOCKET || join(homedir(), ".codex", "ipc", "ipc.sock");

export function isCodexSubscriptionModel(model) {
  return model?.provider === "openai-codex";
}

export function normalizeRateLimitsPayload(payload) {
  const result = payload?.result ?? payload;
  const primary = result?.rateLimits?.primary;
  const usedPercent = primary?.usedPercent;
  if (typeof usedPercent !== "number" || !Number.isFinite(usedPercent)) return undefined;
  const clampedUsed = Math.max(0, Math.min(100, usedPercent));
  return {
    usedPercent: clampedUsed,
    remainingPercent: Math.max(0, Math.min(100, 100 - clampedUsed)),
    resetsAt: primary?.resetsAt,
    windowDurationMins: primary?.windowDurationMins,
    rateLimitReachedType: result?.rateLimits?.rateLimitReachedType,
  };
}

export function colorBucketForRemaining(remainingPercent) {
  if (!Number.isFinite(remainingPercent)) return undefined;
  if (remainingPercent >= 95) return "blue";
  if (remainingPercent >= 75) return "green";
  if (remainingPercent >= 50) return "yellow";
  if (remainingPercent >= 25) return "orange";
  return "red";
}

function ansi256(color, text) {
  return `\u001b[38;5;${color}m${text}\u001b[39m`;
}

export function styleUsageLabel(label, bucket, theme) {
  switch (bucket) {
    case "blue":
      return ansi256(39, label);
    case "green":
      return theme?.fg?.("success", label) ?? ansi256(42, label);
    case "yellow":
      return theme?.fg?.("warning", label) ?? ansi256(220, label);
    case "orange":
      return ansi256(208, label);
    case "red":
      return theme?.fg?.("error", label) ?? ansi256(196, label);
    default:
      return label;
  }
}

export function renderUsageStatus(snapshot, theme) {
  if (!snapshot || !Number.isFinite(snapshot.remainingPercent)) return undefined;
  const remaining = Math.max(0, Math.min(100, Math.round(snapshot.remainingPercent)));
  const label = `Codex ${remaining}%`;
  return styleUsageLabel(label, colorBucketForRemaining(remaining), theme);
}

function parseJsonLines(text) {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .flatMap((line) => {
      try {
        return [JSON.parse(line)];
      } catch {
        return [];
      }
    });
}

export async function readCodexRateLimits({
  codexCli = DEFAULT_CODEX_CLI,
  socketPath = DEFAULT_CODEX_SOCKET,
  requestTimeoutMs = DEFAULT_REQUEST_TIMEOUT_MS,
  spawnImpl = spawn,
} = {}) {
  const requestId = 2;
  const input = [
    {
      id: 1,
      method: "initialize",
      params: {
        clientInfo: { name: "mecha-codex-usage-hud", version: "0.0.0" },
        capabilities: { experimentalApi: true },
      },
    },
    { method: "initialized" },
    { id: requestId, method: "account/rateLimits/read", params: {} },
  ].map((message) => JSON.stringify(message)).join("\n") + "\n";

  return new Promise((resolve) => {
    let settled = false;
    let stdout = "";
    let stderr = "";
    const finish = (value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(value);
    };
    const child = spawnImpl(codexCli, ["app-server", "proxy", "--sock", socketPath], {
      stdio: ["pipe", "pipe", "pipe"],
    });
    const timer = setTimeout(() => {
      child.kill?.();
      finish(undefined);
    }, requestTimeoutMs);

    child.stdout?.setEncoding?.("utf8");
    child.stderr?.setEncoding?.("utf8");
    child.stdout?.on?.("data", (chunk) => { stdout += chunk; });
    child.stderr?.on?.("data", (chunk) => { stderr += chunk; });
    child.on?.("error", () => finish(undefined));
    child.on?.("close", (code) => {
      if (code !== 0) {
        finish(undefined);
        return;
      }
      const response = parseJsonLines(stdout).find((message) => message?.id === requestId);
      if (!response || response.error) {
        finish(undefined);
        return;
      }
      finish(normalizeRateLimitsPayload(response));
    });
    child.stdin?.end?.(input);
    void stderr;
  });
}

export function createCodexUsageHud(pi, {
  pollIntervalMs = DEFAULT_POLL_INTERVAL_MS,
  readRateLimits = readCodexRateLimits,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
} = {}) {
  let interval;
  let currentContext;
  let stopped = false;
  let pollInFlight = false;
  let pendingRefresh = false;
  let generation = 0;

  const clearStatus = (ctx = currentContext) => {
    ctx?.ui?.setStatus?.(STATUS_KEY, undefined);
  };

  const refresh = async (ctx = currentContext) => {
    currentContext = ctx;
    if (!ctx?.ui?.setStatus || !isCodexSubscriptionModel(ctx.model)) {
      generation++;
      clearStatus(ctx);
      return;
    }
    if (pollInFlight) {
      pendingRefresh = true;
      return;
    }
    const pollGeneration = generation;
    pollInFlight = true;
    try {
      const snapshot = await readRateLimits();
      if (stopped || ctx !== currentContext || pollGeneration !== generation || !isCodexSubscriptionModel(ctx.model)) return;
      const status = renderUsageStatus(snapshot, ctx.ui.theme);
      ctx.ui.setStatus(STATUS_KEY, status);
    } catch {
      clearStatus(ctx);
    } finally {
      pollInFlight = false;
      if (pendingRefresh && !stopped) {
        pendingRefresh = false;
        void refresh(currentContext);
      }
    }
  };

  const start = (ctx) => {
    stopped = false;
    generation++;
    currentContext = ctx;
    clearStatus(ctx);
    void refresh(ctx);
    if (interval !== undefined) clearIntervalImpl(interval);
    interval = setIntervalImpl(() => { void refresh(currentContext); }, pollIntervalMs);
  };

  const shutdown = () => {
    stopped = true;
    pendingRefresh = false;
    if (interval !== undefined) {
      clearIntervalImpl(interval);
      interval = undefined;
    }
    clearStatus(currentContext);
    currentContext = undefined;
  };

  pi.on("session_start", async (_event, ctx) => start(ctx));
  pi.on("model_select", async (_event, ctx) => { void refresh(ctx); });
  pi.on("session_shutdown", async () => shutdown());

  return { refresh, start, shutdown, clearStatus };
}

export default function (pi) {
  createCodexUsageHud(pi);
}
