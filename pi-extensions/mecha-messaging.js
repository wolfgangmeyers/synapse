import { randomBytes, randomUUID } from "node:crypto";
import { execFileSync, spawn } from "node:child_process";
import { chmod, copyFile, lstat, mkdir, mkdtemp, rm } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const WebSocket = globalThis.WebSocket;
const NANOID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

function createNanoId() {
  let id = "";
  while (id.length < 21) {
    for (const byte of randomBytes(21 - id.length)) {
      if (byte < 248) id += NANOID_ALPHABET[byte % NANOID_ALPHABET.length];
    }
  }
  return id;
}

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "[::1]"]);
const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
const DEFAULT_RECONNECT_INITIAL_DELAY_MS = 250;
const DEFAULT_RECONNECT_MAX_DELAY_MS = 30_000;
const DEFAULT_SESSION_CHECKPOINT_RETRY_MS = 250;
const DEFAULT_REPLY_TIMEOUT_MS = 5_000;
const DEFAULT_SENTINEL_RPC_TIMEOUT_MS = 180_000;
const MAILBOX_MESSAGE_TYPE = "mecha-agent-message";
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const SENTINEL_RECEIVER_POLICIES = new URL("./sentinel-receiver-policies.json", import.meta.url);

function childSessionIds() {
  if (!(globalThis[CHILD_SESSION_IDS] instanceof Set)) {
    globalThis[CHILD_SESSION_IDS] = new Set();
  }
  return globalThis[CHILD_SESSION_IDS];
}

function childSessionId(event) {
  return typeof event?.sessionId === "string" && event.sessionId.length > 0
    ? event.sessionId
    : undefined;
}

export function recoverSentinelExits(sessionManager, client, onConfirmed = undefined) {
  const pending = new Map();
  const confirmed = new Map();
  for (const entry of sessionManager.getBranch()) {
    const details = entry?.details;
    if (entry?.type !== "custom_message"
      || entry.customType !== "mecha-sentinel-status"
      || typeof details?.occurrence_id !== "string"
      || typeof details.attempt_id !== "string") continue;
    const key = `${details.occurrence_id}\u0000${details.attempt_id}`;
    const sequence = Number.isSafeInteger(details.transition_sequence) ? details.transition_sequence : -1;
    if (details.state === "exited_pending" && Number.isSafeInteger(details.exit_code)) {
      const previous = pending.get(key);
      if (previous === undefined || sequence >= previous.sequence) pending.set(key, { details, sequence });
    }
    if (details.state === "exited" || details.state === "failed") {
      const previous = confirmed.get(key);
      if (previous === undefined || sequence >= previous) confirmed.set(key, sequence);
    }
  }
  for (const [key, { details, sequence }] of pending) {
    if (confirmed.has(key) && confirmed.get(key) >= sequence) continue;
    client.queueSentinelExit({
      messageId: details.message_id,
      occurrenceId: details.occurrence_id,
      attemptId: details.attempt_id,
      exitCode: details.exit_code,
      ...(onConfirmed === undefined ? {} : { onConfirmed: (confirmed) => onConfirmed({
        messageId: details.message_id,
        occurrenceId: details.occurrence_id,
        attemptId: details.attempt_id,
        exitCode: details.exit_code,
      }, confirmed) }),
    });
  }
}

export function resolveAgentId(
  environment = process.env,
  { queryWindowName = queryTmuxWindowName } = {},
) {
  if (typeof environment.TMUX !== "string" || environment.TMUX.length === 0
    || typeof environment.TMUX_PANE !== "string" || !/^%\d+$/.test(environment.TMUX_PANE)) {
    return undefined;
  }
  const agentId = queryWindowName(environment);
  return assertAgentId(typeof agentId === "string" ? agentId.trim() : agentId);
}

