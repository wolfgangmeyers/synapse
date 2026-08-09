import { randomBytes } from "node:crypto";

const WebSocket = globalThis.WebSocket;

const NANOID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function createNanoId() {
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
const DEFAULT_REPLY_TIMEOUT_MS = 5_000;
const MAX_SUBAGENTS = 999;
const MESSAGE_ARRIVAL_PREVIEW_CHARS = 140;

export function assertAgentId(agentId) {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 requires a valid current tmux window name");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(agentId)) {
    throw new Error("Mailbox v2 agent identity is invalid");
  }
  return agentId;
}

export function boundedConversationText(content, maximum = 4096) {
  const blocks = Array.isArray(content) ? content : [content];
  const text = blocks.map((block) => typeof block === "string" ? block : block?.type === "text" && typeof block.text === "string" ? block.text : "").filter(Boolean).join("\n");
  return text.slice(-maximum);
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

function normalizeSubagentSummary(summary) {
  if (summary === undefined || summary === null || typeof summary !== "object") return undefined;
  const id = typeof summary.id === "string" ? summary.id.trim().slice(0, 128) : "";
  const description = typeof summary.description === "string" ? summary.description.trim().slice(0, 512) : "";
  if (id.length === 0 || description.length === 0) return undefined;
  const type = typeof summary.type === "string" ? summary.type.trim().slice(0, 64) : "";
  return type.length > 0 ? { id, type, description } : { id, description };
}

function normalizeSubagents(subagents) {
  if (!Array.isArray(subagents)) return [];
  return subagents.map(normalizeSubagentSummary).filter(Boolean).slice(0, MAX_SUBAGENTS);
}

function arrivalPreview(value) {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  if (text.length <= MESSAGE_ARRIVAL_PREVIEW_CHARS) return text;
  let previewChars = MESSAGE_ARRIVAL_PREVIEW_CHARS - 1;
  const lastCodeUnit = text.charCodeAt(previewChars - 1);
  if (lastCodeUnit >= 0xd800 && lastCodeUnit <= 0xdbff) previewChars -= 1;
  return `${text.slice(0, previewChars)}…`;
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
  let paused = false;
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

  function activity(activityState, { subagentCount = 0, subagents } = {}) {
    if (!["active", "settled", "compacting"].includes(activityState)) {
      throw new TypeError("unsupported mailbox activity state");
    }
    const summaries = normalizeSubagents(subagents);
    const count = Math.max(summaries.length, subagentCount);
    if (!Number.isSafeInteger(count) || count < 0) {
      throw new TypeError("subagentCount must be a non-negative safe integer");
    }
    const clampedCount = Math.min(count, MAX_SUBAGENTS);
    send({
      type: "activity",
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      activity_state: activityState,
      subagent_count: clampedCount,
      ...(summaries.length > 0 || clampedCount === 0 ? { subagents: summaries } : {}),
    });
  }

  function messageArrival(message) {
    const sourceKind = message?.source?.kind;
    if (!message || typeof message.id !== "string" || typeof message.from !== "string" || typeof message.to !== "string"
      || !["agent", "discord", "reminder"].includes(sourceKind) || message.from === agentId) {
      return;
    }
    send({
      type: "message_arrival",
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: leaseId,
      from: message.from,
      ...(typeof message.subject === "string" ? { subject: message.subject } : {}),
      source: { kind: sourceKind },
      preview: arrivalPreview(message.body),
      body: String(message.body ?? ""),
      created_at: message.created_at,
      message_id: message.id,
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
    if (reconnectTimer !== undefined) clearTimeoutImpl(reconnectTimer);
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
    if (stopped || paused || reconnectTimer !== undefined || socket !== undefined) return;
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
    if (paused) return;
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
    if (stopped || paused || socket !== undefined) return;
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
    sendActivity: activity,
    sendMessageArrival: messageArrival,
    pause() {
      if (stopped || paused) return;
      paused = true;
      clearConnectionTimers();
      const currentSocket = socket;
      socket = undefined;
      leaseId = undefined;
      if (currentSocket !== undefined) closeSocket(currentSocket);
    },
    resume() {
      if (stopped || !paused) return initialConnection ?? Promise.resolve();
      paused = false;
      openSocket();
      return initialConnection ?? Promise.resolve();
    },
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
          source: { kind: "agent" },
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
