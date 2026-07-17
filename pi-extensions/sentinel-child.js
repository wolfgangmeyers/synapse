import { spawn } from "node:child_process";
import { chmod, copyFile, lstat, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { StringDecoder } from "node:string_decoder";
import { randomUUID } from "node:crypto";

const DEFAULT_TIMEOUT_MS = 180_000;
const LOOPBACK_HOSTS = new Set(["127.0.0.1", "[::1]"]);

function routerHttpUrl(routerUrl) {
  const url = new URL(routerUrl);
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("Mailbox v2 router URL must use http or https");
  if (!LOOPBACK_HOSTS.has(url.hostname)) throw new Error("Mailbox v2 router URL must be loopback");
  return url.href.replace(/\/$/, "");
}

function assistantResult(message) {
  if (message?.role !== "assistant" || !Array.isArray(message.content)) return undefined;
  return {
    text: message.content.filter((part) => part?.type === "text" && typeof part.text === "string").map((part) => part.text).join("\n").trim(),
    stopReason: typeof message.stopReason === "string" ? message.stopReason : "unknown",
    errorCode: typeof message.errorMessage === "string" && message.errorMessage.length > 0 ? "assistant_error" : undefined,
  };
}

export function runSentinelRpcHost({
  child, prompt, sendResult, markResultSendAttempt, timeoutMs = DEFAULT_TIMEOUT_MS,
  setTimeoutImpl = setTimeout, clearTimeoutImpl = clearTimeout, onDiagnostic = () => {},
} = {}) {
  if (!child?.stdin || !child.stdout || !child.stderr || typeof prompt !== "string"
    || typeof sendResult !== "function" || typeof markResultSendAttempt !== "function"
    || !Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) {
    throw new TypeError("a piped Sentinel RPC child, prompt, result sender, attempt marker, and timeout are required");
  }
  const promptId = randomUUID();
  const decoder = new StringDecoder("utf8");
  let stdoutBuffer = "";
  let promptAccepted = false;
  let agentSettled = false;
  let result;
  let resultSendStarted = false;
  let finalized = false;
  let stderrBytes = 0;
  let malformedFrames = 0;
  const eventCounts = {};
  let resolveCompletion;
  const completion = new Promise((resolveCompletionPromise) => { resolveCompletion = resolveCompletionPromise; });
  const timeout = setTimeoutImpl(() => fail("timeout"), timeoutMs);
  const finish = (state, code) => {
    if (finalized) return false;
    finalized = true;
    clearTimeoutImpl(timeout);
    try { onDiagnostic({ state, code, event_counts: eventCounts, stderr_bytes: stderrBytes, malformed_frames: malformedFrames }); } catch { /* diagnostics cannot affect terminal handling */ }
    resolveCompletion({ state, code });
    return true;
  };
  const fail = (code) => {
    if (!finish("failed", code)) return;
    try { child.kill?.("SIGTERM"); } catch { /* process state is terminally uncertain */ }
  };
  const maybeSendResult = () => {
    if (!promptAccepted || !agentSettled || resultSendStarted || finalized) return;
    if (result === undefined || result.stopReason !== "stop" || result.text.length === 0) return fail(result?.errorCode ?? "no_terminal_result");
    resultSendStarted = true;
    void (async () => {
      try { await markResultSendAttempt(); } catch { fail("result_send_marker_failed"); return; }
      try { await sendResult(result.text); } catch (error) { fail(error?.code === "result_send_rejected" ? "result_send_rejected" : "result_send_unknown"); return; }
      if (finish("succeeded", "result_sent")) {
        try { child.stdin.end(); } catch { fail("stdin_close_failed"); }
      }
    })();
  };
  const handleFrame = (frame) => {
    if (frame === null || typeof frame !== "object" || Array.isArray(frame) || typeof frame.type !== "string") { malformedFrames += 1; fail("malformed_frame"); return; }
    if (frame.type.length <= 64) eventCounts[frame.type] = (eventCounts[frame.type] ?? 0) + 1;
    if (frame.type === "response") {
      if (frame.id !== promptId || frame.command !== "prompt") return;
      if (frame.success !== true) fail("prompt_rejected"); else { promptAccepted = true; maybeSendResult(); }
      return;
    }
    if (frame.type === "extension_error") return fail("extension_error");
    if (frame.type === "turn_end") result = assistantResult(frame.message) ?? result;
    if (frame.type === "agent_end" && Array.isArray(frame.messages)) for (const message of frame.messages) result = assistantResult(message) ?? result;
    if (frame.type === "agent_settled") { agentSettled = true; maybeSendResult(); }
  };
  const consumeStdout = (chunk) => {
    stdoutBuffer += decoder.write(chunk);
    while (!finalized) {
      const newline = stdoutBuffer.indexOf("\n");
      if (newline === -1) return;
      let line = stdoutBuffer.slice(0, newline);
      stdoutBuffer = stdoutBuffer.slice(newline + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      try { handleFrame(JSON.parse(line)); } catch { malformedFrames += 1; fail("malformed_frame"); }
    }
  };
  child.stdout.on("data", consumeStdout);
  child.stdout.once("error", () => { if (!finalized) fail("stdout_error"); });
  child.stdout.once("end", () => {
    const trailing = `${stdoutBuffer}${decoder.end()}`;
    if (!finalized && trailing.length > 0) malformedFrames += 1;
    if (!finalized) fail(trailing.length > 0 ? "malformed_frame" : "stdout_eof");
  });
  child.stderr.on("data", (chunk) => { stderrBytes = Math.min(65_536, stderrBytes + Buffer.byteLength(chunk)); });
  child.stderr.once("error", () => { if (!finalized) fail("stderr_error"); });
  child.once("error", () => { if (!finalized) fail("child_error"); });
  child.once("exit", () => { if (!finalized) fail("child_exit"); });
  try { child.stdin.write(`${JSON.stringify({ id: promptId, type: "prompt", message: prompt })}\n`); } catch { fail("stdin_write_failed"); }
  return { completion };
}

async function prepareAuth(authPath) {
  const source = await lstat(authPath).catch(() => undefined);
  if (!source?.isFile() || source.isSymbolicLink?.() || source.uid !== process.getuid() || (source.mode & 0o777) !== 0o600) {
    throw new Error("child auth must be an owner-controlled regular 0600 file");
  }
  const home = await mkdtemp(join(tmpdir(), "mecha-sentinel-auth-home-"));
  try {
    const destination = join(home, ".pi", "agent", "auth.json");
    await mkdir(join(home, ".pi", "agent"), { recursive: true, mode: 0o700 });
    await copyFile(authPath, destination, 0);
    await chmod(destination, 0o600);
    return { home, cleanup: () => rm(home, { recursive: true, force: true }) };
  } catch (error) {
    await rm(home, { recursive: true, force: true });
    throw error;
  }
}

function bodyFreeStatus(status) {
  process.stdout.write(`${JSON.stringify(status)}\n`);
}

async function main() {
  let buffer = "";
  let config;
  let relayToken;
  let authorizeResultSend;
  const authorization = new Promise((resolveAuthorization) => { authorizeResultSend = resolveAuthorization; });
  process.stdin.setEncoding("utf8");
  process.stdin.on("data", (chunk) => {
    buffer += chunk;
    while (true) {
      const newline = buffer.indexOf("\n");
      if (newline === -1) return;
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      try {
        const command = JSON.parse(line);
        if (command?.type === "start" && config === undefined) config = command;
        if (command?.type === "result_send_authorized" && typeof command.relay_token === "string") { relayToken = command.relay_token; authorizeResultSend(); }
      } catch { /* malformed parent control input cannot disclose content */ }
    }
  });
  await new Promise((resolveStart) => {
    const check = () => { if (config !== undefined) resolveStart(); else setTimeout(check, 1); };
    check();
  });
  if (typeof config.prompt !== "string" || typeof config.model !== "string" || typeof config.auth_path !== "string"
    || typeof config.sender !== "string" || typeof config.recipient !== "string" || typeof config.occurrence_id !== "string" || !Number.isSafeInteger(config.timeout_ms)) {
    bodyFreeStatus({ type: "completed", state: "failed", code: "invalid_start" });
    return;
  }
  let auth;
  try { auth = await prepareAuth(config.auth_path); } catch { bodyFreeStatus({ type: "completed", state: "failed", code: "auth_failed" }); return; }
  const pi = spawn("pi", ["--mode", "rpc", "--provider", "openai-codex", "--model", config.model, "--no-session", "--no-extensions", "--no-context-files", "--no-skills", "--no-builtin-tools"], {
    cwd: auth.home, env: { PATH: process.env.PATH ?? "", HOME: auth.home }, stdio: ["pipe", "pipe", "pipe"], shell: false,
  });
  const exit = new Promise((resolveExit) => { pi.once("exit", () => resolveExit()); pi.once("error", () => resolveExit()); });
  const host = runSentinelRpcHost({
    child: pi, prompt: config.prompt, timeoutMs: config.timeout_ms,
    markResultSendAttempt: async () => { bodyFreeStatus({ type: "result_send_attempt" }); await authorization; },
    sendResult: async (body) => {
      const response = await fetch(`${routerHttpUrl(config.router_url)}/v1/sentinel-child-results`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ occurrence_id: config.occurrence_id, relay_token: relayToken, body }) });
      if (!response?.ok) { const error = new Error("fixed Sentinel result recipient rejected the message"); error.code = "result_send_rejected"; throw error; }
    },
    onDiagnostic: (diagnostic) => bodyFreeStatus({ type: "completed", ...diagnostic }),
  });
  await host.completion;
  await exit;
  await auth.cleanup();
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  void main().catch(() => bodyFreeStatus({ type: "completed", state: "failed", code: "runner_error" }));
}

export default function sentinelChildExtension() {}
