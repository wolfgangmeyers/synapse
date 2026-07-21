import { createMailboxClient, createNanoId, assertAgentId, routerHttpUrl } from "./mailbox-client.js";
export { createMailboxClient } from "./mailbox-client.js";
import { execFile, execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { createRecallState } from "./recall-state.js";
import { createRecallController } from "./recall-controller.js";
import { createRecallPipeline as createPipeline } from "./recall-pipeline.js";
import { runRecallEvaluator as evaluateRecall, validateRecallChoice as validateChoice } from "./recall-evaluator.js";
import { createMemoryCaptureLifecycle } from "./memory-capture.js";
export { createRecallState } from "./recall-state.js";
export { createRecallController } from "./recall-controller.js";
export { createRecallPipeline } from "./recall-pipeline.js";
export { runRecallEvaluator, validateRecallChoice } from "./recall-evaluator.js";

const DEFAULT_SESSION_CHECKPOINT_RETRY_MS = 250;
const MAILBOX_MESSAGE_TYPE = "mecha-agent-message";
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const PI_SUBAGENTS_SERVICE = Symbol.for("@gotgenes/pi-subagents:service");

export function boundedConversationText(content, maximum = 4096) {
  const blocks = Array.isArray(content) ? content : [content];
  const text = blocks.map((block) => typeof block === "string" ? block : block?.type === "text" && typeof block.text === "string" ? block.text : "").filter(Boolean).join("\n");
  return text.slice(-maximum);
}

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

const SETTLED_NOTIFICATION_FOLLOW_UP_MS = [60_000, 300_000, 600_000];

// Managed workers use background subagents, whose native started and terminal
// events share a run ID. Foreground runs lack a matching terminal ID, so they
// remain an upstream lifecycle limitation rather than a heuristic target.
export function createSettledNotificationScheduler({
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
} = {}) {
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("settled notification timers are required");
  }

  const children = new Set();
  const timers = new Set();
  let generation = 0;
  let parentSettled = false;

  function invalidate() {
    generation += 1;
    for (const timer of timers) clearTimeoutImpl(timer);
    timers.clear();
  }

  function schedule(delay, expectedGeneration, send) {
    let timer;
    timer = setTimeoutImpl(() => {
      timers.delete(timer);
      if (generation !== expectedGeneration || children.size > 0) return;
      send();
    }, delay);
    timers.add(timer);
    timer?.unref?.();
  }

  return {
    onParentActivity() {
      parentSettled = false;
      invalidate();
    },
    onParentSettled(send) {
      if (parentSettled || typeof send !== "function") return;
      parentSettled = true;
      if (children.size > 0) return;

      invalidate();
      const settledGeneration = generation;
      send();
      for (const delay of SETTLED_NOTIFICATION_FOLLOW_UP_MS) {
        schedule(delay, settledGeneration, send);
      }
    },
    onChildStarted(sessionId) {
      if (typeof sessionId !== "string" || sessionId.length === 0 || children.has(sessionId)) return;
      children.add(sessionId);
      invalidate();
    },
    onChildDisposed(sessionId) {
      if (typeof sessionId !== "string" || sessionId.length === 0) return;
      children.delete(sessionId);
    },
    dispose() {
      invalidate();
      children.clear();
      parentSettled = false;
    },
    activeChildCount() {
      return children.size;
    },
  };
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
  const subject = message.subject === undefined ? "" : `\nSubject: ${message.subject}`;
  return {
    customType: MAILBOX_MESSAGE_TYPE,
    content: `Mailbox message from ${message.from}${subject}\n\n${message.body}`,
    display: true,
    details: {
      message_id: message.id,
      from: message.from,
      to: message.to,
      ...(message.subject === undefined ? {} : { subject: message.subject }),
    },
  };
}

export async function spawnNativeSentinel(message, service = globalThis[PI_SUBAGENTS_SERVICE]) {
  if (service === undefined || typeof service.spawn !== "function") {
    throw new Error("Pi native subagent service is unavailable");
  }
  return service.spawn("general-purpose", message.body, {
    description: "Scheduled Sentinel work",
    model: message.sentinel.model,
    inheritContext: true,
  });
}