export function queryTmuxWindowName(
  environment = process.env,
  { execFileSync: exec = execFileSync } = {},
) {
  const paneId = environment.TMUX_PANE;
  if (typeof paneId !== "string" || !/^%\d+$/.test(paneId)) {
    throw new Error("Mailbox v2 could not identify the current tmux pane");
  }
  try {
    const windowName = exec("tmux", ["display-message", "-p", "-t", paneId, "#{window_name}"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
    return windowName.trim();
  } catch {
    throw new Error("Mailbox v2 could not read the current tmux window");
  }
}

export function routeMailboxEnvelope(envelope, inject) {
  inject(envelope);
}

export function loadSentinelReceiverPolicies(path = SENTINEL_RECEIVER_POLICIES, readFile = readFileSync) {
  let policies;
  try {
    policies = JSON.parse(readFile(path, "utf8"));
  } catch {
    throw new Error("Sentinel receiver policies are unavailable or malformed");
  }
  if (policies === null || typeof policies !== "object" || Array.isArray(policies)) {
    throw new Error("Sentinel receiver policies are unavailable or malformed");
  }
  for (const policy of Object.values(policies)) {
    if (policy === null || typeof policy !== "object" || Array.isArray(policy)
      || typeof policy.receiver !== "string" || policy.receiver.length === 0
      || !Array.isArray(policy.senders) || policy.senders.length === 0 || !policy.senders.every((sender) => typeof sender === "string" && sender.length > 0)
      || !Array.isArray(policy.models) || policy.models.length === 0 || !policy.models.every((model) => typeof model === "string" && model.length > 0)
      || policy.execution === null || typeof policy.execution !== "object" || Array.isArray(policy.execution)
      || Object.keys(policy.execution).some((key) => !["supervisor", "auth_path", "tools"].includes(key))
      || typeof policy.execution.supervisor !== "string" || !policy.senders.includes(policy.execution.supervisor)
      || typeof policy.execution.auth_path !== "string" || policy.execution.auth_path.length === 0
      || !Array.isArray(policy.execution.tools) || policy.execution.tools.length !== 0) {
      throw new Error("Sentinel receiver policies are unavailable or malformed");
    }
  }
  return policies;
}

export function sentinelExecutionProfile(message, receiverPolicies = {}) {
  const policy = receiverPolicies[message?.sentinel?.receiver_policy];
  const supervisor = message?.sentinel?.origin_sender;
  if (!policy
    || policy.receiver !== message.to
    || typeof supervisor !== "string"
    || !Array.isArray(policy.senders)
    || !policy.senders.includes(supervisor)
    || !Array.isArray(policy.models)
    || !policy.models.includes(message.sentinel.model)
    || policy.execution?.supervisor !== supervisor
    || typeof policy.execution.auth_path !== "string" || policy.execution.auth_path.length === 0
    || !Array.isArray(policy.execution.tools)
    || policy.execution.tools.length !== 0) return undefined;
  return policy.execution;
}

export async function prepareSentinelChildAuth({
  authPath,
  createDirectory = mkdtemp,
  copy = copyFile,
  createDirectoryPath = mkdir,
  changeMode = chmod,
  inspect = lstat,
  removeDirectory = rm,
  getuid = process.getuid,
} = {}) {
  if (typeof authPath !== "string" || authPath.length === 0 || typeof getuid !== "function") {
    throw new TypeError("an explicit operator-provisioned child auth path is required");
  }
  let source;
  try {
    source = await inspect(authPath);
  } catch {
    throw new Error("child auth must be an owner-controlled regular 0600 file");
  }
  if (!source.isFile() || source.isSymbolicLink?.() || source.uid !== getuid() || (source.mode & 0o777) !== 0o600) {
    throw new Error(source.uid !== getuid() ? "child auth must be owned by the current user" : "child auth must be an owner-controlled regular 0600 file");
  }
  const home = await createDirectory(join(tmpdir(), "mecha-sentinel-auth-home-"));
  const authDirectory = join(home, ".pi", "agent");
  const destination = join(authDirectory, "auth.json");
  try {
    await createDirectoryPath(authDirectory, { recursive: true, mode: 0o700 });
    await copy(authPath, destination, 0);
    await changeMode(destination, 0o600);
  } catch (error) {
    await removeDirectory(home, { recursive: true, force: true });
    throw error;
  }
  return {
    home,
    authPath: destination,
    // ponytail: credentials stay filesystem-only; do not return an auth environment entry.
    environment: {},
    cleanup: () => removeDirectory(home, { recursive: true, force: true }),
  };
}

function isBodyFreeRunnerStatus(frame) {
  if (frame === null || typeof frame !== "object" || Array.isArray(frame) || typeof frame.type !== "string") return false;
  if (frame.type === "result_send_attempt") return Object.keys(frame).length === 1;
  return frame.type === "completed"
    && Object.keys(frame).every((key) => ["type", "state", "code", "event_counts", "stderr_bytes", "malformed_frames"].includes(key))
    && (frame.state === "succeeded" || frame.state === "failed")
    && typeof frame.code === "string"
    && (frame.event_counts === undefined || (frame.event_counts !== null && typeof frame.event_counts === "object" && Object.values(frame.event_counts).every((value) => Number.isSafeInteger(value) && value >= 0)))
    && (frame.stderr_bytes === undefined || (Number.isSafeInteger(frame.stderr_bytes) && frame.stderr_bytes >= 0))
    && (frame.malformed_frames === undefined || (Number.isSafeInteger(frame.malformed_frames) && frame.malformed_frames >= 0));
}

export async function launchSentinelChild({
  message,
  supervisor = message?.sentinel?.origin_sender ?? message?.from,
  routerUrl = process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL,
  authPath,
  spawnImpl = spawn,
  markResultSendAttempt = async () => {},
  timeoutMs = DEFAULT_SENTINEL_RPC_TIMEOUT_MS,
  onDiagnostic = () => {},
  onCleanupError = () => {},
} = {}) {
  if (typeof message?.body !== "string" || typeof message.sentinel?.model !== "string" || typeof supervisor !== "string"
    || typeof authPath !== "string" || !Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) {
    throw new TypeError("a Sentinel message with a private body, model, approved supervisor, auth path, and timeout is required");
  }
  const runner = spawnImpl(process.execPath, [new URL("./sentinel-child.js", import.meta.url).pathname], {
    env: { PATH: process.env.PATH ?? "" }, stdio: ["pipe", "pipe", "pipe"], shell: false,
  });
  let observedExit;
  let finalized = false;
  let resultSendStarted = false;
  let authorizationPending = false;
  let pendingRunnerCompletion;
  let stdoutBuffer = "";
  let stderrBytes = 0;
  let resolveCompletion;
  const completion = new Promise((resolve) => { resolveCompletion = resolve; });
  const finish = (state, code) => {
    if (finalized) return;
    finalized = true;
    try { onDiagnostic({ state, code, stderr_bytes: stderrBytes }); } catch { /* diagnostics cannot change terminal handling */ }
    resolveCompletion({ state, code });
  };
  const fail = (code) => {
    if (finalized) return;
    finish("failed", code);
    try { runner.kill?.("SIGTERM"); } catch { /* process state is terminally uncertain */ }
  };
  const finishRunnerCompletion = (status) => {
    if (status.state === "succeeded" && status.code === "result_sent" && authorizationPending) {
      pendingRunnerCompletion = status;
      return;
    }
    finish(status.state, status.code);
  };
  const handleStatus = (status) => {
    if (!isBodyFreeRunnerStatus(status)) return fail("runner_malformed_status");
    if (status.type !== "result_send_attempt") return finishRunnerCompletion(status);
    if (resultSendStarted) return fail("runner_duplicate_result_attempt");
    resultSendStarted = true;
    authorizationPending = true;
    void (async () => {
      let relayToken;
      try { relayToken = await markResultSendAttempt(); } catch { fail("result_send_marker_failed"); return; }
      if (finalized) return;
      try {
        runner.stdin.write(`${JSON.stringify({ type: "result_send_authorized", relay_token: relayToken })}\n`, (error) => {
          if (error) return fail("runner_stdin_error");
          try {
            runner.stdin.end((endError) => {
              if (endError) return fail("runner_stdin_error");
              authorizationPending = false;
              if (pendingRunnerCompletion !== undefined) finishRunnerCompletion(pendingRunnerCompletion);
            });
          } catch { fail("runner_stdin_error"); }
        });
      } catch { fail("runner_stdin_error"); }
    })();
  };
  runner.stdin.once?.("error", () => fail("runner_stdin_error"));
  runner.stdout.on("data", (chunk) => {
    stdoutBuffer += chunk.toString("utf8");
    while (!finalized) {
      const newline = stdoutBuffer.indexOf("\n");
      if (newline === -1) return;
      const line = stdoutBuffer.slice(0, newline); stdoutBuffer = stdoutBuffer.slice(newline + 1);
      try { handleStatus(JSON.parse(line)); } catch { fail("runner_malformed_status"); }
    }
  });
  runner.stdout.once("error", () => fail("runner_stdout_error"));
  runner.stdout.once("end", () => { if (!finalized) fail(stdoutBuffer.length === 0 ? "runner_stdout_eof" : "runner_malformed_status"); });
  runner.stderr.on("data", (chunk) => { stderrBytes = Math.min(65_536, stderrBytes + Buffer.byteLength(chunk)); });
  runner.once("error", () => fail("runner_error"));
  runner.once("exit", (code) => { observedExit ??= Number.isSafeInteger(code) ? code : 1; if (!finalized) fail("runner_exit"); });
  try {
    runner.stdin.write(`${JSON.stringify({
      type: "start", prompt: message.body, model: message.sentinel.model, router_url: routerUrl,
      sender: message.to, recipient: supervisor, occurrence_id: message.id, auth_path: authPath, timeout_ms: timeoutMs,
    })}\n`);
  } catch {
    fail("runner_stdin_write_failed");
  }
  return {
    childIdentity: `child-${runner.pid}`,
    completion,
    onExit(callback) {
      if (observedExit !== undefined) return queueMicrotask(() => callback(observedExit));
      runner.once("exit", (code) => callback(Number.isSafeInteger(code) ? code : 1));
      runner.once("error", () => callback(1));
    },
  };
}

function assertAgentId(agentId) {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 requires a valid current tmux window name");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(agentId)) {
    throw new Error("Mailbox v2 agent identity is invalid");
  }
  return agentId;
}

export function routerHttpUrl(routerUrl) {
  const url = new URL(routerUrl);
  if (!["http:", "https:"].includes(url.protocol) || !LOOPBACK_HOSTS.has(url.hostname)
    || !["", "/"].includes(url.pathname) || url.search || url.hash) {
    throw new Error("MECHA_ROUTER_URL must be an exact loopback HTTP(S) base URL");
  }
  return url.toString().replace(/\/$/, "");
}

export function routerWebSocketUrl(routerUrl) {
  const url = new URL(routerHttpUrl(routerUrl));
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.pathname = "/v1/agents";
  return url.toString();
}

export function parseReplyArguments(argumentsText) {
  const [recipient, ...bodyParts] = argumentsText.trim().split(/\s+/);
  const body = bodyParts.join(" ");
  if (!recipient || !body || recipient.includes("@")) {
    throw new Error("Usage: /mecha-reply <recipient> <message>");
  }
  return { recipient, body };
}

function socketOpen(socket) {
  return socket?.readyState === (WebSocket?.OPEN ?? 1);
}

function connectionRefused(error) {
  return error?.code === "ECONNREFUSED" || error?.cause?.code === "ECONNREFUSED";
}

function validateReconnectOptions(initialDelayMs, maxDelayMs) {
  if (!Number.isSafeInteger(initialDelayMs) || initialDelayMs <= 0) {
    throw new TypeError("reconnectInitialDelayMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxDelayMs) || maxDelayMs < initialDelayMs) {
    throw new TypeError("reconnectMaxDelayMs must be a safe integer no smaller than reconnectInitialDelayMs");
  }
}

export function createMailboxClient({
  agentId,
  routerUrl = process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL,
  onEnvelope,
  onConnectionState = () => {},
  onKickstartFailure = () => {},
  kickstart = () => {},
  log = () => {},
  webSocketFactory = (url) => new WebSocket(url),
  fetchImpl = fetch,
  generateId = createNanoId,
  renewIntervalMs = 10_000,
  reconnectInitialDelayMs = DEFAULT_RECONNECT_INITIAL_DELAY_MS,
  reconnectMaxDelayMs = DEFAULT_RECONNECT_MAX_DELAY_MS,
  random = Math.random,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
} = {}) {
  assertAgentId(agentId);
  if (typeof onEnvelope !== "function") throw new TypeError("onEnvelope is required");
  if (typeof onConnectionState !== "function") throw new TypeError("onConnectionState must be a function");
  if (typeof onKickstartFailure !== "function") throw new TypeError("onKickstartFailure must be a function");
  if (typeof kickstart !== "function") throw new TypeError("kickstart must be a function");
  if (!Number.isSafeInteger(renewIntervalMs) || renewIntervalMs <= 0) {
    throw new TypeError("renewIntervalMs must be a positive safe integer");
  }
  if (typeof random !== "function") throw new TypeError("random must be a function");
  validateReconnectOptions(reconnectInitialDelayMs, reconnectMaxDelayMs);
  routerHttpUrl(routerUrl);

  const instanceId = generateId();
  let socket;
  let leaseId;
  let renewTimer;
  let reconnectTimer;
  let reconnectAttempt = 0;
  let stopped = false;
  let initialConnection;
  let resolveInitialConnection;
  let rejectInitialConnection;
  const pendingSentinelStates = new Map();
  const deferredSentinelExits = new Map();

  function report(state, error) {
    try {
      onConnectionState({ state, error });
    } catch {
      // Connection diagnostics must not alter delivery behavior.
    }
  }

  function send(fields, pendingSentinelState) {
    if (!socketOpen(socket)) throw new Error("Mailbox v2 socket is not connected");
    const frameId = generateId();
    if (pendingSentinelState !== undefined) pendingSentinelStates.set(frameId, pendingSentinelState);
    try {
      socket.send(JSON.stringify({ version: "v1", frame_id: frameId, ...fields }));
    } catch (error) {
      pendingSentinelStates.delete(frameId);
      throw error;
    }
    return frameId;
  }

  function rejectPendingSentinelStates(error) {
    for (const { reject } of pendingSentinelStates.values()) reject(error);
    pendingSentinelStates.clear();
  }

  function queueSentinelExit({ occurrenceId, attemptId, exitCode, onConfirmed }) {
    const key = `${occurrenceId}\u0000${attemptId}`;
    const exit = { occurrenceId, attemptId, exitCode, onConfirmed };
    deferredSentinelExits.set(key, exit);
    if (leaseId === undefined) return;
    void sendSentinelLifecycle("sentinel_exited", exit).then((state) => {
      if (state.state === "exited" || state.state === "failed") {
        deferredSentinelExits.delete(key);
        onConfirmed?.(state);
      }
    }).catch(() => {
      // Keep the outcome until the recipient reconnects with an active lease.
    });
  }

  function flushSentinelExits() {
    for (const exit of deferredSentinelExits.values()) queueSentinelExit(exit);
  }

  function sendSentinelLifecycle(type, {
    delivery,
    occurrenceId,
    attemptId,
    childIdentity,
    exitCode,
  } = {}) {
    if (!["sentinel_claim", "sentinel_launching", "sentinel_spawn_failed", "sentinel_launched", "sentinel_exited"].includes(type)
      || typeof occurrenceId !== "string"
      || typeof attemptId !== "string") {
      throw new TypeError("a valid Sentinel lifecycle frame is required");
    }
    if (type !== "sentinel_exited" && (!delivery || typeof delivery.messageId !== "string" || !Number.isSafeInteger(delivery.attemptNumber))) {
      throw new TypeError("an active Sentinel delivery is required");
    }
    return new Promise((resolve, reject) => {
      send({
        type,
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: leaseId,
        occurrence_id: occurrenceId,
        attempt_id: attemptId,
        ...(delivery === undefined ? {} : { message_id: delivery.messageId, attempt_number: delivery.attemptNumber }),
        ...(childIdentity === undefined ? {} : { child_identity: childIdentity }),
        ...(exitCode === undefined ? {} : { exit_code: exitCode }),
      }, { resolve, reject });
    });
  }

  function receipt(type, delivery, reason) {
    if (!delivery || typeof delivery.messageId !== "string"
      || !Number.isSafeInteger(delivery.attemptNumber) || delivery.attemptNumber <= 0) {
      throw new TypeError("a valid delivery receipt is required");
    }
    if (!["injection_requested", "ack", "nack"].includes(type)) {
      throw new TypeError("unsupported mailbox delivery receipt");
    }
    send({
      type,
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      message_id: delivery.messageId,
      attempt_number: delivery.attemptNumber,
      ...(type === "nack" ? { reason } : {}),
    });
  }

  function clearConnectionTimers() {
    clearInterval(renewTimer);
    renewTimer = undefined;
    clearTimeoutImpl(reconnectTimer);
    reconnectTimer = undefined;
  }

  function reportKickstartFailure(error) {
    if (stopped) return;
    log(`MECHA_V2_FRAME kickstart_failed agent=${agentId} error=${error.message}`);
    try {
      onKickstartFailure(error);
    } catch {
      // Failure diagnostics cannot change reconnect behavior.
    }
  }

  function scheduleReconnect({ kickstartRouter = false } = {}) {
    if (stopped || reconnectTimer !== undefined || socket !== undefined) return;
    if (kickstartRouter) {
      try {
        Promise.resolve(kickstart()).catch(reportKickstartFailure);
      } catch (error) {
        reportKickstartFailure(error);
      }
    }
    const cap = Math.min(
      reconnectMaxDelayMs,
      reconnectInitialDelayMs * (2 ** Math.min(reconnectAttempt, 30)),
    );
    const jitter = Math.max(0, Math.min(1, Number(random()) || 0));
    const delay = Math.max(1, Math.floor(cap * (0.5 + (jitter / 2))));
    reconnectAttempt += 1;
    report("reconnecting");
    reconnectTimer = setTimeoutImpl(() => {
      reconnectTimer = undefined;
      openSocket();
    }, delay);
  }

  function disconnected(currentSocket, error, { kickstartRouter = false } = {}) {
    if (socket !== currentSocket) return;
    clearInterval(renewTimer);
    renewTimer = undefined;
    socket = undefined;
    leaseId = undefined;
    rejectPendingSentinelStates(error ?? new Error("Mailbox v2 socket disconnected"));
    if (stopped) return;
    report("disconnected", error);
    scheduleReconnect({ kickstartRouter });
  }

  function closeSocket(currentSocket) {
    if (currentSocket.readyState === (WebSocket?.OPEN ?? 1)
      || currentSocket.readyState === (WebSocket?.CONNECTING ?? 0)) {
      try {
        currentSocket.close();
      } catch {
        // The reconnect path is already active.
      }
    }
  }

  async function receive(frame) {
    if (frame.type === "registered" && frame.agent_id === agentId && frame.instance_id === instanceId) {
      leaseId = frame.lease_id;
      reconnectAttempt = 0;
      log(`MECHA_V2_FRAME registered agent=${agentId} lease=${leaseId}`);
      if (renewTimer === undefined) {
        renewTimer = setInterval(() => {
          try {
            send({ type: "lease_renew", agent_id: agentId, instance_id: instanceId, lease_id: leaseId });
          } catch (error) {
            log(`MECHA_V2_FRAME lease_renew_failed agent=${agentId} error=${error.message}`);
          }
        }, renewIntervalMs);
      }
      if (resolveInitialConnection !== undefined) {
        resolveInitialConnection();
        resolveInitialConnection = undefined;
        rejectInitialConnection = undefined;
      }
      flushSentinelExits();
      report("connected");
      return;
    }
    if (frame.type === "sentinel_state") {
      const pending = pendingSentinelStates.get(frame.request_id);
      if (pending !== undefined) {
        pendingSentinelStates.delete(frame.request_id);
        pending.resolve(frame);
      }
      return;
    }
    if (frame.type !== "message") {
      if (frame.type === "error" || frame.type === "lease_rejected") {
        const pending = pendingSentinelStates.get(frame.request_id);
        if (pending !== undefined) {
          pendingSentinelStates.delete(frame.request_id);
          pending.reject(new Error(`Mailbox v2 Sentinel lifecycle rejected (${frame.code ?? "unknown"})`));
        }
        log(`MECHA_V2_FRAME ${frame.type} agent=${agentId} code=${frame.code ?? "unknown"}`);
      }
      return;
    }
    const delivery = { messageId: frame.message.id, attemptNumber: frame.attempt_number };
    try {
      await onEnvelope(frame.message, delivery);
    } catch (error) {
      try {
        receipt("nack", delivery, "injection_failed");
      } catch {
        // A lost connection leaves the lease for router timeout/replay.
      }
      log(`MECHA_V2_FRAME injection_failed agent=${agentId} message=${frame.message.id} error=${error.message}`);
    }
  }

  function openSocket() {
    if (stopped || socket !== undefined) return;
    let currentSocket;
    let opened = false;
    try {
      currentSocket = webSocketFactory(routerWebSocketUrl(routerUrl));
      socket = currentSocket;
      report("connecting");
    } catch (error) {
      report("disconnected", error);
      scheduleReconnect({ kickstartRouter: connectionRefused(error) });
      return;
    }
    currentSocket.addEventListener("open", () => {
      if (socket !== currentSocket || stopped) return;
      opened = true;
      try {
        send({ type: "register", agent_id: agentId, instance_id: instanceId });
      } catch (error) {
        disconnected(currentSocket, error);
        closeSocket(currentSocket);
      }
    });
    currentSocket.addEventListener("message", async (payload) => {
      if (socket !== currentSocket || stopped) return;
      let frame;
      try {
        frame = JSON.parse(typeof payload.data === "string"
          ? payload.data
          : Buffer.from(payload.data).toString("utf8"));
      } catch {
        const error = new Error("Mailbox v2 router sent invalid JSON");
        disconnected(currentSocket, error);
        closeSocket(currentSocket);
        return;
      }
      await receive(frame);
      if (frame.type === "error" || frame.type === "lease_rejected") {
        const error = new Error(`Mailbox v2 registration rejected (${frame.code ?? "unknown"})`);
        disconnected(currentSocket, error);
        closeSocket(currentSocket);
      }
    });
    currentSocket.addEventListener("error", (event) => {
      const error = event.error ?? new Error("Mailbox v2 socket error");
      disconnected(currentSocket, error, {
        kickstartRouter: !opened && connectionRefused(error),
      });
      closeSocket(currentSocket);
    }, { once: true });
    currentSocket.addEventListener("close", () => disconnected(currentSocket));
  }

  return {
    instanceId,
    connect() {
      if (initialConnection === undefined) {
        initialConnection = new Promise((resolve, reject) => {
          resolveInitialConnection = resolve;
          rejectInitialConnection = reject;
        });
      }
      if (!stopped) openSocket();
      return initialConnection;
    },
    sendReceipt: receipt,
    sendSentinelLifecycle,
    queueSentinelExit,
    async sendReply({ to, body, subject = "Pi mailbox reply" }) {
      if (typeof to !== "string" || to.length === 0 || to.includes("@")) throw new Error("a mailbox recipient is required");
      if (typeof body !== "string" || body.length === 0) throw new Error("a reply body is required");
      const response = await fetchImpl(`${routerHttpUrl(routerUrl)}/v1/messages`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        signal: AbortSignal.timeout(DEFAULT_REPLY_TIMEOUT_MS),
        body: JSON.stringify({
          id: generateId(),
          from: agentId,
          to,
          subject,
          body,
          created_at: new Date().toISOString(),
        }),
      });
      const payload = await response.json();
      if (!response.ok || payload?.message?.state === undefined) {
        throw new Error(`Mailbox v2 router rejected reply (${payload?.error?.code ?? response.status})`);
      }
      log(`MECHA_V2_REPLY accepted from=${agentId} to=${to} message=${payload.message.id}`);
      return payload.message;
    },
    close() {
      if (stopped) return;
      stopped = true;
      clearConnectionTimers();
      const currentSocket = socket;
      socket = undefined;
      leaseId = undefined;
      rejectPendingSentinelStates(new Error("Mailbox v2 client closed"));
      deferredSentinelExits.clear();
      if (rejectInitialConnection !== undefined) {
        rejectInitialConnection(new Error("Mailbox v2 client closed before connecting"));
        resolveInitialConnection = undefined;
        rejectInitialConnection = undefined;
      }
      if (currentSocket !== undefined) closeSocket(currentSocket);
    },
  };
}

function isMailboxSessionEntry(entry, messageId) {
  return entry?.type === "custom_message"
    && entry.customType === MAILBOX_MESSAGE_TYPE
    && entry.details?.message_id === messageId;
}

function isSentinelStatusEntry(entry, { messageId, occurrenceId, attemptId, state }) {
  return entry?.type === "custom_message"
    && entry.customType === "mecha-sentinel-status"
    && entry.details?.message_id === messageId
    && entry.details?.occurrence_id === occurrenceId
    && entry.details?.attempt_id === attemptId
    && entry.details?.state === state;
}

function readPersistedSessionEntries(sessionFile, readSessionFile) {
  const source = readSessionFile(sessionFile, "utf8");
  return source.split("\n")
    .filter((line) => line.length > 0)
    .map((line) => JSON.parse(line));
}

export function mailboxSessionDeliveryState(sessionManager, messageId, {
  readSessionFile = readFileSync,
  onPersistenceError = () => {},
} = {}) {
  const branch = sessionManager.getBranch();
  if (!branch.some((entry) => isMailboxSessionEntry(entry, messageId))) return "absent";
  if (!sessionManager.isPersisted()) return "history_unverified";
  const sessionFile = sessionManager.getSessionFile();
  if (typeof sessionFile !== "string" || sessionFile.length === 0) return "history_unverified";
  try {
    return readPersistedSessionEntries(sessionFile, readSessionFile)
      .some((entry) => isMailboxSessionEntry(entry, messageId))
      ? "durable"
      : "history_unverified";
  } catch (error) {
    if (error?.code !== "ENOENT") onPersistenceError(error);
    return "history_unverified";
  }
}

function mailboxCustomMessage(message) {
  return {
    customType: MAILBOX_MESSAGE_TYPE,
    content: `Mailbox message from ${message.from}\n\n${message.body}`,
    display: true,
    details: { message_id: message.id, from: message.from, to: message.to },
  };
}

export function createPiMailboxDeliveryCoordinator({
  pi,
  sessionContext,
  client,
  defer = setImmediate,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
  checkpointRetryDelayMs = DEFAULT_SESSION_CHECKPOINT_RETRY_MS,
  readSessionFile = readFileSync,
  launchChild = launchSentinelChild,
  sentinelReceiverPolicies = {},
} = {}) {
  if (pi === undefined || typeof pi.sendMessage !== "function") {
    throw new TypeError("pi.sendMessage is required");
  }
  if (sessionContext?.sessionManager === undefined) {
    throw new TypeError("a Pi session context is required");
  }
  if (client === undefined || typeof client.sendReceipt !== "function") {
    throw new TypeError("a mailbox client with sendReceipt is required");
  }
  if (typeof defer !== "function") throw new TypeError("defer must be a function");
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("session checkpoint timers are required");
  }
  if (!Number.isSafeInteger(checkpointRetryDelayMs) || checkpointRetryDelayMs <= 0) {
    throw new TypeError("checkpointRetryDelayMs must be a positive safe integer");
  }
  if (typeof readSessionFile !== "function") throw new TypeError("readSessionFile must be a function");
  if (typeof launchChild !== "function") throw new TypeError("launchChild must be a function");
  if (sentinelReceiverPolicies === null || typeof sentinelReceiverPolicies !== "object" || Array.isArray(sentinelReceiverPolicies)) {
    throw new TypeError("Sentinel receiver policies must be an object");
  }

  const sessionId = sessionContext.sessionManager.getSessionId();
  const pending = new Map();
  const sentinelInFlight = new Map();
  const reportedPersistenceErrors = new Set();
  let active = true;

  function receipt(type, delivery, reason) {
    client.sendReceipt(type, delivery, reason);
  }

  function persistenceUnavailable(context) {
    context.ui?.setStatus("mecha-mailbox", "mailbox: waiting for durable session checkpoint");
  }

  function sessionDeliveryState(context, messageId) {
    return mailboxSessionDeliveryState(context.sessionManager, messageId, {
      readSessionFile,
      onPersistenceError: (error) => {
        if (reportedPersistenceErrors.has(messageId)) return;
        reportedPersistenceErrors.add(messageId);
        context.ui?.notify(`Mailbox v2 session checkpoint unreadable: ${error.message}`, "error");
      },
    });
  }

  function confirmExistingDelivery(delivery, context) {
    const state = sessionDeliveryState(context, delivery.messageId);
    if (state !== "durable") {
      persistenceUnavailable(context);
      return false;
    }
    try {
      receipt("injection_requested", delivery);
      receipt("ack", delivery);
      context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      return true;
    } catch {
      // The persisted session entry makes the next router replay safe to acknowledge.
      return false;
    }
  }

  function clearCheckpointTimer(pendingDelivery) {
    if (pendingDelivery.checkpointTimer !== undefined) {
      clearTimeoutImpl(pendingDelivery.checkpointTimer);
      pendingDelivery.checkpointTimer = undefined;
    }
  }

  function checkDurableCheckpoint(messageId, context) {
    if (!active || context.sessionManager.getSessionId() !== sessionId) return;
    const latest = pending.get(messageId);
    if (latest === undefined) return;
    const state = sessionDeliveryState(context, messageId);
    if (state === "durable") {
      clearCheckpointTimer(latest);
      pending.delete(messageId);
      reportedPersistenceErrors.delete(messageId);
      try {
        receipt("ack", latest.delivery);
        context.ui?.setStatus("mecha-mailbox", "mailbox: connected");
      } catch {
        // The persisted session entry makes the next router replay safe to acknowledge.
      }
      return;
    }
    persistenceUnavailable(context);
    if (latest.checkpointTimer !== undefined) return;
    latest.checkpointTimer = setTimeoutImpl(() => {
      latest.checkpointTimer = undefined;
      checkDurableCheckpoint(messageId, context);
    }, checkpointRetryDelayMs);
  }

  function recordSentinelStatus(message, state, lifecycle = {}) {
    const status = {
      messageId: message.id,
      occurrenceId: lifecycle.occurrenceId,
      attemptId: lifecycle.attemptId,
      transitionSequence: lifecycle.transitionSequence ?? state.transition_sequence,
      state: state.state,
    };
    pi.sendMessage({
      customType: "mecha-sentinel-status",
      content: `[Sentinel ${message.id}] ${state.state}${state.outcome === undefined ? "" : ` (${state.outcome})`}`,
      display: true,
      details: {
        message_id: message.id,
        state: state.state,
        ...(state.outcome === undefined ? {} : { outcome: state.outcome }),
        ...(lifecycle.occurrenceId === undefined ? {} : { occurrence_id: lifecycle.occurrenceId }),
        ...(lifecycle.attemptId === undefined ? {} : { attempt_id: lifecycle.attemptId }),
        ...(status.transitionSequence === undefined ? {} : { transition_sequence: status.transitionSequence }),
        ...(lifecycle.childIdentity === undefined ? {} : { child_identity: lifecycle.childIdentity }),
        ...(lifecycle.exitCode === undefined ? {} : { exit_code: lifecycle.exitCode }),
      },
    }, { deliverAs: "followUp", triggerTurn: false });
    if (typeof sessionContext.sessionManager.isPersisted !== "function") return;
    if (!sessionContext.sessionManager.isPersisted()) throw new Error("Sentinel parent lifecycle status is not durable");
    const sessionFile = sessionContext.sessionManager.getSessionFile?.();
    if (typeof sessionFile !== "string" || !sessionContext.sessionManager.getBranch().some((entry) => isSentinelStatusEntry(entry, status))) {
      throw new Error("Sentinel parent lifecycle status is not durable");
    }
    try {
      if (!readPersistedSessionEntries(sessionFile, readSessionFile).some((entry) => isSentinelStatusEntry(entry, status))) {
        throw new Error("Sentinel parent lifecycle status is not durable");
      }
    } catch (error) {
      if (error?.message === "Sentinel parent lifecycle status is not durable") throw error;
      throw new Error("Sentinel parent lifecycle status is not durable");
    }
  }

  async function onSentinelEnvelope(message, delivery) {
    const profile = sentinelExecutionProfile(message, sentinelReceiverPolicies);
    if (profile === undefined) {
      recordSentinelStatus(message, { state: "rejected_profile" });
      return;
    }
    if (typeof client.sendSentinelLifecycle !== "function") throw new Error("Mailbox v2 client cannot report Sentinel lifecycle");
    const occurrenceId = message.id;
    const attemptId = String(delivery.attemptNumber);
    const lifecycleIdentity = { occurrenceId, attemptId };
    let state = await client.sendSentinelLifecycle("sentinel_claim", { delivery, occurrenceId, attemptId });
    let transitionSequence = state.transition_sequence;
    recordSentinelStatus(message, state, lifecycleIdentity);
    if (state.state !== "claimed") return;
    state = await client.sendSentinelLifecycle("sentinel_launching", { delivery, occurrenceId, attemptId });
    transitionSequence = state.transition_sequence;
    recordSentinelStatus(message, state, lifecycleIdentity);
    if (state.state !== "launching") return;
    let releaseResultSend;
    let resultSendEnabled = false;
    let relayToken;
    const resultSendReady = new Promise((resolve) => { releaseResultSend = resolve; });
    let child;
    try {
      child = await launchChild({
        message,
        supervisor: profile.supervisor,
        authPath: profile.auth_path,
        markResultSendAttempt: async () => {
          await resultSendReady;
          if (!resultSendEnabled || typeof relayToken !== "string") throw new Error("Sentinel launch was not durably confirmed");
          recordSentinelStatus(message, { state: "result_send_attempted" }, { ...lifecycleIdentity, transitionSequence, childIdentity: child?.childIdentity });
          return relayToken;
        },
        onCleanupError: () => recordSentinelStatus(message, { state: "cleanup_failed" }, lifecycleIdentity),
      });
    } catch {
      await client.sendSentinelLifecycle("sentinel_spawn_failed", { delivery, occurrenceId, attemptId });
      return;
    }
    recordSentinelStatus(message, { state: "spawned_pending" }, { ...lifecycleIdentity, childIdentity: child.childIdentity });
    let exitCode;
    let exitReported = false;
    const reportExit = () => {
      if (exitCode === undefined || exitReported) return;
      exitReported = true;
      recordSentinelStatus(message, { state: "exited_pending" }, { ...lifecycleIdentity, transitionSequence, exitCode });
      if (typeof client.queueSentinelExit === "function") {
        client.queueSentinelExit({
          occurrenceId, attemptId, exitCode,
          onConfirmed: (confirmed) => recordSentinelStatus(message, confirmed, { ...lifecycleIdentity, exitCode }),
        });
      } else {
        void client.sendSentinelLifecycle("sentinel_exited", { occurrenceId, attemptId, exitCode });
      }
    };
    let launchSettled = false;
    child.onExit((code) => {
      exitCode = code;
      if (launchSettled) reportExit();
    });
    try {
      state = await client.sendSentinelLifecycle("sentinel_launched", {
        delivery,
        occurrenceId,
        attemptId,
        childIdentity: child.childIdentity,
      });
      transitionSequence = state.transition_sequence;
      relayToken = state.relay_token;
      recordSentinelStatus(message, state, { ...lifecycleIdentity, childIdentity: child.childIdentity });
      resultSendEnabled = typeof relayToken === "string";
    } catch {
      // The child exists but its launch acknowledgement is uncertain; preserve reconciliation data locally.
      recordSentinelStatus(message, { state: "post_spawn_uncertain" }, { ...lifecycleIdentity, childIdentity: child.childIdentity });
    } finally {
      releaseResultSend();
      launchSettled = true;
      reportExit();
    }
  }

  function onEnvelope(message, delivery) {
    if (!active) return undefined;
    if (message.sentinel !== undefined && message.sentinel.sentinel_child === undefined) {
      const existing = sentinelInFlight.get(message.id);
      if (existing !== undefined) return existing;
      const lifecycle = onSentinelEnvelope(message, delivery).finally(() => sentinelInFlight.delete(message.id));
      sentinelInFlight.set(message.id, lifecycle);
      return lifecycle;
    }
    const currentContext = sessionContext;
    const state = sessionDeliveryState(currentContext, message.id);
    if (state === "durable") {
      confirmExistingDelivery(delivery, currentContext);
      return;
    }

    const existing = pending.get(message.id);
    if (existing !== undefined) {
      existing.delivery = delivery;
      try {
        receipt("injection_requested", delivery);
      } catch {
        // The original Pi injection remains pending; do not NACK it as a failed injection.
      }
      return;
    }
    if (state === "history_unverified") {
      persistenceUnavailable(currentContext);
      return;
    }

    pending.set(message.id, { delivery, checkpointTimer: undefined });
    try {
      routeMailboxEnvelope(message, (envelope) => {
        pi.sendMessage(mailboxCustomMessage(envelope), { deliverAs: "steer", triggerTurn: true });
      });
    } catch {
      pending.delete(message.id);
      try {
        receipt("nack", delivery, "injection_failed");
      } catch {
        // The router's lease timeout preserves retry when the socket is gone.
      }
      return;
    }
    // Keep this pending on a receipt-send failure: Pi may have queued the message already.
    try {
      receipt("injection_requested", delivery);
    } catch {
      // The router lease will time out or the reconnecting client will receive a replay.
    }
  }

  function onMessageEnd(event, context) {
    const messageId = event.message?.details?.message_id;
    if (event.message?.role !== "custom" || event.message.customType !== MAILBOX_MESSAGE_TYPE
      || typeof messageId !== "string") {
      return;
    }
    if (context.sessionManager.getSessionId() !== sessionId) return;
    const pendingDelivery = pending.get(messageId);
    if (pendingDelivery === undefined) return;

    defer(() => checkDurableCheckpoint(messageId, context));
  }

  return {
    onEnvelope,
    onMessageEnd,
    recordSentinelExitConfirmed({ messageId, occurrenceId, attemptId, exitCode }, confirmed) {
      recordSentinelStatus({ id: messageId }, confirmed, { occurrenceId, attemptId, exitCode });
    },
    close() {
      active = false;
      for (const pendingDelivery of pending.values()) clearCheckpointTimer(pendingDelivery);
      pending.clear();
      reportedPersistenceErrors.clear();
    },
  };
}

