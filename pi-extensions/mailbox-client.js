import { randomBytes, randomUUID } from "node:crypto";

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
const DEFAULT_CAPABILITY_TIMEOUT_MS = 2_000;
const MAX_SUBAGENTS = 999;
const LIFECYCLE_JOURNAL_MAX_ITEMS = 256;
const LIFECYCLE_JOURNAL_MAX_BYTES = 262_144;
const LIFECYCLE_JOURNAL_MAX_ITEM_BYTES = 65_536;
const LIFECYCLE_JOURNAL_MAX_AGE_MS = 10 * 60_000;
const SUBAGENT_STATUSES = new Set(["queued", "running", "completed", "steered", "aborted", "stopped", "error"]);
const MESSAGE_ARRIVAL_PREVIEW_CHARS = 140;

// This pattern is narrower than the protocol's agentId on purpose, and the difference is a dot.
// The protocol allows `A-Za-z0-9._-` (protocol/v1/schema.json, $defs.agentId); a tmux window name
// cannot contain a dot, so no mailbox agent ever has one. Self-registered Claude sessions, which
// exist to show presence in the router UI and are not mailbox addresses, take dotted ids for
// exactly that reason. Allowing a dot here merges the two id spaces: an ad hoc session could then
// register under a live worker's name, and this function is what stops such an id from ever
// reaching a mail-injecting sidecar. Widening it removes that guarantee silently.
export function assertAgentId(agentId) {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 requires a valid current tmux window name");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(agentId)) {
    throw new Error("Mailbox v2 agent identity is invalid");
  }
  return agentId;
}

// Self-registered Claude sessions exist to show presence in the router UI. They are not mailbox
// addresses and must never be able to become one, so this REQUIRES the dot that assertAgentId
// forbids. The two validators are complements: no string satisfies both, so a presence id cannot
// be handed to a mail-injecting sidecar and a worker id cannot be claimed by an ad hoc session.
// The pattern otherwise matches the protocol's agentId (protocol/v1/schema.json, $defs.agentId).
export function assertPresenceAgentId(agentId) {
  if (typeof agentId !== "string" || agentId.length === 0) {
    throw new Error("Mailbox v2 presence identity is required");
  }
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(agentId) || !agentId.includes(".")) {
    throw new Error("Mailbox v2 presence identity must be dotted and cannot be a worker id");
  }
  if (agentId.length > 128) throw new Error("Mailbox v2 presence identity is too long");
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
  const status = typeof summary.status === "string" ? summary.status : undefined;
  const normalized = { id, ...(type.length > 0 ? { type } : {}), description };
  return status !== undefined && SUBAGENT_STATUSES.has(status) ? { ...normalized, status } : normalized;
}

function normalizeSubagents(subagents) {
  if (!Array.isArray(subagents)) return [];
  return subagents.map(normalizeSubagentSummary).filter(Boolean).slice(0, MAX_SUBAGENTS);
}

