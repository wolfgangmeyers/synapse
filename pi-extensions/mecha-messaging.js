import { randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

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
const MAILBOX_MESSAGE_TYPE = "mecha-agent-message";
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const PI_SUBAGENTS_SERVICE = Symbol.for("@gotgenes/pi-subagents:service");

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

export function transientChildAgentId(sessionId, generateId = createNanoId) {
  if (typeof sessionId !== "string" || sessionId.length === 0 || typeof generateId !== "function") {
    throw new TypeError("a child session ID and ID generator are required");
  }
  return `subagent-${generateId()}`;
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

  function report(state, error) {
    try {
      onConnectionState({ state, error });
    } catch {
      // Connection diagnostics must not alter delivery behavior.
    }
  }

  function send(fields) {
    if (!socketOpen(socket)) throw new Error("Mailbox v2 socket is not connected");
    const frameId = generateId();
    socket.send(JSON.stringify({ version: "v1", frame_id: frameId, ...fields }));
    return frameId;
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
      report("connected");
      return;
    }
    if (frame.type !== "message") {
      if (frame.type === "error" || frame.type === "lease_rejected") {
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

export async function spawnNativeSentinel(message) {
  // The native package documents this global symbol as its cross-extension service channel.
  const service = globalThis[PI_SUBAGENTS_SERVICE];
  if (service === undefined || typeof service.spawn !== "function") {
    throw new Error("Pi native subagent service is unavailable");
  }
  return service.spawn("general-purpose", message.body, {
    description: "Scheduled Sentinel work",
    model: message.sentinel.model,
    inheritContext: true,
  });
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
  spawnSubagent = spawnNativeSentinel,
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
  if (typeof spawnSubagent !== "function") throw new TypeError("spawnSubagent must be a function");

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

  function recordSentinelStatus(message, subagentId) {
    // ponytail: this status deliberately contains no scheduled prompt or derived text.
    pi.sendMessage({
      customType: "mecha-sentinel-status",
      content: "Sentinel work handed off to a native subagent.",
      display: false,
      details: { message_id: message.id, subagent_id: subagentId, state: "handed_off" },
    }, { deliverAs: "followUp", triggerTurn: false });
  }

  async function onSentinelEnvelope(message, delivery) {
    const existing = sentinelInFlight.get(message.id);
    if (existing !== undefined) return existing;
    const handoff = (async () => {
      let subagentId;
      try {
        subagentId = await spawnSubagent(message);
      } catch {
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      if (typeof subagentId !== "string" || subagentId.length === 0) {
        try { receipt("nack", delivery, "injection_failed"); } catch { /* normal delivery timeout preserves replay */ }
        return;
      }
      try { recordSentinelStatus(message, subagentId); } catch { /* a local status must not duplicate a handed-off child */ }
      try {
        receipt("injection_requested", delivery);
        receipt("ack", delivery);
      } catch {
        // The native child already exists. The normal router delivery lifecycle decides any replay.
      }
    })();
    sentinelInFlight.set(message.id, handoff);
    return handoff;
  }

  function onEnvelope(message, delivery) {
    if (!active) return undefined;
    if (message.sentinel !== undefined) return onSentinelEnvelope(message, delivery);
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

    close() {
      active = false;
      for (const pendingDelivery of pending.values()) clearCheckpointTimer(pendingDelivery);
      pending.clear();
      sentinelInFlight.clear();
      reportedPersistenceErrors.clear();
    },
  };
}

export function createMechaMessagingExtension(pi, {
  resolveCurrentAgentId = resolveAgentId,
  createClient = createMailboxClient,
} = {}) {
  let client;
  let coordinator;
  let startupError;
  const children = childSessionIds();
  const childReplyClients = new Map();

  pi.registerTool({
    name: "mecha_parent_message",
    label: "Message parent",
    description: "Child-only: send an ordinary Mailbox message to this native subagent's target parent.",
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["message"],
      properties: { message: { type: "string", minLength: 1 } },
    },
    async execute(_toolCallId, { message }, _signal, _onUpdate, toolContext) {
      const childClient = childReplyClients.get(toolContext.sessionManager.getSessionId());
      const parentAgentId = resolveCurrentAgentId();
      if (childClient === undefined || parentAgentId === undefined || typeof message !== "string" || message.length === 0) {
        return { content: [{ type: "text", text: "Parent message is unavailable in this session." }], isError: true };
      }
      try {
        const sent = await childClient.sendReply({ to: parentAgentId, body: message, subject: "Pi subagent message" });
        return { content: [{ type: "text", text: "Parent message accepted." }], details: { message_id: sent.id } };
      } catch {
        return { content: [{ type: "text", text: "Parent message was not accepted." }], isError: true };
      }
    },
  });

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
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      try {
        const parentAgentId = resolveCurrentAgentId();
        if (parentAgentId === undefined) return;
        childReplyClients.set(sessionId, createClient({
          agentId: transientChildAgentId(sessionId),
          onEnvelope: () => {},
        }));
      } catch {
        // A child without ordinary Mailbox send capability can still finish natively.
      }
      return;
    }
    try {
      const agentId = resolveCurrentAgentId();
      if (agentId === undefined) return;
      pi.registerCommand("mecha-parent-message", {
        description: "Child-only ordinary Mailbox message to its target parent: /mecha-parent-message <message>",
        handler: async (argumentsText, commandContext) => {
          try {
            const childClient = childReplyClients.get(commandContext.sessionManager.getSessionId());
            const parentAgentId = resolveCurrentAgentId();
            const body = argumentsText.trim();
            if (childClient === undefined || parentAgentId === undefined || body.length === 0) {
              throw new Error("This command is available only to a child with a parent message");
            }
            const message = await childClient.sendReply({ to: parentAgentId, body, subject: "Pi subagent message" });
            commandContext.ui.notify(`Mailbox v2 child message accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(error.message, "error");
          }
        },
      });
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
      coordinator = createPiMailboxDeliveryCoordinator({
        pi,
        sessionContext: context,
        client: mailboxClient,
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
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      childReplyClients.delete(sessionId);
      return;
    }
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