export function createMechaMessagingExtension(pi, {
  resolveCurrentAgentId = resolveAgentId,
  createClient = createMailboxClient,
  sentinelReceiverPolicies,
  loadReceiverPolicies = loadSentinelReceiverPolicies,
} = {}) {
  let client;
  let coordinator;
  let startupError;
  const children = childSessionIds();

  pi.events.on("subagents:child:session-created", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.add(sessionId);
  });
  pi.events.on("subagents:child:disposed", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.delete(sessionId);
  });

  pi.on("message_end", (event, context) => coordinator?.onMessageEnd(event, context));

  pi.on("session_start", (_event, context) => {
    if (children.has(context.sessionManager.getSessionId())) return;
    try {
      const agentId = resolveCurrentAgentId();
      if (agentId === undefined) return;
      pi.registerCommand("mecha-reply", {
        description: "Send a Mailbox v2 reply: /mecha-reply <recipient> <message>",
        handler: async (argumentsText, commandContext) => {
          try {
            if (startupError !== undefined) throw startupError;
            if (client === undefined) throw new Error("Mailbox v2 is not initialized");
            const { recipient, body } = parseReplyArguments(argumentsText);
            const message = await client.sendReply({ to: recipient, body });
            commandContext.ui.notify(`Mailbox v2 reply accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(error.message, "error");
          }
        },
      });
      let mailboxClient;
      mailboxClient = createClient({
        agentId,
        onEnvelope: (message, delivery) => coordinator?.onEnvelope(message, delivery),
        kickstart: async () => {
          if (typeof process.getuid !== "function") return;
          const result = await pi.exec(
            "launchctl",
            ["kickstart", `gui/${process.getuid()}/${process.env.MECHA_ROUTER_LAUNCHD_LABEL ?? "life.meyers.mecha-router"}`],
            { timeout: 5_000 },
          );
          if (result.code !== 0) {
            throw new Error(result.stderr.trim() || `launchctl exited ${result.code}`);
          }
        },
        onKickstartFailure: (error) => {
          context.ui.setStatus("mecha-mailbox", "mailbox: router start failed; retrying");
          context.ui.notify(`Mailbox v2 router kickstart failed: ${error.message}`, "error");
        },
        onConnectionState: ({ state, error }) => {
          if (client !== mailboxClient) return;
          if (state === "connected") {
            context.ui.setStatus("mecha-mailbox", "mailbox: connected");
          } else if (state === "connecting" || state === "reconnecting") {
            context.ui.setStatus("mecha-mailbox", "mailbox: reconnecting");
          } else if (state === "disconnected") {
            context.ui.setStatus("mecha-mailbox", "mailbox: disconnected");
            if (error !== undefined) context.ui.notify(`Mailbox v2 reconnecting: ${error.message}`, "error");
          }
        },
        log: () => {},
      });
      client = mailboxClient;
      let localReceiverPolicies = sentinelReceiverPolicies;
      if (localReceiverPolicies === undefined) {
        try {
          localReceiverPolicies = loadReceiverPolicies();
        } catch (error) {
          localReceiverPolicies = {};
          context.ui.notify(`Mailbox v2 Sentinel policy unavailable: ${error.message}`, "error");
        }
      }
      coordinator = createPiMailboxDeliveryCoordinator({
        pi,
        sessionContext: context,
        client: mailboxClient,
        sentinelReceiverPolicies: localReceiverPolicies,
      });
      recoverSentinelExits(context.sessionManager, mailboxClient, (exit, confirmed) => {
        coordinator?.recordSentinelExitConfirmed(exit, confirmed);
      });
      void mailboxClient.connect().catch((error) => {
        if (client === mailboxClient) context.ui.notify(`Mailbox v2 unavailable: ${error.message}`, "error");
      });
    } catch (error) {
      startupError = error;
      context.ui.notify(`Mailbox v2 unavailable: ${error.message}`, "error");
    }
  });

  pi.on("session_shutdown", (_event, context) => {
    if (children.has(context.sessionManager.getSessionId())) return;
    coordinator?.close();
    coordinator = undefined;
    client?.close();
    client = undefined;
    context.ui.setStatus("mecha-mailbox", undefined);
  });
}

export default function mechaMessagingExtension(pi) {
  return createMechaMessagingExtension(pi);
}