function normalizeTerminalReason(reason) {
  const normalized = typeof reason === "string" ? reason.trim().toLowerCase() : "";
  return /^[a-z][a-z0-9_]{0,63}$/.test(normalized) ? normalized : "completed";
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
  // Which id space this client belongs to. Presence-only clients pass assertPresenceAgentId, which
  // accepts exactly the ids assertAgentId rejects.
  assertIdentity = assertAgentId,
  // Lifecycle identity is process-local. It intentionally survives socket/lease replacement,
  // but a new extension process receives a fresh instance, session, and epoch.
  parentSessionId = generateId(),
  producerEpoch = randomUUID(),
  capabilityTimeoutMs = DEFAULT_CAPABILITY_TIMEOUT_MS,
  now = Date.now,
  onLifecycleReady = () => {},
  onLifecycleDiagnostic = () => {},
  onLifecycleTrace = () => {},
} = {}) {
  assertIdentity(agentId);
  if (typeof onEnvelope !== "function") throw new TypeError("onEnvelope is required");
  if (typeof onConnectionState !== "function") throw new TypeError("onConnectionState must be a function");
  if (typeof onKickstartFailure !== "function") throw new TypeError("onKickstartFailure must be a function");
  if (typeof kickstart !== "function") throw new TypeError("kickstart must be a function");
  if (!Number.isSafeInteger(renewIntervalMs) || renewIntervalMs <= 0) {
    throw new TypeError("renewIntervalMs must be a positive safe integer");
  }
  if (typeof random !== "function") throw new TypeError("random must be a function");
  if (!Number.isSafeInteger(capabilityTimeoutMs) || capabilityTimeoutMs <= 0) throw new TypeError("capabilityTimeoutMs must be a positive safe integer");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  if (typeof onLifecycleReady !== "function" || typeof onLifecycleDiagnostic !== "function" || typeof onLifecycleTrace !== "function") throw new TypeError("lifecycle callbacks must be functions");
  if (typeof parentSessionId !== "string" || parentSessionId.length === 0 || typeof producerEpoch !== "string" || !/^[0-9a-fA-F-]{36}$/.test(producerEpoch)) throw new TypeError("valid lifecycle identity is required");
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
  let lifecycleSupported = false;
  let lifecycleReady = false;
  let lifecycleConnectionDisabled = false;
  let lifecycleSequence = 0;
  let lifecycleRouterVersion = 0;
  // The newest full assertion is retained separately from the retry journal. A reconnect must
  // lead with it, because a replacement lease is fenced until the router accepts a snapshot.
  let lifecycleSnapshotChildren = [];
  const lifecycleJournal = new Map();

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

  function lifecycleTrace(event, fields = {}) {
    try {
      onLifecycleTrace({ event, agent_id: agentId, instance_id: instanceId, parent_session_id: parentSessionId, ...fields });
    } catch {
      // Trace collection cannot alter lifecycle delivery.
    }
  }

  function lifecycleFrameTrace(frame, phase) {
    return {
      phase,
      frame_type: frame.type,
      event_id: frame.event_id,
      client_sequence: frame.client_sequence,
      snapshot_count: Array.isArray(frame.subagents) ? frame.subagents.length : undefined,
      subagent_count: frame.subagent === undefined ? undefined : 1,
      subagents: frame.subagents ?? (frame.subagent === undefined ? undefined : [frame.subagent]),
      pending: lifecycleJournal.size,
      bytes: serializedLifecycleBytes(frame),
    };
  }

  function lifecycleDiagnostic(reason) {
    lifecycleTrace("client_lifecycle_diagnostic", { reason, pending: lifecycleJournal.size, bytes: journalBytes() });
    try { onLifecycleDiagnostic({ reason, pending: lifecycleJournal.size }); } catch { /* diagnostics cannot alter lifecycle delivery */ }
  }

  function canonicalLifecycleFrame(fields) {
    return { version: "v1", frame_id: generateId(), ...fields };
  }

  function serializedLifecycleBytes(frame) {
    return Buffer.byteLength(JSON.stringify(frame), "utf8");
  }

  function lifecycleSnapshotFields(subagents = lifecycleSnapshotChildren) {
    return {
      type: "subagent_lifecycle_snapshot", agent_id: agentId, instance_id: instanceId,
      parent_session_id: parentSessionId, lease_id: leaseId, producer_epoch: producerEpoch,
      client_sequence: ++lifecycleSequence, event_id: generateId(), subagents,
    };
  }

  function failLifecycleClosed() {
    lifecycleTrace("client_lifecycle_closed", { reason: "journal_oversize", pending: lifecycleJournal.size, bytes: journalBytes() });
    lifecycleJournal.clear();
    lifecycleReady = false;
    lifecycleConnectionDisabled = true;
    lifecycleDiagnostic("journal_oversize");
  }

  function journalBytes() {
    let bytes = 0;
    for (const item of lifecycleJournal.values()) bytes += item.bytes;
    return bytes;
  }

  function makeReplacementSnapshot(reason, snapshot) {
    const frame = canonicalLifecycleFrame(lifecycleSnapshotFields(snapshot));
    const bytes = serializedLifecycleBytes(frame);
    if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES || bytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
      failLifecycleClosed();
      return undefined;
    }
    lifecycleJournal.clear();
    lifecycleJournal.set(frame.event_id, { frame, bytes, enqueuedAt: now() });
    lifecycleTrace("client_lifecycle_journal_replaced", { ...lifecycleFrameTrace(frame, reason), pending: lifecycleJournal.size, bytes });
    lifecycleDiagnostic(reason);
    return frame;
  }

  function lifecycleCanJournal() {
    return lifecycleSupported && lifecycleReady && !lifecycleConnectionDisabled && leaseId !== undefined;
  }

  function journalLifecycle(fields, snapshot) {
    if (!lifecycleCanJournal()) return undefined;
    const frame = canonicalLifecycleFrame(fields);
    const bytes = serializedLifecycleBytes(frame);
    if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES) {
      failLifecycleClosed();
      return undefined;
    }
    const oldest = lifecycleJournal.values().next().value;
    if (oldest !== undefined && now() - oldest.enqueuedAt >= LIFECYCLE_JOURNAL_MAX_AGE_MS) {
      return makeReplacementSnapshot("journal_expired", snapshot);
    }
    if (lifecycleJournal.size >= LIFECYCLE_JOURNAL_MAX_ITEMS || journalBytes() + bytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
      return makeReplacementSnapshot("journal_collapsed", snapshot);
    }
    lifecycleJournal.set(frame.event_id, { frame, bytes, enqueuedAt: now() });
    lifecycleTrace("client_lifecycle_journaled", { ...lifecycleFrameTrace(frame, "queued"), pending: lifecycleJournal.size, bytes });
    return frame;
  }

  function rebindLifecycleJournalLease() {
    if (leaseId === undefined || lifecycleJournal.size === 0) return;
    const rebound = [];
    let totalBytes = 0;
    for (const item of lifecycleJournal.values()) {
      const frame = { ...item.frame, frame_id: generateId(), lease_id: leaseId };
      const bytes = serializedLifecycleBytes(frame);
      totalBytes += bytes;
      if (bytes > LIFECYCLE_JOURNAL_MAX_ITEM_BYTES || totalBytes > LIFECYCLE_JOURNAL_MAX_BYTES) {
        failLifecycleClosed();
        return;
      }
      rebound.push([frame.event_id, { frame, bytes, enqueuedAt: item.enqueuedAt }]);
    }
    lifecycleJournal.clear();
    for (const [eventId, item] of rebound) lifecycleJournal.set(eventId, item);
  }

  function flushLifecycleJournal() {
    if (!socketOpen(socket) || !lifecycleReady || lifecycleConnectionDisabled) return;
    const entries = [...lifecycleJournal.values()];
    // A replacement lease must see the newest complete assertion first. Sending an older
    // snapshot first could authoritatively omit a child that the producer already knows runs.
    const repair = entries
      .filter((item) => item.frame.type === "subagent_lifecycle_snapshot")
      .sort((left, right) => right.frame.client_sequence - left.frame.client_sequence)[0];
    const pending = repair === undefined
      ? entries.sort((left, right) => left.frame.client_sequence - right.frame.client_sequence)
      : [repair, ...entries.filter((item) => item !== repair)
        .sort((left, right) => left.frame.client_sequence - right.frame.client_sequence)];
    for (const item of pending) {
      lifecycleTrace("client_lifecycle_publish_attempt", lifecycleFrameTrace(item.frame, "flush"));
      try {
        socket.send(JSON.stringify(item.frame));
        lifecycleTrace("client_lifecycle_published", lifecycleFrameTrace(item.frame, "flush"));
      } catch (error) {
        lifecycleTrace("client_lifecycle_publish_error", { ...lifecycleFrameTrace(item.frame, "flush"), error_name: error?.name ?? "Error" });
        throw error;
      }
    }
  }

  function lifecycleSnapshot(subagents) {
    lifecycleSnapshotChildren = normalizeSubagents(subagents);
    if (!lifecycleCanJournal()) {
      lifecycleTrace("client_lifecycle_publish_deferred", {
        frame_type: "subagent_lifecycle_snapshot", snapshot_count: lifecycleSnapshotChildren.length,
        subagents: lifecycleSnapshotChildren, ready: lifecycleReady, available: lifecycleSupported,
      });
      return undefined;
    }
    const frame = journalLifecycle(lifecycleSnapshotFields(), lifecycleSnapshotChildren);
    if (frame !== undefined) flushLifecycleJournal();
    return frame?.event_id;
  }

  function lifecycleEvent({ lifecycleEvent, subagent, terminalReason, snapshot }) {
    if (!['started', 'terminal'].includes(lifecycleEvent) || subagent === undefined) throw new TypeError("a lifecycle event and child are required");
    const child = normalizeSubagentSummary(subagent);
    if (child === undefined) throw new TypeError("a safe lifecycle child is required");
    if (!Array.isArray(snapshot)) throw new TypeError("a complete lifecycle snapshot is required");
    lifecycleSnapshotChildren = normalizeSubagents(snapshot);
    if (!lifecycleCanJournal()) {
      lifecycleTrace("client_lifecycle_publish_deferred", {
        frame_type: "subagent_lifecycle_event", snapshot_count: lifecycleSnapshotChildren.length,
        subagent_count: 1, subagents: [child], ready: lifecycleReady, available: lifecycleSupported,
      });
      return undefined;
    }
    const frame = journalLifecycle({
      type: "subagent_lifecycle_event", agent_id: agentId, instance_id: instanceId,
      parent_session_id: parentSessionId, lease_id: leaseId, producer_epoch: producerEpoch,
      client_sequence: ++lifecycleSequence, event_id: generateId(), lifecycle_event: lifecycleEvent,
      subagent: child, ...(lifecycleEvent === "terminal" ? { terminal_reason: normalizeTerminalReason(terminalReason) } : {}),
    }, lifecycleSnapshotChildren);
    if (frame !== undefined) flushLifecycleJournal();
    return frame?.event_id;
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
      ...(delivery.items === undefined || delivery.items.length <= 1 ? {} : {
        delivery_items: delivery.items.map((item) => ({
          message_id: item.messageId,
          message_incarnation_id: item.messageIncarnationId,
          attempt_number: item.attemptNumber,
          delivery_expires_at: item.deliveryExpiresAt,
        })),
      }),
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
    lifecycleTrace("client_lifecycle_reconnect_scheduled", { attempt: reconnectAttempt, phase: kickstartRouter ? "kickstart" : "socket" });
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
    lifecycleSupported = false;
    lifecycleReady = false;
    lifecycleConnectionDisabled = false;
    lifecycleTrace("client_lifecycle_disconnected", { connected: false, error_name: error?.name, phase: kickstartRouter ? "kickstart" : "socket" });
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

  async function discoverLifecycle(currentSocket) {
    lifecycleTrace("client_lifecycle_capability_lookup", { phase: "started" });
    try {
      const response = await fetchImpl(`${routerHttpUrl(routerUrl)}/v1/capabilities`, { signal: AbortSignal.timeout(capabilityTimeoutMs) });
      const capabilities = response.ok ? await response.json() : undefined;
      if (socket !== currentSocket || stopped) return;
      lifecycleSupported = capabilities?.subagent_lifecycle_v2 === true;
      lifecycleTrace("client_lifecycle_capability_result", { available: lifecycleSupported, ready: response.ok });
      if (!lifecycleSupported) return;
      lifecycleConnectionDisabled = false;
      lifecycleTrace("client_lifecycle_sync_enable_attempt", { router_version: lifecycleRouterVersion });
      send({ type: "subagent_sync_enable", agent_id: agentId, instance_id: instanceId, lease_id: leaseId,
        ...(lifecycleRouterVersion > 0 ? { after_version: lifecycleRouterVersion } : {}) });
    } catch (error) {
      lifecycleTrace("client_lifecycle_capability_error", { error_name: error?.name ?? "Error" });
      // 404/timeouts are the old-router fallback. Do not log payloads or source text.
      if (socket === currentSocket && !stopped) lifecycleSupported = false;
    }
  }

  async function receive(frame) {
    if (frame.type === "registered" && frame.agent_id === agentId && frame.instance_id === instanceId) {
      leaseId = frame.lease_id;
      lifecycleTrace("client_lifecycle_registered", { connected: true, pending: lifecycleJournal.size });
      rebindLifecycleJournalLease();
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
      void discoverLifecycle(socket);
      return;
    }
    if (frame.type === "subagent_state_snapshot" || frame.type === "subagent_state_replay") {
      lifecycleRouterVersion = Math.max(lifecycleRouterVersion, frame.router_version ?? 0);
      lifecycleTrace("client_lifecycle_router_snapshot", { frame_type: frame.type, router_version: lifecycleRouterVersion });
      if (lifecycleSupported && !lifecycleConnectionDisabled) {
        const reconnect = lifecycleJournal.size > 0;
        lifecycleReady = true;
        // This current assertion is first after every enablement/reconnect. It fences a
        // replacement lease before any older retry can be replayed.
        lifecycleSnapshot(lifecycleSnapshotChildren);
        lifecycleTrace("client_lifecycle_ready", { router_version: lifecycleRouterVersion, reconnect, pending: lifecycleJournal.size });
        try { onLifecycleReady({ routerVersion: lifecycleRouterVersion, reconnect }); } catch { /* local reporting cannot break mailbox delivery */ }
        flushLifecycleJournal();
      }
      return;
    }
    if (frame.type === "subagent_lifecycle_acknowledged") {
      const pending = [...lifecycleJournal.values()].find((item) => item.frame.frame_id === frame.request_id);
      if (pending === undefined
        || frame.event_id !== pending.frame.event_id
        || frame.client_sequence !== pending.frame.client_sequence
        || frame.producer_epoch !== pending.frame.producer_epoch
        || frame.agent_id !== agentId
        || frame.instance_id !== instanceId
        || frame.parent_session_id !== parentSessionId
        || frame.lease_id !== pending.frame.lease_id) {
        lifecycleTrace("client_lifecycle_ack_ignored", { event_id: frame.event_id, router_version: frame.router_version ?? 0 });
        return;
      }
      // The router sends this correlated acknowledgment only after its transaction commits.
      lifecycleRouterVersion = Math.max(lifecycleRouterVersion, frame.router_version ?? 0);
      lifecycleJournal.delete(pending.frame.event_id);
      lifecycleTrace("client_lifecycle_acknowledged", { event_id: frame.event_id, client_sequence: frame.client_sequence, router_version: lifecycleRouterVersion, pending: lifecycleJournal.size });
      return;
    }
    if (frame.type !== "message") {
      if (frame.type === "error" || frame.type === "lease_rejected") {
        log(`MECHA_V2_FRAME ${frame.type} agent=${agentId} code=${frame.code ?? "unknown"}`);
      }
      return;
    }
    const items = Array.isArray(frame.delivery_items)
      ? frame.delivery_items.map((item) => ({
        messageId: item.message_id,
        messageIncarnationId: item.message_incarnation_id,
        attemptNumber: item.attempt_number,
        deliveryExpiresAt: item.delivery_expires_at,
      }))
      : [{
        messageId: frame.message.id,
        messageIncarnationId: frame.message_incarnation_id,
        attemptNumber: frame.attempt_number,
        deliveryExpiresAt: frame.delivery_expires_at,
      }];
    const delivery = {
      messageId: frame.message.id,
      attemptNumber: frame.attempt_number,
      items,
    };
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
        send({ type: "register", agent_id: agentId, instance_id: instanceId, delivery_receipts: "delivery_items_v1" });
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
    parentSessionId,
    producerEpoch,
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
    sendLifecycleSnapshot: lifecycleSnapshot,
    sendLifecycleEvent: lifecycleEvent,
    requestLifecycleResync() {
      if (!lifecycleSupported || !lifecycleReady || leaseId === undefined) {
        lifecycleTrace("client_lifecycle_resync_deferred", { available: lifecycleSupported, ready: lifecycleReady, router_version: lifecycleRouterVersion });
        return false;
      }
      lifecycleTrace("client_lifecycle_resync_attempt", { router_version: lifecycleRouterVersion });
      send({ type: "subagent_state_resync", agent_id: agentId, instance_id: instanceId, lease_id: leaseId, after_version: lifecycleRouterVersion });
      return true;
    },
    lifecycleStatus() { return { supported: lifecycleSupported, ready: lifecycleReady, routerVersion: lifecycleRouterVersion, pending: lifecycleJournal.size, bytes: journalBytes() }; },
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
