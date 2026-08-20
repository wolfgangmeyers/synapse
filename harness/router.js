import { createServer } from "node:http";
import { once } from "node:events";
import { createHash } from "node:crypto";
import { lstat, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

import WebSocket, { WebSocketServer } from "ws";

import {
  DEFAULT_DELIVERY_LEASE_DURATION_MS,
  DEFAULT_LEASE_DURATION_MS,
  DEFAULT_MAX_DELIVERY_ATTEMPTS,
  DEFAULT_CONVERSATION_HISTORY_LIMIT,
  DEFAULT_CONVERSATION_RETENTION_MS,
  DeliveryNotActiveError,
  DuplicateAgentLeaseError,
  IdempotencyConflictError,
  LeaseNotActiveError,
  QueueLimitError,
  ReminderCancellationConflictError,
  ReminderIdempotencyConflictError,
  ReminderValidationError,
  validateReminder,
} from "./db.js";
import { registeredServerJobs, serverJobEnvelope } from "./server_jobs.js";
import { createNanoId } from "./id.js";
import { RECALL_POLICY, MemoryCatalogError } from "./memory-catalog.js";
import { createNativeSubagentTrace, summarizeNativeSubagents } from "./native_subagent_trace.js";
import {
  ABSOLUTE_MAX_BODY_BYTES,
  DEFAULT_MAX_BODY_BYTES,
  ProtocolValidationError,
  validateClientFrame,
  validateEnvelope,
  validateServerFrame,
} from "./protocol.js";

const DEFAULT_MAX_FRAME_BYTES = 64 * 1024;
const MAX_MODEL_SUMMARIES = 1_000;
const MODEL_CATALOG_MAX_FRAME_BYTES = 60 * 1024;
const DEFAULT_PENDING_MODEL_CONTROL_TTL_MS = 30_000;
const DEFAULT_MAX_PENDING_MODEL_CONTROLS = 256;
const DEFAULT_MAX_PENDING_MODEL_CONTROLS_PER_SOURCE = 16;
// Accommodates text messages plus up to three browser-compressed image attachments
// while still capping a single loopback request.
const MAX_REQUEST_BYTES = 6 * 1024 * 1024;
const DEFAULT_MAX_REQUEST_BYTES = MAX_REQUEST_BYTES;
const DEFAULT_MAX_IMAGE_ATTACHMENTS = 3;
const DEFAULT_MAX_IMAGE_ATTACHMENT_BYTES = 768 * 1024;
const DEFAULT_MAX_IMAGE_ATTACHMENT_TOTAL_BYTES = DEFAULT_MAX_IMAGE_ATTACHMENTS * DEFAULT_MAX_IMAGE_ATTACHMENT_BYTES;
const DEFAULT_IMAGE_ATTACHMENT_TTL_MS = 24 * 60 * 60 * 1000;
const MAX_IMAGE_PIXELS = 40_000_000;
const MAX_IMAGE_EDGE = 16_384;
const IMAGE_ATTACHMENT_MIMES = new Map([
  ["image/png", { extension: "png", magic: [[0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]] }],
  ["image/jpeg", { extension: "jpg", magic: [[0xff, 0xd8, 0xff]] }],
  ["image/webp", { extension: "webp", riff: true }],
  ["image/gif", { extension: "gif", magic: [[0x47, 0x49, 0x46, 0x38, 0x37, 0x61], [0x47, 0x49, 0x46, 0x38, 0x39, 0x61]] }],
]);
const DEFAULT_MAX_REQUESTS_PER_WINDOW = 60;
const DEFAULT_RATE_LIMIT_WINDOW_MS = 60 * 1000;
// How long the lifecycle sweep may go unrecorded. The sweep runs every second, so
// logging every idle pass added ~75k lines a day and no signal; logging none of them
// made an idle router and a wedged one look identical in the log, which is the failure
// shape that wastes the most time — something dead that reads as healthy. One record
// an hour is 24 lines a day against a 16 MiB cap, and "no sweep record for over an
// hour" becomes a usable liveness check.
const DEFAULT_SWEEP_HEARTBEAT_INTERVAL_MS = 60 * 60 * 1000;
const DEFAULT_MECHA_DEVELOPER_UPDATE_REMINDER_INTERVAL_MS = 2 * 60 * 60 * 1000;
const MECHA_DEVELOPER_REPO_PATH = "/Users/wolfgang/code/mecha-developer";
const ALLOWED_HOSTS = new Set(["127.0.0.1", "::1", "0.0.0.0"]);
const CORS_HEADERS = Object.freeze({
  "access-control-allow-origin": "*",
  "access-control-allow-methods": "GET, POST, DELETE, OPTIONS",
  "access-control-allow-headers": "content-type",
});
const HTTP_ERROR_MESSAGES = Object.freeze({
  invalid_request: "Request does not match the mailbox protocol.",
  escaped_newlines: "Message body contains escaped newline text (\\n or \\r). Send real multiline content instead of escaped newline sequences.",
  payload_too_large: "Request exceeds local mailbox limits.",
  queue_full: "Recipient queue is full.",
  rate_limited: "Sender rate limit exceeded.",
  idempotency_conflict: "Message ID already belongs to a different request.",
  dead_letter_required: "Message is not a dead letter.",
  invalid_reminder: "Reminder request is invalid.",
  reminder_idempotency_conflict: "Reminder ID already belongs to a different request.",
  reminder_not_cancelable: "Reminder is already linked to a delivery.",
  reminder_target_unavailable: "Reminder target has never connected.",
  not_found: "Resource was not found.",
  browser_origin_rejected: "Browser origins are not accepted.",
  internal_error: "Router could not complete the request.",
});

export function createAgentRouter({
  repository,
  host = "127.0.0.1",
  port = 0,
  path = "/v1/agents",
  leaseDurationMs = DEFAULT_LEASE_DURATION_MS,
  deliveryLeaseDurationMs = DEFAULT_DELIVERY_LEASE_DURATION_MS,
  maxDeliveryAttempts = DEFAULT_MAX_DELIVERY_ATTEMPTS,
  timeoutSweepIntervalMs = 1_000,
  sweepHeartbeatIntervalMs = DEFAULT_SWEEP_HEARTBEAT_INTERVAL_MS,
  mechaDeveloperUpdateReminderIntervalMs = DEFAULT_MECHA_DEVELOPER_UPDATE_REMINDER_INTERVAL_MS,
  deliveryOfferCoalesceMs = 25,
  activityTtlMs = 15_000,
  pendingModelControlTtlMs = DEFAULT_PENDING_MODEL_CONTROL_TTL_MS,
  maxPendingModelControls = DEFAULT_MAX_PENDING_MODEL_CONTROLS,
  maxPendingModelControlsPerSource = DEFAULT_MAX_PENDING_MODEL_CONTROLS_PER_SOURCE,
  generateFrameId = createNanoId,
  maxFrameBytes = DEFAULT_MAX_FRAME_BYTES,
  maxRequestBytes = DEFAULT_MAX_REQUEST_BYTES,
  maxRequestsPerWindow = DEFAULT_MAX_REQUESTS_PER_WINDOW,
  maxImageAttachments = DEFAULT_MAX_IMAGE_ATTACHMENTS,
  maxImageAttachmentBytes = DEFAULT_MAX_IMAGE_ATTACHMENT_BYTES,
  maxImageAttachmentTotalBytes = DEFAULT_MAX_IMAGE_ATTACHMENT_TOTAL_BYTES,
  imageAttachmentTtlMs = DEFAULT_IMAGE_ATTACHMENT_TTL_MS,
  imageAttachmentDirectory = join(tmpdir(), "mecha-router-images"),
  rateLimitWindowMs = DEFAULT_RATE_LIMIT_WINDOW_MS,
  clock = () => new Date(),
  eventLogger = () => {},
  onOfferError = defaultOfferError,
  onReminderError = defaultReminderError,
  onServerJobError = defaultServerJobError,
  memoryCatalog,
  nativeSubagentTrace = createNativeSubagentTrace(),
} = {}) {
  if (repository === undefined
    || typeof repository.enqueue !== "function"
    || typeof repository.acquireAgentLease !== "function"
    || typeof repository.renewAgentLease !== "function"
    || typeof repository.releaseAgentLease !== "function"
    || typeof repository.leaseFor !== "function"
    || typeof repository.claimNextMessage !== "function"
    || typeof repository.recordInjectionRequested !== "function"
    || typeof repository.acknowledgeDelivery !== "function"
    || typeof repository.nackDelivery !== "function"
    || typeof repository.recoverTimedOutDeliveries !== "function"
    || typeof repository.recoverExpiredUnclaimedMessages !== "function") {
    throw new TypeError("repository must provide agent lease and delivery operations");
  }
  if (!Number.isSafeInteger(leaseDurationMs) || leaseDurationMs <= 0) {
    throw new TypeError("leaseDurationMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(deliveryLeaseDurationMs) || deliveryLeaseDurationMs <= 0) {
    throw new TypeError("deliveryLeaseDurationMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxDeliveryAttempts) || maxDeliveryAttempts <= 0) {
    throw new TypeError("maxDeliveryAttempts must be a positive safe integer");
  }
  if (!Number.isSafeInteger(sweepHeartbeatIntervalMs) || sweepHeartbeatIntervalMs <= 0) {
    throw new TypeError("sweepHeartbeatIntervalMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(timeoutSweepIntervalMs) || timeoutSweepIntervalMs <= 0) {
    throw new TypeError("timeoutSweepIntervalMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(mechaDeveloperUpdateReminderIntervalMs) || mechaDeveloperUpdateReminderIntervalMs <= 0) {
    throw new TypeError("mechaDeveloperUpdateReminderIntervalMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(deliveryOfferCoalesceMs) || deliveryOfferCoalesceMs < 0 || deliveryOfferCoalesceMs > 1_000) {
    throw new TypeError("deliveryOfferCoalesceMs must be a safe integer from 0 through 1000");
  }
  if (!Number.isSafeInteger(activityTtlMs) || activityTtlMs <= 0) {
    throw new TypeError("activityTtlMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(pendingModelControlTtlMs) || pendingModelControlTtlMs <= 0) {
    throw new TypeError("pendingModelControlTtlMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxPendingModelControls) || maxPendingModelControls <= 0) {
    throw new TypeError("maxPendingModelControls must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxPendingModelControlsPerSource)
    || maxPendingModelControlsPerSource <= 0
    || maxPendingModelControlsPerSource > maxPendingModelControls) {
    throw new TypeError("maxPendingModelControlsPerSource must be a positive safe integer no greater than maxPendingModelControls");
  }
  if (!Number.isSafeInteger(maxFrameBytes) || maxFrameBytes <= 0) {
    throw new TypeError("maxFrameBytes must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxRequestBytes)
    || maxRequestBytes <= 0
    || maxRequestBytes > MAX_REQUEST_BYTES) {
    throw new TypeError(`maxRequestBytes must be a positive safe integer no greater than ${MAX_REQUEST_BYTES}`);
  }
  if (!Number.isSafeInteger(maxRequestsPerWindow) || maxRequestsPerWindow <= 0) {
    throw new TypeError("maxRequestsPerWindow must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxImageAttachments) || maxImageAttachments < 0 || maxImageAttachments > DEFAULT_MAX_IMAGE_ATTACHMENTS) {
    throw new TypeError("maxImageAttachments must be 0 through 3");
  }
  if (!Number.isSafeInteger(maxImageAttachmentBytes) || maxImageAttachmentBytes <= 0) {
    throw new TypeError("maxImageAttachmentBytes must be a positive safe integer");
  }
  if (!Number.isSafeInteger(maxImageAttachmentTotalBytes) || maxImageAttachmentTotalBytes < maxImageAttachmentBytes) {
    throw new TypeError("maxImageAttachmentTotalBytes must be at least maxImageAttachmentBytes");
  }
  if (!Number.isSafeInteger(imageAttachmentTtlMs) || imageAttachmentTtlMs <= 0) {
    throw new TypeError("imageAttachmentTtlMs must be a positive safe integer");
  }
  if (!Number.isSafeInteger(rateLimitWindowMs) || rateLimitWindowMs <= 0) {
    throw new TypeError("rateLimitWindowMs must be a positive safe integer");
  }
  if (typeof clock !== "function") throw new TypeError("clock must be a function");
  if (typeof eventLogger !== "function") throw new TypeError("eventLogger must be a function");
  if (typeof onOfferError !== "function") throw new TypeError("onOfferError must be a function");
  if (typeof onReminderError !== "function") throw new TypeError("onReminderError must be a function");
  if (typeof onServerJobError !== "function") throw new TypeError("onServerJobError must be a function");
  if (nativeSubagentTrace === undefined || typeof nativeSubagentTrace.record !== "function") {
    throw new TypeError("nativeSubagentTrace must provide record");
  }
  if (!ALLOWED_HOSTS.has(host)) {
    throw new TypeError("host must be 127.0.0.1, ::1, or 0.0.0.0");
  }

  if (memoryCatalog !== undefined
    && (!["rebuild", "lookup", "expand", "touch", "apply"].every((method) => typeof memoryCatalog[method] === "function"))) {
    throw new TypeError("memoryCatalog must provide rebuild, lookup, expand, and touch");
  }

  const sessions = new Map();
  const conversationSessions = new Map();
  const requestTimesBySender = new Map();
  const pendingDeliveryOffers = new Map();
  const activityByAgentId = new Map();
  const goalByAgentId = new Map();
  const modelByAgentId = new Map();
  // Model-control results are useful only while both exact sessions and leases
  // remain connected. TTL and capacity limits bound targets that never reply.
  const pendingModelControls = new Map();
  // V2 is deliberately unavailable instead of silently becoming a second
  // process-local lifecycle store when a router starts against a pre-017 DB.
  const lifecycleV2Available = typeof repository.migrationIds === "function"
    && repository.migrationIds().includes("017_subagent_lifecycle")
    && ["applySubagentLifecycleEvent", "applySubagentLifecycleSnapshot", "subagentLifecycleSnapshot", "replaySubagentLifecycle", "pruneSubagentLifecycle", "subagentLifecycleProducerFor"].every((method) => typeof repository[method] === "function");
  const traceLifecycle = (event, fields = {}) => nativeSubagentTrace.record(event, fields);
  const lifecycleTraceFields = (frame) => ({
    frame_type: frame?.type,
    agent_id: frame?.agent_id,
    instance_id: frame?.instance_id,
    parent_session_id: frame?.parent_session_id,
    event_id: frame?.event_id,
    client_sequence: frame?.client_sequence,
    snapshot_count: Array.isArray(frame?.subagents) ? frame.subagents.length : undefined,
    subagent_count: frame?.subagent === undefined ? undefined : 1,
    subagents: summarizeNativeSubagents(frame?.subagents ?? (frame?.subagent === undefined ? undefined : [frame.subagent])),
  });
  const isLifecycleFrameType = (type) => [
    "subagent_sync_enable", "subagent_state_resync", "subagent_lifecycle_snapshot", "subagent_lifecycle_event",
  ].includes(type);
  traceLifecycle("router_lifecycle_initialized", { available: lifecycleV2Available });
  let timeoutSweepTimer;
  let mechaDeveloperUpdateReminderTimer;
  // When the last lifecycle_sweep_completed was written, for any reason.
  let lastSweepRecordMs;
  const server = createServer((request, response) => {
    void handleHttpRequest(request, response).catch(() => {
      emit("router_error", { error_code: "internal_error" });
      sendHttpError(response, 500, "internal_error");
    });
  });
  const webSocketServer = new WebSocketServer({
    server,
    path,
    maxPayload: maxFrameBytes,
    perMessageDeflate: false,
    verifyClient: ({ origin }, done) => {
      if (origin !== undefined) {
        done(false, 403, "browser origins are not accepted");
        return;
      }
      done(true);
    },
  });
  webSocketServer.on("error", () => {
    // The HTTP server's listener reports bind failures to router.listen().
  });

  webSocketServer.on("connection", (socket) => {
    let session;
    socket.on("error", () => {
      // A close event still releases a registered lease.
    });
    socket.on("message", (payload, isBinary) => {
      let parsed;
      try {
        if (isBinary) throw new ProtocolValidationError();
        parsed = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(payload));
        const frame = validateClientFrame(parsed);
        if (isLifecycleFrameType(frame.type)) traceLifecycle("router_lifecycle_validated", lifecycleTraceFields(frame));
        if (frame.type === "register") {
          if (session !== undefined) {
            sendError(socket, generateFrameId, "already_registered", frame.frame_id);
            return;
          }
          register(socket, frame);
          return;
        }
        if (frame.type === "lease_renew") {
          renew(socket, frame);
          return;
        }
        if (frame.type === "activity") {
          receiveActivity(socket, frame);
          return;
        }
        if (frame.type === "model_catalog") {
          receiveModelCatalog(socket, frame);
          return;
        }
        if (frame.type === "model_control") {
          receiveModelControl(socket, frame);
          return;
        }
        if (frame.type === "model_control_result") {
          receiveModelControlResult(socket, frame);
          return;
        }
        if (frame.type === "subagent_sync_enable") {
          enableSubagentSync(socket, frame);
          return;
        }
        if (frame.type === "subagent_state_resync") {
          resyncSubagentState(socket, frame);
          return;
        }
        if (frame.type === "subagent_lifecycle_snapshot" || frame.type === "subagent_lifecycle_event") {
          receiveSubagentLifecycle(socket, frame);
          return;
        }
        if (frame.type === "message_arrival") {
          receiveMessageArrival(socket, frame);
          return;
        }
        if (["injection_requested", "ack", "nack"].includes(frame.type)) {
          receiveDeliveryReceipt(socket, frame);
          return;
        }
        sendFrame(socket, generateFrameId, {
          type: "pong",
          request_id: frame.frame_id,
        });
      } catch (error) {
        if (isLifecycleFrameType(parsed?.type)) {
          traceLifecycle("router_lifecycle_validation_error", {
            frame_type: parsed.type,
            error_name: error?.name ?? "Error",
          });
        }
        if (error instanceof ProtocolValidationError || error instanceof SyntaxError || error instanceof TypeError) {
          emit("router_error", { error_code: "invalid_frame" });
          sendError(socket, generateFrameId, "invalid_frame");
          return;
        }
        emit("router_error", { error_code: "internal_error" });
        sendError(socket, generateFrameId, "internal_error");
        closeSocket(socket, 1011, "internal server error");
      }
    });
    socket.on("close", () => {
      if (session === undefined) return;
      removePendingModelControlsForSession(session);
      if (session.conversationSync === "v1") {
        const subscribers = conversationSessions.get(session.agentId);
        if (subscribers?.get(session.instanceId) === session) subscribers.delete(session.instanceId);
        if (subscribers !== undefined && subscribers.size > 0) {
          if (sessions.get(session.agentId) === session) {
            const replacement = subscribers.values().next().value;
            sessions.set(session.agentId, replacement);
            offerSession(replacement);
          }
          return;
        }
        conversationSessions.delete(session.agentId);
      }
      if (sessions.get(session.agentId) === session) {
        sessions.delete(session.agentId);
        cancelPendingDeliveryOffer(session.agentId);
        modelByAgentId.delete(session.agentId);
        if (session.suppressActivityClearOnClose !== true && observableAgentActivity(session.agentId) !== "compacting") {
          clearAgentActivity(session.agentId);
        }
      }
      try {
        const released = repository.releaseAgentLease({
          agentId: session.agentId,
          instanceId: session.deliveryInstanceId,
          leaseId: session.leaseId,
        });
        emit("agent_disconnected", {
          agent_id: session.agentId,
          instance_id: session.instanceId,
          lease_id: session.leaseId,
          released,
        });
      } catch {
        // Preserve an uncertain lease for stale-lease recovery instead of crashing the router.
        emit("router_error", {
          agent_id: session.agentId,
          instance_id: session.instanceId,
          lease_id: session.leaseId,
          error_code: "lease_release_failed",
        });
      }
    });

    function register(currentSocket, frame) {
      const subscribers = conversationSessions.get(frame.agent_id);
      if (frame.conversation_sync === "v1" && subscribers !== undefined) {
        const reconnecting = subscribers.get(frame.instance_id);
        const owner = sessions.get(frame.agent_id);
        const lease = owner === undefined ? undefined : repository.leaseFor(frame.agent_id);
        if (owner !== undefined && lease !== undefined
          && Date.parse(lease.expires_at) > currentTimeMs(clock)) {
          if (owner.instanceId !== frame.instance_id
            || modelByAgentId.get(frame.agent_id)?.instanceId !== frame.instance_id) {
            activityByAgentId.delete(frame.agent_id);
            goalByAgentId.delete(frame.agent_id);
            modelByAgentId.delete(frame.agent_id);
          }
          session = createSession(currentSocket, frame, lease, owner.deliveryInstanceId);
          subscribers.set(frame.instance_id, session);
          if (owner === reconnecting) sessions.set(frame.agent_id, session);
          emitRegistered(frame, lease);
          sendRegistered(frame, lease);
          if (reconnecting !== undefined && reconnecting.socket !== currentSocket) {
            sendError(reconnecting.socket, generateFrameId, "lease_expired");
            closeSocket(reconnecting.socket, 4001, "lease expired");
          }
          if (sessions.get(frame.agent_id) === session) offerSession(session);
          return;
        }
        conversationSessions.delete(frame.agent_id);
        sessions.delete(frame.agent_id);
        for (const stale of subscribers.values()) {
          sendError(stale.socket, generateFrameId, "lease_expired");
          closeSocket(stale.socket, 4001, "lease expired");
        }
      }

      const leaseId = generateFrameId();
      try {
        const lease = repository.acquireAgentLease({
          agentId: frame.agent_id,
          instanceId: frame.instance_id,
          leaseId,
          leaseDurationMs,
        });
        const replacement = sessions.get(frame.agent_id);
        if ((replacement !== undefined && replacement.instanceId !== frame.instance_id)
          || (modelByAgentId.get(frame.agent_id)?.instanceId !== undefined
            && modelByAgentId.get(frame.agent_id).instanceId !== frame.instance_id)) {
          activityByAgentId.delete(frame.agent_id);
          goalByAgentId.delete(frame.agent_id);
          modelByAgentId.delete(frame.agent_id);
        }
        session = createSession(currentSocket, frame, lease, frame.instance_id);
        cancelPendingDeliveryOffer(frame.agent_id);
        sessions.set(frame.agent_id, session);
        if (frame.conversation_sync === "v1") {
          conversationSessions.set(frame.agent_id, new Map([[frame.instance_id, session]]));
        }
        emitRegistered(frame, lease);
        if (replacement !== undefined && replacement.socket !== currentSocket) {
          sendError(replacement.socket, generateFrameId, "lease_expired");
          closeSocket(replacement.socket, 4001, "lease expired");
        }
        sendRegistered(frame, lease);
        offerSession(session);
      } catch (error) {
        if (error instanceof DuplicateAgentLeaseError) {
          rejectDuplicateRegistration(frame, error.activeInstanceId);
          return;
        }
        throw error;
      }

      function rejectDuplicateRegistration(candidate, activeInstanceId) {
        emit("agent_registration_rejected", {
          agent_id: candidate.agent_id,
          instance_id: candidate.instance_id,
          error_code: "duplicate_identity",
        });
        sendFrame(currentSocket, generateFrameId, {
          type: "lease_rejected",
          request_id: candidate.frame_id,
          code: "duplicate_identity",
          agent_id: candidate.agent_id,
          instance_id: candidate.instance_id,
          active_instance_id: activeInstanceId,
        });
        closeSocket(currentSocket, 4009, "duplicate identity");
      }

      function createSession(candidateSocket, candidate, lease, deliveryInstanceId) {
        const registeredSession = {
          socket: candidateSocket,
          agentId: candidate.agent_id,
          instanceId: candidate.instance_id,
          deliveryInstanceId,
          leaseId: lease.lease_id,
          agentRole: candidate.agent_role ?? "agent",
          clientType: candidate.client_type,
          ackConfirmation: candidate.ack_confirmation,
          conversationSync: candidate.conversation_sync,
          deliveryReceipts: candidate.delivery_receipts,
          connectedAt: lease.acquired_at,
          deliveryPaused: observableAgentActivity(candidate.agent_id) === "compacting",
          lifecycleEnabled: false,
        };
        registeredSession.offer = () => offerNextMessage(candidateSocket, registeredSession);
        return registeredSession;
      }

      function emitRegistered(candidate, lease) {
        emit("agent_registered", {
          agent_id: lease.agent_id,
          instance_id: candidate.instance_id,
          lease_id: lease.lease_id,
          recovered: lease.recovered ?? false,
          agent_role: session.agentRole,
          ...(session.clientType === undefined ? {} : { client_type: session.clientType }),
          ...(session.ackConfirmation === undefined ? {} : { ack_confirmation: session.ackConfirmation }),
          ...(session.conversationSync === undefined ? {} : { conversation_sync: session.conversationSync }),
        });
      }

      function sendRegistered(candidate, lease) {
        sendFrame(currentSocket, generateFrameId, {
          type: "registered",
          request_id: candidate.frame_id,
          agent_id: lease.agent_id,
          instance_id: candidate.instance_id,
          lease_id: lease.lease_id,
          expires_at: lease.expires_at,
          recovered: lease.recovered ?? false,
          agent_role: session.agentRole,
          ...(session.ackConfirmation === undefined ? {} : { ack_confirmation: session.ackConfirmation }),
          ...(session.conversationSync === undefined ? {} : { conversation_sync: session.conversationSync }),
        });
      }
    }

    function renew(currentSocket, frame) {
      if (session === undefined
        || session.agentId !== frame.agent_id
        || session.instanceId !== frame.instance_id
        || session.leaseId !== frame.lease_id) {
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      try {
        const lease = repository.renewAgentLease({
          agentId: frame.agent_id,
          instanceId: session.deliveryInstanceId,
          leaseId: frame.lease_id,
          leaseDurationMs,
        });
        emit("agent_lease_renewed", {
          agent_id: lease.agent_id,
          instance_id: session.instanceId,
          lease_id: lease.lease_id,
        });
        sendFrame(currentSocket, generateFrameId, {
          type: "registered",
          request_id: frame.frame_id,
          agent_id: lease.agent_id,
          instance_id: session.instanceId,
          lease_id: lease.lease_id,
          expires_at: lease.expires_at,
          recovered: false,
          agent_role: session.agentRole,
          ...(session.ackConfirmation === undefined
            ? {}
            : { ack_confirmation: session.ackConfirmation }),
          ...(session.conversationSync === undefined ? {} : { conversation_sync: session.conversationSync }),
        });
      } catch (error) {
        if (error instanceof LeaseNotActiveError) {
          sendError(currentSocket, generateFrameId, error.code, frame.frame_id);
          closeSocket(currentSocket, 4003, "lease not active");
          return;
        }
        throw error;
      }
    }

    function enableSubagentSync(currentSocket, frame) {
      traceLifecycle("router_lifecycle_sync_enable_received", lifecycleTraceFields(frame));
      if (!lifecycleV2Available) {
        traceLifecycle("router_lifecycle_sync_enable_rejected", { ...lifecycleTraceFields(frame), reason: "migration_017_required" });
        sendError(currentSocket, generateFrameId, "migration_017_required", frame.frame_id);
        return;
      }
      if (!matchesSessionLease(frame)) {
        traceLifecycle("router_lifecycle_sync_enable_rejected", { ...lifecycleTraceFields(frame), reason: "lease_not_active" });
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      session.lifecycleEnabled = true;
      if (frame.after_version !== undefined && frame.after_version > 0) {
        const replay = repository.replaySubagentLifecycle({ afterVersion: frame.after_version });
        traceLifecycle("router_lifecycle_sync_replay", {
          ...lifecycleTraceFields(frame), state: replay.mode, router_version: replay.router_version,
          snapshot_count: replay.updates?.length ?? replay.agents?.length ?? 0,
        });
        if (replay.mode === "replay") {
          sendFrame(currentSocket, generateFrameId, {
            type: "subagent_state_replay",
            router_version: replay.router_version,
            updates: replay.updates.map((update) => lifecycleAgentSnapshot(update.parent_agent_id, update.router_version)),
          });
          return;
        }
      }
      sendLifecycleSnapshot(currentSocket);
    }

    function resyncSubagentState(currentSocket, frame) {
      traceLifecycle("router_lifecycle_resync_received", lifecycleTraceFields(frame));
      if (!lifecycleV2Available) {
        traceLifecycle("router_lifecycle_resync_rejected", { ...lifecycleTraceFields(frame), reason: "migration_017_required" });
        sendError(currentSocket, generateFrameId, "migration_017_required", frame.frame_id);
        return;
      }
      if (!matchesSessionLease(frame)) {
        traceLifecycle("router_lifecycle_resync_rejected", { ...lifecycleTraceFields(frame), reason: "lease_not_active" });
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      const replay = repository.replaySubagentLifecycle({ afterVersion: frame.after_version });
      traceLifecycle("router_lifecycle_resync_replay", {
        ...lifecycleTraceFields(frame), state: replay.mode, router_version: replay.router_version,
        snapshot_count: replay.updates?.length ?? replay.agents?.length ?? 0,
      });
      if (replay.mode === "replay") {
        sendFrame(currentSocket, generateFrameId, {
          type: "subagent_state_replay",
          router_version: replay.router_version,
          updates: replay.updates.map((update) => lifecycleAgentSnapshot(update.parent_agent_id, update.router_version)),
        });
      } else sendLifecycleSnapshot(currentSocket);
    }

    function receiveSubagentLifecycle(currentSocket, frame) {
      traceLifecycle("router_lifecycle_received", lifecycleTraceFields(frame));
      if (!lifecycleV2Available) {
        traceLifecycle("router_lifecycle_rejected", { ...lifecycleTraceFields(frame), reason: "migration_017_required" });
        sendError(currentSocket, generateFrameId, "migration_017_required", frame.frame_id);
        return;
      }
      if (!matchesSessionLease(frame)) {
        if (session !== undefined) session.suppressActivityClearOnClose = true;
        traceLifecycle("router_lifecycle_rejected", { ...lifecycleTraceFields(frame), reason: "lease_not_active" });
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      let result;
      try {
        traceLifecycle("router_lifecycle_persist_attempt", lifecycleTraceFields(frame));
        result = frame.type === "subagent_lifecycle_snapshot"
          ? repository.applySubagentLifecycleSnapshot(frame, { allowLeaseRebind: true })
          : repository.applySubagentLifecycleEvent(frame);
      } catch (error) {
        traceLifecycle("router_lifecycle_persist_error", { ...lifecycleTraceFields(frame), error_name: error?.name ?? "Error" });
        throw error;
      }
      traceLifecycle("router_lifecycle_persisted", {
        ...lifecycleTraceFields(frame), disposition: result.disposition, router_version: result.router_version,
      });
      // The repository transaction completed before the acknowledgement is sent.
      sendFrame(currentSocket, generateFrameId, {
        type: "subagent_lifecycle_acknowledged",
        request_id: frame.frame_id,
        event_id: frame.event_id,
        client_sequence: frame.client_sequence,
        producer_epoch: frame.producer_epoch,
        agent_id: frame.agent_id,
        instance_id: frame.instance_id,
        parent_session_id: frame.parent_session_id,
        lease_id: frame.lease_id,
        disposition: result.disposition,
        router_version: result.router_version,
        ...(result.applied_version === undefined ? {} : { applied_version: result.applied_version }),
      });
      traceLifecycle("router_lifecycle_acknowledged", {
        ...lifecycleTraceFields(frame), disposition: result.disposition, router_version: result.router_version,
      });
      if (result.disposition === "applied") {
        const agent = lifecycleAgentSnapshot(frame.agent_id, result.router_version);
        broadcastLifecycleUpdate(agent, result.router_version);
      }
    }

    function matchesSessionLease(frame) {
      return session !== undefined
        && sessionHasActiveLease(session)
        && session.agentId === frame.agent_id
        && session.instanceId === frame.instance_id
        && session.leaseId === frame.lease_id;
    }

    function receiveActivity(currentSocket, frame) {
      if (session === undefined
        || !sessionHasActiveLease(session)
        || session.agentId !== frame.agent_id
        || session.instanceId !== frame.instance_id
        || session.leaseId !== frame.lease_id) {
        if (session !== undefined) session.suppressActivityClearOnClose = true;
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      // Keep publishing byte-compatible legacy activity during the rollout. The
      // connected-agent snapshot selects durable children once this producer is v2.
      updateAgentActivity(
        session.agentId,
        frame.activity_state,
        frame.subagent_count ?? 0,
        frame.subagents,
        frame.goal,
        session.instanceId,
        frame.current_model,
      );
    }

    function receiveModelCatalog(currentSocket, frame) {
      if (!matchesSessionLease(frame)) {
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      const previousProjection = modelByAgentId.get(session.agentId);
      const modelSummaries = normalizeModelSummaries(frame.model_summaries);
      modelByAgentId.set(session.agentId, {
        instanceId: session.instanceId,
        currentModel: previousProjection?.instanceId === session.instanceId
          ? previousProjection.currentModel
          : undefined,
        availableModels: modelSummaries,
      });
      broadcastAgentModelCatalog(session.agentId, session.instanceId, modelSummaries);
    }

    function receiveModelControl(currentSocket, frame) {
      emit("model_control_received", {
        control_id: frame.control_id,
        source_agent_id: frame.agent_id,
        source_instance_id: frame.instance_id,
        source_lease_id: frame.lease_id,
        target_agent_id: frame.target_agent_id,
        requested_provider: frame.model.provider,
        requested_model_id: frame.model.id,
      });
      if (!matchesSessionLease(frame)) {
        emit("model_control_source_lease_rejected", {
          control_id: frame.control_id,
          source_agent_id: frame.agent_id,
          source_instance_id: frame.instance_id,
          source_lease_id: frame.lease_id,
          target_agent_id: frame.target_agent_id,
        });
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      const target = sessions.get(frame.target_agent_id);
      if (target === undefined
        || target.socket.readyState !== WebSocket.OPEN
        || !sessionHasActiveLease(target)) {
        emit("model_control_target_unavailable", {
          control_id: frame.control_id,
          source_agent_id: session.agentId,
          source_instance_id: session.instanceId,
          target_agent_id: frame.target_agent_id,
          reason: "target_not_connected",
        });
        sendError(currentSocket, generateFrameId, "target_not_connected", frame.frame_id);
        return;
      }
      pruneExpiredPendingModelControls();
      const key = pendingModelControlKey(session.agentId, frame.control_id);
      if (pendingModelControls.has(key)) {
        emitModelControlPendingRejected(frame, target, "duplicate_control_id");
        sendError(currentSocket, generateFrameId, "model_control_already_pending", frame.frame_id);
        return;
      }
      let sourcePendingCount = 0;
      for (const pending of pendingModelControls.values()) {
        if (pending.sourceSession === session) sourcePendingCount += 1;
      }
      if (pendingModelControls.size >= maxPendingModelControls
        || sourcePendingCount >= maxPendingModelControlsPerSource) {
        emitModelControlPendingRejected(frame, target, "capacity_exceeded");
        sendError(currentSocket, generateFrameId, "model_control_capacity_exceeded", frame.frame_id);
        return;
      }
      let forwarded = false;
      try {
        forwarded = sendFrame(target.socket, generateFrameId, {
          type: "model_control",
          agent_id: target.agentId,
          instance_id: target.instanceId,
          lease_id: target.leaseId,
          source_agent_id: session.agentId,
          control_id: frame.control_id,
          action: frame.action,
          model: frame.model,
        });
      } catch {
        // A synchronous WebSocket send failure is a failed forward.
      }
      if (!forwarded) {
        emit("model_control_target_unavailable", {
          control_id: frame.control_id,
          source_agent_id: session.agentId,
          source_instance_id: session.instanceId,
          target_agent_id: target.agentId,
          reason: "target_send_failed",
        });
        sendError(currentSocket, generateFrameId, "target_send_failed", frame.frame_id);
        return;
      }
      pendingModelControls.set(key, {
        controlId: frame.control_id,
        requestFrameId: frame.frame_id,
        sourceSession: session,
        sourceAgentId: session.agentId,
        sourceInstanceId: session.instanceId,
        sourceLeaseId: session.leaseId,
        targetSession: target,
        targetAgentId: target.agentId,
        targetInstanceId: target.instanceId,
        targetLeaseId: target.leaseId,
        expiresAtMs: currentTimeMs(clock) + pendingModelControlTtlMs,
      });
      emit("model_control_forwarded", {
        control_id: frame.control_id,
        source_agent_id: session.agentId,
        source_instance_id: session.instanceId,
        target_agent_id: target.agentId,
        target_instance_id: target.instanceId,
        target_lease_id: target.leaseId,
        requested_provider: frame.model.provider,
        requested_model_id: frame.model.id,
      });
    }

    function receiveModelControlResult(currentSocket, frame) {
      if (!matchesSessionLease(frame)) {
        emit("model_control_result_lease_rejected", {
          control_id: frame.control_id,
          source_agent_id: frame.source_agent_id,
          target_agent_id: frame.agent_id,
          target_instance_id: frame.instance_id,
          target_lease_id: frame.lease_id,
        });
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      pruneExpiredPendingModelControls();
      const key = pendingModelControlKey(frame.source_agent_id, frame.control_id);
      const pending = pendingModelControls.get(key);
      if (pending === undefined
        || pending.targetSession !== session
        || pending.targetAgentId !== frame.agent_id
        || pending.targetInstanceId !== frame.instance_id
        || pending.targetLeaseId !== frame.lease_id) {
        emit("model_control_result_pending_rejected", {
          control_id: frame.control_id,
          source_agent_id: frame.source_agent_id,
          target_agent_id: frame.agent_id,
          target_instance_id: frame.instance_id,
          target_lease_id: frame.lease_id,
          reason: pending === undefined ? "not_pending" : "target_mismatch",
        });
        sendError(currentSocket, generateFrameId, "model_control_result_not_pending", frame.frame_id);
        return;
      }
      const source = pending.sourceSession;
      if (source.agentId !== frame.source_agent_id
        || source.instanceId !== pending.sourceInstanceId
        || source.leaseId !== pending.sourceLeaseId
        || source.socket.readyState !== WebSocket.OPEN
        || !sessionHasActiveLease(source)) {
        pendingModelControls.delete(key);
        emitPendingModelControlRemoved(pending, "source_unavailable");
        emit("model_control_result_pending_rejected", {
          control_id: frame.control_id,
          source_agent_id: frame.source_agent_id,
          target_agent_id: frame.agent_id,
          target_instance_id: frame.instance_id,
          target_lease_id: frame.lease_id,
          reason: "source_unavailable",
        });
        sendError(currentSocket, generateFrameId, "model_control_result_not_pending", frame.frame_id);
        return;
      }
      // Consume before changing projection or forwarding so every later result is
      // rejected as a duplicate without touching the retained catalog.
      pendingModelControls.delete(key);
      emit("model_control_result_received", {
        control_id: frame.control_id,
        source_agent_id: frame.source_agent_id,
        target_agent_id: frame.agent_id,
        target_instance_id: frame.instance_id,
        target_lease_id: frame.lease_id,
        accepted: frame.accepted,
        ...(frame.error_code === undefined ? {} : { error_code: frame.error_code }),
        ...(frame.current_model === undefined ? {} : {
          current_provider: frame.current_model.provider,
          current_model_id: frame.current_model.id,
        }),
      });
      const previousProjection = modelByAgentId.get(session.agentId);
      const currentModel = normalizeModelSummary(frame.current_model);
      const availableModels = frame.available_models === undefined
        ? previousProjection?.availableModels ?? []
        : normalizeModelSummaries(frame.available_models);
      if (currentModel !== undefined || frame.available_models !== undefined) {
        modelByAgentId.set(session.agentId, {
          instanceId: session.instanceId,
          currentModel: currentModel ?? previousProjection?.currentModel,
          availableModels,
        });
      }
      const resultForwarded = sendFrame(source.socket, generateFrameId, {
        type: "model_control_result",
        agent_id: session.agentId,
        instance_id: session.instanceId,
        lease_id: session.leaseId,
        source_agent_id: frame.source_agent_id,
        control_id: frame.control_id,
        accepted: frame.accepted,
        ...(frame.error_code === undefined ? {} : { error_code: frame.error_code }),
        ...(frame.current_model === undefined ? {} : { current_model: frame.current_model }),
        ...(frame.available_models === undefined ? {} : { available_models: frame.available_models }),
      });
      if (!resultForwarded) {
        emit("model_control_result_source_unavailable", {
          control_id: frame.control_id,
          source_agent_id: frame.source_agent_id,
          target_agent_id: session.agentId,
          target_instance_id: session.instanceId,
        });
        return;
      }
      emit("model_control_result_forwarded", {
        control_id: frame.control_id,
        source_agent_id: source.agentId,
        source_instance_id: source.instanceId,
        target_agent_id: session.agentId,
        target_instance_id: session.instanceId,
        accepted: frame.accepted,
        ...(frame.error_code === undefined ? {} : { error_code: frame.error_code }),
        ...(frame.current_model === undefined ? {} : {
          current_provider: frame.current_model.provider,
          current_model_id: frame.current_model.id,
        }),
      });
    }

    function receiveMessageArrival(currentSocket, frame) {
      if (session === undefined
        || !sessionHasActiveLease(session)
        || session.agentId !== frame.agent_id
        || session.instanceId !== frame.instance_id
        || session.leaseId !== frame.lease_id) {
        if (session !== undefined) session.suppressActivityClearOnClose = true;
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      broadcastMessageArrival({
        agentId: session.agentId,
        from: frame.from,
        subject: frame.subject,
        source: frame.source,
        preview: frame.preview,
        body: frame.body,
        createdAt: frame.created_at,
        messageId: frame.message_id,
      });
    }

    function receiveDeliveryReceipt(currentSocket, frame) {
      if (session === undefined
        || !sessionIsActive(session)
        || session.agentId !== frame.agent_id
        || session.instanceId !== frame.instance_id
        || session.leaseId !== frame.lease_id) {
        sendError(currentSocket, generateFrameId, "lease_not_active", frame.frame_id);
        closeSocket(currentSocket, 4003, "lease not active");
        return;
      }
      const receipt = {
        agentId: frame.agent_id,
        instanceId: session.deliveryInstanceId,
        leaseId: frame.lease_id,
        messageId: frame.message_id,
        attemptNumber: frame.attempt_number,
        ...(frame.delivery_items === undefined ? {} : { deliveryItems: frame.delivery_items }),
        allowBatchWithoutDeliveryItems: frame.delivery_items === undefined
          && session.deliveryReceipts !== "delivery_items_v1",
      };
      try {
        if (frame.type === "injection_requested") {
          repository.recordInjectionRequested({ ...receipt, maxDeliveryAttempts });
          emit("delivery_injection_requested", {
            message_id: receipt.messageId,
            agent_id: receipt.agentId,
            attempt: receipt.attemptNumber,
            lease_id: receipt.leaseId,
          });
          return;
        }
        if (frame.type === "ack") {
          const acknowledged = repository.acknowledgeDelivery({ ...receipt, maxDeliveryAttempts });
          emit("delivery_acknowledged", {
            message_id: receipt.messageId,
            agent_id: receipt.agentId,
            attempt: receipt.attemptNumber,
            lease_id: receipt.leaseId,
          });
          if (session.ackConfirmation === "correlated_v1") {
            sendFrame(currentSocket, generateFrameId, {
              type: "delivery_acknowledged",
              request_id: frame.frame_id,
              agent_id: receipt.agentId,
              instance_id: session.instanceId,
              lease_id: receipt.leaseId,
              message_id: acknowledged.message_id,
              message_incarnation_id: acknowledged.message_incarnation_id,
              attempt_number: receipt.attemptNumber,
            });
          }
          offerNextMessage(currentSocket, session);
          return;
        }
        const result = repository.nackDelivery({
          ...receipt,
          reason: frame.reason,
          maxDeliveryAttempts,
        });
        emit("delivery_nacked", {
          message_id: receipt.messageId,
          agent_id: receipt.agentId,
          attempt: receipt.attemptNumber,
          lease_id: receipt.leaseId,
          reason: frame.reason,
          state: result.state,
        });
        if (result.state === "dead_letter") {
          emit("message_dead_lettered", {
            message_id: receipt.messageId,
            agent_id: receipt.agentId,
            attempt: receipt.attemptNumber,
            lease_id: receipt.leaseId,
            reason: frame.reason,
          });
          emitRecurringReminderRearmed(result, frame.reason);
        }
        offerNextMessage(currentSocket, session);
      } catch (error) {
        if (error instanceof LeaseNotActiveError) {
          sendError(currentSocket, generateFrameId, error.code, frame.frame_id);
          closeSocket(currentSocket, 4003, "lease not active");
          return;
        }
        if (error instanceof DeliveryNotActiveError) {
          sendError(currentSocket, generateFrameId, error.code, frame.frame_id);
          offerNextMessage(currentSocket, session);
          return;
        }
        throw error;
      }
    }

    function offerNextMessage(currentSocket, currentSession) {
      if (sessions.get(currentSession.agentId) !== currentSession
        || currentSocket.readyState !== WebSocket.OPEN
        || currentSession.deliveryPaused === true) return;
      try {
        const delivery = repository.claimNextMessage({
          agentId: currentSession.agentId,
          instanceId: currentSession.deliveryInstanceId,
          leaseId: currentSession.leaseId,
          deliveryLeaseDurationMs,
        });
        if (delivery === undefined) return;
        emit("delivery_offered", {
          message_id: delivery.message.id,
          agent_id: currentSession.agentId,
          attempt: delivery.attempt_number,
          lease_id: currentSession.leaseId,
          delivery_expires_at: delivery.delivery_expires_at,
          item_count: delivery.items?.length ?? 1,
        });
        // ponytail: legacy V1 clients never negotiated correlated delivery confirmation.
        sendFrame(currentSocket, generateFrameId, {
          type: "message",
          message: deliveryEnvelope(currentSession, delivery.message),
          ...(currentSession.ackConfirmation === "correlated_v1"
            ? { message_incarnation_id: delivery.message.message_incarnation_id }
            : {}),
          attempt_number: delivery.attempt_number,
          delivery_expires_at: delivery.delivery_expires_at,
          ...((delivery.items?.length ?? 1) > 1 ? { delivery_items: delivery.items } : {}),
        }, currentSession.ackConfirmation === "correlated_v1");
      } catch (error) {
        if (error instanceof LeaseNotActiveError) {
          sendError(currentSocket, generateFrameId, error.code);
          closeSocket(currentSocket, 4003, "lease not active");
          return;
        }
        throw error;
      }
    }
  });

  return {
    async listen() {
      synchronizeRegisteredServerJobs();
      server.listen({ host, port });
      await once(server, "listening");
      try {
        const address = server.address();
        if (address === null || typeof address === "string") {
          throw new Error("agent router did not bind a TCP address");
        }
        const hostname = address.address.includes(":") ? `[${address.address}]` : address.address;
        const websocketUrl = `ws://${hostname}:${address.port}${path}`;
        emit("router_started", {
          host: address.address,
          port: address.port,
          router_url: websocketUrl,
        });
        void sweepImageAttachments();
        sweepMessageLifecycles();
        timeoutSweepTimer = setInterval(() => { sweepMessageLifecycles(); void sweepImageAttachments(); }, timeoutSweepIntervalMs);
        mechaDeveloperUpdateReminderTimer = setInterval(
          sendMechaDeveloperUpdateReminder,
          mechaDeveloperUpdateReminderIntervalMs,
        );
        mechaDeveloperUpdateReminderTimer.unref?.();
        return websocketUrl;
      } catch (error) {
        await new Promise((resolve, reject) => {
          server.close((closeError) => (closeError === undefined ? resolve() : reject(closeError)));
        }).catch(() => {});
        emit("router_stopped");
        throw error;
      }
    },

    async close() {
      clearInterval(timeoutSweepTimer);
      clearInterval(mechaDeveloperUpdateReminderTimer);
      for (const timer of pendingDeliveryOffers.values()) clearTimeout(timer);
      pendingDeliveryOffers.clear();
      pendingModelControls.clear();
      for (const socket of webSocketServer.clients) {
        socket.terminate();
      }
      await new Promise((resolve) => webSocketServer.close(resolve));
      await new Promise((resolve, reject) => {
        server.close((error) => (error === undefined ? resolve() : reject(error)));
      });
      await memoryCatalog?.close?.();
      await sweepImageAttachments();
      emit("router_stopped");
    },

    enqueueMessage(envelope) {
      return enqueueMessage(envelope);
    },

    sweepDeliveryTimeouts,
    sweepExpiredUnclaimedMessages,
    sweepDueReminders,
    sweepDueServerJobs,
    sweepStaleAgentResources,
    sweepMessageLifecycles,
    sendMechaDeveloperUpdateReminder,
  };

  function sweepMessageLifecycles() {
    pruneExpiredRateLimitEntries(currentTimeMs(clock));
    pruneExpiredPendingModelControls();
    const staleAgents = sweepStaleAgentResources();
    const expired = sweepExpiredUnclaimedMessages();
    const timedOut = sweepDeliveryTimeouts();
    const reminderOutcomes = {};
    const serverJobOutcomes = {};
    const reminders = sweepDueReminders(reminderOutcomes);
    const serverJobs = sweepDueServerJobs(serverJobOutcomes);
    const conversations = sweepConversationHistory();
    let lifecycle;
    try {
      lifecycle = lifecycleV2Available
        ? repository.pruneSubagentLifecycle()
        : { expiredRunning: 0, tombstones: 0, ledger: 0, producers: 0, updates: [] };
    } catch (error) {
      traceLifecycle("router_lifecycle_heartbeat_error", { error_name: error?.name ?? "Error" });
      throw error;
    }
    // pruneSubagentLifecycle returns only after its transaction commits. Publish
    // retention terminals afterward, just as producer transitions are published.
    for (const update of lifecycle.updates) {
      broadcastLifecycleUpdate(
        lifecycleAgentSnapshot(update.parentAgentId, update.routerVersion),
        update.routerVersion,
      );
    }
    sweepExpiredAgentActivity();
    traceLifecycle("router_lifecycle_heartbeat", {
      available: lifecycleV2Available,
      subagent_count: lifecycle.expiredRunning,
      snapshot_count: lifecycle.updates.length,
      heartbeat_interval_ms: timeoutSweepIntervalMs,
    });
    const sweepCounts = {
      stale_agent_count: staleAgents.length,
      expired_message_count: expired.length,
      timed_out_delivery_count: timedOut.length,
      reminder_success_count: reminderOutcomes.success ?? 0,
      reminder_failure_count: reminderOutcomes.failure ?? 0,
      server_job_success_count: serverJobOutcomes.success ?? 0,
      server_job_failure_count: serverJobOutcomes.failure ?? 0,
      ...(conversations.deleted_count === 0
        ? {}
        : { conversation_message_pruned_count: conversations.deleted_count }),
      ...(lifecycle.expiredRunning === 0 ? {} : { lifecycle_running_expired_count: lifecycle.expiredRunning }),
      ...(lifecycle.tombstones === 0 ? {} : { lifecycle_tombstone_pruned_count: lifecycle.tombstones }),
      ...(lifecycle.ledger === 0 ? {} : { lifecycle_ledger_pruned_count: lifecycle.ledger }),
      ...(lifecycle.producers === 0 ? {} : { lifecycle_producer_pruned_count: lifecycle.producers }),
    };
    // An all-zero sweep records no work, and the sweep runs every second, so logging
    // every one of them is pure volume. Log the ones that did something, plus an
    // all-zero heartbeat at most once per sweepHeartbeatIntervalMs so the absence of
    // records still means something. Any record resets the interval: a busy router
    // never pays for a heartbeat on top of its real ones.
    const now = currentTimeMs(clock);
    if (lastSweepRecordMs === undefined) lastSweepRecordMs = now;
    const didWork = Object.values(sweepCounts).some((count) => count > 0);
    if (didWork || now - lastSweepRecordMs >= sweepHeartbeatIntervalMs) {
      emit("lifecycle_sweep_completed", sweepCounts);
      lastSweepRecordMs = now;
    }
    return { staleAgents, expired, timedOut, reminders, serverJobs, conversations, lifecycle };
  }

  function sendMechaDeveloperUpdateReminder() {
    const now = clock();
    const createdAt = now.toISOString();
    const bucket = Math.floor(now.getTime() / mechaDeveloperUpdateReminderIntervalMs);
    try {
      const result = enqueueMessage({
        id: `router-mecha-developer-update-reminder-${bucket}`,
        from: "router-update-reminder",
        to: "supervisor",
        subject: "Check mecha-developer updates",
        body: mechaDeveloperUpdateReminderBody(),
        created_at: createdAt,
      });
      emit("mecha_developer_update_reminder_sent", {
        message_id: result.message.id,
        created: result.created,
      });
      return result;
    } catch (error) {
      emit("router_error", {
        error_code: error?.code ?? "mecha_developer_update_reminder_failed",
      });
      return undefined;
    }
  }

  function sweepConversationHistory() {
    if (typeof repository.pruneConversationHistory !== "function") {
      return { deleted_count: 0, unreferenced_attachment_ids: [] };
    }
    const result = repository.pruneConversationHistory({
      retentionMs: DEFAULT_CONVERSATION_RETENTION_MS,
      historyLimit: DEFAULT_CONVERSATION_HISTORY_LIMIT,
    });
    void removeUnreferencedAttachments(result.unreferenced_attachment_ids);
    return result;
  }

  function sweepDeliveryTimeouts() {
    const recovered = repository.recoverTimedOutDeliveries({ maxDeliveryAttempts });
    for (const outcome of recovered) {
      emit("delivery_timed_out", {
        message_id: outcome.message_id,
        agent_id: outcome.agent_id,
        attempt: outcome.attempt,
        lease_id: outcome.lease_id,
        state: outcome.state,
        reason: "delivery_timeout",
      });
      if (outcome.state === "dead_letter") {
        emit("message_dead_lettered", {
          message_id: outcome.message_id,
          agent_id: outcome.agent_id,
          attempt: outcome.attempt,
          lease_id: outcome.lease_id,
          reason: "delivery_timeout",
        });
        emitRecurringReminderRearmed(outcome, "delivery_timeout");
      }
    }
    for (const session of sessions.values()) offerSession(session);
    return recovered;
  }

  function sweepExpiredUnclaimedMessages() {
    const expired = repository.recoverExpiredUnclaimedMessages();
    for (const messageId of expired) emit("message_expired", { message_id: messageId });
    return expired;
  }

  function sweepDueReminders(outcomes = {}) {
    if (typeof repository.fireDueReminders !== "function") return [];
    const scheduledForByReminderId = new Map();
    const results = repository.fireDueReminders({
      onAttempt: ({ reminderId, scheduledFor }) => {
        scheduledForByReminderId.set(reminderId, scheduledFor);
        emit("reminder_started", { reminder_id: reminderId, scheduled_for: scheduledFor });
      },
    });
    const fired = [];
    outcomes.success = 0;
    outcomes.failure = 0;
    for (const outcome of results) {
      if (outcome.reminder !== undefined) {
        const { reminder, message } = outcome;
        fired.push(outcome);
        outcomes.success += 1;
        emit("reminder_succeeded", {
          reminder_id: reminder.id,
          scheduled_for: scheduledForByReminderId.get(reminder.id),
          linked_message_id: message.id,
          agent_id: message.to,
        });
        if (outcome.conversation_created === true) {
          fanoutConversationMessage(outcome.conversation_message);
          sweepConversationHistory();
        }
        offerQueuedMessages(reminder.target);
        continue;
      }
      const { reminder_id: reminderId, error_code: code } = outcome;
      if (reminderId === undefined) continue;
      outcomes.failure += 1;
      emit("reminder_failed", {
        reminder_id: reminderId,
        scheduled_for: scheduledForByReminderId.get(reminderId),
        error_code: code,
      });
      try {
        onReminderError({ reminderId, code });
      } catch {
        // Scheduler observability must not interfere with later due reminders.
      }
    }
    return fired;
  }

  function synchronizeRegisteredServerJobs() {
    if (typeof repository.synchronizeServerJobs !== "function") return false;
    const jobs = repository.synchronizeServerJobs(registeredServerJobs);
    for (const job of jobs) {
      emit("server_job_registered", {
        job_key: job.job_key,
        definition_fingerprint: job.definition_fingerprint,
        scheduled_for: job.next_due_at,
      });
    }
    return true;
  }

  function sweepDueServerJobs(outcomes = {}) {
    if (typeof repository.dueServerJobs !== "function"
      || typeof repository.completeServerJobRun !== "function"
      || typeof repository.failServerJobRun !== "function") {
      return [];
    }
    const completed = [];
    outcomes.success = 0;
    outcomes.failure = 0;
    for (const job of repository.dueServerJobs()) {
      emit("server_job_started", {
        job_key: job.job_key,
        scheduled_for: job.next_due_at,
      });
      try {
        const result = repository.completeServerJobRun({
          jobKey: job.job_key,
          scheduledFor: job.next_due_at,
          envelopeForNow: (now) => serverJobEnvelope({
            key: job.job_key,
            scheduledFor: job.next_due_at,
            now,
          }),
        });
        if (result === undefined) continue;
        completed.push(result);
        outcomes.success += 1;
        emit("server_job_succeeded", {
          job_key: result.run.job_key,
          scheduled_for: result.run.scheduled_for,
          attempt: result.run.attempt_number,
          message_id: result.message.id,
        });
        offerQueuedMessages(result.message.to);
      } catch (error) {
        let failed;
        try {
          failed = repository.failServerJobRun({
            jobKey: job.job_key,
            scheduledFor: job.next_due_at,
            errorCode: serverJobErrorCode(error),
          });
        } catch {
          // A failed outcome marker must not block a later due server job.
        }
        if (failed === undefined) continue;
        outcomes.failure += 1;
        emit("server_job_failed", {
          job_key: failed.run.job_key,
          scheduled_for: failed.run.scheduled_for,
          attempt: failed.run.attempt_number,
          error_code: failed.run.error_code,
          terminal: failed.run.outcome === "failed_terminal",
        });
        try {
          onServerJobError({
            jobKey: job.job_key,
            scheduledFor: job.next_due_at,
            code: failed.run.error_code,
            terminal: failed.run.outcome === "failed_terminal",
          });
        } catch {
          // Scheduler observability must not interfere with later due server jobs.
        }
      }
    }
    return completed;
  }

  function sweepStaleAgentResources() {
    if (typeof repository.recoverStaleLeases === "function") {
      repository.recoverStaleLeases();
    }
    if (typeof repository.recoverStaleAgentResources !== "function") return [];
    return repository.recoverStaleAgentResources();
  }

  function sessionIsActive(candidate) {
    if (candidate.conversationSync === "v1") {
      return conversationSessions.get(candidate.agentId)?.get(candidate.instanceId) === candidate;
    }
    return sessions.get(candidate.agentId) === candidate;
  }

  function offerQueuedMessages(agentId) {
    if (agentId === undefined) {
      for (const session of sessions.values()) scheduleDeliveryOffer(session.agentId);
      return;
    }
    scheduleDeliveryOffer(agentId);
  }

  function scheduleDeliveryOffer(agentId) {
    // Hold the first offer for one short tick so rapid HTTP posts can merge before
    // any delivery attempt is created; claim-time batching still handles older queues.
    const session = sessions.get(agentId);
    if (session === undefined) return;
    if (deliveryOfferCoalesceMs === 0) {
      offerSession(session);
      return;
    }
    if (pendingDeliveryOffers.has(agentId)) return;
    const timer = setTimeout(() => {
      pendingDeliveryOffers.delete(agentId);
      const currentSession = sessions.get(agentId);
      if (currentSession !== undefined) offerSession(currentSession);
    }, deliveryOfferCoalesceMs);
    timer.unref?.();
    pendingDeliveryOffers.set(agentId, timer);
  }

  function cancelPendingDeliveryOffer(agentId) {
    const timer = pendingDeliveryOffers.get(agentId);
    if (timer === undefined) return;
    clearTimeout(timer);
    pendingDeliveryOffers.delete(agentId);
  }

  function sessionHasActiveLease(candidate, generatedAt = new Date(currentTimeMs(clock)).toISOString()) {
    if (!sessionIsActive(candidate)) return false;
    const lease = repository.leaseFor(candidate.agentId);
    return lease !== undefined
      && lease.lease_id === candidate.leaseId
      && lease.instance_id === candidate.deliveryInstanceId
      && lease.expires_at > generatedAt;
  }

  function pendingModelControlKey(sourceAgentId, controlId) {
    return `${sourceAgentId}\u0000${controlId}`;
  }

  function emitModelControlPendingRejected(frame, target, reason) {
    emit("model_control_pending_rejected", {
      control_id: frame.control_id,
      source_agent_id: frame.agent_id,
      source_instance_id: frame.instance_id,
      source_lease_id: frame.lease_id,
      target_agent_id: frame.target_agent_id,
      ...(target === undefined ? {} : {
        target_instance_id: target.instanceId,
        target_lease_id: target.leaseId,
      }),
      reason,
    });
  }

  function emitPendingModelControlRemoved(pending, reason) {
    emit("model_control_pending_removed", {
      control_id: pending.controlId,
      source_agent_id: pending.sourceAgentId,
      source_instance_id: pending.sourceInstanceId,
      source_lease_id: pending.sourceLeaseId,
      target_agent_id: pending.targetAgentId,
      target_instance_id: pending.targetInstanceId,
      target_lease_id: pending.targetLeaseId,
      reason,
    });
  }

  function pruneExpiredPendingModelControls(nowMs = currentTimeMs(clock)) {
    for (const [key, pending] of pendingModelControls.entries()) {
      if (pending.expiresAtMs > nowMs) continue;
      pendingModelControls.delete(key);
      emitPendingModelControlRemoved(pending, "expired");
      if (pending.sourceSession.socket.readyState === WebSocket.OPEN
        && sessionHasActiveLease(pending.sourceSession)) {
        sendError(
          pending.sourceSession.socket,
          generateFrameId,
          "model_control_expired",
          pending.requestFrameId,
        );
      }
    }
  }

  function removePendingModelControlsForSession(disconnectedSession) {
    for (const [key, pending] of pendingModelControls.entries()) {
      const sourceDisconnected = pending.sourceSession === disconnectedSession;
      const targetDisconnected = pending.targetSession === disconnectedSession;
      if (!sourceDisconnected && !targetDisconnected) continue;
      pendingModelControls.delete(key);
      emitPendingModelControlRemoved(
        pending,
        sourceDisconnected ? "source_disconnected" : "target_disconnected",
      );
      if (targetDisconnected
        && !sourceDisconnected
        && pending.sourceSession.socket.readyState === WebSocket.OPEN
        && sessionHasActiveLease(pending.sourceSession)) {
        sendError(
          pending.sourceSession.socket,
          generateFrameId,
          "target_not_connected",
          pending.requestFrameId,
        );
      }
    }
  }

  function observableAgentActivity(agentId, nowMs = currentTimeMs(clock)) {
    const activity = activityByAgentId.get(agentId);
    if (activity === undefined || activity.expiresAtMs <= nowMs) return "idle";
    if (activity.state === "compacting") return "compacting";
    return activity.activeUntilMs > nowMs ? "active" : "idle";
  }

  function observableSubagents(agentId, nowMs = currentTimeMs(clock)) {
    const activity = activityByAgentId.get(agentId);
    return activity !== undefined && activity.expiresAtMs > nowMs ? activity.subagents : [];
  }

  function observableSubagentCount(agentId, nowMs = currentTimeMs(clock)) {
    const activity = activityByAgentId.get(agentId);
    return activity !== undefined && activity.expiresAtMs > nowMs ? activity.subagentCount : 0;
  }

  function normalizeGoalProjection(value) {
    if (value === null || typeof value !== "object" || Array.isArray(value)
      || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
      || value.goal.length > 4_000) return undefined;
    if (!value.enabled && value.goal !== "") return undefined;
    if (value.enabled && value.goal.length === 0) return undefined;
    return { enabled: value.enabled, goal: value.enabled ? value.goal : "" };
  }

  function normalizeModelSummary(value) {
    if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
    const provider = typeof value.provider === "string" ? value.provider.trim().slice(0, 128) : "";
    const id = typeof value.id === "string" ? value.id.trim().slice(0, 256) : "";
    if (provider.length === 0 || id.length === 0) return undefined;
    const name = typeof value.name === "string" ? value.name.trim().slice(0, 256) : "";
    return { provider, id, ...(name.length > 0 ? { name } : {}) };
  }

  function normalizeModelSummaries(values) {
    if (!Array.isArray(values)) return [];
    const seen = new Set();
    return values.map(normalizeModelSummary).filter((model) => {
      if (model === undefined) return false;
      const key = `${model.provider}\u0000${model.id}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    }).slice(0, MAX_MODEL_SUMMARIES);
  }

  function normalizeSubagents(subagents, subagentCount = 0) {
    if (!Array.isArray(subagents)) return [];
    return subagents.map((summary) => {
      if (summary === null || typeof summary !== "object") return undefined;
      const id = typeof summary.id === "string" ? summary.id.trim().slice(0, 128) : "";
      const description = typeof summary.description === "string" ? summary.description.trim().slice(0, 512) : "";
      if (id.length === 0 || description.length === 0) return undefined;
      const type = typeof summary.type === "string" ? summary.type.trim().slice(0, 64) : "";
      const status = ["queued", "running", "completed", "steered", "aborted", "stopped", "error"].includes(summary.status) ? summary.status : undefined;
      return { id, ...(type.length > 0 ? { type } : {}), description, ...(status === undefined ? {} : { status }) };
    }).filter(Boolean).slice(0, Math.min(999, Math.max(0, subagentCount || 999)));
  }

  function sameSubagents(left = [], right = []) {
    return left.length === right.length && left.every((summary, index) => {
      const other = right[index];
      return other !== undefined && summary.id === other.id && summary.type === other.type && summary.description === other.description && summary.status === other.status;
    });
  }

  function sendLifecycleSnapshot(socket) {
    const snapshot = repository.subagentLifecycleSnapshot();
    traceLifecycle("router_lifecycle_snapshot_projected", {
      router_version: snapshot.router_version,
      snapshot_count: snapshot.agents.length,
      subagent_count: snapshot.agents.reduce((count, agent) => count + (agent.subagent_count ?? 0), 0),
    });
    sendFrame(socket, generateFrameId, {
      type: "subagent_state_snapshot",
      router_version: snapshot.router_version,
      agents: snapshot.agents,
    });
  }

  function lifecycleAgentSnapshot(agentId, version = undefined) {
    const snapshot = repository.subagentLifecycleSnapshot({ parentAgentId: agentId });
    const agent = snapshot.agents[0] ?? { agent_id: agentId, subagent_version: snapshot.router_version, subagent_count: 0, subagents: [] };
    traceLifecycle("router_lifecycle_projection_read", {
      agent_id: agentId,
      router_version: version ?? snapshot.router_version,
      subagent_count: agent.subagent_count,
      subagents: summarizeNativeSubagents(agent.subagents),
    });
    return { ...agent, ...(version === undefined ? {} : { subagent_version: version }) };
  }

  function broadcastLifecycleUpdate(agent, routerVersion) {
    let recipientCount = 0;
    for (const recipient of conversationSessions.values()) {
      for (const candidate of recipient.values()) {
        if (candidate.lifecycleEnabled !== true) continue;
        recipientCount += 1;
        sendFrame(candidate.socket, generateFrameId, {
          type: "subagent_state_update",
          router_version: routerVersion,
          agent,
        });
      }
    }
    traceLifecycle("router_lifecycle_projected", {
      agent_id: agent.agent_id,
      router_version: routerVersion,
      recipient_count: recipientCount,
      subagent_count: agent.subagent_count,
      subagents: summarizeNativeSubagents(agent.subagents),
    });
  }

  function broadcastAgentActivity(agentId, activityState, subagentCount = 0, subagents = [], generatedAt = new Date(currentTimeMs(clock)).toISOString(), instanceId, goal, currentModel) {
    for (const recipient of conversationSessions.values()) {
      for (const session of recipient.values()) {
        sendFrame(session.socket, generateFrameId, {
          type: "agent_activity",
          agent_id: agentId,
          ...(instanceId === undefined ? {} : { instance_id: instanceId }),
          activity_state: activityState,
          subagent_count: subagentCount,
          subagents,
          ...(goal === undefined ? {} : { goal }),
          ...(currentModel === undefined ? {} : { current_model: currentModel }),
          generated_at: generatedAt,
        });
      }
    }
  }

  function broadcastAgentModelCatalog(agentId, instanceId, modelSummaries, generatedAt = new Date(currentTimeMs(clock)).toISOString()) {
    const currentModel = modelByAgentId.get(agentId)?.currentModel;
    for (const recipient of conversationSessions.values()) {
      for (const session of recipient.values()) {
        sendFrame(session.socket, generateFrameId, {
          type: "agent_model_catalog",
          agent_id: agentId,
          instance_id: instanceId,
          model_summaries: modelSummaries,
          ...(currentModel === undefined ? {} : { current_model: currentModel }),
          generated_at: generatedAt,
        });
      }
    }
  }

  function broadcastMessageArrival({ agentId, from, subject, source, preview, body, createdAt, messageId }) {
    for (const recipient of conversationSessions.values()) {
      for (const session of recipient.values()) {
        sendFrame(session.socket, generateFrameId, {
          type: "message_arrival",
          agent_id: agentId,
          from,
          ...(subject === undefined ? {} : { subject }),
          source,
          preview,
          body,
          created_at: createdAt,
          message_id: messageId,
        });
      }
    }
  }

  function updateAgentActivity(agentId, activityState, subagentCount = 0, subagents, incomingGoal, instanceId, incomingModel) {
    const nowMs = currentTimeMs(clock);
    const generatedAt = new Date(nowMs).toISOString();
    const currentGoal = goalByAgentId.get(agentId);
    if (currentGoal !== undefined && instanceId !== undefined && currentGoal.instanceId !== instanceId) goalByAgentId.delete(agentId);
    const normalizedGoal = incomingGoal === undefined ? undefined : normalizeGoalProjection(incomingGoal);
    if (normalizedGoal !== undefined && instanceId !== undefined) goalByAgentId.set(agentId, { instanceId, goal: normalizedGoal });
    const goal = goalByAgentId.get(agentId)?.goal;
    const normalizedModel = incomingModel === undefined ? undefined : normalizeModelSummary(incomingModel);
    const previousProjection = modelByAgentId.get(agentId);
    const previousModel = previousProjection?.currentModel;
    if (normalizedModel !== undefined
      && (previousProjection === undefined || previousProjection.instanceId === instanceId)) {
      modelByAgentId.set(agentId, {
        instanceId,
        currentModel: normalizedModel,
        availableModels: previousProjection?.availableModels ?? [],
      });
    }
    const modelProjection = modelByAgentId.get(agentId);
    const previousState = observableAgentActivity(agentId, nowMs);
    const previousCount = observableSubagentCount(agentId, nowMs);
    const previousSubagents = observableSubagents(agentId, nowMs);
    const summaries = normalizeSubagents(subagents, subagentCount);
    const nextCount = Math.min(999, Math.max(0, subagentCount, summaries.length));
    const nextState = activityState === "compacting" ? "compacting" : activityState === "active" ? "active" : "idle";
    const session = sessions.get(agentId);
    if (session !== undefined) session.deliveryPaused = nextState === "compacting";
    const visibleCount = nextState === "compacting" ? 0 : nextCount;
    const visibleSubagents = nextState === "compacting" ? [] : summaries;
    const previousGoal = currentGoal?.goal;
    if (nextState === "active" || nextState === "compacting" || nextCount > 0) {
      activityByAgentId.set(agentId, {
        state: nextState,
        instanceId,
        activeUntilMs: nextState === "active" ? nowMs + activityTtlMs : nowMs,
        expiresAtMs: nextState === "compacting" ? Number.POSITIVE_INFINITY : nowMs + activityTtlMs,
        subagentCount: visibleCount,
        subagents: visibleSubagents,
      });
      if (previousState !== nextState || previousCount !== visibleCount || !sameSubagents(previousSubagents, visibleSubagents) || JSON.stringify(previousGoal) !== JSON.stringify(goal)
        || JSON.stringify(previousModel) !== JSON.stringify(modelProjection?.currentModel)) {
        broadcastAgentActivity(agentId, nextState, visibleCount, visibleSubagents, generatedAt, instanceId, goal, modelProjection?.currentModel);
      }
      if (previousState === "compacting" && nextState !== "compacting" && session !== undefined) {
        session.deliveryPaused = false;
        offerQueuedMessages(agentId);
      }
      return;
    }
    activityByAgentId.delete(agentId);
    if (previousState === "compacting" && session !== undefined) {
      session.deliveryPaused = false;
      offerQueuedMessages(agentId);
    }
    if (previousState !== "idle" || previousCount !== 0 || previousSubagents.length > 0 || JSON.stringify(previousGoal) !== JSON.stringify(goal)
      || JSON.stringify(previousModel) !== JSON.stringify(modelProjection?.currentModel)) {
      broadcastAgentActivity(agentId, "idle", 0, [], generatedAt, instanceId, goal, modelProjection?.currentModel);
    }
  }

  function clearAgentActivity(agentId) {
    const instanceId = sessions.get(agentId)?.instanceId ?? goalByAgentId.get(agentId)?.instanceId;
    updateAgentActivity(agentId, "settled", 0, undefined, { enabled: false, goal: "" }, instanceId);
    goalByAgentId.delete(agentId);
  }

  function sweepExpiredAgentActivity() {
    const nowMs = currentTimeMs(clock);
    const generatedAt = new Date(nowMs).toISOString();
    for (const [agentId, activity] of activityByAgentId.entries()) {
      if (activity.expiresAtMs > nowMs) continue;
      activityByAgentId.delete(agentId);
      const session = sessions.get(agentId);
      if (session !== undefined && session.deliveryPaused === true) {
        session.deliveryPaused = false;
        offerQueuedMessages(agentId);
      }
      const modelProjection = modelByAgentId.get(agentId);
      broadcastAgentActivity(
        agentId,
        "idle",
        0,
        [],
        generatedAt,
        activity.instanceId,
        goalByAgentId.get(agentId)?.instanceId === activity.instanceId ? goalByAgentId.get(agentId).goal : undefined,
        modelProjection?.currentModel,
      );
    }
  }

  function connectedAgentsSnapshot() {
    const generatedAt = new Date(currentTimeMs(clock)).toISOString();
    const agents = [];
    const lifecycleSnapshot = lifecycleV2Available ? repository.subagentLifecycleSnapshot() : { router_version: 0, agents: [] };
    const lifecycleByAgent = new Map(lifecycleSnapshot.agents.map((agent) => [agent.agent_id, agent]));
    for (const session of sessions.values()) {
      const lease = repository.leaseFor(session.agentId);
      if (session.socket.readyState !== WebSocket.OPEN || !sessionHasActiveLease(session, generatedAt)) continue;
      const producer = lifecycleV2Available ? repository.subagentLifecycleProducerFor(session.agentId) : undefined;
      const lifecycle = lifecycleByAgent.get(session.agentId)
        ?? (producer?.authority === "v2" ? lifecycleAgentSnapshot(session.agentId) : undefined);
      const modelProjection = modelByAgentId.get(session.agentId)?.instanceId === session.instanceId
        ? modelByAgentId.get(session.agentId)
        : undefined;
      agents.push({
        agent_id: session.agentId,
        agent_role: session.agentRole,
        connected_at: session.connectedAt,
        lease_expires_at: lease.expires_at,
        activity_state: observableAgentActivity(session.agentId),
        subagent_count: lifecycle?.subagent_count ?? observableSubagentCount(session.agentId),
        subagents: lifecycle?.subagents ?? observableSubagents(session.agentId),
        ...(modelProjection?.currentModel === undefined
          ? {}
          : { current_model: modelProjection.currentModel }),
        available_models: modelProjection?.availableModels ?? [],
        ...(goalByAgentId.get(session.agentId)?.instanceId === session.instanceId
          ? { goal: goalByAgentId.get(session.agentId).goal }
          : {}),
        ...(lifecycle === undefined ? {} : { subagent_version: lifecycle.subagent_version }),
      });
    }
    agents.sort((left, right) => left.agent_id < right.agent_id ? -1 : left.agent_id > right.agent_id ? 1 : 0);
    return { generated_at: generatedAt, activity_snapshot_version: lifecycleSnapshot.router_version, agents };
  }

  function deliveryEnvelope(recipientSession, message) {
    const envelope = toEnvelope(message);
    // Batched bodies carry per-item reply hints; a single sender footer would be misleading.
    if (envelope.body.startsWith("Mailbox message batch\n\n")) return envelope;
    // Sentinel hands the stored opaque prompt to Pi unchanged; Mailbox reply text is parent-chat-only metadata.
    if (message.sentinel !== undefined || message.from === recipientSession.agentId) return envelope;

    const senderSession = sessions.get(message.from);
    const now = new Date(currentTimeMs(clock)).toISOString();
    const senderLease = senderSession === undefined ? undefined : repository.leaseFor(message.from);
    if (senderSession === undefined
      || senderSession.socket.readyState !== WebSocket.OPEN
      || senderLease === undefined
      || senderLease.lease_id !== senderSession.leaseId
      || senderLease.instance_id !== senderSession.deliveryInstanceId
      || senderLease.expires_at <= now) return envelope;

    const footer = `\n\n[Reply via mailbox to ${message.from}.]`;
    const maxBodyBytes = repository.admissionLimits?.().maxBodyBytes ?? DEFAULT_MAX_BODY_BYTES;
    if (Buffer.byteLength(envelope.body, "utf8") + Buffer.byteLength(footer, "utf8") > maxBodyBytes) {
      return envelope;
    }
    return { ...envelope, body: `${envelope.body}${footer}` };
  }

  function enqueueMessage(envelope, options = undefined) {
    const sourcedEnvelope = { ...envelope, source: normalizeMessageSource(envelope.source, envelope) };
    const result = repository.enqueue(sourcedEnvelope, {
      ...options,
      disableBatching: conversationSessions.has(sourcedEnvelope.to),
    });
    emit("message_enqueued", {
      message_id: result.message.id,
      to: result.message.to,
      agent_id: result.message.to,
      state: result.message.state,
      created: result.created,
      ...(result.deduped === true ? { deduped: true } : {}),
    });
    if (result.conversation_created === true) {
      fanoutConversationMessage(result.conversation_message);
      sweepConversationHistory();
    }
    offerQueuedMessages(result.message.to);
    return result;
  }

  function normalizeMessageSource(source, envelope) {
    if (source !== undefined && source !== null && typeof source === "object" && !Array.isArray(source)) {
      const kind = source.kind;
      if (["agent", "web_ui", "android", "discord", "reminder", "system", "unknown"].includes(kind)) return { kind };
    }
    if (envelope?.subject === "discord.inbound.v1") return { kind: "discord" };
    const senderSessions = conversationSessions.get(envelope?.from);
    if (senderSessions !== undefined) {
      for (const session of senderSessions.values()) {
        if (session.clientType === "web") return { kind: "web_ui" };
        if (session.clientType === "android") return { kind: "android" };
      }
    }
    return { kind: "agent" };
  }

  function fanoutConversationMessage(message) {
    const recipients = new Set([
      ...(conversationSessions.get(message.from)?.values() ?? []),
      ...(conversationSessions.get(message.to)?.values() ?? []),
    ]);
    for (const recipient of recipients) {
      sendFrame(recipient.socket, generateFrameId, {
        type: "conversation_message",
        message,
      });
    }
  }

  async function handleHttpRequest(request, response) {
    const requestUrl = new URL(request.url ?? "/", "http://router.local");
    if (isBrowserCorsRoute(request.method, requestUrl.pathname)) applyCorsHeaders(response);
    if (request.method === "OPTIONS") {
      if (isBrowserCorsRoute(request.method, requestUrl.pathname)) {
        response.writeHead(204, { "cache-control": "no-store" });
        response.end();
      } else {
        sendHttpError(response, 404, "not_found");
      }
      return;
    }
    if (request.method === "GET" && requestUrl.pathname === "/v1/health") {
      sendHttpJson(response, 200, {
        status: "ok",
        version: "v1",
        schema_migrations: repository.migrationIds(),
      });
      return;
    }
    if (request.method === "GET" && requestUrl.pathname === "/v1/capabilities") {
      sendHttpJson(response, 200, {
        model_catalog_v1: true,
        model_catalog_limits: { frame_bytes: Math.min(MODEL_CATALOG_MAX_FRAME_BYTES, maxFrameBytes) },
        subagent_lifecycle_v2: lifecycleV2Available,
        ...(lifecycleV2Available ? { lifecycle_limits: { subagents: 999, id_bytes: 128, description_bytes: 512 } } : { lifecycle_error: "migration_017_required" }),
      });
      return;
    }
    if (request.method === "GET" && requestUrl.pathname === "/v1/memory/policy") {
      sendHttpJson(response, 200, { recall: RECALL_POLICY });
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/memory/rebuild") {
      await handleMemoryRequest(request, response, async () => ({ records: await memoryCatalog.rebuild() }));
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/memory/lookup") {
      await handleMemoryRequest(request, response, async (payload) => ({ records: await memoryCatalog.lookup(payload) }));
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/memory/expand") {
      await handleMemoryRequest(request, response, async (payload) => ({ records: await memoryCatalog.expand(payload) }));
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/memory/apply") {
      await handleMemoryRequest(request, response, async (payload) => await memoryCatalog.apply(payload));
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/memory/touch") {
      await handleMemoryRequest(request, response, async (payload) => ({ relevant_at: await memoryCatalog.touch(payload) }));
      return;
    }
    if (request.method === "GET" && requestUrl.pathname === "/v1/agents") {
      sendHttpJson(response, 200, connectedAgentsSnapshot());
      return;
    }
    if (request.method === "GET" && requestUrl.pathname === "/v1/conversations") {
      try {
        const owner = requestUrl.searchParams.get("owner");
        const peer = requestUrl.searchParams.get("peer");
        if (owner === null || peer === null) throw new TypeError("owner and peer are required");
        const messages = repository.conversationHistory({ owner, peer });
        sendHttpJson(response, 200, {
          owner,
          peer,
          high_water_sequence: repository.conversationHighWaterSequence(),
          messages,
        });
      } catch (error) {
        if (error instanceof TypeError) sendHttpError(response, 400, "invalid_request");
        else throw error;
      }
      return;
    }
    const attachmentId = matchAttachmentRoute(requestUrl.pathname);
    if (request.method === "GET" && attachmentId !== undefined) {
      await serveImageAttachment(response, attachmentId);
      return;
    }
    if (request.method === "POST" && requestUrl.pathname === "/v1/messages") {
      await enqueueHttpMessage(request, response);
      return;
    }
    if (requestUrl.pathname === "/v1/reminders") {
      await handleReminderCollection(request, response);
      return;
    }

    const reminderRoute = matchReminderRoute(requestUrl.pathname);
    if (reminderRoute !== undefined) {
      await handleReminderItem(request, response, reminderRoute);
      return;
    }

    const messageRoute = matchMessageRoute(requestUrl.pathname);
    if (messageRoute !== undefined) {
      const { messageId, action } = messageRoute;
      if (request.method === "GET" && action === undefined) {
        const message = repository.messageFor(messageId);
        if (message === undefined) sendHttpError(response, 404, "not_found");
        else sendHttpJson(response, 200, { message });
        return;
      }
      if (request.method === "POST" && action === "retry") {
        try {
          if (repository.messageFor(messageId) === undefined) {
            sendHttpError(response, 404, "not_found");
            return;
          }
          const message = repository.retryDeadLetter(messageId);
          if (message === undefined) sendHttpError(response, 409, "dead_letter_required");
          else {
            emit("message_retried", {
              message_id: message.id,
              agent_id: message.to,
              state: message.state,
            });
            offerQueuedMessages(message.to);
            sendHttpJson(response, 200, { message });
          }
        } catch (error) {
          if (error instanceof QueueLimitError) sendHttpError(response, 429, error.code);
          else throw error;
        }
        return;
      }
      if (request.method === "DELETE" && action === undefined) {
        if (repository.messageFor(messageId) === undefined) {
          sendHttpError(response, 404, "not_found");
          return;
        }
        if (!repository.dismissDeadLetter(messageId)) {
          sendHttpError(response, 409, "dead_letter_required");
        } else {
          response.writeHead(204, { "cache-control": "no-store" });
          response.end();
        }
        return;
      }
    }
    sendHttpError(response, 404, "not_found");
  }

  async function handleMemoryRequest(request, response, operation) {
    if (memoryCatalog === undefined) { sendHttpError(response, 404, "not_found"); return; }
    if (!isJsonContentType(request.headers["content-type"])) { sendHttpError(response, 400, "invalid_request"); return; }
    try { sendHttpJson(response, 200, await operation(await readJsonRequest(request, maxRequestBytes))); }
    catch (error) {
      if (error instanceof HttpRequestError || error instanceof MemoryCatalogError) sendHttpError(response, error.statusCode ?? 400, "invalid_request");
      else throw error;
    }
  }

  async function handleReminderCollection(request, response) {
    if (request.method === "GET") {
      sendHttpJson(response, 200, { reminders: repository.reminders() });
      return;
    }
    if (request.method === "DELETE") {
      try {
        const result = repository.cancelAllReminders();
        sendHttpJson(response, 200, result);
      } catch (error) {
        if (error instanceof ReminderCancellationConflictError) {
          sendReminderConflict(response, error);
        } else {
          throw error;
        }
      }
      return;
    }
    if (request.method !== "POST") {
      sendHttpError(response, 404, "not_found");
      return;
    }
    if (!isJsonContentType(request.headers["content-type"])) {
      sendHttpError(response, 400, "invalid_request");
      return;
    }
    try {
      const reminder = await readJsonRequest(request, maxRequestBytes);
      const candidate = validateReminder(reminder, {
        maxBodyBytes: repository.admissionLimits?.().maxBodyBytes ?? DEFAULT_MAX_BODY_BYTES,
      });
      if (repository.reminderFor(candidate.id) === undefined) {
        enforceRateLimit(candidate.creator);
      }
      const result = repository.createReminder(reminder);
      if (result.created) sweepDueReminders();
      const current = repository.reminderFor(reminder.id);
      if (current === undefined) {
        sendHttpError(response, 409, "reminder_target_unavailable");
        return;
      }
      sendHttpJson(response, result.created ? 201 : 200, {
        reminder: current,
      });
    } catch (error) {
      if (error instanceof HttpRequestError) {
        sendHttpError(response, error.statusCode, error.code);
      } else if (error instanceof ReminderValidationError) {
        sendHttpError(response, 400, error.code);
      } else if (error instanceof ProtocolValidationError) {
        sendHttpError(response, 400, error.code);
      } else if (error instanceof ReminderIdempotencyConflictError) {
        sendHttpError(response, 409, error.code);
      } else if (error instanceof RateLimitError) {
        sendHttpError(response, 429, error.code, { "retry-after": String(error.retryAfterSeconds) });
      } else {
        throw error;
      }
    }
  }

  async function handleReminderItem(request, response, reminderId) {
    if (request.method === "PATCH") {
      if (!isJsonContentType(request.headers["content-type"])) {
        sendHttpError(response, 400, "invalid_request");
        return;
      }
      try {
        const patch = await readJsonRequest(request, maxRequestBytes);
        const result = repository.updateReminder(reminderId, patch);
        if (result === undefined) {
          sendHttpError(response, 404, "not_found");
          return;
        }
        // Re-sweep after a schedule change so a now-due reminder fires promptly,
        // matching the post-create behavior in handleReminderCollection.
        if (result.schedulingChanged) sweepDueReminders();
        sendHttpJson(response, 200, { reminder: result.reminder });
      } catch (error) {
        if (error instanceof HttpRequestError) {
          sendHttpError(response, error.statusCode, error.code);
        } else if (error instanceof ReminderValidationError) {
          sendHttpError(response, 400, error.code);
        } else if (error instanceof ProtocolValidationError) {
          sendHttpError(response, 400, error.code);
        } else {
          throw error;
        }
      }
      return;
    }
    if (request.method !== "DELETE") {
      sendHttpError(response, 404, "not_found");
      return;
    }
    try {
      const result = repository.cancelReminder(reminderId);
      if (result === undefined) {
        sendHttpError(response, 404, "not_found");
      } else {
        response.writeHead(204, { "cache-control": "no-store" });
        response.end();
      }
    } catch (error) {
      if (error instanceof ReminderValidationError) {
        sendHttpError(response, 400, error.code);
      } else if (error instanceof ReminderCancellationConflictError) {
        sendReminderConflict(response, error);
      } else {
        throw error;
      }
    }
  }

  async function enqueueHttpMessage(request, response) {
    if (!isJsonContentType(request.headers["content-type"])) {
      sendHttpError(response, 400, "invalid_request");
      return;
    }
    let envelope;
    let materialized;
    let attachments = [];
    let conversationBody = "";
    try {
      const payload = await readJsonRequest(request, maxRequestBytes);
      conversationBody = String(payload?.body ?? "");
      rejectEscapedNewlines(conversationBody);
      materialized = materializeImageAttachments(payload);
      attachments = materialized.attachments;
      envelope = validateEnvelope(materialized.envelope, {
        maxBodyBytes: repository.admissionLimits?.().maxBodyBytes ?? DEFAULT_MAX_BODY_BYTES,
      });
    } catch (error) {
      if (error instanceof HttpRequestError) {
        sendHttpError(response, error.statusCode, error.code);
      } else if (error instanceof ProtocolValidationError) {
        sendHttpError(response, error.code === "payload_too_large" ? 413 : 400, error.code);
      } else {
        sendHttpError(response, 400, "invalid_request");
      }
      return;
    }

    try {
      if (repository.messageFor(envelope.id) === undefined) enforceRateLimit(envelope.from);
      await writeImageAttachments(materialized);
      const result = enqueueMessage(envelope, {
        conversation: { body: conversationBody, attachments },
      });
      sendHttpJson(response, result.created ? 201 : 200, {
        message: {
          ...(result.batched ? { ...result.message, batched: true } : result.message),
          ...(attachments.length === 0 ? {} : { attachments }),
        },
        ...(result.conversation_message === undefined
          ? {}
          : { conversation_message: result.conversation_message }),
      });
    } catch (error) {
      await cleanupWrittenImageAttachments(materialized, envelope === undefined ? undefined : repository.messageFor(envelope.id));
      if (error instanceof IdempotencyConflictError) {
        sendHttpError(response, 409, error.code);
      } else if (error instanceof QueueLimitError) {
        sendHttpError(response, 429, error.code);
      } else if (error instanceof ProtocolValidationError) {
        sendHttpError(response, error.code === "payload_too_large" ? 413 : 400, error.code);
      } else if (error instanceof RateLimitError) {
        sendHttpError(response, 429, error.code, { "retry-after": String(error.retryAfterSeconds) });
      } else {
        emit("router_error", {
          message_id: envelope.id,
          agent_id: envelope.to,
          error_code: "internal_error",
        });
        sendHttpError(response, 500, "internal_error");
      }
    }
  }

  function materializeImageAttachments(payload) {
    const { attachments: rawAttachments = [], ...candidate } = payload && typeof payload === "object" && !Array.isArray(payload) ? payload : {};
    if (!Array.isArray(rawAttachments)) throw new ProtocolValidationError();
    if (rawAttachments.length > maxImageAttachments) throw new ProtocolValidationError("payload_too_large");
    if (rawAttachments.length === 0) return { envelope: candidate, attachments: [], files: [] };

    let totalBytes = 0;
    const files = [];
    const attachments = [];
    for (let index = 0; index < rawAttachments.length; index += 1) {
      const attachment = rawAttachments[index];
      const decoded = decodeImageDataUrl(attachment, { maxImageAttachmentBytes });
      totalBytes += decoded.bytes.length;
      if (totalBytes > maxImageAttachmentTotalBytes) throw new ProtocolValidationError("payload_too_large");
      const hash = createHash("sha256").update(decoded.bytes).digest("hex");
      const messageId = safeAttachmentMessageId(candidate.id);
      const fileName = `${messageId}-${index + 1}-${hash.slice(0, 24)}.${decoded.extension}`;
      const filePath = join(imageAttachmentDirectory, fileName);
      files.push({ filePath, bytes: decoded.bytes });
      attachments.push({
        id: fileName,
        url: `/v1/attachments/${encodeURIComponent(fileName)}`,
        mime_type: decoded.mimeType,
        size_bytes: decoded.bytes.length,
        name: sanitizeAttachmentName(attachment.name),
      });
    }

    return { envelope: { ...candidate, body: attachmentBody(candidate.body, files) }, attachments, files };
  }

  async function writeImageAttachments(materialized) {
    if (!materialized?.files?.length) return;
    await ensureSafeImageAttachmentDirectory({ create: true });
    for (const file of materialized.files) {
      try {
        if (!(await lstat(file.filePath)).isFile()) throw new ProtocolValidationError();
      } catch (error) {
        if (error instanceof ProtocolValidationError) throw error;
        if (error?.code !== "ENOENT") throw error;
      }
      await writeFile(file.filePath, file.bytes, { mode: 0o600 });
    }
  }

  async function cleanupWrittenImageAttachments(materialized, existingMessage) {
    if (!materialized?.files?.length) return;
    const existingBody = String(existingMessage?.body ?? "");
    await Promise.all(materialized.files
      .filter((file) => !existingBody.includes(file.filePath))
      .map((file) => rm(file.filePath, { force: true }).catch(() => {})));
  }

  async function ensureSafeImageAttachmentDirectory({ create = false } = {}) {
    try {
      if (create) await mkdir(imageAttachmentDirectory, { recursive: true, mode: 0o700 });
      const metadata = await lstat(imageAttachmentDirectory);
      if (!metadata.isDirectory() || metadata.isSymbolicLink()) throw new ProtocolValidationError();
      if (typeof process.getuid === "function" && metadata.uid !== process.getuid()) throw new ProtocolValidationError();
      if ((metadata.mode & 0o077) !== 0) throw new ProtocolValidationError();
    } catch (error) {
      if (error?.code === "ENOENT" && !create) return false;
      throw error;
    }
    return true;
  }

  async function serveImageAttachment(response, attachmentId) {
    const safeId = basename(attachmentId);
    if (safeId !== attachmentId || !/^[A-Za-z0-9._-]+\.(?:png|jpg|webp|gif)$/.test(safeId)) {
      sendHttpError(response, 404, "not_found");
      return;
    }
    const filePath = join(imageAttachmentDirectory, safeId);
    try {
      await ensureSafeImageAttachmentDirectory();
      const linkMetadata = await lstat(filePath);
      const metadata = await stat(filePath);
      const retained = repository.conversationAttachmentReferenced?.(safeId) === true;
      if (!linkMetadata.isFile() || !metadata.isFile()
        || (!retained && metadata.mtimeMs < currentTimeMs(clock) - imageAttachmentTtlMs)) {
        sendHttpError(response, 404, "not_found");
        return;
      }
      const mimeType = mimeTypeForAttachmentId(safeId);
      response.writeHead(200, { "cache-control": "no-store", "content-type": mimeType });
      response.end(await readFile(filePath));
    } catch {
      sendHttpError(response, 404, "not_found");
    }
  }

  async function sweepImageAttachments() {
    try {
      if (!await ensureSafeImageAttachmentDirectory()) return;
      const cutoff = currentTimeMs(clock) - imageAttachmentTtlMs;
      const entries = await readdir(imageAttachmentDirectory, { withFileTypes: true });
      await Promise.all(entries
        .filter((entry) => entry.isFile())
        .map(async (entry) => {
          const filePath = join(imageAttachmentDirectory, entry.name);
          try {
            const metadata = await stat(filePath);
            const retained = repository.conversationAttachmentReferenced?.(entry.name) === true;
            if (!retained && metadata.mtimeMs < cutoff) await rm(filePath, { force: true });
          } catch {
            // Best-effort temp cleanup should not affect router availability.
          }
        }));
    } catch {
      // The temp directory may not exist yet.
    }
  }

  async function removeUnreferencedAttachments(attachmentIds) {
    if (!Array.isArray(attachmentIds) || attachmentIds.length === 0) return;
    try {
      if (!await ensureSafeImageAttachmentDirectory()) return;
      await Promise.all(attachmentIds.map((attachmentId) => {
        const safeId = basename(attachmentId);
        if (safeId !== attachmentId || !/^[A-Za-z0-9._-]+\.(?:png|jpg|webp|gif)$/.test(safeId)) return undefined;
        return rm(join(imageAttachmentDirectory, safeId), { force: true });
      }));
    } catch {
      // Retention cleanup is retried by the regular image sweep.
    }
  }

  function enforceRateLimit(sender) {
    const now = currentTimeMs(clock);
    pruneExpiredRateLimitEntries(now);
    const active = requestTimesBySender.get(sender) ?? [];
    if (active.length >= maxRequestsPerWindow) {
      requestTimesBySender.set(sender, active);
      const retryAfterSeconds = Math.max(1, Math.ceil((active[0] + rateLimitWindowMs - now) / 1_000));
      throw new RateLimitError(retryAfterSeconds);
    }
    active.push(now);
    requestTimesBySender.set(sender, active);
  }

  function pruneExpiredRateLimitEntries(now) {
    for (const [sender, timestamps] of requestTimesBySender) {
      const active = timestamps.filter((timestamp) => timestamp > now - rateLimitWindowMs);
      if (active.length === 0) requestTimesBySender.delete(sender);
      else if (active.length !== timestamps.length) requestTimesBySender.set(sender, active);
    }
  }

  function emitRecurringReminderRearmed(outcome, reason) {
    const rearmed = outcome.recurring_reminder_rearmed;
    if (rearmed === undefined) return;
    for (const reminder of Array.isArray(rearmed) ? rearmed : [rearmed]) {
      emit("recurring_reminder_rearmed", {
        reminder_id: reminder.reminder_id,
        message_id: outcome.message_id,
        next_due_at: reminder.next_due_at,
        reason,
      });
    }
  }

  function emit(event, fields = {}) {
    try {
      eventLogger(event, fields);
    } catch {
      // A diagnostic sink is never part of the authoritative delivery path.
    }
  }

  function offerSession(session) {
    try {
      session.offer();
    } catch (error) {
      emit("router_error", {
        agent_id: session.agentId,
        instance_id: session.instanceId,
        lease_id: session.leaseId,
        error_code: typeof error?.code === "string" ? error.code : "internal_error",
      });
      try {
        onOfferError({
          agentId: session.agentId,
          code: typeof error?.code === "string" ? error.code : "internal_error",
        });
      } catch {
        // Observability must not change the result of an already durable mutation.
      }
    }
  }
}

class HttpRequestError extends Error {
  constructor(statusCode, code) {
    super(code);
    this.statusCode = statusCode;
    this.code = code;
  }
}

class RateLimitError extends Error {
  constructor(retryAfterSeconds) {
    super("sender rate limit exceeded");
    this.code = "rate_limited";
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

function defaultOfferError({ agentId, code }) {
  console.error(JSON.stringify({
    event: "delivery_offer_failed",
    agent_id: agentId,
    code,
  }));
}

function defaultReminderError({ reminderId, code }) {
  console.error(JSON.stringify({
    event: "reminder_fire_failed",
    reminder_id: reminderId,
    code,
  }));
}

function defaultServerJobError({ jobKey, scheduledFor, code, terminal }) {
  console.error(JSON.stringify({
    event: "server_job_run_failed",
    job_key: jobKey,
    scheduled_for: scheduledFor,
    code,
    terminal,
  }));
}

function serverJobErrorCode(error) {
  return typeof error?.code === "string" && /^[a-z][a-z0-9_]{0,63}$/.test(error.code)
    ? error.code
    : "internal_error";
}

function currentTimeMs(clock) {
  const now = clock();
  if (!(now instanceof Date) || Number.isNaN(now.getTime())) {
    throw new TypeError("clock must return a valid Date");
  }
  return now.getTime();
}

function isJsonContentType(value) {
  return typeof value === "string" && /^application\/json(?:\s*;|$)/i.test(value);
}

function matchMessageRoute(pathname) {
  const match = /^\/v1\/messages\/([^/]+)(?:\/(retry))?$/.exec(pathname);
  if (match === null) return undefined;
  try {
    return { messageId: decodeURIComponent(match[1]), action: match[2] };
  } catch {
    return undefined;
  }
}

function matchAttachmentRoute(pathname) {
  const match = /^\/v1\/attachments\/([^/]+)$/.exec(pathname);
  if (match === null) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return undefined;
  }
}

function matchReminderRoute(pathname) {
  const match = /^\/v1\/reminders\/([^/]+)$/.exec(pathname);
  if (match === null) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return undefined;
  }
}

function decodeImageDataUrl(attachment, { maxImageAttachmentBytes }) {
  if (attachment === null || typeof attachment !== "object" || Array.isArray(attachment) || attachment.type !== "image") {
    throw new ProtocolValidationError();
  }
  const dataUrl = String(attachment.data_url ?? "");
  const match = /^data:([^;,]+);base64,([A-Za-z0-9+/]+={0,2})$/.exec(dataUrl);
  if (match === null || match[2].length % 4 !== 0) throw new ProtocolValidationError();
  const mimeType = match[1].toLowerCase();
  const config = IMAGE_ATTACHMENT_MIMES.get(mimeType);
  if (config === undefined) throw new ProtocolValidationError();
  const bytes = Buffer.from(match[2], "base64");
  if (bytes.length === 0 || bytes.length > maxImageAttachmentBytes || bytes.toString("base64") !== match[2].replace(/=+$/, "") + "=".repeat((4 - (match[2].replace(/=+$/, "").length % 4)) % 4)) {
    throw new ProtocolValidationError("payload_too_large");
  }
  if (!imageMagicMatches(bytes, config)) throw new ProtocolValidationError();
  const dimensions = imageDimensions(bytes, mimeType);
  if (dimensions === undefined
    || dimensions.width > MAX_IMAGE_EDGE
    || dimensions.height > MAX_IMAGE_EDGE
    || dimensions.width * dimensions.height > MAX_IMAGE_PIXELS) {
    throw new ProtocolValidationError();
  }
  return { mimeType, extension: config.extension, bytes };
}

function imageMagicMatches(bytes, config) {
  if (config.riff) return bytes.length >= 12
    && bytes.subarray(0, 4).toString("ascii") === "RIFF"
    && bytes.subarray(8, 12).toString("ascii") === "WEBP";
  return config.magic.some((magic) => magic.every((byte, index) => bytes[index] === byte));
}

function imageDimensions(bytes, mimeType) {
  if (mimeType === "image/png") {
    if (bytes.length < 24 || bytes.subarray(12, 16).toString("ascii") !== "IHDR") return undefined;
    return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20) };
  }
  if (mimeType === "image/gif") {
    if (bytes.length < 10) return undefined;
    return { width: bytes.readUInt16LE(6), height: bytes.readUInt16LE(8) };
  }
  if (mimeType === "image/jpeg") return jpegDimensions(bytes);
  if (mimeType !== "image/webp" || bytes.length < 30 || bytes.readUInt32LE(4) + 8 !== bytes.length) return undefined;
  const chunk = bytes.subarray(12, 16).toString("ascii");
  if (chunk === "VP8X") {
    return {
      width: 1 + readUInt24LE(bytes, 24),
      height: 1 + readUInt24LE(bytes, 27),
    };
  }
  if (chunk === "VP8 " && bytes.length >= 30
    && bytes[23] === 0x9d && bytes[24] === 0x01 && bytes[25] === 0x2a) {
    return {
      width: bytes.readUInt16LE(26) & 0x3fff,
      height: bytes.readUInt16LE(28) & 0x3fff,
    };
  }
  if (chunk === "VP8L" && bytes.length >= 25 && bytes[20] === 0x2f) {
    return {
      width: 1 + bytes[21] + ((bytes[22] & 0x3f) << 8),
      height: 1 + (bytes[22] >> 6) + (bytes[23] << 2) + ((bytes[24] & 0x0f) << 10),
    };
  }
  return undefined;
}

function jpegDimensions(bytes) {
  let offset = 2;
  while (offset + 4 <= bytes.length) {
    if (bytes[offset] !== 0xff) return undefined;
    while (bytes[offset] === 0xff) offset += 1;
    const marker = bytes[offset++];
    if (marker === 0xd8 || marker === 0xd9) continue;
    if (marker === 0xda) return undefined;
    if (offset + 2 > bytes.length) return undefined;
    const length = bytes.readUInt16BE(offset);
    if (length < 2 || offset + length > bytes.length) return undefined;
    if ([0xc0, 0xc1, 0xc2, 0xc3, 0xc5, 0xc6, 0xc7, 0xc9, 0xca, 0xcb, 0xcd, 0xce, 0xcf].includes(marker)) {
      if (length < 7) return undefined;
      return { width: bytes.readUInt16BE(offset + 5), height: bytes.readUInt16BE(offset + 3) };
    }
    offset += length;
  }
  return undefined;
}

function readUInt24LE(bytes, offset) {
  return bytes[offset] | (bytes[offset + 1] << 8) | (bytes[offset + 2] << 16);
}

function rejectEscapedNewlines(body) {
  if (body.includes("\\n") || body.includes("\\r")) {
    throw new ProtocolValidationError("escaped_newlines");
  }
}

function attachmentBody(body, files) {
  const text = String(body ?? "").trim();
  const references = files.map((file, index) => `${index + 1}. ${file.filePath}`).join("\n");
  return `${text}${text ? "\n\n" : ""}Attached image${files.length === 1 ? "" : "s"}:\n${references}`;
}

function safeAttachmentMessageId(value) {
  const id = String(value ?? "");
  return /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(id) ? id : createHash("sha256").update(id).digest("hex").slice(0, 24);
}

function sanitizeAttachmentName(value) {
  const name = String(value ?? "").trim();
  if (name === "") return undefined;
  return basename(name).replace(/[^A-Za-z0-9._ -]/g, "").slice(0, 120) || undefined;
}

function mimeTypeForAttachmentId(attachmentId) {
  if (attachmentId.endsWith(".png")) return "image/png";
  if (attachmentId.endsWith(".jpg")) return "image/jpeg";
  if (attachmentId.endsWith(".webp")) return "image/webp";
  if (attachmentId.endsWith(".gif")) return "image/gif";
  return "application/octet-stream";
}

function readJsonRequest(request, maxRequestBytes) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let byteLength = 0;
    let settled = false;
    const fail = (error) => {
      if (settled) return;
      settled = true;
      request.resume();
      reject(error);
    };
    request.on("data", (chunk) => {
      if (settled) return;
      byteLength += chunk.length;
      if (byteLength > maxRequestBytes) {
        fail(new HttpRequestError(413, "payload_too_large"));
      } else {
        chunks.push(chunk);
      }
    });
    request.on("aborted", () => fail(new HttpRequestError(400, "invalid_request")));
    request.on("error", () => fail(new HttpRequestError(400, "invalid_request")));
    request.on("end", () => {
      if (settled) return;
      try {
        const payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks)));
        settled = true;
        resolve(payload);
      } catch {
        fail(new HttpRequestError(400, "invalid_request"));
      }
    });
  });
}

function mechaDeveloperUpdateReminderBody() {
  return `mecha-developer update check reminder:\n\nCheck whether ${MECHA_DEVELOPER_REPO_PATH} has upstream changes available. Follow the supervisor skill section \"mecha-developer update reminder\"; this router reminder is advisory only.`;
}

function sendHttpJson(response, statusCode, payload, headers = {}) {
  response.writeHead(statusCode, {
    "cache-control": "no-store",
    "content-type": "application/json; charset=utf-8",
    ...headers,
  });
  response.end(JSON.stringify(payload));
}

function applyCorsHeaders(response) {
  for (const [header, value] of Object.entries(CORS_HEADERS)) response.setHeader(header, value);
}

function isBrowserCorsRoute(method, pathname) {
  if (method === "OPTIONS") return ["/v1/messages", "/v1/health"].includes(pathname);
  return (method === "GET" && pathname === "/v1/health")
    || (method === "POST" && pathname === "/v1/messages");
}

function sendHttpError(response, statusCode, code, headers = {}) {
  sendHttpJson(response, statusCode, {
    error: {
      code,
      message: HTTP_ERROR_MESSAGES[code] ?? HTTP_ERROR_MESSAGES.internal_error,
    },
  }, headers);
}

function sendReminderConflict(response, error) {
  sendHttpJson(response, 409, {
    error: {
      code: error.code,
      message: HTTP_ERROR_MESSAGES[error.code],
      reminder_id: error.id,
      reminder_state: error.state,
      linked_message_state: error.linkedMessageState,
    },
  });
}

function sendError(socket, generateFrameId, code, requestId) {
  sendFrame(socket, generateFrameId, {
    type: "error",
    code,
    ...(requestId === undefined ? {} : { request_id: requestId }),
  });
}

function sendFrame(socket, generateFrameId, fields, validate = true) {
  if (socket.readyState !== WebSocket.OPEN) return false;
  const frame = {
    version: "v1",
    frame_id: generateFrameId(),
    ...fields,
  };
  if (validate) validateServerFrame(frame);
  socket.send(JSON.stringify(frame));
  return true;
}

function closeSocket(socket, code, reason) {
  if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
    socket.close(code, reason);
  }
}

function toEnvelope(message) {
  return {
    id: message.id,
    from: message.from,
    to: message.to,
    ...(message.subject === undefined ? {} : { subject: message.subject }),
    ...(message.sentinel === undefined ? {} : { sentinel: message.sentinel }),
    ...(message.source === undefined ? {} : { source: message.source }),
    body: message.body,
    created_at: message.created_at,
  };
}