export function captureNativeSentinelSpawner(service = globalThis[PI_SUBAGENTS_SERVICE]) {
  return (message) => spawnNativeSentinel(message, service);
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
  createSettledScheduler = createSettledNotificationScheduler,
} = {}) {
  let client;
  let coordinator;
  let startupError;
  let activeWork = ""; let recallAbort; let recallProcess; let policyAbort; let policyReady; let capture;
  const ensureCapture = async (signal) => { if (capture !== undefined) return capture; const response = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5_000)]) : AbortSignal.timeout(5_000) }); if (!response.ok) throw new Error("capture policy unavailable"); const { recall: policy } = await response.json(); capture = createMemoryCaptureLifecycle({ policy, context: async ({ signal } = {}) => { const lookup = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/lookup`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify({ stores: ["default"] }) }); if (!lookup.ok) throw new Error("capture destination unavailable"); const { records } = await lookup.json(); return records; }, apply: async (body, signal) => { const applied = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/apply`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify(body) }); if (!applied.ok) throw new Error("capture apply failed"); return applied.json(); } }); return capture; };
  const ensureRecallPolicy = async () => { if (policyReady !== undefined) return policyReady; policyAbort = new AbortController(); const timeout = setTimeout(() => policyAbort?.abort(), 5_000); policyReady = fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: policyAbort.signal }).then((response) => { if (!response.ok) throw new Error("recall policy unavailable"); return response.json(); }).then(({ recall: policy }) => { recallController.configure(policy); return policy; }).finally(() => clearTimeout(timeout)).catch((error) => { policyReady = undefined; throw error; }); return policyReady; };
  let recall; let recallConfigurationError;
  try { recall = createRecallState(); } catch (error) { recallConfigurationError = error; recall = createRecallState({}); }
  let recallPipeline;
  const recallController = createRecallController({
    cancel: () => { policyAbort?.abort(); recallAbort?.abort(); recallProcess?.kill(); },
    evaluate: async (active) => {
      const status = recall.status();
      if (!status.enabled || !active()) return undefined;
      recallPipeline = createPipeline({ routerUrl: process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL, stores: status.stores, workSnapshot: activeWork, evaluate: evaluateRecall, validateChoice, onPolicy: (policy) => recallController.configure(policy), registerAbort: (controller) => { recallAbort = controller; }, registerProcess: (child) => { recallProcess = child; }, send: pi.sendMessage.bind(pi) });
      return recallPipeline.evaluate(active);
    },
    recall: (choice, active) => recallPipeline?.recall(choice, active),
  });
  let settledNotificationAgentId;
  let settledNotificationPending = false;
  let settledNotificationRecipient;
  try {
    settledNotificationRecipient = assertAgentId(process.env.MECHA_PI_NOTIFY_ON_SETTLED);
  } catch {
    // The launcher rejects invalid values; a manually configured Pi stays disabled.
  }
  const children = childSessionIds();
  const settledNotifications = createSettledScheduler();
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

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_recall",
    label: "Mecha Recall",
    description: "Enable, disable, or inspect process-local Markdown recall.",
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["enable", "disable", "status"] },
      stores: { type: "array", items: { type: "string" } },
    } },
    async execute(_toolCallId, input) {
      try {
        if (recallConfigurationError !== undefined) throw recallConfigurationError;
        if (input.action === "enable") { recallController.stop(); recall.enable(input.stores); }
        else if (input.action === "disable") { recall.disable(); recallController.stop(); }
        else if (input.action !== "status") throw new Error("unsupported mecha_recall action");
        const status = recall.status();
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: error.message }], details: {}, isError: true };
      }
    },
  });

  pi.events.on("subagents:started", (event) => {
    settledNotifications.onChildStarted(event?.id);
  });
  pi.events.on("subagents:completed", (event) => {
    settledNotifications.onChildDisposed(event?.id);
  });
  pi.events.on("subagents:failed", (event) => {
    settledNotifications.onChildDisposed(event?.id);
  });
  pi.events.on("subagents:child:session-created", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.add(sessionId);
  });
  pi.events.on("subagents:child:disposed", (event) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) children.delete(sessionId);
  });

  pi.on("session_before_compact", async (event, context) => { if (children.has(context.sessionManager.getSessionId())) return undefined; try { await (await ensureCapture(event.signal)).compact([...(event.preparation?.messagesToSummarize ?? []), ...(event.preparation?.turnPrefixMessages ?? [])], { signal: event.signal }); } catch {} return undefined; });
  pi.on("message_end", (event, context) => { if (!children.has(context.sessionManager.getSessionId())) activeWork = boundedConversationText(event.message?.content, 4096); coordinator?.onMessageEnd(event, context); });
  pi.on("message_start", async (_event, context) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_start", (_event, context) => {
    if (settledNotificationRecipient !== undefined && !children.has(context.sessionManager.getSessionId())) {
      settledNotificationPending = true;
      settledNotifications.onParentActivity();
    }
  });
  pi.on("agent_settled", async (_event, context) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_settled", (_event, context) => {
    if (children.has(context.sessionManager.getSessionId()) || !settledNotificationPending) return;

    // Consume before sendReply starts so a later run can arm independently.
    settledNotificationPending = false;
    settledNotifications.onParentSettled(() => {
      const notificationClient = client;
      const worker = settledNotificationAgentId;
      if (notificationClient === undefined || worker === undefined) return;
      try {
        Promise.resolve(notificationClient.sendReply({
          to: settledNotificationRecipient,
          subject: "Pi worker settled",
          body: `Worker ${worker} has settled and is idle.`,
        })).catch((error) => {
          context.ui.notify(`Pi settled notification failed: ${error.message}`, "error");
        });
      } catch (error) {
        context.ui.notify(`Pi settled notification failed: ${error.message}`, "error");
      }
    });
  });
  pi.on("session_shutdown", async (_event, context) => { if (children.has(context.sessionManager.getSessionId())) return; recallController.stop(); const controller = new AbortController(); const deadlineAt = Date.now() + 10_000; const timer = setTimeout(() => controller.abort(), 10_000); try { const branch = context.sessionManager.getBranch(); const boundary = branch.map(({ type }) => type).lastIndexOf("compaction"); await (await ensureCapture(controller.signal)).shutdown(branch.slice(boundary + 1), { deadlineAt, signal: controller.signal }); } catch {} finally { controller.abort(); clearTimeout(timer); } });

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
      settledNotificationPending = false;
      settledNotificationAgentId = agentId;
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
      // Child sessions inherit extensions and can replace the package's global
      // service symbol. Bind scheduled work to this resident parent service.
      coordinator = createPiMailboxDeliveryCoordinator({
        pi,
        sessionContext: context,
        client: mailboxClient,
        spawnSubagent: captureNativeSentinelSpawner(),
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
    settledNotificationPending = false;
    settledNotificationAgentId = undefined;
    settledNotifications.dispose();
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
